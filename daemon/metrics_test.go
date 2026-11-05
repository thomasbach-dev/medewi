package daemon

import (
	"math"
	"net"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/thomasbach-dev/medewi/brew"
)

func metricsDaemon(t *testing.T, lines [][]byte) (*Daemon, *prometheus.Registry) {
	t.Helper()
	cfg := simConfig(lines)
	cfg.Once = true
	s := startSim(t, cfg)
	dc := daemonConfig(s.Addr().String(), openStore(t))
	reg := prometheus.NewRegistry()
	dc.Registry = reg
	d, _ := runDaemon(t, dc)
	return d, reg
}

func val(t *testing.T, reg *prometheus.Registry, name string, labels ...string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			for i := 0; i+1 < len(labels); i += 2 {
				ok := false
				for _, l := range m.GetLabel() {
					if l.GetName() == labels[i] && l.GetValue() == labels[i+1] {
						ok = true
					}
				}
				if !ok {
					continue next
				}
			}
			if m.GetGauge() != nil {
				return m.GetGauge().GetValue()
			}
			return m.GetCounter().GetValue()
		}
	}
	t.Fatalf("metric %s %v not found", name, labels)
	return 0
}

func TestMetricsAfterShot(t *testing.T) {
	// Idle, a 12-line pumping run (Volume 3..36 ml), a coast, a sentinel.
	ls := idleLines(4)
	for i := range 12 {
		ls = append(ls, line('*', "PID", 93, 93, 9, 9, 120, float64(3*(i+1))))
	}
	ls = append(ls, repeat(40, line('-', "PID", 93, 93, 1, -1, 0, 36))...)
	ls = append(ls, line('-', "PID", 77.7, 93, 1, -1, 0, 36))
	d, reg := metricsDaemon(t, ls)
	waitSentinel(t, d)
	// The shot is stored before the sentinel, so its metrics are set.
	if got := val(t, reg, "medewi_shots_total"); got != 1 {
		t.Errorf("shots = %v, want 1", got)
	}
	if got := val(t, reg, "medewi_flushes_total"); got != 0 {
		t.Errorf("flushes = %v, want 0", got)
	}
	if got := val(t, reg, "medewi_pump_seconds_total"); got < 0.1 || got > 5 {
		t.Errorf("pump seconds = %v, want about 0.3 (12 lines, 25 ms apart)", got)
	}
	if got := val(t, reg, "medewi_volume_liters_total"); math.Abs(got-0.036) > 1e-9 {
		t.Errorf("volume liters = %v, want 0.036", got)
	}
	if got := val(t, reg, "medewi_report_lines_total"); got != 57 {
		t.Errorf("report lines = %v, want 57", got)
	}
	if got := val(t, reg, "medewi_boiler_celsius"); got != 77.7 {
		t.Errorf("boiler = %v, want 77.7", got)
	}
	if val(t, reg, "medewi_machine_connected") != 1 || val(t, reg, "medewi_spec_mismatch") != 0 ||
		val(t, reg, "medewi_reconnects_total") != 0 || val(t, reg, "medewi_fragments_total") != 0 ||
		val(t, reg, "medewi_machine_ready") != 0 {
		t.Error("connected/mismatch/reconnects wrong")
	}
}

func TestMetricsWandDraw(t *testing.T) {
	ls := idleLines(4)
	vol := 0.0
	for range 20 {
		vol += 4
		ls = append(ls, line('-', "PID", 93, 93, 1, -1, 400, vol))
	}
	ls = append(ls, repeat(30, line('-', "PID", 93, 93, 1, -1, 0, vol))...)
	ls = append(ls, line('-', "PID", 77.7, 93, 1, -1, 0, vol))
	d, reg := metricsDaemon(t, ls)
	waitSentinel(t, d)
	if got := val(t, reg, "medewi_water_draws_total"); got != 1 {
		t.Errorf("draws = %v, want 1", got)
	}
	w := val(t, reg, "medewi_water_liters_total")
	if w <= 0 || val(t, reg, "medewi_volume_liters_total") != w {
		t.Errorf("water %v liters, volume %v; want equal and > 0", w, val(t, reg, "medewi_volume_liters_total"))
	}
	if val(t, reg, "medewi_shots_total") != 0 || val(t, reg, "medewi_flushes_total") != 0 {
		t.Error("a wand draw counted as a shot or flush")
	}
}

func TestPowerStateOneHot(t *testing.T) {
	ls := append(idleLines(4), repeat(10, line('-', "ECO", 93, 79, 0, -1, 0, 0))...)
	ls = append(ls, line('-', "PID", 77.7, 93, 0, -1, 0, 0))
	// Standby masks the temperature; check after the sentinel.
	ls = append(ls, repeat(10, line('-', "???", 0, 0, 0, 0, 0, 0))...)
	d, reg := metricsDaemon(t, ls)
	waitFor(t, "standby", func() bool { return d.Status().Power == PowerStandby })
	for _, p := range powerStates {
		want := 0.0
		if p == PowerStandby {
			want = 1
		}
		if got := val(t, reg, "medewi_power_state", "state", string(p)); got != want {
			t.Errorf("power_state{%s} = %v, want %v", p, got, want)
		}
	}
	if got := val(t, reg, "medewi_boiler_celsius"); got != 77.7 {
		t.Errorf("boiler = %v, want last value 77.7 kept while masked", got)
	}
	if got := testutil.CollectAndCount(reg, "medewi_power_state"); got != 4 {
		t.Errorf("power_state series = %d, want 4", got)
	}
}

func TestMetricsInterruptedAddsNoShot(t *testing.T) {
	m := newMetrics(nil)
	m.brewClosed(brew.Summary{Kind: brew.Interrupted, Duration: 2e9, VolumeML: 10})
	if testutil.ToFloat64(m.shots)+testutil.ToFloat64(m.flushes) != 0 ||
		testutil.ToFloat64(m.pumpSeconds) != 2 || testutil.ToFloat64(m.volumeLiters) != 0.01 {
		t.Error("interrupted brew mishandled")
	}
}

func TestNilRegistryNoPanic(t *testing.T) {
	cfg := simConfig(shotLines(9))
	cfg.Once = true
	s := startSim(t, cfg)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitSentinel(t, d)
}

func TestMetricsFlush(t *testing.T) {
	d, reg := metricsDaemon(t, shotLines(-1)) // no pressure setpoint: a flush
	waitSentinel(t, d)
	if val(t, reg, "medewi_flushes_total") != 1 || val(t, reg, "medewi_shots_total") != 0 {
		t.Error("want one flush and no shot")
	}
}

func TestMetricsCountsFailedReconnectAttempts(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // nothing listens here now
	dc := daemonConfig(addr, openStore(t))
	reg := prometheus.NewRegistry()
	dc.Registry = reg
	runDaemon(t, dc)
	waitFor(t, "failed attempts counted", func() bool { return val(t, reg, "medewi_reconnects_total") >= 3 })
	if val(t, reg, "medewi_machine_connected") != 0 {
		t.Error("connected = 1 with nothing listening")
	}
}
