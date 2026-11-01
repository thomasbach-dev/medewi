package sim

import (
	"bufio"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func start(t *testing.T, cfg Config) *Server {
	t.Helper()
	s := New(cfg)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

func dial(t *testing.T, s *Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, bufio.NewReader(c)
}

func readLine(t *testing.T, r *bufio.Reader, c net.Conn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v (got %q)", err, line)
	}
	return line
}

func TestBannerOnConnect(t *testing.T) {
	s := start(t, Config{})
	c, r := dial(t, s)
	if got := readLine(t, r, c); got != "$id: leva!\r\n" {
		t.Fatalf("banner = %q", got)
	}
}

func TestCannedResponseFansOutToAllClients(t *testing.T) {
	s := start(t, Config{Responses: map[string][]string{"MCh": {"one", "two"}}})
	c1, r1 := dial(t, s)
	c2, r2 := dial(t, s)
	readLine(t, r1, c1)
	readLine(t, r2, c2)
	if _, err := c1.Write([]byte("MCh\r\n")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		r *bufio.Reader
		c net.Conn
	}{{r1, c1}, {r2, c2}} {
		if got := readLine(t, p.r, p.c); got != "one\r\n" {
			t.Fatalf("got %q", got)
		}
		if got := readLine(t, p.r, p.c); got != "two\r\n" {
			t.Fatalf("got %q", got)
		}
	}
}

func TestUnknownCommandSendsNothing(t *testing.T) {
	s := start(t, Config{Responses: map[string][]string{"MCh": {"x"}}})
	c, r := dial(t, s)
	readLine(t, r, c)
	_, _ = c.Write([]byte("bogus\nMCh\n"))
	if got := readLine(t, r, c); got != "x\r\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFifthClientRejected(t *testing.T) {
	s := start(t, Config{})
	for range 4 {
		c, r := dial(t, s)
		readLine(t, r, c)
	}
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	if n, err := c.Read(buf); err == nil || n != 0 {
		t.Fatalf("expected immediate close, got n=%d err=%v", n, err)
	}
}

func TestIdleTimeoutCloses(t *testing.T) {
	s := start(t, Config{IdleTimeout: 50 * time.Millisecond})
	c, r := dial(t, s)
	readLine(t, r, c)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := r.ReadByte(); err == nil {
		t.Fatal("expected close")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("server did not close in time")
	}
}

func TestTripwireCounts(t *testing.T) {
	s := start(t, Config{TripwireAddr: "127.0.0.1:0"})
	if s.TripwireHits() != 0 {
		t.Fatal("hits should start at 0")
	}
	c, err := net.Dial("tcp", s.TripwireAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for s.TripwireHits() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.TripwireHits(); got != 1 {
		t.Fatalf("hits = %d", got)
	}
}

func TestStopIdempotentNoLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	s := New(Config{TripwireAddr: "127.0.0.1:0"})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	c, r := dial(t, s)
	readLine(t, r, c)
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := r.ReadByte(); err == nil {
		t.Fatal("client should be closed")
	}
	_ = c.Close()
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d -> %d", before, after)
	}
}

func TestSlowClientDroppedHealthyClientGetsEverything(t *testing.T) {
	const (
		lines    = 300 // larger than the out queue: must not drop a healthy client
		triggers = 1500
		window   = 100 // max unread responses the healthy client may lag
	)
	resp := make([]string, lines)
	for i := range resp {
		resp[i] = fmt.Sprintf("L%03d-%s", i, strings.Repeat("x", 40))
	}
	s := start(t, Config{Responses: map[string][]string{"X": resp}})
	slow, rs := dial(t, s) // never reads after the banner
	fast, rf := dial(t, s)
	readLine(t, rs, slow)
	readLine(t, rf, fast)

	var received atomic.Int64 // complete responses read by the healthy client
	go func() {
		for i := range triggers {
			for int64(i)-received.Load() >= window {
				time.Sleep(time.Millisecond)
			}
			if _, err := fast.Write([]byte("X\n")); err != nil {
				return
			}
		}
	}()
	for r := range triggers {
		for i := range lines {
			want := resp[i] + "\r\n"
			if got := readLine(t, rf, fast); got != want {
				t.Fatalf("response %d line %d = %q, want %q", r, i, got, want)
			}
		}
		received.Add(1)
	}

	// The non-reader must have been dropped: draining it ends in EOF/reset.
	_ = slow.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1<<16)
	for {
		if _, err := slow.Read(buf); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("slow client was not disconnected")
			}
			break
		}
	}
}
