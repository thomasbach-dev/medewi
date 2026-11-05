package leva

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrBootloaderPort is returned by Dial for port 2323. That is the machine's
// bootloader port: the first byte sent to it resets the controller.
var ErrBootloaderPort = errors.New("refusing bootloader port 2323")

// ErrClosed is returned by operations on a Conn that has been closed.
var ErrClosed = errors.New("leva: connection closed")

const (
	bootloaderPort = 2323
	// defaultReportBuf is the default capacity of the reports channel.
	defaultReportBuf = 256
	defaultIdleGap   = 300 * time.Millisecond
	// defaultFirstLine is how long Command waits for the first output line.
	defaultFirstLine = 2 * time.Second
)

// maxLine bounds a single wire line. A longer line is discarded in full (up
// to and including its LF), counted in Oversized, and the reader carries on
// with the next line; it never fails the connection. A variable only so
// tests can shrink it before creating a Conn.
var maxLine = 1 << 20

type config struct {
	idleGap   time.Duration
	firstLine time.Duration
	reportBuf int
}

// Option configures a Conn.
type Option func(*config)

// WithIdleGap sets how long Command waits without new output before it
// considers the response complete (default 300 ms).
func WithIdleGap(d time.Duration) Option { return func(c *config) { c.idleGap = d } }

// WithFirstLineTimeout sets how long Command waits for the first output line
// of a reply before giving up with an empty result (default 2 s). It should be
// well above the idle gap, since the machine can be slow to start answering.
func WithFirstLineTimeout(d time.Duration) Option {
	return func(c *config) { c.firstLine = d }
}

// WithReportBuffer sets the capacity of the reports channel (default 256).
func WithReportBuffer(n int) Option { return func(c *config) { c.reportBuf = n } }

// Conn owns one connection to a leva! controller. A single reader goroutine
// strips the trailing CR+LF from every line (project ruling R2) and
// classifies the stripped line: "{" prefix is a report, "$id:" prefix is a
// banner, anything else is command output. Lines are delivered
// byte-identically otherwise (NUL and Latin-1 bytes included).
type Conn struct {
	nc  net.Conn
	cfg config

	cmdMu sync.Mutex // serialises Command (one collector at a time)
	quiet bool       // guarded by cmdMu: previous Command aborted, reply may be pending
	wmu   sync.Mutex // serialises writes to nc

	mu        sync.Mutex // guards everything below
	id        string
	err       error
	closed    bool
	reporting bool
	collect   bool
	lines     []string
	notify    chan struct{}
	taps      map[int]func(string, []byte)
	nextTap   int

	// reports is created in NewConn and closed (by the reader only) when
	// the reader exits. It is fed only between StartReports and StopReports.
	reports chan []byte
	done    chan struct{} // closed when the reader goroutine has exited

	dropped   atomic.Uint64
	oversized atomic.Uint64
	banners   atomic.Uint64
	frames    atomic.Uint64
}

// Dial connects to a leva! controller's command port. The port is resolved
// and checked BEFORE any connection attempt; port 2323 yields
// ErrBootloaderPort.
func Dial(ctx context.Context, addr string, opts ...Option) (*Conn, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("leva: dial %q: %w", addr, err)
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return nil, fmt.Errorf("leva: dial %q: %w", addr, err)
	}
	if port == bootloaderPort {
		return nil, ErrBootloaderPort
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return NewConn(nc, opts...), nil
}

// NewConn wraps an established net.Conn and starts the reader goroutine.
// The Conn takes ownership of nc; Close closes it. NewConn performs no port
// check; use Dial for real machines.
func NewConn(nc net.Conn, opts ...Option) *Conn {
	cfg := config{idleGap: defaultIdleGap, firstLine: defaultFirstLine, reportBuf: defaultReportBuf}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.reportBuf < 1 {
		cfg.reportBuf = 1
	}
	c := &Conn{
		nc:      nc,
		cfg:     cfg,
		taps:    map[int]func(string, []byte){},
		reports: make(chan []byte, cfg.reportBuf),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// ID returns the last "$id:" banner line seen (without CR+LF), or "".
func (c *Conn) ID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.id
}

// Err returns nil while the connection is healthy and after a local Close.
// After the reader terminates abnormally (peer disconnect, read error) it
// returns the cause.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Dropped returns the number of reports discarded because the reports
// channel was full.
func (c *Conn) Dropped() uint64 { return c.dropped.Load() }

// Oversized returns the number of lines discarded for exceeding the
// maximum line length (1 MiB).
func (c *Conn) Oversized() uint64 { return c.oversized.Load() }

// Banners returns the number of "$id:" banner lines seen so far.
func (c *Conn) Banners() uint64 { return c.banners.Load() }

// Frames returns the number of "<…>" display frames seen so far.
func (c *Conn) Frames() uint64 { return c.frames.Load() }

// WaitReady blocks until the machine is ready to take commands: it has sent
// two banners, or it has sent one and settle has elapsed since WaitReady
// first observed it. The real machine swallows commands sent shortly after
// connect, and its idle banner heartbeat (about 1/s) makes the second banner
// a reliable readiness signal. It returns ctx.Err() when ctx is done first,
// or the connection error if the connection dies.
func WaitReady(ctx context.Context, c *Conn, settle time.Duration) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	var seen time.Time
	for {
		n := c.Banners()
		if n >= 2 {
			return nil
		}
		if n >= 1 {
			if seen.IsZero() {
				seen = time.Now()
			}
			if time.Since(seen) >= settle {
				return nil
			}
		}
		if err := c.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// Tap registers fn to observe all raw traffic. dir is "tx" or "rx". fn is
// called synchronously from the writing caller (tx) or the reader goroutine
// (rx), so it must be quick and must not call back into the Conn. p is only
// valid during the call. The returned func unregisters fn.
func (c *Conn) Tap(fn func(dir string, p []byte)) (remove func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextTap
	c.nextTap++
	c.taps[id] = fn
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.taps, id)
	}
}

func (c *Conn) fireTaps(dir string, p []byte) {
	c.mu.Lock()
	if len(c.taps) == 0 {
		c.mu.Unlock()
		return
	}
	fns := make([]func(string, []byte), 0, len(c.taps))
	for _, fn := range c.taps {
		fns = append(fns, fn)
	}
	c.mu.Unlock()
	for _, fn := range fns {
		fn(dir, p)
	}
}

// tapReader reports raw received bytes to the taps.
type tapReader struct{ c *Conn }

func (t tapReader) Read(p []byte) (int, error) {
	n, err := t.c.nc.Read(p)
	if n > 0 {
		t.c.fireTaps("rx", p[:n])
	}
	return n, err
}

// send writes line + CR+LF. It honours ctx by interrupting a blocked write
// via the write deadline.
func (c *Conn) send(ctx context.Context, line string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	closed, err := c.closed, c.err
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(fired)
		_ = c.nc.SetWriteDeadline(time.Now())
	})
	buf := []byte(line + "\r\n")
	// Tap before writing so a tap sees the tx before the reply's first
	// byte; a failed write is still tapped.
	c.fireTaps("tx", buf)
	_, werr := c.nc.Write(buf)
	if !stop() {
		// The cancel hook has started; stop() does not wait for it. Wait
		// until it has finished before clearing, otherwise it could set
		// its past deadline after our clear and wedge every later write.
		<-fired
		_ = c.nc.SetWriteDeadline(time.Time{})
		// A ctx error beats the resulting timeout.
		if werr != nil && ctx.Err() != nil {
			werr = ctx.Err()
		}
	}
	if werr != nil {
		return fmt.Errorf("leva: write: %w", werr)
	}
	return nil
}

// Command sends cmd and returns the command-output lines (CR+LF stripped;
// no reports, no banners). Collection is two-phase: first Command waits up to
// the first-line timeout (WithFirstLineTimeout, default 2 s) for the first
// output line; once one has arrived it collects further lines until no new
// line has arrived for the idle gap. If no line arrives within the first-line
// timeout, Command returns an empty slice and a nil error, so a command that
// produces no output costs the full first-line timeout. Reports and banners that interleave are routed to their own
// consumers and do not extend the gap. Command calls are serialised. Lines
// arriving while no Command is in flight are discarded. If the connection
// dies mid-command, the lines collected so far are returned together with
// the error.
//
// If a Command is aborted by its context after the command was written, the
// machine's reply may still arrive. The next Command therefore first waits
// for one idle gap of silence, discarding whatever command output arrives,
// before sending. Residual risk: a reply that is delayed by more than the
// idle gap can still leak into a later Command's result; that is inherent
// to idle-gap collection.
func (c *Conn) Command(ctx context.Context, cmd string) ([]string, error) {
	return c.CommandOpts(ctx, cmd, c.cfg.firstLine, c.cfg.idleGap)
}

// CommandOpts is Command with per-call timings: firstLine bounds the wait for
// the first output line and idleGap ends collection after that. Use it for
// replies that pause mid-stream (long settings dumps). The quiet-drain before
// sending still uses the configured idle gap.
func (c *Conn) CommandOpts(ctx context.Context, cmd string, firstLine, idleGap time.Duration) ([]string, error) {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()

	notify := make(chan struct{}, 1)
	c.mu.Lock()
	c.collect, c.lines, c.notify = true, nil, notify
	c.mu.Unlock()
	take := func() []string {
		c.mu.Lock()
		defer c.mu.Unlock()
		l := c.lines
		c.collect, c.lines, c.notify = false, nil, nil
		return l
	}
	// fail maps a waitIdle outcome to Command's result.
	fail := func(err error) ([]string, error) {
		lines := take()
		if err == errConnDone {
			if e := c.Err(); e != nil {
				return lines, e
			}
			return lines, ErrClosed
		}
		return nil, err
	}

	if c.quiet {
		if err := c.waitIdle(ctx, notify, c.cfg.idleGap, c.cfg.idleGap); err != nil {
			return fail(err)
		}
		c.quiet = false
		c.mu.Lock()
		c.lines = nil
		// Drop tokens for the discarded lines so phase 1 below only reacts to
		// output of this command. Under mu, so no new token is lost.
		select {
		case <-notify:
		default:
		}
		c.mu.Unlock()
	}

	if err := c.send(ctx, cmd); err != nil {
		take()
		c.quiet = true // the write may have been partially delivered
		return nil, err
	}
	if err := c.waitIdle(ctx, notify, firstLine, idleGap); err != nil {
		if err != errConnDone {
			c.quiet = true // command sent, reply still pending
		}
		return fail(err)
	}
	return take(), nil
}

var errConnDone = errors.New("leva: reader finished")

// waitIdle blocks until no notification arrived for one idle gap (nil), ctx
// is done (ctx.Err()) or the reader exited (errConnDone). The wait for the
// first notification is bounded by first instead of gap.
func (c *Conn) waitIdle(ctx context.Context, notify <-chan struct{}, first, gap time.Duration) error {
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-notify:
			timer.Reset(gap)
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return errConnDone
		}
	}
}

// StartReports sends MCr and returns the reports channel. It carries raw
// report lines (starting "{", CR+LF stripped) and is closed when the
// connection ends; check Err for the cause. The channel is buffered
// (WithReportBuffer). If the consumer falls behind and the buffer is full,
// the newest report is dropped and counted (Dropped); the reader never
// blocks on the consumer. Calling StartReports again returns the same
// channel.
func (c *Conn) StartReports(ctx context.Context) (<-chan []byte, error) {
	c.mu.Lock()
	c.reporting = true
	c.mu.Unlock()
	if err := c.send(ctx, "MCr"); err != nil {
		c.mu.Lock()
		c.reporting = false
		c.mu.Unlock()
		return nil, err
	}
	return c.reports, nil
}

// StartDisplay sends "MC@": the machine then mirrors its LCD as one
// "<BKL…>" frame line every 100 ms until "MC " (StopReports) or "MCr"
// (StartReports). The two streams are mutually exclusive: MC@ replaces the
// report stream and MCr replaces the display. Frames are only visible to
// taps; they never count as command replies, so Command keeps working while
// the display streams. Never send the display protocol's button codes ("MC"
// + raw byte).
func (c *Conn) StartDisplay(ctx context.Context) error {
	return c.send(ctx, "MC@")
}

// StopReports sends "MC " (MC + space), which resets the machine's state and
// stops the report stream (and the display stream, see StartDisplay). Reports still in flight afterwards are discarded.
func (c *Conn) StopReports(ctx context.Context) error {
	c.mu.Lock()
	c.reporting = false
	c.mu.Unlock()
	return c.send(ctx, "MC ")
}

// Close closes the connection, waits for the reader goroutine to exit and
// closes the reports channel. It is idempotent and safe after a peer
// disconnect. Err is nil after a local Close.
func (c *Conn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	err := c.nc.Close()
	<-c.done
	if err != nil && errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// readLoop is the only reader of nc and the only closer of c.reports.
func (c *Conn) readLoop() {
	defer close(c.done)
	defer close(c.reports)

	br := bufio.NewReaderSize(tapReader{c}, 4096)
	var acc []byte
	oversize := false
	for {
		frag, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull || err == nil {
			if !oversize {
				if len(acc)+len(frag) > maxLine {
					oversize, acc = true, nil
				} else {
					acc = append(acc, frag...)
				}
			}
			if err == bufio.ErrBufferFull {
				continue
			}
			if oversize {
				c.oversized.Add(1)
			} else {
				c.dispatch(acc)
			}
			acc, oversize = acc[:0], false
			continue
		}
		// Read error or EOF. A trailing partial line (no LF) is discarded.
		c.mu.Lock()
		if !c.closed {
			c.err = fmt.Errorf("leva: connection lost: %w", err)
		}
		c.mu.Unlock()
		return
	}
}

// dispatch strips the trailing LF and CR from raw and routes the line.
// raw is reused by the caller, so anything retained is copied.
func (c *Conn) dispatch(raw []byte) {
	line := bytes.TrimSuffix(raw, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	switch {
	case bytes.HasPrefix(line, []byte("{")):
		c.mu.Lock()
		deliver := c.reporting
		c.mu.Unlock()
		if !deliver {
			return
		}
		select {
		case c.reports <- bytes.Clone(line):
		default:
			c.dropped.Add(1)
		}
	case bytes.HasPrefix(line, []byte("$id:")):
		c.mu.Lock()
		c.id = string(line)
		c.mu.Unlock()
		c.banners.Add(1)
	case bytes.HasPrefix(line, []byte("<")):
		c.frames.Add(1)
	default:
		c.mu.Lock()
		if c.collect {
			c.lines = append(c.lines, string(line))
			select {
			case c.notify <- struct{}{}:
			default:
			}
		}
		c.mu.Unlock()
	}
}
