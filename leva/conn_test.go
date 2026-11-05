package leva

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testGap = 30 * time.Millisecond

// fakeServer is the machine side of a net.Pipe. It records everything the
// client sends and lets tests write arbitrary byte chunks.
type fakeServer struct {
	nc net.Conn
}

func newPair(t *testing.T, opts ...Option) (*Conn, *fakeServer) {
	t.Helper()
	cl, sv := net.Pipe()
	c := NewConn(cl, append([]Option{WithIdleGap(testGap)}, opts...)...)
	t.Cleanup(func() {
		_ = c.Close()
		_ = sv.Close()
	})
	return c, &fakeServer{nc: sv}
}

// readLine reads up to and including the next LF from the client.
func (s *fakeServer) readLine(t *testing.T) string {
	t.Helper()
	var out []byte
	b := make([]byte, 1)
	_ = s.nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, err := s.nc.Read(b); err != nil {
			t.Fatalf("server read: %v (got %q)", err, out)
		}
		out = append(out, b[0])
		if b[0] == '\n' {
			return string(out)
		}
	}
}

func (s *fakeServer) write(t *testing.T, p string) {
	t.Helper()
	_ = s.nc.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := s.nc.Write([]byte(p)); err != nil {
		t.Fatalf("server write: %v", err)
	}
}

func recvReport(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case r, ok := <-ch:
		if !ok {
			t.Fatal("reports channel closed")
		}
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for report")
		return nil
	}
}

func TestDialRefuses2323(t *testing.T) {
	// If the guard were missing, a listener on 2323 would receive a
	// connection; try to bind one so the test can prove nothing connects.
	var accepted chan struct{}
	if ln, err := net.Listen("tcp", "127.0.0.1:2323"); err == nil {
		accepted = make(chan struct{}, 1)
		defer func() { _ = ln.Close() }()
		go func() {
			if c, err := ln.Accept(); err == nil {
				accepted <- struct{}{}
				_ = c.Close()
			}
		}()
	}

	t.Setenv("MEDEWI_MACHINE_ADDR", "127.0.0.1:2323")
	for _, addr := range []string{
		"host:2323",
		"127.0.0.1:2323",
		"[::1]:2323",
		"rancilio:02323",
		os.Getenv("MEDEWI_MACHINE_ADDR"),
	} {
		c, err := Dial(context.Background(), addr)
		if !errors.Is(err, ErrBootloaderPort) {
			t.Errorf("Dial(%q) err = %v, want ErrBootloaderPort", addr, err)
		}
		if c != nil {
			t.Errorf("Dial(%q) returned a conn", addr)
		}
	}
	if accepted != nil {
		select {
		case <-accepted:
			t.Fatal("a connection attempt reached port 2323")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestDialRejectsBadAddr(t *testing.T) {
	if _, err := Dial(context.Background(), "no-port"); err == nil {
		t.Fatal("want error for address without port")
	}
}

func TestDialConnects(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("$id: leva!\r\n"))
		_ = c.Close()
	}()
	c, err := Dial(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	<-c.done
	if got := c.ID(); got != "$id: leva!" {
		t.Errorf("ID = %q", got)
	}
}

func TestCommandCollectsResponse(t *testing.T) {
	c, s := newPair(t)
	go func() {
		if got := s.readLine(t); got != "MCh\r\n" {
			t.Errorf("tx = %q, want %q", got, "MCh\r\n")
		}
		s.write(t, "first\r\nsecond\r\n")
	}()
	got, err := c.Command(context.Background(), "MCh")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCommandContextCancel(t *testing.T) {
	c, s := newPair(t, WithIdleGap(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		s.readLine(t)
		s.write(t, "partial\r\n")
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := c.Command(ctx, "MCh")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestDemuxInterleaved(t *testing.T) {
	c, s := newPair(t)
	go func() {
		s.readLine(t) // MCr
	}()
	reports, err := c.StartReports(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		s.readLine(t) // MCh
		s.write(t, "line1\r\n{r1}\r\nline2\r\n$id: leva!\r\n{r2}\r\nline3\r\n")
	}()
	got, err := c.Command(context.Background(), "MCh")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"line1", "line2", "line3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("command lines = %q, want %q", got, want)
	}
	for _, want := range []string{"{r1}", "{r2}"} {
		if r := recvReport(t, reports); string(r) != want {
			t.Errorf("report = %q, want %q", r, want)
		}
	}
	if id := c.ID(); id != "$id: leva!" {
		t.Errorf("ID = %q", id)
	}
	select {
	case r := <-reports:
		t.Errorf("unexpected extra report %q", r)
	default:
	}
}

func TestReportsBinarySafe(t *testing.T) {
	c, s := newPair(t)
	go func() { s.readLine(t) }()
	reports, err := c.StartReports(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := "{1.5;\x00;\xdf;\xe4;x}"
	s.write(t, raw+"\r\n")
	if r := recvReport(t, reports); !bytes.Equal(r, []byte(raw)) {
		t.Errorf("report = %q, want %q", r, raw)
	}
}

func TestSplitCRLFAcrossReads(t *testing.T) {
	c, s := newPair(t)
	go func() { s.readLine(t) }()
	reports, err := c.StartReports(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"{a", "b}\r", "\n{c}", "\r", "\n"} {
		s.write(t, chunk)
		time.Sleep(5 * time.Millisecond)
	}
	for _, want := range []string{"{ab}", "{c}"} {
		if r := recvReport(t, reports); string(r) != want {
			t.Errorf("report = %q, want %q", r, want)
		}
	}
}

func TestOversizedLineDropped(t *testing.T) {
	old := maxLine
	maxLine = 8192
	t.Cleanup(func() { maxLine = old })
	c, s := newPair(t)
	go func() {
		s.readLine(t)
		s.write(t, strings.Repeat("x", maxLine+10)+"\r\nok\r\n")
	}()
	got, err := c.Command(context.Background(), "MCh")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ok"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %d lines (first %.20q), want [ok]", len(got), got)
	}
	if c.Oversized() != 1 {
		t.Errorf("Oversized = %d, want 1", c.Oversized())
	}
}

func TestReportOverflowDrops(t *testing.T) {
	c, s := newPair(t, WithReportBuffer(2))
	go func() { s.readLine(t) }()
	reports, err := c.StartReports(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s.write(t, "{1}\r\n{2}\r\n{3}\r\n{4}\r\n{5}\r\n")
	deadline := time.Now().Add(2 * time.Second)
	for c.Dropped() != 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.Dropped() != 3 {
		t.Fatalf("Dropped = %d, want 3", c.Dropped())
	}
	// Oldest reports are kept; newest are dropped.
	for _, want := range []string{"{1}", "{2}"} {
		if r := recvReport(t, reports); string(r) != want {
			t.Errorf("report = %q, want %q", r, want)
		}
	}
}

func TestStopReportsSendsMCSpace(t *testing.T) {
	c, s := newPair(t)
	got := make(chan string, 1)
	go func() { got <- s.readLine(t) }()
	if err := c.StopReports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g := <-got; g != "MC \r\n" {
		t.Errorf("tx = %q", g)
	}
}

func TestPeerDisconnectClosesReports(t *testing.T) {
	c, s := newPair(t)
	go func() { s.readLine(t) }()
	reports, err := c.StartReports(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Err() != nil {
		t.Fatalf("Err before disconnect = %v", c.Err())
	}
	_ = s.nc.Close()

	select {
	case _, ok := <-reports:
		if ok {
			t.Fatal("unexpected report")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reports channel not closed after peer disconnect")
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("reader goroutine did not exit")
	}
	if c.Err() == nil {
		t.Error("Err() = nil after peer disconnect")
	}
	if _, err := c.Command(context.Background(), "MCh"); err == nil {
		t.Error("Command on dead conn should fail")
	}
	// Close after peer disconnect is safe and idempotent.
	_ = c.Close()
	_ = c.Close()
}

func TestCloseTerminatesReader(t *testing.T) {
	c, _ := newPair(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("reader goroutine did not exit after Close")
	}
	if err := c.Err(); err != nil {
		t.Errorf("Err after local Close = %v, want nil", err)
	}
}

func TestTapSeesBothDirections(t *testing.T) {
	c, s := newPair(t)
	var mu sync.Mutex
	var tx, rx []byte
	remove := c.Tap(func(dir string, p []byte) {
		mu.Lock()
		defer mu.Unlock()
		switch dir {
		case "tx":
			tx = append(tx, p...)
		case "rx":
			rx = append(rx, p...)
		default:
			t.Errorf("bad dir %q", dir)
		}
	})
	go func() {
		s.readLine(t)
		s.write(t, "a\r\n{r}\r\n")
	}()
	if _, err := c.Command(context.Background(), "MCh"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if string(tx) != "MCh\r\n" || string(rx) != "a\r\n{r}\r\n" {
		t.Errorf("tx=%q rx=%q", tx, rx)
	}
	tx, rx = nil, nil
	mu.Unlock()

	remove()
	go func() {
		s.readLine(t)
		s.write(t, "b\r\n")
	}()
	if _, err := c.Command(context.Background(), "MCh"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tx) != 0 || len(rx) != 0 {
		t.Errorf("tap fired after remove: tx=%q rx=%q", tx, rx)
	}
}

func TestCancelDuringSendDoesNotWedge(t *testing.T) {
	cl, sv := net.Pipe()
	c := NewConn(cl, WithIdleGap(testGap))
	t.Cleanup(func() { _ = c.Close(); _ = sv.Close() })
	go func() { _, _ = io.Copy(io.Discard, sv) }()

	for i := range 300 {
		ctx, cancel := context.WithCancel(context.Background())
		go cancel()
		_ = c.send(ctx, "MC ") // may succeed or fail; must not leave a stale deadline
		cancel()
		if err := c.send(context.Background(), "MC "); err != nil {
			t.Fatalf("iteration %d: send after cancel failed: %v", i, err)
		}
	}
}

func TestCancelledCommandReplyDoesNotLeak(t *testing.T) {
	c, s := newPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		s.readLine(t)
		cancel()
		time.Sleep(testGap / 3)
		s.write(t, "late\r\n")
	}()
	if _, err := c.Command(ctx, "MCh"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	go func() {
		s.readLine(t)
		s.write(t, "fresh\r\n")
	}()
	got, err := c.Command(context.Background(), "MCh")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"fresh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCommandWaitsForSlowFirstLine(t *testing.T) {
	c, s := newPair(t, WithFirstLineTimeout(2*time.Second))
	go func() {
		s.readLine(t)
		time.Sleep(3 * testGap)
		s.write(t, "slow\r\nsecond\r\n")
	}()
	got, err := c.Command(context.Background(), "MCh")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"slow", "second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCommandNoOutputBounded(t *testing.T) {
	const first = 100 * time.Millisecond
	c, s := newPair(t, WithFirstLineTimeout(first))
	go func() { s.readLine(t) }()
	start := time.Now()
	got, err := c.Command(context.Background(), "MC ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %q, want no lines", got)
	}
	if d := time.Since(start); d < first || d > 1500*time.Millisecond {
		t.Errorf("took %v, want about %v", d, first)
	}
}

func TestCommandFirstLineWaitHonoursContext(t *testing.T) {
	c, s := newPair(t, WithFirstLineTimeout(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		s.readLine(t)
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := c.Command(ctx, "MCh")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("cancel took %v", d)
	}
}

func TestWaitReadyTwoBannersFast(t *testing.T) {
	c, sv := newPair(t)
	go func() {
		_, _ = sv.nc.Write([]byte("$id: leva!\r\n"))
		time.Sleep(100 * time.Millisecond)
		_, _ = sv.nc.Write([]byte("$id: leva!\r\n"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := WaitReady(ctx, c, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v, want well under settle", d)
	}
	if c.Banners() < 2 {
		t.Errorf("Banners=%d", c.Banners())
	}
}

func TestWaitReadySingleBannerSettles(t *testing.T) {
	c, sv := newPair(t)
	go func() { _, _ = sv.nc.Write([]byte("$id: leva!\r\n")) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	settle := 300 * time.Millisecond
	start := time.Now()
	if err := WaitReady(ctx, c, settle); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < settle {
		t.Errorf("returned after %v, before settle %v", d, settle)
	}
}

func TestWaitReadyNoBannerDeadline(t *testing.T) {
	c, _ := newPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := WaitReady(ctx, c, 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline exceeded", err)
	}
}

func TestCommandOptsLongGap(t *testing.T) {
	reply := func(s *fakeServer) {
		s.readLine(t)
		s.write(t, "a\r\n")
		time.Sleep(3 * testGap)
		// In the Command case nobody may be reading any more; a failed late
		// write must not fail the (finished) test.
		_ = s.nc.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = s.nc.Write([]byte("b\r\n"))
	}
	t.Run("long idle gap bridges the pause", func(t *testing.T) {
		c, s := newPair(t)
		go reply(s)
		got, err := c.CommandOpts(context.Background(), "MCm", time.Second, 5*testGap)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Command keeps the configured gap", func(t *testing.T) {
		c, s := newPair(t)
		go reply(s)
		got, err := c.Command(context.Background(), "MCm")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"a"}; !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestStartDisplaySendsMCAt(t *testing.T) {
	c, s := newPair(t)
	got := make(chan string, 1)
	go func() { got <- s.readLine(t) }()
	if err := c.StartDisplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g := <-got; g != "MC@\r\n" {
		t.Errorf("tx = %q", g)
	}
}

// Display frames arrive every 100 ms on the real machine, faster than the
// idle gap: they must neither end up in a reply nor keep Command's
// pre-send drain from ever seeing a quiet line.
func TestDisplayFramesNotCommandReplies(t *testing.T) {
	c, s := newPair(t)
	go func() { s.readLine(t) }() // MC@
	if err := c.StartDisplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(testGap / 3):
				_, _ = s.nc.Write([]byte("<002SOAK 1.0b       1: 91.0\xdf     PID|             4% 0.0b\x05  0s   0ml>\r\n"))
			}
		}
	}()
	go func() {
		s.readLine(t) // MC?
		s.write(t, "XML=x\r\n")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := c.Command(ctx, "MC?")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"XML=x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("command lines = %q, want %q", got, want)
	}
	if c.Frames() == 0 {
		t.Error("Frames() = 0, want frames counted")
	}
}

// A tap must see a command's tx before the first byte of its reply: the
// raw traffic log keys reply handling on the tx it saw last.
func TestTapTXBeforeReply(t *testing.T) {
	c, s := newPair(t)
	var mu sync.Mutex
	var seq []string
	c.Tap(func(dir string, _ []byte) {
		mu.Lock()
		defer mu.Unlock()
		seq = append(seq, dir)
	})
	const rounds = 50
	go func() {
		for range rounds {
			s.readLine(t)
			s.write(t, "XML=test\r\n")
		}
	}()
	for i := range rounds {
		mu.Lock()
		seq = append(seq, "round")
		mu.Unlock()
		if _, err := c.Command(context.Background(), "MC?"); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	state := ""
	for _, d := range seq {
		switch d {
		case "round":
			state = "round"
		case "tx":
			state = "tx"
		case "rx":
			if state == "round" {
				t.Fatalf("rx before this round's tx (state %q)", state)
			}
			state = "rx"
		}
	}
}
