package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
)

func TestPowerFromPayload(t *testing.T) {
	for _, tc := range []struct {
		p    string
		want PowerState
	}{
		{"{ - PID 93.0", PowerActive},
		{"{ - ECO 79.0", PowerEco},
		{"{ ? ??? ????", PowerStandby},
		{"{ - XYZ 93.0", PowerUnknown},
		{"{ -", PowerUnknown},
	} {
		if got := powerFromPayload([]byte(tc.p)); got != tc.want {
			t.Errorf("powerFromPayload(%q) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestStatusFollowsStream(t *testing.T) {
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
	old := mcqSpacing
	mcqSpacing = 50 * time.Millisecond
	t.Cleanup(func() { mcqSpacing = old })

	cfg := simConfig(idleLines(20))
	cfg.Responses = nil
	s := startSim(t, cfg)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
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

func TestDisconnectMidShotInterrupted(t *testing.T) {
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
	waitFor(t, "interrupted brew stored", func() bool {
		bs := brews(t, st)
		return len(bs) == 1 && bs[0].Kind == string(brew.Interrupted) && bs[0].EndedAt != nil
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

func TestEventExecRuns(t *testing.T) {
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
	st := openStore(t)
	id, err := st.BeginBrew(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := startSim(t, simConfig(idleLines(20)))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), st))
	waitFor(t, "dangling brew closed", func() bool {
		bs := brews(t, st)
		return len(bs) == 1 && bs[0].Kind == string(brew.Interrupted)
	})
	waitFor(t, "LastBrew loaded", func() bool {
		lb := d.Status().LastBrew
		return lb != nil && lb.ID == id && lb.Kind == string(brew.Interrupted)
	})
}

// TestStalledStreamReconnects: another client's "MC " stops the global
// stream while TCP stays healthy. The daemon must notice the silence,
// store the open brew as interrupted and reconnect.
func TestStalledStreamReconnects(t *testing.T) {
	old := reportStallTimeout
	reportStallTimeout = 300 * time.Millisecond
	// Registered before runDaemon, so it runs after the daemon has stopped.
	t.Cleanup(func() { reportStallTimeout = old })

	var pump [][]byte
	for i := range 20 {
		pump = append(pump, line('*', "PID", 93, 93, 9, 9, 120, float64(i+1)))
	}
	s := startSim(t, simConfig(pump))
	st := openStore(t)
	cfg := daemonConfig(s.Addr().String(), st)
	cfg.BackoffMin, cfg.BackoffMax = 500*time.Millisecond, 500*time.Millisecond
	d, stop := runDaemon(t, cfg)
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
	waitFor(t, "interrupted brew stored", func() bool {
		bs := brews(t, st)
		return len(bs) >= 1 && bs[0].Kind == string(brew.Interrupted) && bs[0].EndedAt != nil
	})
	waitFor(t, "reconnected with reports restarted", func() bool {
		st := d.Status()
		return st.Connected && st.SpecOK && count(s.Received(), "MCr") >= 2
	})
	stop()
	assertOnlyAllowed(t, s)
}

// A machine that is switched off refuses or drops connections: that is a
// normal state, logged at info level, not a warning.
func TestDialFailureIsInfo(t *testing.T) {
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	runDaemon(t, daemonConfig(addr, openStore(t)))
	waitFor(t, "dial failure logged", func() bool { return strings.Contains(logs.String(), "dial failed") })
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "dial failed") && !strings.Contains(line, "level=INFO") {
			t.Errorf("dial failure not at info level: %s", line)
		}
	}
}

func TestDefaultMaxBackoff(t *testing.T) {
	if got := New(Config{}).cfg.BackoffMax; got != 3*time.Minute {
		t.Errorf("default BackoffMax = %v, want 3m", got)
	}
}
