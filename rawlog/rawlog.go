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
	nameLayout   = "20060102T150405.000Z"
	nameSuffix   = ".jsonl.gz"
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
	}
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
type Session struct {
	l   *Log
	seq uint64

	mu     sync.Mutex
	closed bool
	buf    []byte    // partial rx line
	bufAt  time.Time // arrival of its first byte
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
		s.l.enqueue(item{sess: s, at: now, dir: fixture.TX, data: bytes.Clone(p)})
	case fixture.RX:
		s.rx(now, p)
	}
}

// rx splits p into lines; the caller holds s.mu.
func (s *Session) rx(now time.Time, p []byte) {
	for len(p) > 0 {
		if len(s.buf) == 0 {
			s.bufAt = now
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
			s.emit(s.buf[:k])
			s.buf = bytes.Clone(s.buf[k:])
			s.bufAt = now
		}
	}
}

// emit queues data as one rx event stamped s.bufAt; the caller holds s.mu.
func (s *Session) emit(data []byte) {
	s.l.enqueue(item{sess: s, at: s.bufAt, dir: fixture.RX, data: bytes.Clone(data)})
}

// Close emits the partial rx line and ends the session's file. It is
// idempotent.
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
	if len(s.buf) > 0 {
		s.emit(s.buf)
		s.buf = nil
	}
	// Accepted: with a full buffer the end marker is dropped; the file is then
	// closed at the next session, rotation or Log.Close.
	s.l.enqueue(item{sess: s, at: s.l.cfg.Now(), end: true})
}

// writer owns the open file; only the writer goroutine touches it.
type writer struct {
	l *Log

	seq   uint64 // current session
	ended bool   // its end marker was seen

	f       *os.File
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
		w.f, w.start = f, at
		w.gz = gzip.NewWriter(f)
		w.enc = json.NewEncoder(w.gz)
		w.dirty = false
		return nil
	}
	return fmt.Errorf("no free file name near %s", at.UTC().Format(nameLayout))
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
