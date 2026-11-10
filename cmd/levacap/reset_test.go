package main

import (
	"bufio"
	"bytes"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/sim"
)

// leaveStreamOn plays another device: it turns reporting on and hangs up
// without "MC ", so the global stream stays on and the banners stay silent.
func leaveStreamOn(t *testing.T, s *sim.Server) {
	t.Helper()
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("MCr\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("no report after MCr: %v", err)
		}
		if strings.HasPrefix(line, "{") {
			return
		}
	}
}

func TestCaptureStreamOnExitsWithHint(t *testing.T) {
	s := startSim(t, sim.Config{})
	leaveStreamOn(t, s)
	var out, errb bytes.Buffer
	code := run([]string{"capture", "--scenario", "commands", "--addr", s.Addr().String(), "--out", t.TempDir()},
		strings.NewReader(""), &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	for _, want := range []string{"no $id banner", "another device", "levacap session-reset"} {
		if !strings.Contains(strings.ToLower(errb.String()), want) {
			t.Errorf("stderr %q lacks %q", errb.String(), want)
		}
	}
	if got := s.Received(); !slices.Equal(got, []string{"MCr"}) {
		t.Errorf("sim received %q: capture must send nothing", got)
	}
}

func TestSessionResetStopsLeftoverStream(t *testing.T) {
	s := startSim(t, sim.Config{})
	leaveStreamOn(t, s)
	var out, errb bytes.Buffer
	if code := run([]string{"session-reset", "--addr", s.Addr().String()}, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if got := s.Received(); !slices.Equal(got, []string{"MCr", "MC "}) {
		t.Errorf("sim received %q, want the leftover MCr and one \"MC \"", got)
	}
	if !strings.Contains(out.String(), "banner is back") {
		t.Errorf("stdout %q does not confirm the banner", out.String())
	}
}

func TestSessionResetIdleSendsNothing(t *testing.T) {
	s := startSim(t, sim.Config{})
	var out, errb bytes.Buffer
	if code := run([]string{"session-reset", "--addr", s.Addr().String()}, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if got := s.Received(); len(got) != 0 {
		t.Errorf("sim received %q, want nothing", got)
	}
	if !strings.Contains(out.String(), "nothing sent") {
		t.Errorf("stdout %q", out.String())
	}
}

// fakeMachine streams report lines (if stream) to one client, never sends a
// banner, ignores every command and records what it receives.
type fakeMachine struct {
	ln   net.Listener
	mu   sync.Mutex
	got  []byte
	done chan struct{}
}

func startFake(t *testing.T, stream bool) *fakeMachine {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeMachine{ln: ln, done: make(chan struct{})}
	t.Cleanup(func() { close(f.done); _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		go func() {
			buf := make([]byte, 64)
			for {
				n, err := c.Read(buf)
				f.mu.Lock()
				f.got = append(f.got, buf[:n]...)
				f.mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-f.done:
				return
			case <-tick.C:
			}
			if stream {
				if _, err := c.Write(synthLine(" 93.50")); err != nil {
					return
				}
			}
		}
	}()
	return f
}

func (f *fakeMachine) received() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.got)
}

// Another device still connected turns its stream straight back on, which
// this fake models by ignoring "MC ": the reset must not claim success.
func TestSessionResetStreamKeepsRunning(t *testing.T) {
	f := startFake(t, true)
	var out, errb bytes.Buffer
	if code := run([]string{"session-reset", "--addr", f.ln.Addr().String()}, nil, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1; stdout %s", code, out.String())
	}
	if got := f.received(); got != "MC \r\n" {
		t.Errorf("fake received %q, want exactly one \"MC \"", got)
	}
	if !strings.Contains(errb.String(), "another device") {
		t.Errorf("stderr %q", errb.String())
	}
}

func TestSessionResetSilentMachineSendsNothing(t *testing.T) {
	f := startFake(t, false)
	var out, errb bytes.Buffer
	if code := run([]string{"session-reset", "--addr", f.ln.Addr().String()}, nil, &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if got := f.received(); got != "" {
		t.Errorf("fake received %q, want nothing", got)
	}
	if !strings.Contains(errb.String(), "nothing received") {
		t.Errorf("stderr %q", errb.String())
	}
}
