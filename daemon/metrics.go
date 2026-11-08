package daemon

import (
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/thomasbach-dev/medewi/brew"
)

// metrics are the daemon's Prometheus instruments. They always exist, so
// call sites need no nil checks; they are registered only when
// Config.Registry is set.
type metrics struct {
	connected, specMismatch, ready    prometheus.Gauge
	machineReady                      prometheus.Gauge
	boilerC, setpointC, heaterPercent prometheus.Gauge
	power                             *prometheus.GaugeVec
	shots, flushes, pumpSeconds       prometheus.Counter
	volumeLiters, waterDraws, waterL  prometheus.Counter
	lines, decodeErrors, reconnects   prometheus.Counter
	fragments                         prometheus.CounterFunc
	frags                             fragmentCount
}

// fragmenter is the part of *leva.Conn the fragment count reads.
type fragmenter interface{ Fragments() uint64 }

// fragmentCount sums the fragments of the ended sessions and the live one.
// A session's end moves its count over under one lock, so the sum never dips.
type fragmentCount struct {
	mu     sync.Mutex
	closed uint64
	live   fragmenter
}

func (f *fragmentCount) attach(c fragmenter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live = c
}

// detach ends c's session; call it once c no longer reads.
func (f *fragmentCount) detach(c fragmenter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c != nil && f.live == c {
		f.closed += c.Fragments()
		f.live = nil
	}
}

func (f *fragmentCount) value() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.closed
	if f.live != nil {
		n += f.live.Fragments()
	}
	return float64(n)
}

var powerStates = []PowerState{PowerActive, PowerEco, PowerStandby, PowerUnknown}

func newMetrics(reg prometheus.Registerer) *metrics {
	g := func(name, help string) prometheus.Gauge {
		return prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	}
	c := func(name, help string) prometheus.Counter {
		return prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	}
	m := &metrics{
		connected:     g("medewi_machine_connected", "1 while a connection to the machine is up."),
		specMismatch:  g("medewi_spec_mismatch", "1 while the machine's XML name differs from the loaded spec."),
		ready:         g("medewi_ready", "1 while the boiler is settled on the setpoint and the machine is active."),
		machineReady:  g("medewi_machine_ready", "1 from the machine's own [READY] until eco, standby or a disconnect."),
		boilerC:       g("medewi_boiler_celsius", "Boiler temperature; keeps its last value when masked."),
		setpointC:     g("medewi_setpoint_celsius", "Boiler setpoint; keeps its last value when masked."),
		heaterPercent: g("medewi_heater_percent", "Heater output."),
		power: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "medewi_power_state", Help: "1 for the machine's current power state, 0 for the others.",
		}, []string{"state"}),
		shots:        c("medewi_shots_total", "Completed shots."),
		flushes:      c("medewi_flushes_total", "Completed flushes."),
		pumpSeconds:  c("medewi_pump_seconds_total", "Seconds of finished brews, truncated ones included."),
		volumeLiters: c("medewi_volume_liters_total", "Liters of finished brews and wand draws."),
		waterDraws:   c("medewi_water_draws_total", "Wand water draws."),
		waterL:       c("medewi_water_liters_total", "Liters drawn at the wand."),
		lines:        c("medewi_report_lines_total", "Report lines received."),
		decodeErrors: c("medewi_decode_errors_total", "Report lines skipped (checksum or decode error)."),
		reconnects:   c("medewi_reconnects_total", "Connection attempts after the first, failed ones included."),
	}
	m.fragments = prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "medewi_fragments_total", Help: "Report or frame pieces cut off at a stream switch.",
	}, m.frags.value)
	for _, s := range powerStates {
		m.power.WithLabelValues(string(s)).Set(0)
	}
	if reg != nil {
		for _, col := range []prometheus.Collector{
			m.connected, m.specMismatch, m.ready, m.machineReady, m.boilerC, m.setpointC, m.heaterPercent, m.power,
			m.shots, m.flushes, m.pumpSeconds, m.volumeLiters, m.waterDraws, m.waterL,
			m.lines, m.decodeErrors, m.reconnects, m.fragments,
		} {
			if err := reg.Register(col); err != nil {
				slog.Error("registering metric", "err", err)
			}
		}
	}
	return m
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// status mirrors a Status into the gauges. A masked (nil) temperature
// keeps the gauge's last value.
func (m *metrics) status(s Status) {
	m.connected.Set(b2f(s.Connected))
	if s.SpecOK || !s.Connected {
		m.specMismatch.Set(0)
	}
	m.ready.Set(b2f(s.Ready))
	m.machineReady.Set(b2f(s.MachineReady))
	if s.Temp != nil {
		m.boilerC.Set(*s.Temp)
	}
	if s.Setpoint != nil {
		m.setpointC.Set(*s.Setpoint)
	}
	for _, p := range powerStates {
		m.power.WithLabelValues(string(p)).Set(b2f(s.Power == p))
	}
}

// brewClosed counts a finished brew. Truncated ones are no shot or
// flush but still add pump time and volume.
func (m *metrics) brewClosed(s brew.Summary) {
	switch s.Kind {
	case brew.Shot:
		m.shots.Inc()
	case brew.Flush:
		m.flushes.Inc()
	}
	m.pumpSeconds.Add(s.Duration.Seconds())
	m.volumeLiters.Add(s.VolumeML / 1000)
}

func (m *metrics) waterDrawn(w brew.WaterDraw) {
	m.waterDraws.Inc()
	m.waterL.Add(w.VolumeML / 1000)
	m.volumeLiters.Add(w.VolumeML / 1000)
}
