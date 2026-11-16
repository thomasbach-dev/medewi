package sim

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock the test advances by hand: a timer fires only when
// the test moves the time to (or past) its deadline, and Now is exactly
// that deadline while the woken goroutine runs.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*fakeWaiter // in registration order
}

type fakeWaiter struct {
	at time.Time
	c  chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(0, 0)} }

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &fakeWaiter{at: f.now.Add(d), c: make(chan time.Time, 1)}
	if d <= 0 {
		w.c <- f.now
		return w.c, func() {}
	}
	f.waiters = append(f.waiters, w)
	return w.c, func() { f.remove(w) }
}

func (f *fakeClock) remove(w *fakeWaiter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, x := range f.waiters {
		if x == w {
			f.waiters = append(f.waiters[:i], f.waiters[i+1:]...)
			return
		}
	}
}

func (f *fakeClock) pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// fireNext fires the earliest timer due by limit (the first registered on
// a tie) and sets the time to its deadline; with none due it sets the time
// to limit and returns false.
func (f *fakeClock) fireNext(limit time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := -1
	for i, w := range f.waiters {
		if !w.at.After(limit) && (next < 0 || w.at.Before(f.waiters[next].at)) {
			next = i
		}
	}
	if next < 0 {
		if limit.After(f.now) {
			f.now = limit
		}
		return false
	}
	w := f.waiters[next]
	f.waiters = append(f.waiters[:next], f.waiters[next+1:]...)
	f.now = w.at
	w.c <- w.at
	return true
}

// fakeSim is a sim on a fakeClock.
type fakeSim struct {
	*Server
	fc *fakeClock
}

func startFake(t *testing.T, cfg Config) *fakeSim {
	t.Helper()
	s := New(cfg)
	fc := newFakeClock()
	s.clk = fc
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return &fakeSim{Server: s, fc: fc}
}

// settle waits until every replayer and mirror goroutine waits on the fake
// clock (or has exited) and every client queue is drained to its socket:
// nothing more happens until the clock moves.
func (h *fakeSim) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if h.fc.pending() == int(h.clockUsers.Load()) && h.queuesEmpty() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sim did not settle: %d timers, %d clock users", h.fc.pending(), h.clockUsers.Load())
		}
		runtime.Gosched()
	}
}

func (h *fakeSim) queuesEmpty() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if len(c.out) > 0 {
			return false
		}
	}
	return true
}

// advance moves the fake time forward by d, firing every timer due on the
// way one at a time and letting the sim settle after each.
func (h *fakeSim) advance(t *testing.T, d time.Duration) {
	t.Helper()
	h.settle(t)
	limit := h.fc.Now().Add(d)
	for h.fc.fireNext(limit) {
		h.settle(t)
	}
}

// waitFor waits (in real time) until cond holds under the sim's lock: for
// a command sent over TCP to be handled.
func (h *fakeSim) waitFor(t *testing.T, what string, cond func(s *Server) bool) {
	t.Helper()
	waitUntil(t, what, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return cond(h.Server)
	})
	h.settle(t)
}

func mirrorOn(s *Server) bool    { return s.stopMirror != nil }
func replayOn(s *Server) bool    { return s.stopReplay != nil }
func lcdOf(s *Server) string     { return string(s.lcd) }
func replayEnded(s *Server) bool { return s.reporting && s.stopReplay == nil }
