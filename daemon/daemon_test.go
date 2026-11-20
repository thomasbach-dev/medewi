package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

func TestPowerFromPayload(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		p    string
		want PowerState
	}{
		{"{ - PID 93.0", PowerActive},
		{"{ - ECO 79.0", PowerEco},
		{"{ ? ??? ????", PowerStandby},
		{"{ - CLD  21.4", PowerCold},
		{"{ - XYZ 93.0", PowerUnknown},
		{"{ -", PowerUnknown},
	} {
		if got := powerFromPayload([]byte(tc.p)); got != tc.want {
			t.Errorf("powerFromPayload(%q) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestStatusFollowsStream(t *testing.T) {
	t.Parallel()
	s := startSim(t, simConfig(idleLines(20)))
	d, stop := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitFor(t, "connected, spec ok, active, temp 93", func() bool {
		st := d.Status()
		return st.Connected && st.SpecOK && st.Power == PowerActive && st.Temp != nil && *st.Temp == 93
	})
	waitFor(t, "ready", func() bool { return d.Status().Ready })
	st := d.Status()
	if st.Firmware != "3.2" {
		t.Errorf("Firmware = %q, want 3.2", st.Firmware)
	}
	if st.Setpoint == nil || *st.Setpoint != 93 {
		t.Errorf("Setpoint = %v, want 93", st.Setpoint)
	}
	stop()
	// The sim reads the final command asynchronously.
	waitFor(t, "MC<space> at shutdown", func() bool {
		rcv := s.Received()
		return len(rcv) > 0 && rcv[len(rcv)-1] == "MC "
	})
	assertOnlyAllowed(t, s)
}

// shotLines is idle, a pumping run with a pressure setpoint, a long coast,
// then a wand draw (marker '-', Volume growing) and a sentinel line (temp
// 77.7) that tells the test everything has been processed.
func shotLines(psp float64) [][]byte {
	ls := idleLines(4)
	vol := 0.0
	for range 12 {
		vol += 3
		ls = append(ls, line('*', "PID", 93, 93, 9, psp, 120, vol))
	}
	ls = append(ls, repeat(40, line('-', "PID", 93, 93, 1, -1, 30, vol))...)
	for range 20 {
		vol += 4
		ls = append(ls, line('-', "PID", 93, 93, 1, -1, 400, vol))
	}
	ls = append(ls, repeat(30, line('-', "PID", 93, 93, 1, -1, 0, vol))...)
	return append(ls, line('-', "PID", 77.7, 93, 1, -1, 0, vol))
}

func waitSentinel(t *testing.T, d *Daemon) {
	t.Helper()
	waitFor(t, "sentinel line", func() bool {
		st := d.Status()
		return st.Temp != nil && *st.Temp == 77.7
	})
}

func TestRecordsShotEndToEnd(t *testing.T) {
	t.Parallel()
	cfg := simConfig(shotLines(9))
	cfg.Once = true
	s := startSim(t, cfg)
	st := openStore(t)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), st))
	waitSentinel(t, d)

	bs := brews(t, st)
	if len(bs) != 1 {
		t.Fatalf("brews = %+v, want exactly one", bs)
	}
	b := bs[0]
	if b.Kind != string(brew.Shot) || b.EndedAt == nil {
		t.Fatalf("brew = %+v, want a finished shot", b)
	}
	if b.VolumeML != 36 || b.PeakBar != 9 {
		t.Errorf("volume %v peak %v, want 36 and 9 (the wand draw must not count)", b.VolumeML, b.PeakBar)
	}
	pts, err := st.Points(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 12 {
		t.Errorf("points = %d, want 12", len(pts))
	}
	status := d.Status()
	if status.BrewOpen || status.LastBrew == nil || status.LastBrew.ID != b.ID {
		t.Errorf("status = %+v, want closed brew and LastBrew %d", status, b.ID)
	}
	assertOnlyAllowed(t, s)
}

func TestMCqUnansweredRetries(t *testing.T) {
	t.Parallel()
	cfg := simConfig(idleLines(20))
	cfg.Responses = nil
	s := startSim(t, cfg)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)), func(k *tuning) { k.mcqSpacing = 50 * time.Millisecond })
	waitFor(t, "more than 3 MC?", func() bool { return count(s.Received(), "MC?") > 3 })
	if d.Status().SpecOK {
		t.Error("SpecOK without an XML= reply")
	}
	if n := count(s.Received(), "MCr"); n != 0 {
		t.Errorf("MCr sent %d times without a spec match", n)
	}
	assertOnlyAllowed(t, s)
}

func TestMissingFieldWarns(t *testing.T) {
	// Not parallel: it swaps the default slog logger (see TestDialFailuresQuiet).
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cfg := simConfig(shotLines(9))
	cfg.Once = true
	s := startSim(t, cfg)
	st := openStore(t)
	dcfg := daemonConfig(s.Addr().String(), st)
	dcfg.Spec = specWithout("Pressure SP")
	d, _ := runDaemon(t, dcfg)
	waitSentinel(t, d)

	bs := brews(t, st)
	if len(bs) != 1 || bs[0].Kind != string(brew.Flush) {
		t.Fatalf("brews = %+v, want one flush", bs)
	}
	if n := strings.Count(logs.String(), "Pressure SP"); n != 1 {
		t.Errorf("missing field logged %d times, want once:\n%s", n, logs)
	}
}

func TestSpecMismatchDegrades(t *testing.T) {
	t.Parallel()
	cfg := simConfig(idleLines(20))
	cfg.Responses = map[string][]string{"MC?": {"XML=other"}}
	s := startSim(t, cfg)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitFor(t, "connected and MC? sent", func() bool {
		return d.Status().Connected && count(s.Received(), "MC?") > 0
	})
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		st := d.Status()
		if !st.Connected || st.SpecOK || st.Power != PowerUnknown {
			t.Fatalf("status = %+v, want connected, spec mismatch, power unknown", st)
		}
	}
	rcv := s.Received()
	if count(rcv, "MCr") != 0 || count(rcv, "MC?") != 1 {
		t.Errorf("received %q, want exactly one MC? and no MCr", rcv)
	}
	assertOnlyAllowed(t, s)
}

func TestReadyDropsInStandby(t *testing.T) {
	t.Parallel()
	cfg := simConfig(idleLines(20))
	cfg.Switch = map[string][]fixture.Event{
		"TEST-STANDBY": stream(repeat(20, line('?', "???", 0, 0, 0, 0, 0, 0))...),
	}
	s := startSim(t, cfg)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitFor(t, "ready", func() bool { return d.Status().Ready })

	// A second client flips the sim's stream to standby lines.
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Write([]byte("TEST-STANDBY\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "standby", func() bool { return d.Status().Power == PowerStandby })
	if st := d.Status(); st.Ready || st.Temp != nil {
		t.Errorf("status = %+v, want not ready and temp masked", st)
	}
}

func TestDisconnectMidShotTruncatedLost(t *testing.T) {
	t.Parallel()
	var pump [][]byte
	for i := range 20 {
		pump = append(pump, line('*', "PID", 93, 93, 9, 9, 120, float64(i+1)))
	}
	cfg := simConfig(pump)
	s := startSim(t, cfg)
	addr := s.Addr().String()
	st := openStore(t)
	d, _ := runDaemon(t, daemonConfig(addr, st))
	waitFor(t, "brew open", func() bool { return d.Status().BrewOpen })

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "truncated brew stored", func() bool {
		bs := brews(t, st)
		return len(bs) == 1 && bs[0].Kind == string(brew.TruncatedLost) && bs[0].EndedAt != nil
	})
	waitFor(t, "disconnected", func() bool {
		st := d.Status()
		return !st.Connected && !st.BrewOpen && st.Power == PowerUnknown
	})

	cfg.Addr = addr
	cfg.Stream = stream(idleLines(20)...)
	startSim(t, cfg)
	waitFor(t, "reconnected", func() bool {
		st := d.Status()
		return st.Connected && st.SpecOK && st.Power == PowerActive
	})
}

func TestShutdownMidShotTruncatedShutdown(t *testing.T) {
	t.Parallel()
	var pump [][]byte
	for i := range 20 {
		pump = append(pump, line('*', "PID", 93, 93, 9, 9, 120, float64(i+1)))
	}
	s := startSim(t, simConfig(pump))
	st := openStore(t)
	d, stop := runDaemon(t, daemonConfig(s.Addr().String(), st))
	waitFor(t, "brew open", func() bool { return d.Status().BrewOpen })

	stop()
	bs := brews(t, st)
	if len(bs) != 1 || bs[0].Kind != string(brew.TruncatedShutdown) || bs[0].EndedAt == nil {
		t.Fatalf("brews = %+v, want one truncated-shutdown with EndedAt", bs)
	}
}

func TestEventExecRuns(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "events")
	s := startSim(t, simConfig(idleLines(20)))
	cfg := daemonConfig(s.Addr().String(), openStore(t))
	cfg.EventExec = fmt.Sprintf(`echo "$MEDEWI_EVENT $MEDEWI_TEMP $MEDEWI_SETPOINT" >> '%s'`, out)
	runDaemon(t, cfg)
	waitFor(t, "READY in the hook's output", func() bool {
		b, _ := os.ReadFile(out)
		return strings.Contains(string(b), "READY 93.0 93.0\n")
	})
}

func TestDanglingClosedAtStartup(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	id, err := st.BeginBrew(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := startSim(t, simConfig(idleLines(20)))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), st))
	waitFor(t, "dangling brew closed", func() bool {
		bs := brews(t, st)
		return len(bs) == 1 && bs[0].Kind == string(brew.TruncatedCrash)
	})
	waitFor(t, "LastBrew loaded", func() bool {
		lb := d.Status().LastBrew
		return lb != nil && lb.ID == id && lb.Kind == string(brew.TruncatedCrash)
	})
}

// TestStalledStreamReconnects: another client's "MC " stops the global
// stream while TCP stays healthy. The daemon must notice the silence,
// store the open brew as truncated and reconnect.
func TestStalledStreamReconnects(t *testing.T) {
	t.Parallel()
	var pump [][]byte
	for i := range 20 {
		pump = append(pump, line('*', "PID", 93, 93, 9, 9, 120, float64(i+1)))
	}
	s := startSim(t, simConfig(pump))
	st := openStore(t)
	cfg := daemonConfig(s.Addr().String(), st)
	cfg.BackoffMin, cfg.BackoffMax = 500*time.Millisecond, 500*time.Millisecond
	d, stop := runDaemon(t, cfg, func(k *tuning) { k.reportStallTimeout = 300 * time.Millisecond })
	waitFor(t, "brew open", func() bool { return d.Status().BrewOpen })

	// A second client stops the stream for everyone; TCP stays up.
	c, err := net.Dial("tcp", s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Write([]byte("MC \r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "disconnected", func() bool { return !d.Status().Connected })
	waitFor(t, "truncated brew stored", func() bool {
		bs := brews(t, st)
		return len(bs) >= 1 && bs[0].Kind == string(brew.TruncatedLost) && bs[0].EndedAt != nil
	})
	waitFor(t, "reconnected with reports restarted", func() bool {
		st := d.Status()
		return st.Connected && st.SpecOK && count(s.Received(), "MCr") >= 2
	})
	stop()
	assertOnlyAllowed(t, s)
}

// A machine that is switched off refuses or drops connections: that is a
// normal state. The first failed dial is logged at info level, never as a
// warning; the repeats (one every BackoffMax) go to debug, so the journal
// stays quiet. The machine coming back is logged at info level.
func TestDialFailuresQuiet(t *testing.T) {
	// Not parallel: it swaps the default slog logger. Serial tests finish
	// before any parallel one starts, so nothing else logs meanwhile.
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // nothing listens here now: the machine is off
	runDaemon(t, daemonConfig(addr, openStore(t)))
	dialLines := func() []string {
		var out []string
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "dial failed") {
				out = append(out, line)
			}
		}
		return out
	}
	waitFor(t, "three failed dials logged", func() bool { return len(dialLines()) >= 3 })
	for i, line := range dialLines() {
		want := "level=DEBUG"
		if i == 0 {
			want = "level=INFO"
		}
		if !strings.Contains(line, want) {
			t.Errorf("failed dial %d not at %s: %s", i, want, line)
		}
	}

	// Power-on: the machine answers on the same address.
	cfg := simConfig(idleLines(40))
	cfg.Addr = addr
	startSim(t, cfg)
	waitFor(t, "reconnect logged", func() bool { return strings.Contains(logs.String(), "machine reachable again") })
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "machine reachable again") &&
			(!strings.Contains(line, "level=INFO") || !strings.Contains(line, "failed_dials=") || !strings.Contains(line, "unreachable_for=")) {
			t.Errorf("reconnect line wrong: %s", line)
		}
	}
}

// A dial to a switched-off machine can hang (no RST, no ARP answer) instead
// of failing: each attempt is bounded, so the loop keeps redialling. A
// failed or cancelled dial must count as a failure (the backoff runs on).
func TestDialBounded(t *testing.T) {
	t.Parallel()
	if got := defaultTuning().dialTimeout; got != time.Minute {
		t.Errorf("default dialTimeout = %v, want 1m", got)
	}
	var calls atomic.Int32
	deadlines := make(chan time.Duration, 16)
	_, _ = runDaemon(t, daemonConfig("127.0.0.1:1", openStore(t)), func(tn *tuning) {
		tn.dialTimeout = 40 * time.Millisecond
		tn.dial = func(ctx context.Context, _ string) (*leva.Conn, error) {
			calls.Add(1)
			dl, ok := ctx.Deadline()
			if !ok {
				dl = time.Now().Add(time.Hour)
			}
			select {
			case deadlines <- time.Until(dl):
			default:
			}
			<-ctx.Done() // hangs like a dial to a host that never answers
			return nil, ctx.Err()
		}
	})
	waitFor(t, "a hanging dial is given up and retried", func() bool { return calls.Load() >= 3 })
	for range 3 {
		if left := <-deadlines; left <= 0 || left > 40*time.Millisecond {
			t.Errorf("dial context deadline %v away, want within the 40ms dial timeout", left)
		}
	}
}

// The cap bounds how late medewid notices a power-on: a cold boiler
// reaches its setpoint in 3–4 min, and the heat-up must be captured.
func TestDefaultMaxBackoff(t *testing.T) {
	t.Parallel()
	if got := New(Config{}).cfg.BackoffMax; got != 10*time.Second {
		t.Errorf("default BackoffMax = %v, want 10s", got)
	}
}
