// Package sim implements a fake leva! controller speaking the same plain-text
// TCP protocol as the real firmware, so other components can be developed and
// tested without a machine.
//
// Like the real module, all output is shared "UART" traffic: canned responses
// to any client's command are fanned out to every connected client. The
// banner is a connect greeting and goes only to the new client.
package sim

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
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
	Responses map[string][]string
	// Stream is the recorded traffic for replay. Replay writes Event.Data
	// verbatim (fixtures carry their own CRLF) as one queue item per RX event;
	// TX events are skipped. MCr starts replay, MC<space> stops it, MCR while
	// reporting resends the last item. With no RX events, MCr sets the flag
	// but emits nothing, and MCR sends nothing.
	Stream []fixture.Event
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
	// stopReplay is non-nil exactly while a replayer goroutine owns the
	// stream; closing it (under mu) tells that replayer to exit.
	reporting  bool
	stopReplay chan struct{}
	lastItem   []byte

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
	return &Server{cfg: cfg, clients: make(map[*client]struct{})}
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
	return nil
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
	s.stopReplayLocked()
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
		c.out <- []byte(s.cfg.Banner + "\r\n") // empty queue: cannot block
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
		switch cmd {
		case "MCr":
			s.startReplay()
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

// startReplay handles MCr: set the global flag and, if no replayer runs yet
// and there is something to replay, start exactly one.
func (s *Server) startReplay() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.reporting = true
	if s.stopReplay != nil {
		return
	}
	var events []fixture.Event
	for _, ev := range s.cfg.Stream {
		if ev.Dir == fixture.RX {
			events = append(events, ev)
		}
	}
	if len(events) == 0 {
		return
	}
	stop := make(chan struct{})
	s.stopReplay = stop
	s.wg.Add(1)
	go s.replay(events, stop)
}

// stopReporting handles MC<space>: clear the flag and stop the replayer, for
// every client regardless of who started it.
func (s *Server) stopReporting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reporting = false
	s.stopReplayLocked()
}

// stopReplayLocked signals the current replayer, if any, to exit. s.mu held.
func (s *Server) stopReplayLocked() {
	if s.stopReplay != nil {
		close(s.stopReplay)
		s.stopReplay = nil
	}
}

// resendLast handles MCR: while reporting, resend the last replayed item.
func (s *Server) resendLast() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reporting && s.lastItem != nil {
		s.fanOutLocked(s.lastItem)
	}
}

// emit queues one replayed item to all clients unless stop was signalled.
func (s *Server) emit(data []byte, stop chan struct{}) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopReplay != stop { // stopped (or superseded) since we last looked
		return false
	}
	s.lastItem = data
	s.fanOutLocked(data)
	return true
}

// replay emits events verbatim, paced by their T deltas / Speed, looping until
// stop is closed.
func (s *Server) replay(events []fixture.Event, stop chan struct{}) {
	defer s.wg.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C
	prev := events[0].T
	for {
		for _, ev := range events {
			if !s.sleep(timer, time.Duration(float64(ev.T-prev)*float64(time.Millisecond)/s.cfg.Speed), stop) {
				return
			}
			prev = ev.T
			if !s.emit(ev.Data, stop) {
				return
			}
		}
		if !s.sleep(timer, minLoopPause, stop) {
			return
		}
		prev = events[0].T
	}
}

// sleep waits d (if positive) or until stop closes; false means stopped.
func (s *Server) sleep(timer *time.Timer, d time.Duration, stop chan struct{}) bool {
	if d <= 0 {
		select {
		case <-stop:
			return false
		default:
			return true
		}
	}
	timer.Reset(d)
	select {
	case <-stop:
		if !timer.Stop() {
			<-timer.C
		}
		return false
	case <-timer.C:
		return true
	}
}
