// Package rawlog keeps the raw machine traffic of a session as gzip'd
// fixture logs (one JSON-lines fixture.Event per line), so a failure noticed
// days later can be read back from the exact bytes. The tap side only
// enqueues into a bounded buffer; one writer goroutine does all disk work.
package rawlog

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

const (
	// maxPiece is the longest rx event: a longer partial line is emitted
	// in pieces of this size.
	maxPiece = 4096
	// maxNameTries bounds the O_EXCL collision bump of the file name.
	maxNameTries = 1000
	// maxReportLine bounds a kept `{` or `<` line including its terminator;
	// a longer one is a report or display frame with a lost CRLF that glued
	// the next line (maybe a dump line) onto it.
	maxReportLine = 1024
	nameLayout    = "20060102T150405.000Z"
	nameSuffix    = ".jsonl.gz"
)

// Config configures a Log. Zero values select the documented defaults.
type Config struct {
	Dir         string
	Retention   time.Duration    // files older than this are deleted; 0 = keep
	MaxBytes    int64            // directory total cap; 0 = no cap
	RotateEvery time.Duration    // default 1h
	FlushEvery  time.Duration    // default 5s
	ReplyWindow time.Duration    // default 5s
	DumpQuiet   time.Duration    // default 60s
	Buffer      int              // queued events, default 4096
	Now         func() time.Time // default time.Now; must be goroutine-safe

	writeHook func(stop <-chan struct{}) // tests only: called before each write; stop is closed by Log.Close
}

func (c *Config) defaults() {
	if c.RotateEvery <= 0 {
		c.RotateEvery = time.Hour
	}
	if c.FlushEvery <= 0 {
		c.FlushEvery = 5 * time.Second
	}
	if c.ReplyWindow <= 0 {
		c.ReplyWindow = 5 * time.Second
	}
	if c.DumpQuiet <= 0 {
		c.DumpQuiet = 60 * time.Second
	}
	if c.Buffer <= 0 {
		c.Buffer = 4096
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// item is one queued event, or the end marker of a session.
type item struct {
	sess *Session
	at   time.Time
	dir  fixture.Dir
	data []byte
	end  bool // session closed
}

// Log writes the traffic of successive sessions to files in a directory.
// All methods are no-ops on a nil receiver.
type Log struct {
	cfg   Config
	items chan item     // never closed: taps may fire at any time
	quit  chan struct{} // closed by Close
	done  chan struct{} // closed when the writer has exited

	closed atomic.Bool

	mu      sync.Mutex // guards nextSeq and closeErr
	nextSeq uint64

	closeOnce sync.Once
	closeErr  error

	writeErrors atomic.Uint64
	dropped     atomic.Uint64
	redacted    atomic.Uint64

	// Dump state is shared by all sessions: a dump seen in one keeps the
	// windows of the next shut (a reconnect must not reopen them).
	dumpMu   sync.Mutex
	lastDump time.Time // latest MCu/MCm tx or dump line; zero at start
	dumpSeq  uint64    // counts dump events

	w writer // owned by the writer goroutine
}

// Open creates the directory (mode 0700) and starts the writer.
func Open(cfg Config) (*Log, error) {
	cfg.defaults()
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("rawlog: create dir: %w", err)
	}
	// MkdirAll leaves an existing directory alone (systemd may have made it
	// 0755); the raw traffic must not be world-readable.
	if err := os.Chmod(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("rawlog: chmod dir: %w", err)
	}
	l := &Log{
		cfg:   cfg,
		items: make(chan item, cfg.Buffer),
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	l.w.l = l
	l.w.prune("")
	go l.run()
	return l, nil
}

// Close stops the taps, drains the queue, finishes the open file and waits
// for the writer. It is idempotent.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.closed.Store(true)
		close(l.quit)
	})
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closeErr
}

// Session starts a new session: its traffic goes to a file of its own.
func (l *Log) Session() *Session {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextSeq++
	return &Session{l: l, seq: l.nextSeq}
}

// enqueue queues it without ever blocking; a full buffer drops it.
func (l *Log) enqueue(it item) {
	select {
	case l.items <- it:
	default:
		l.dropped.Add(1)
	}
}

// Dropped is the number of events (or end markers) dropped because the
// buffer was full.
func (l *Log) Dropped() uint64 {
	if l == nil {
		return 0
	}
	return l.dropped.Load()
}

// Redacted is the number of received lines the allowlist dropped (a line
// split into pieces counts once).
func (l *Log) Redacted() uint64 {
	if l == nil {
		return 0
	}
	return l.redacted.Load()
}

// WriteErrors is the number of open, write, flush and close errors.
func (l *Log) WriteErrors() uint64 {
	if l == nil {
		return 0
	}
	return l.writeErrors.Load()
}

func (l *Log) run() {
	defer close(l.done)
	tick := time.NewTicker(l.cfg.FlushEvery)
	defer tick.Stop()
	for {
		select {
		case it := <-l.items:
			l.w.handle(it)
		case <-tick.C:
			l.w.flush()
		case <-l.quit:
			for {
				select {
				case it := <-l.items:
					l.w.handle(it)
				default:
					err := l.w.finish()
					l.mu.Lock()
					l.closeErr = err
					l.mu.Unlock()
					return
				}
			}
		}
	}
}

// Session is the traffic of one connection. All methods are no-ops on a
// nil receiver.
//
// Received lines are an allowlist (spec requirement 5): the machine's
// MCu/MCm replies carry the license key and must never reach a file. A line
// is kept only if it is a report ({), a banner ($id:) or inside a reply
// window; dump lines are never kept and shut the windows.
type Session struct {
	l   *Log
	seq uint64

	mu     sync.Mutex
	closed bool
	buf    []byte    // partial rx line
	bufAt  time.Time // arrival of its first byte

	// Current rx line: decided once, when the line completes or its first
	// piece is emitted; later pieces follow keep.
	decided bool
	keep    bool
	lineWin bool   // inside a reply window at the first byte
	lineSeq uint64 // Log dumpSeq at the first byte

	windowUntil time.Time // reply window open until then
	pending     int       // dropped lines not yet announced by a note
}

// Tap records p; its signature fits leva.Conn.Tap. It copies p, never
// blocks and does no I/O. After Session.Close or Log.Close it does nothing.
func (s *Session) Tap(dir string, p []byte) {
	if s == nil || len(p) == 0 || s.l.closed.Load() {
		return
	}
	now := s.l.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	switch fixture.Dir(dir) {
	case fixture.TX:
		s.tx(now, p)
		s.flushNote(now)
		s.l.enqueue(item{sess: s, at: now, dir: fixture.TX, data: bytes.Clone(p)})
	case fixture.RX:
		s.rx(now, p)
	}
}

// tx updates the reply window from the commands in p (all of them: a
// write may carry several); the caller holds s.mu. MCu/MCm (any suffix)
// are dump commands: they close the window and start the quiet period. The
// stream toggles MCr and "MC " get no reply. Any other command opens a
// window unless a dump is recent.
func (s *Session) tx(now time.Time, p []byte) {
	cfg := &s.l.cfg
	for _, cmd := range bytes.FieldsFunc(p, func(r rune) bool { return r == '\r' || r == '\n' }) {
		switch {
		case bytes.HasPrefix(cmd, []byte("MCu")), bytes.HasPrefix(cmd, []byte("MCm")):
			s.dump(now)
		case bytes.Equal(cmd, []byte("MCr")), bytes.Equal(cmd, []byte("MC ")), bytes.Equal(cmd, []byte("MC@")):
		case now.Sub(s.l.lastDumpAt()) < cfg.DumpQuiet:
			// quiet: a dump is recent, no window
		default:
			s.windowUntil = now.Add(cfg.ReplyWindow)
		}
	}
}

// dump records a dump in sight: the window closes and the quiet period
// restarts; the caller holds s.mu.
func (s *Session) dump(now time.Time) {
	s.windowUntil = time.Time{}
	s.l.dumpMu.Lock()
	defer s.l.dumpMu.Unlock()
	// Never move back: another session's Tap may carry an older now.
	if now.After(s.l.lastDump) {
		s.l.lastDump = now
	}
	s.l.dumpSeq++
}

// rx splits p into lines; the caller holds s.mu.
func (s *Session) rx(now time.Time, p []byte) {
	for len(p) > 0 {
		if len(s.buf) == 0 {
			s.bufAt = now
			if !s.decided {
				s.lineWin, s.lineSeq = now.Before(s.windowUntil), s.l.dumpSeqNow()
			}
		}
		n := len(p)
		nl := bytes.IndexByte(p, '\n')
		if nl >= 0 {
			n = nl + 1
		}
		s.buf = append(s.buf, p[:n]...)
		p = p[n:]
		for len(s.buf) >= maxPiece || (nl >= 0 && len(s.buf) > 0 && s.buf[len(s.buf)-1] == '\n') {
			k := min(len(s.buf), maxPiece)
			s.piece(now, s.buf[:k], k == len(s.buf) && s.buf[k-1] == '\n')
			s.buf = bytes.Clone(s.buf[k:])
			s.bufAt = now
		}
	}
}

// piece handles one chunk of the current rx line: the first piece decides
// for the whole line; complete says the line ends with this piece. The
// caller holds s.mu.
func (s *Session) piece(now time.Time, data []byte, complete bool) {
	if !s.decided {
		s.decided = true
		s.keep = s.judge(now, data)
		if s.keep && (data[0] == '{' || data[0] == '<') && len(data) > maxReportLine {
			s.keep = false
		}
		if !s.keep {
			s.pending++
			s.l.redacted.Add(1)
		}
	}
	if s.keep {
		s.flushNote(s.bufAt)
		s.l.enqueue(item{sess: s, at: s.bufAt, dir: fixture.RX, data: bytes.Clone(data)})
	}
	if complete {
		s.decided = false
	}
}

// judge decides from the start of a line whether it is kept. A dump line
// (| or :, the Intel HEX of MCm) is never kept and records a dump. Otherwise
// reports, display frames (MC@) and banners are kept, and anything else
// only inside the window the line started in, provided no dump happened
// since its first byte.
func (s *Session) judge(now time.Time, data []byte) bool {
	line := bytes.TrimRight(data, "\r\n")
	if len(line) > 0 && (line[0] == '|' || line[0] == ':') {
		s.dump(now)
		return false
	}
	if len(line) > 0 && (line[0] == '{' || line[0] == '<') || bytes.HasPrefix(line, []byte("$id:")) {
		return true
	}
	return s.lineWin && s.lineSeq == s.l.dumpSeqNow()
}

// lastDumpAt is the time of the latest dump of any session.
func (l *Log) lastDumpAt() time.Time {
	l.dumpMu.Lock()
	defer l.dumpMu.Unlock()
	return l.lastDump
}

func (l *Log) dumpSeqNow() uint64 {
	l.dumpMu.Lock()
	defer l.dumpMu.Unlock()
	return l.dumpSeq
}

// flushNote queues the note for the lines dropped since the last kept
// event, stamped at; the caller holds s.mu.
func (s *Session) flushNote(at time.Time) {
	if s.pending == 0 {
		return
	}
	text := fmt.Sprintf("dropped %d lines", s.pending)
	if s.pending == 1 {
		text = "dropped 1 line"
	}
	s.pending = 0
	s.l.enqueue(item{sess: s, at: at, dir: fixture.Note, data: []byte(text)})
}

// Close judges and emits the partial rx line, announces dropped lines and
// ends the session's file. It is idempotent.
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.l.closed.Load() {
		return // the queue is dead: nothing to emit into
	}
	now := s.l.cfg.Now()
	if len(s.buf) > 0 {
		s.piece(now, s.buf, true)
		s.buf = nil
	}
	s.flushNote(now)
	// Accepted: with a full buffer the end marker is dropped; the file is then
	// closed at the next session, rotation or Log.Close.
	s.l.enqueue(item{sess: s, at: now, end: true})
}

// writer owns the open file; only the writer goroutine touches it.
type writer struct {
	l *Log

	seq   uint64 // current session
	ended bool   // its end marker was seen

	f       *os.File
	path    string // name of f
	gz      *gzip.Writer
	enc     *json.Encoder
	start   time.Time // open time of f
	dirty   bool      // unflushed events in gz
	broken  bool      // file abandoned after a write error
	brokenA time.Time // when
}

func (w *writer) handle(it item) {
	switch {
	case it.sess.seq < w.seq, it.sess.seq == w.seq && w.ended:
		return // no longer the current session
	case it.sess.seq > w.seq:
		_ = w.finish()
		w.seq, w.ended, w.broken = it.sess.seq, false, false
	}
	if it.end {
		_ = w.finish()
		w.ended = true
		return
	}
	w.write(it)
}

func (w *writer) write(it item) {
	cfg := &w.l.cfg
	if w.f != nil && it.at.Sub(w.start) >= cfg.RotateEvery {
		_ = w.finish()
	}
	if w.f == nil {
		if w.broken && it.at.Sub(w.brokenA) < cfg.RotateEvery {
			return
		}
		w.broken = false
		if err := w.open(it.at); err != nil {
			w.fail(err, it.at)
			return
		}
	}
	if cfg.writeHook != nil {
		cfg.writeHook(w.l.quit)
	}
	// Clamp: a partial line stamped before a rotation that a later item
	// triggered would otherwise get a negative t (spec req 3).
	ev := fixture.Event{T: max(0, it.at.Sub(w.start).Milliseconds()), Dir: it.dir, Data: it.data}
	if err := w.enc.Encode(ev); err != nil {
		w.fail(err, it.at)
		return
	}
	w.dirty = true
}

// open creates a new file named after at (UTC), bumping the millisecond on
// a name collision.
func (w *writer) open(at time.Time) error {
	// The directory may have been deleted since Open.
	if err := os.MkdirAll(w.l.cfg.Dir, 0o700); err != nil {
		return err
	}
	name := at.UTC()
	for range maxNameTries {
		path := filepath.Join(w.l.cfg.Dir, name.Format(nameLayout)+nameSuffix)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			name = name.Add(time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
		w.f, w.path, w.start = f, path, at
		w.gz = gzip.NewWriter(f)
		w.enc = json.NewEncoder(w.gz)
		w.dirty = false
		w.prune(path)
		return nil
	}
	return fmt.Errorf("no free file name near %s", at.UTC().Format(nameLayout))
}

// prune deletes log files older than Retention (by name time), then the
// oldest ones while the total size exceeds MaxBytes. The open file is
// excluded from that total (and never deleted), so the directory can exceed
// the cap by one file. Files whose names do not parse are left alone. Errors are logged, not counted.
func (w *writer) prune(open string) {
	cfg := &w.l.cfg
	if cfg.Retention <= 0 && cfg.MaxBytes <= 0 {
		return
	}
	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		slog.Warn("rawlog: prune: reading dir failed", "err", err)
		return
	}
	type logFile struct {
		path string
		at   time.Time
		size int64
	}
	var files []logFile // sorted by name, hence by time
	var total int64
	now := cfg.Now()
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), nameSuffix)
		if !ok || e.IsDir() {
			continue
		}
		at, err := time.Parse(nameLayout, name)
		if err != nil {
			continue
		}
		path := filepath.Join(cfg.Dir, e.Name())
		if path == open {
			continue
		}
		info, err := e.Info()
		if err != nil {
			slog.Warn("rawlog: prune: stat failed", "file", path, "err", err)
			continue
		}
		if cfg.Retention > 0 && now.Sub(at) > cfg.Retention {
			w.remove(path)
			continue
		}
		files = append(files, logFile{path, at, info.Size()})
		total += info.Size()
	}
	if cfg.MaxBytes <= 0 {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	for _, f := range files {
		if total <= cfg.MaxBytes {
			return
		}
		if w.remove(f.path) {
			total -= f.size
		}
	}
}

// remove deletes path and reports whether it succeeded.
func (w *writer) remove(path string) bool {
	if err := os.Remove(path); err != nil {
		slog.Warn("rawlog: prune: remove failed", "file", path, "err", err)
		return false
	}
	return true
}

// fail counts a write error and abandons the current file until the next
// rotation or session. The daemon never fails because of the log.
func (w *writer) fail(err error, at time.Time) {
	w.l.writeErrors.Add(1)
	slog.Error("rawlog: write failed, abandoning file", "err", err)
	// The file is abandoned: its close error adds nothing.
	_ = w.closeFile()
	w.broken, w.brokenA = true, at
}

func (w *writer) flush() {
	if w.f == nil || !w.dirty {
		return
	}
	if err := w.gz.Flush(); err != nil {
		w.fail(err, w.l.cfg.Now())
		return
	}
	w.dirty = false
}

// finish closes the file and counts and logs a failure (spec req 6).
func (w *writer) finish() error {
	err := w.closeFile()
	if err != nil {
		w.l.writeErrors.Add(1)
		slog.Error("rawlog: closing file failed", "err", err)
	}
	return err
}

// closeFile finishes the gzip stream and closes the file, if any.
func (w *writer) closeFile() error {
	if w.f == nil {
		return nil
	}
	err := w.gz.Close()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	w.f, w.gz, w.enc, w.dirty = nil, nil, nil, false
	return err
}

// ReadFile reads a log file. A missing gzip trailer (the file being written,
// or one left by a crash) is tolerated: the events up to there are returned.
func ReadFile(path string) ([]fixture.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("rawlog: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, nil // no complete header yet
	}
	if err != nil {
		return nil, fmt.Errorf("rawlog: %s: %w", path, err)
	}
	var events []fixture.Event
	dec := json.NewDecoder(gz)
	for {
		var ev fixture.Event
		err := dec.Decode(&ev)
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return events, nil
		}
		if err != nil {
			return events, fmt.Errorf("rawlog: %s: event %d: %w", path, len(events), err)
		}
		events = append(events, ev)
	}
}
