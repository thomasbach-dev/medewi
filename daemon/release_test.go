package daemon

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
)

// acceptCounter listens on a free local port and counts (then closes) every
// accepted connection; the sim has no connection counter.
func acceptCounter(t *testing.T) (addr string, n func() int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	var hits atomic.Int64
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			_ = c.Close()
		}
	}()
	return l.Addr().String(), func() int { return int(hits.Load()) }
}

func shrinkBannerTimeout(t *testing.T) { shrink(t, &releaseBannerTimeout, 500*time.Millisecond) }

func lastCmd(cmds []string) string {
	if len(cmds) == 0 {
		return ""
	}
	return cmds[len(cmds)-1]
}

func waitReconnected(t *testing.T, d *Daemon) {
	t.Helper()
	waitFor(t, "reconnected, spec ok", func() bool {
		st := d.Status()
		return st.Connected && st.SpecOK
	})
}

func TestReleaseClosesSession(t *testing.T) {
	shrinkBannerTimeout(t)
	s := startSim(t, simConfig(idleLines(20)))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)

	until := time.Now().Add(time.Hour)
	res, err := d.Release(reqCtx(t), until)
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if res.Banner == nil || !*res.Banner {
		t.Errorf("Banner = %v, want true", res.Banner)
	}
	if !res.Until.Equal(until) {
		t.Errorf("Until = %v, want %v", res.Until, until)
	}
	st := d.Status()
	if st.Connected {
		t.Error("still connected after Release returned")
	}
	if st.ReleasedUntil == nil || !st.ReleasedUntil.Equal(until) {
		t.Errorf("ReleasedUntil = %v, want %v", st.ReleasedUntil, until)
	}
	rcv := s.Received()
	if lastCmd(rcv) != "MC " {
		t.Errorf("last command %q, want \"MC \": %q", lastCmd(rcv), rcv)
	}
	time.Sleep(300 * time.Millisecond)
	if again := s.Received(); !slices.Equal(again, rcv) {
		t.Errorf("sent while released: %q after %q", again[len(rcv):], rcv)
	}
	assertOnlyAllowed(t, s)
}

func TestReleaseMidShotTruncatedReleased(t *testing.T) {
	shrinkBannerTimeout(t)
	s := startSim(t, simConfig(pumpLines(20)))
	st := openStore(t)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), st))
	waitFor(t, "brew open", func() bool { return d.Status().BrewOpen })

	if _, err := d.Release(reqCtx(t), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Release: %v", err)
	}
	bs := brews(t, st)
	if len(bs) != 1 || bs[0].Kind != string(brew.TruncatedReleased) || bs[0].EndedAt == nil {
		t.Fatalf("brews = %+v, want one truncated-released with EndedAt", bs)
	}
}

func TestReleaseExpiresAndReconnects(t *testing.T) {
	shrinkBannerTimeout(t)
	s := startSim(t, simConfig(idleLines(20)))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)

	if _, err := d.Release(reqCtx(t), time.Now().Add(300*time.Millisecond)); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if d.Status().Connected {
		t.Error("still connected after Release returned")
	}
	waitReconnected(t, d)
	if st := d.Status(); st.ReleasedUntil != nil {
		t.Errorf("ReleasedUntil = %v after expiry, want nil", st.ReleasedUntil)
	}
}

func TestReclaim(t *testing.T) {
	shrinkBannerTimeout(t)
	s := startSim(t, simConfig(idleLines(20)))
	cfg := daemonConfig(s.Addr().String(), openStore(t))
	// Only the wake-up can explain a reconnect faster than this.
	cfg.BackoffMin, cfg.BackoffMax = time.Minute, time.Minute
	d, _ := runDaemon(t, cfg)
	waitActive(t, d)

	if err := d.Reclaim(reqCtx(t)); err != nil {
		t.Fatalf("Reclaim with nothing released: %v", err)
	}
	if _, err := d.Release(reqCtx(t), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if d.Status().Connected {
		t.Fatal("still connected after Release returned")
	}
	if err := d.Reclaim(reqCtx(t)); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if st := d.Status(); st.ReleasedUntil != nil {
		t.Errorf("ReleasedUntil = %v after Reclaim, want nil", st.ReleasedUntil)
	}
	waitReconnected(t, d)
	if _, ok, err := cfg.Store.Release(context.Background()); err != nil || ok {
		t.Errorf("stored release after Reclaim: ok %v err %v, want none", ok, err)
	}
}

func TestReleaseReplacesUntil(t *testing.T) {
	shrinkBannerTimeout(t)
	s := startSim(t, simConfig(idleLines(20)))
	st := openStore(t)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), st))
	waitActive(t, d)

	if _, err := d.Release(reqCtx(t), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Release +1h: %v", err)
	}
	later := time.Now().Add(2 * time.Hour)
	res, err := d.Release(reqCtx(t), later)
	if err != nil {
		t.Fatalf("Release +2h: %v", err)
	}
	if res.Banner != nil {
		t.Errorf("Banner = %v with no session up, want nil", *res.Banner)
	}
	if got := d.Status().ReleasedUntil; got == nil || !got.Equal(later) {
		t.Errorf("ReleasedUntil = %v, want %v", got, later)
	}
	if got, ok, err := st.Release(context.Background()); err != nil || !ok || !got.Equal(later) {
		t.Errorf("stored release = %v %v %v, want %v", got, ok, err, later)
	}
	if _, err := d.Release(reqCtx(t), time.Now().Add(300*time.Millisecond)); err != nil {
		t.Fatalf("Release +300ms: %v", err)
	}
	waitReconnected(t, d)
}

func TestReleaseRefusals(t *testing.T) {
	shrinkBannerTimeout(t)
	s := startSim(t, simConfig(idleLines(20)))
	cfg, _, _ := rawDaemonConfig(t, s.Addr().String())
	d, _ := runDaemon(t, cfg)
	waitActive(t, d)

	if _, err := d.Release(reqCtx(t), time.Now().Add(-time.Second)); !errors.Is(err, ErrInvalidRelease) {
		t.Fatalf("Release in the past = %v, want ErrInvalidRelease", err)
	}
	if st := d.Status(); !st.Connected || st.ReleasedUntil != nil {
		t.Fatalf("status = %+v after a refused release, want connected and not released", st)
	}
	if _, err := d.Release(reqCtx(t), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := d.SetPower(reqCtx(t), PowerEco); !errors.Is(err, ErrReleased) || !strings.Contains(err.Error(), "until") {
		t.Errorf("SetPower while released = %v, want ErrReleased naming the end", err)
	}
	if _, err := d.DumpMCu(reqCtx(t)); !errors.Is(err, ErrReleased) {
		t.Errorf("DumpMCu while released = %v, want ErrReleased", err)
	}
	if err := d.Mark("x"); !errors.Is(err, ErrReleased) {
		t.Errorf("Mark while released = %v, want ErrReleased", err)
	}
	assertOnlyAllowed(t, s)
}

func TestReleaseStoreFailure(t *testing.T) {
	shrinkBannerTimeout(t)
	t.Run("release", func(t *testing.T) {
		s := startSim(t, simConfig(idleLines(20)))
		st := openStore(t)
		d, _ := runDaemon(t, daemonConfig(s.Addr().String(), st))
		waitActive(t, d)
		_ = st.Close()

		if _, err := d.Release(reqCtx(t), time.Now().Add(time.Hour)); !errors.Is(err, ErrReleaseStore) {
			t.Fatalf("Release with a closed store = %v, want ErrReleaseStore", err)
		}
		if st := d.Status(); !st.Connected || st.ReleasedUntil != nil {
			t.Errorf("status = %+v, want still connected and not released", st)
		}
	})
	t.Run("reclaim", func(t *testing.T) {
		s := startSim(t, simConfig(idleLines(20)))
		st := openStore(t)
		d, _ := runDaemon(t, daemonConfig(s.Addr().String(), st))
		waitActive(t, d)
		if _, err := d.Release(reqCtx(t), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Release: %v", err)
		}
		_ = st.Close()

		if err := d.Reclaim(reqCtx(t)); !errors.Is(err, ErrReleaseStore) {
			t.Fatalf("Reclaim with a closed store = %v, want ErrReleaseStore", err)
		}
		time.Sleep(200 * time.Millisecond)
		if st := d.Status(); st.Connected || st.ReleasedUntil == nil {
			t.Errorf("status = %+v, want still released", st)
		}
	})
}

func TestReleasePersistsAcrossRestart(t *testing.T) {
	t.Run("future", func(t *testing.T) {
		addr, n := acceptCounter(t)
		st := openStore(t)
		if err := st.SetRelease(context.Background(), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		d, _ := runDaemon(t, daemonConfig(addr, st))
		time.Sleep(500 * time.Millisecond)
		if got := n(); got != 0 {
			t.Errorf("%d connections while released, want 0", got)
		}
		if d.Status().ReleasedUntil == nil {
			t.Error("ReleasedUntil not loaded from the store")
		}
	})
	t.Run("expired", func(t *testing.T) {
		s := startSim(t, simConfig(idleLines(20)))
		st := openStore(t)
		if err := st.SetRelease(context.Background(), time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		d, _ := runDaemon(t, daemonConfig(s.Addr().String(), st))
		waitActive(t, d)
		if got := d.Status().ReleasedUntil; got != nil {
			t.Errorf("ReleasedUntil = %v for an expired release, want nil", got)
		}
	})
}

// An unreadable release state must not lead to a dial: Run fails closed.
func TestReleaseUnreadableFailsClosed(t *testing.T) {
	addr, n := acceptCounter(t)
	st := openStore(t)
	_ = st.Close()
	d := New(daemonConfig(addr, st))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Run(ctx); err == nil {
		t.Fatal("Run with an unreadable release state returned nil")
	}
	if got := n(); got != 0 {
		t.Errorf("%d connections, want 0", got)
	}
}

func TestReleaseDuringConnect(t *testing.T) {
	cfg := simConfig(idleLines(20))
	cfg.BannerInterval = 0 // one banner on connect: WaitReady waits out Settle
	s := startSim(t, cfg)
	dcfg := daemonConfig(s.Addr().String(), openStore(t))
	dcfg.Settle = 5 * time.Second
	d, _ := runDaemon(t, dcfg)
	time.Sleep(300 * time.Millisecond) // dialled and holding in WaitReady
	if d.Status().Connected {
		t.Fatal("connected before Settle")
	}

	start := time.Now()
	res, err := d.Release(reqCtx(t), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("Release took %v during WaitReady, want well under Settle", el)
	}
	if res.Banner == nil || !*res.Banner {
		t.Errorf("Banner = %v, want true (one banner seen)", res.Banner)
	}
	time.Sleep(200 * time.Millisecond)
	if rcv := s.Received(); len(rcv) != 0 {
		t.Errorf("sim received %q, want nothing", rcv)
	}
}

// A display sample in flight at release must not leave MC@ or MCr after the
// final "MC ". Samples are made frequent to hit that window.
func TestReleaseWithDisplaySampling(t *testing.T) {
	shrinkBannerTimeout(t)
	for range 5 {
		cfg := simConfig(idleLines(40))
		cfg.Responses["MC@"] = []string{readyFrame}
		s := startSim(t, cfg)
		dcfg := daemonConfig(s.Addr().String(), openStore(t))
		dcfg.DisplayEvery, dcfg.DisplayFor = 50*time.Millisecond, 30*time.Millisecond
		d, stop := runDaemon(t, dcfg)
		waitActive(t, d)
		waitFor(t, "a sample", func() bool { return count(s.Received(), "MC@") >= 1 })

		if _, err := d.Release(reqCtx(t), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("Release: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		if cmds := s.Received(); lastCmd(cmds) != "MC " {
			t.Fatalf("last command %q, want \"MC \": %q", lastCmd(cmds), cmds[max(0, len(cmds)-4):])
		}
		stop()
		assertOnlyAllowed(t, s, "MC@")
	}
}

// Without the idle banner after "MC " the release still goes through and
// says so.
func TestReleaseBannerMissing(t *testing.T) {
	shrink(t, &releaseBannerTimeout, 200*time.Millisecond)
	cfg := simConfig(idleLines(20))
	cfg.BannerInterval = 0
	s := startSim(t, cfg)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)

	res, err := d.Release(reqCtx(t), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if res.Banner == nil || *res.Banner {
		t.Errorf("Banner = %v, want false", res.Banner)
	}
	if d.Status().Connected {
		t.Error("still connected after Release returned")
	}
	if lastCmd(s.Received()) != "MC " {
		t.Errorf("last command %q, want \"MC \"", lastCmd(s.Received()))
	}
}

func TestReleaseWhileDisconnected(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	d, _ := runDaemon(t, daemonConfig(addr, openStore(t)))

	res, err := d.Release(reqCtx(t), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if res.Banner != nil {
		t.Errorf("Banner = %v with no session up, want nil", *res.Banner)
	}
	if d.Status().ReleasedUntil == nil {
		t.Error("ReleasedUntil not set")
	}
}
