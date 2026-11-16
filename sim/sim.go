// Package sim implements a fake leva! controller speaking the same plain-text
// TCP protocol as the real firmware, so other components can be developed and
// tested without a machine.
//
// Like the real module, all output is shared "UART" traffic: canned responses
// to any client's command are fanned out to every connected client. The
// banner is a connect greeting and goes only to the new client, unless
// a stream (reports or the display mirror) is on: then, like the real
// machine, the new client gets only the stream.
package sim

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

const (
	defaultAddr        = "127.0.0.1:0"
	defaultMaxClients  = 4
	defaultIdleTimeout = 5 * time.Minute
	defaultBanner      = "$id: leva!"
	maxLineBytes       = 4096
	outQueueLen        = 256
	writeTimeout       = 5 * time.Second
)

// Config configures a Server. Zero values select the documented defaults.
type Config struct {
	// Addr is the listen address (default 127.0.0.1:0).
	Addr string
	// TripwireAddr, if non-empty, is a second listener that only counts and
	// closes connections; it catches tooling dialling a bootloader-like port.
	TripwireAddr string
	// MaxClients is the maximum number of simultaneous clients (default 4).
	MaxClients int
	// IdleTimeout closes a connection that sends nothing for this long
	// (default 5 minutes).
	IdleTimeout time.Duration
	// Speed is the stream replay speed factor (default 1.0): inter-event
	// delays from the recorded T values are divided by it.
	Speed float64
	// Banner is sent, CRLF-terminated, to each new client (default "$id: leva!").
	Banner string
	// Responses maps a received command line to the lines sent in reply to
	// all clients. The sim appends CRLF to each. Must not be mutated after New.
	// MCr, MC@, MC<space> and MCR are handled by the sim itself; entries
	// for them are never sent.
	Responses map[string][]string
	// Stream is the recorded traffic for replay, on one timeline (T deltas /
	// Speed). TX events are skipped. An RX event is an LCD frame or
	// report-stream traffic (reports, notices, banners); the last record
	// that starts in it decides (leva.LastRecordStart): a stream switch
	// glues records, also inside a TCP read, and the last one is what the
	// machine was sending then. An event with no record start (a
	// continuation piece) takes the class of the RX event before it; a
	// first event without one is report-stream traffic.
	//
	// MCr emits the report-stream events verbatim (fixtures carry their own
	// CRLF), one queue item per event; frame events are skipped but keep
	// their time slot. The sim holds the current LCD like the machine: the
	// latest whole recorded frame (an intact "<…>" record, reassembled
	// across events and split from glued records) the timeline has passed,
	// updated in every mode; before the first, the stream's first whole
	// frame. MC@ mirrors it, at once and then every 100 ms /
	// Speed (at least 1 ms), and skips the report events; recorded frames
	// are never emitted themselves, and cut or glued frame pieces never
	// become the LCD. The last of MCr and MC@ sent wins, like on the
	// machine; switching between them keeps the timeline. MC<space> stops
	// both. MCR resends the current LCD in display mode and the last report
	// item otherwise. With no RX events (or no whole frames, or no reports),
	// MCr/MC@ set the flag but emit nothing, and MCR sends nothing.
	Stream []fixture.Event
	// BannerInterval, if positive, makes the sim repeat Banner to all clients
	// at this interval while no stream is on, like the real machine's idle
	// heartbeat. It pauses while MCr reporting or the MC@ display mirror is
	// on and resumes after MC<space>. Zero (default): the banner is sent
	// once, on connect.
	BannerInterval time.Duration
	// Switch maps a received command line to a replacement for Stream, so
	// tests can model commands that change what the machine reports (eco,
	// wake). If reporting is on, the current replay stops and the new stream
	// starts from its beginning; otherwise it is used at the next MCr. The
	// current LCD becomes the new stream's first whole frame. A key
	// that is also in Responses gets both. Must not be mutated after New.
	Switch map[string][]fixture.Event
	// Once replays the stream a single time instead of looping. After the
	// last event the replayer exits; reporting stays on (MCR still resends)
	// and the display mirror keeps showing the last frame. A later MCr or
	// MC@ starts the replay again from t=0, with the first frame on the LCD.
	Once bool
}

// Server is a fake leva! controller.
type Server struct {
	cfg Config

	mu       sync.Mutex
	ln, trip net.Listener
	clients  map[*client]struct{}
	started  bool
	stopped  bool

	// Global reporting state, like the real machine's single UART toggle.
	// reporting means a stream is on; display says which one (MC@ frames,
	// not MCr reports). stopReplay is non-nil exactly while a replayer
	// goroutine owns the stream; closing it (under mu) tells that replayer
	// to exit. stopMirror is the same for the display mirror goroutine,
	// which runs exactly while display is on.
	reporting  bool
	display    bool
	stopReplay chan struct{}
	stopMirror chan struct{}
	lastItem   []byte          // last report item emitted, for MCR
	stream     []fixture.Event // current replay source; Switch replaces it
	lcd        []byte          // current LCD: a whole frame plus CRLF, or nil
	// clk paces the replayer and the display mirror (tests substitute it
	// before Start); clockUsers counts their running goroutines, so a test
	// can tell when all of them wait on clk.
	clk        clock
	clockUsers atomic.Int32

	received []string // every command line from any client, in order

	quit chan struct{} // closed by Stop; ends the heartbeat goroutine

	wg   sync.WaitGroup
	hits atomic.Int64
}

// New returns a Server for cfg with defaults applied.
func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.MaxClients <= 0 {
		cfg.MaxClients = defaultMaxClients
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.Speed <= 0 {
		cfg.Speed = 1.0
	}
	if cfg.Banner == "" {
		cfg.Banner = defaultBanner
	}
	return &Server{
		cfg: cfg, clients: make(map[*client]struct{}), quit: make(chan struct{}),
		stream: cfg.Stream, lcd: firstFrame(cfg.Stream), clk: realClock{},
	}
}

// Received returns a copy of every command line received from any client so
// far, CR+LF stripped, in arrival order.
func (s *Server) Received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

// Start opens the listeners and begins serving. It may be called once.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("sim: already started")
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("sim: listen: %w", err)
	}
	var trip net.Listener
	if s.cfg.TripwireAddr != "" {
		trip, err = net.Listen("tcp", s.cfg.TripwireAddr)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("sim: tripwire listen: %w", err)
		}
	}
	s.started, s.ln, s.trip = true, ln, trip
	s.wg.Add(1)
	go s.acceptLoop(ln)
	if trip != nil {
		s.wg.Add(1)
		go s.tripwireLoop(trip)
	}
	if s.cfg.BannerInterval > 0 {
		s.wg.Add(1)
		go s.heartbeat()
	}
	return nil
}

// heartbeat re-sends the banner to every client each BannerInterval unless
// a stream (reports or display) is on. The banner is one whole queue item,
// like any other output.
func (s *Server) heartbeat() {
	defer s.wg.Done()
	t := time.NewTicker(s.cfg.BannerInterval)
	defer t.Stop()
	msg := []byte(s.cfg.Banner + "\r\n")
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
			s.mu.Lock()
			if !s.reporting && !s.stopped {
				s.fanOutLocked(msg)
			}
			s.mu.Unlock()
		}
	}
}

// Addr returns the main listener address, or nil before Start.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// TripwireAddr returns the tripwire listener address, or nil if none.
func (s *Server) TripwireAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.trip == nil {
		return nil
	}
	return s.trip.Addr()
}

// TripwireHits returns how many connections the tripwire has seen.
func (s *Server) TripwireHits() int { return int(s.hits.Load()) }

// Stop closes the listeners and all clients and waits for every goroutine to
// exit. It is safe to call more than once.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	close(s.quit)
	s.stopReplayLocked()
	s.stopMirrorLocked()
	var errs []error
	for _, l := range []net.Listener{s.ln, s.trip} {
		if l != nil {
			if err := l.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	for c := range s.clients {
		c.close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return errors.Join(errs...)
}

func (s *Server) acceptLoop(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		s.mu.Lock()
		if s.stopped || len(s.clients) >= s.cfg.MaxClients {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		c := &client{conn: conn, out: make(chan []byte, outQueueLen), done: make(chan struct{})}
		if !s.reporting { // the real machine is silent while a stream is on
			c.out <- []byte(s.cfg.Banner + "\r\n") // empty queue: cannot block
		}
		s.clients[c] = struct{}{}
		s.wg.Add(2)
		s.mu.Unlock()
		go s.writeLoop(c)
		go s.readLoop(c)
	}
}

func (s *Server) tripwireLoop(ln net.Listener) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		n := s.hits.Add(1)
		fmt.Fprintf(os.Stderr, "sim: TRIPWIRE: connection #%d from %s on %s -- something dialled a bootloader-like port!\n",
			n, conn.RemoteAddr(), ln.Addr())
		_ = conn.Close()
	}
}

func (s *Server) readLoop(c *client) {
	defer s.wg.Done()
	defer s.remove(c)
	r := bufio.NewReaderSize(c.conn, maxLineBytes)
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(s.cfg.IdleTimeout))
		line, err := r.ReadSlice('\n')
		if err != nil {
			return // EOF, idle timeout, closed, or over-long line
		}
		cmd := strings.TrimRight(string(line), "\r\n")
		s.mu.Lock()
		s.received = append(s.received, cmd)
		s.mu.Unlock()
		if events, ok := s.cfg.Switch[cmd]; ok {
			s.switchStream(events)
		}
		switch cmd {
		case "MCr":
			s.startReplay(false)
			continue
		case "MC@":
			s.startReplay(true)
			continue
		case "MC ":
			s.stopReporting()
			continue
		case "MCR":
			s.resendLast()
			continue
		}
		if resp, ok := s.cfg.Responses[cmd]; ok {
			s.broadcast(resp)
		}
	}
}

// broadcast queues one response, CRLF-joined, as a single queue item for every
// client, so a response is never cut off mid-way. It never blocks: a client
// whose queue is full (outQueueLen whole responses behind) is too slow and
// gets disconnected, so one stalled reader cannot hold up the others.
func (s *Server) broadcast(lines []string) {
	if len(lines) == 0 {
		return
	}
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteString("\r\n")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fanOutLocked([]byte(sb.String()))
}

// fanOutLocked queues msg (shared read-only) to every client. s.mu must be held.
func (s *Server) fanOutLocked(msg []byte) {
	for c := range s.clients {
		select {
		case c.out <- msg:
		default:
			c.close()
		}
	}
}

func (s *Server) writeLoop(c *client) {
	defer s.wg.Done()
	defer s.remove(c)
	for {
		select {
		case <-c.done:
			return
		case b := <-c.out:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := c.conn.Write(b); err != nil {
				return
			}
		}
	}
}

func (s *Server) remove(c *client) {
	c.close()
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

type client struct {
	conn net.Conn
	out  chan []byte
	done chan struct{}
	once sync.Once
}

// close is idempotent and safe from any goroutine; out is never closed, so
// late sends cannot panic.
func (c *client) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

// minLoopPause keeps a stream whose events all share one timestamp from
// spinning when it wraps around.
const minLoopPause = time.Millisecond

// lcdFramePeriod is the machine's LCD mirror cadence; minFramePeriod is the
// floor at high Speed (the sim's timers pace no finer, and a faster
// mirror would only flood the client queues).
const (
	lcdFramePeriod = 100 * time.Millisecond
	minFramePeriod = time.Millisecond
)

// displayPeriod is the display mirror's period: 100 ms / Speed, at least
// 1 ms.
func (s *Server) displayPeriod() time.Duration {
	return max(time.Duration(float64(lcdFramePeriod)/s.cfg.Speed), minFramePeriod)
}

// startReplay handles MCr (display false) and MC@ (display true): set the
// global flags and, if no replayer runs yet and there is something to
// replay, start exactly one. A running replayer keeps its timeline and
// only switches which events it emits. MC@ (re)starts the display mirror,
// which sends the current LCD at once; MCr stops it.
func (s *Server) startReplay(display bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.reporting, s.display = true, display
	if s.stopReplay == nil {
		s.startReplayerLocked()
	}
	s.stopMirrorLocked()
	if display {
		s.startMirrorLocked()
	}
}

// startReplayerLocked starts a replayer on the current stream if it has RX
// events; a new timeline starts with the stream's first whole frame on the
// LCD. s.mu held; no replayer may be running.
func (s *Server) startReplayerLocked() {
	var events []fixture.Event
	var frames []bool
	var lcds [][]byte
	var sc lcdScanner
	frame := false
	for _, ev := range s.stream {
		if ev.Dir == fixture.RX {
			found := sc.feed(ev.Data)
			var lcd []byte
			if len(found) > 0 {
				lcd = found[len(found)-1]
			}
			// An event that completes an LCD frame is display traffic: it
			// never also goes out as a report item.
			frame = isFrameEvent(ev.Data, frame) || lcd != nil
			events = append(events, ev)
			frames = append(frames, frame)
			lcds = append(lcds, lcd)
		}
	}
	s.lcd = firstFrame(s.stream)
	if len(events) == 0 {
		return
	}
	stop := make(chan struct{})
	s.stopReplay = stop
	s.wg.Add(1)
	s.clockUsers.Add(1)
	go s.replay(events, frames, lcds, stop)
}

// lcdScanner finds the whole display frames in RX traffic that may begin
// or end anywhere (a levacap event is a TCP read, a raw-log event a line):
// it carries the unterminated rest of one event over to the next, splits
// each CRLF-terminated line into records (leva.SplitLine, as leva.Conn
// does) and keeps the intact frames. A cut-off frame never counts; a whole
// one glued on after it does.
type lcdScanner struct {
	pending []byte
}

// feed scans one event and returns the whole frames it completes, each as
// an LCD item (the frame plus CRLF), in order.
func (sc *lcdScanner) feed(data []byte) [][]byte {
	buf := append(append([]byte(nil), sc.pending...), data...)
	var found [][]byte
	for {
		i := bytes.Index(buf, []byte("\r\n"))
		if i < 0 {
			break
		}
		for _, p := range leva.SplitLine(buf[:i]) {
			if p.Fragment {
				continue
			}
			if _, err := leva.ParseFrame(p.Data); err == nil {
				found = append(found, append(append(make([]byte, 0, len(p.Data)+2), p.Data...), '\r', '\n'))
			}
		}
		buf = buf[i+2:]
	}
	if len(buf) > maxLineBytes { // no line end in sight: no frame either
		buf = nil
	}
	sc.pending = append([]byte(nil), buf...)
	return found
}

// firstFrame returns the first whole frame in events' RX traffic as an LCD
// item, or nil if there is none.
func firstFrame(events []fixture.Event) []byte {
	var sc lcdScanner
	for _, ev := range events {
		if ev.Dir != fixture.RX {
			continue
		}
		if found := sc.feed(ev.Data); len(found) > 0 {
			return found[0]
		}
	}
	return nil
}

// startMirrorLocked sends the current LCD to all clients and starts the
// display mirror goroutine. s.mu held; no mirror may be running.
func (s *Server) startMirrorLocked() {
	if s.lcd != nil {
		s.fanOutLocked(s.lcd)
	}
	stop := make(chan struct{})
	s.stopMirror = stop
	s.wg.Add(1)
	s.clockUsers.Add(1)
	go s.mirror(s.clk.Now(), s.displayPeriod(), stop)
}

// stopMirrorLocked signals the display mirror, if any, to exit. s.mu held.
func (s *Server) stopMirrorLocked() {
	if s.stopMirror != nil {
		close(s.stopMirror)
		s.stopMirror = nil
	}
}

// mirror sends the current LCD to all clients at start + k periods
// (k = 1, 2, …) until stop closes. Like a ticker it keeps the cadence: a
// late tick sends once and the ticks it overran are dropped, never caught
// up with a burst. With no LCD (a stream without whole frames) it sends
// nothing.
func (s *Server) mirror(start time.Time, period time.Duration, stop chan struct{}) {
	defer s.wg.Done()
	defer s.clockUsers.Add(-1)
	for k := 1; ; {
		if !s.sleep(start.Add(time.Duration(k)*period).Sub(s.clk.Now()), stop) {
			return
		}
		s.mu.Lock()
		if s.stopMirror != stop {
			s.mu.Unlock()
			return
		}
		if s.lcd != nil {
			s.fanOutLocked(s.lcd)
		}
		s.mu.Unlock()
		k = int(s.clk.Now().Sub(start)/period) + 1
	}
}

// isFrameEvent reports whether an RX event belongs to the display stream:
// the last record starting in it is a frame. Reports, notices and banners
// are report-stream traffic; an event with no record start is a
// continuation piece and keeps prev, the class of the event before it.
func isFrameEvent(data []byte, prev bool) bool {
	i := leva.LastRecordStart(data)
	if i < 0 {
		return prev
	}
	return data[i] == '<'
}

// switchStream handles a Switch command: replace the stream, put its first
// whole frame on the LCD and, while reporting, restart replay on it.
func (s *Server) switchStream(events []fixture.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stream = events
	s.lcd = firstFrame(events)
	if !s.reporting || s.stopped {
		return
	}
	s.stopReplayLocked()
	s.startReplayerLocked()
}

// stopReporting handles MC<space>: clear the flags and stop the replayer
// and the display mirror, for every client regardless of who started them.
func (s *Server) stopReporting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reporting, s.display = false, false
	s.stopReplayLocked()
	s.stopMirrorLocked()
}

// stopReplayLocked signals the current replayer, if any, to exit. s.mu held.
func (s *Server) stopReplayLocked() {
	if s.stopReplay != nil {
		close(s.stopReplay)
		s.stopReplay = nil
	}
}

// resendLast handles MCR: resend the current LCD in display mode, the last
// replayed report item in report mode, nothing when no stream is on.
func (s *Server) resendLast() {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.lastItem
	if s.display {
		item = s.lcd
	}
	if s.reporting && item != nil {
		s.fanOutLocked(item)
	}
}

// emit handles one replayed event unless stop was signalled: a whole
// frame (lcd non-nil) replaces the current LCD, in every mode; report
// traffic is queued to all clients in report mode. Frame events are never
// queued (the display mirror sends the LCD), and report events are
// skipped in display mode; either still used up its time slot.
func (s *Server) emit(data []byte, frame bool, lcd []byte, stop chan struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopReplay != stop { // stopped (or superseded) since we last looked
		return false
	}
	if lcd != nil {
		s.lcd = lcd
	}
	if frame || s.display {
		return true
	}
	s.lastItem = data
	s.fanOutLocked(data)
	return true
}

// replay walks events paced by their T deltas / Speed, looping until stop
// is closed (or, with Once, until the last event); see emit.
func (s *Server) replay(events []fixture.Event, frames []bool, lcds [][]byte, stop chan struct{}) {
	defer s.wg.Done()
	defer s.clockUsers.Add(-1)
	prev := events[0].T
	for {
		for i, ev := range events {
			if !s.sleep(time.Duration(float64(ev.T-prev)*float64(time.Millisecond)/s.cfg.Speed), stop) {
				return
			}
			prev = ev.T
			if !s.emit(ev.Data, frames[i], lcds[i], stop) {
				return
			}
		}
		if s.cfg.Once {
			s.finishReplay(stop)
			return
		}
		if !s.sleep(minLoopPause, stop) {
			return
		}
		prev = events[0].T
	}
}

// finishReplay releases the stream after a Once replay so a later MCr or
// Switch can start a new replayer. Reporting stays on.
func (s *Server) finishReplay(stop chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopReplay == stop {
		s.stopReplay = nil
	}
}

// sleep waits d (if positive) on the server's clock or until stop closes;
// false means stopped.
func (s *Server) sleep(d time.Duration, stop chan struct{}) bool {
	if d <= 0 {
		select {
		case <-stop:
			return false
		default:
			return true
		}
	}
	c, cancel := s.clk.NewTimer(d)
	select {
	case <-stop:
		cancel()
		return false
	case <-c:
		return true
	}
}

// clock is the time source of the replayer and the display mirror. The
// real one is the wall clock; tests substitute one they advance by hand,
// so pacing can be asserted exactly.
type clock interface {
	Now() time.Time
	// NewTimer returns a channel that receives once d has passed, and a
	// cancel func that releases the timer.
	NewTimer(d time.Duration) (<-chan time.Time, func())
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}
