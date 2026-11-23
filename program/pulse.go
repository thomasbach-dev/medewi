package program

import (
	"slices"
	"time"
)

const (
	// pulseGap splits a pulse when no report came for this long.
	pulseGap = 20 * time.Second
	// rampWindow must hold at least rampValues distinct setpoints.
	rampWindow = 20 * time.Second
	rampValues = 4
	// rampShortest keeps Ramp & Soak overrides of flushes and shots out.
	rampShortest = 60 * time.Second
	// minRatioML is the smallest Volume a ml-per-tick ratio is learned
	// from: Volume counts whole ml.
	minRatioML = 200
	// coast is how long the counter may grow after the pump stopped.
	coast = 3 * time.Second
	// tuneGap is the longest interruption of an autotune.
	tuneGap = 60 * time.Second
)

// pulse is a stretch of pump marker '*'.
type pulse struct {
	first, last Sample
	pressures   []float64
	phases      map[float64]int
	steps       []step
	shot        bool  // a Pressure SP was set
	cont        *bool // the tick counter went on; nil: unknown
	// Counters at the pulse's start: equal ones mean nothing happened in
	// between. resets counts tick-counter resets with the pump off.
	brk, modes, setpoints, resets int
	openStart, openEnd            bool
	startML, endML                *float64 // volume counter before and at the end

	before, after *float64 // pressure just before the pulse and just after it
}

// step is a stretch of one phase within a pulse.
type step struct {
	phase          float64
	start, end     time.Time
	pressure, flow *float64 // at its end
}

func (p *pulse) add(s Sample) {
	p.last = s
	if s.Pressure != nil {
		p.pressures = append(p.pressures, *s.Pressure)
	}
	p.shot = p.shot || s.PressureSP != nil
	if s.Phase == nil {
		return
	}
	p.phases[*s.Phase]++
	if n := len(p.steps); n == 0 || p.steps[n-1].phase != *s.Phase {
		p.steps = append(p.steps, step{phase: *s.Phase, start: s.At})
	}
	st := &p.steps[len(p.steps)-1]
	st.end, st.pressure, st.flow = s.At, s.Pressure, s.Flow
}

// half2 returns the pressures of the pulse's second half.
func (p *pulse) half2() []float64 { return p.pressures[len(p.pressures)/2:] }

func (p *pulse) half2Max() float64 { return maxOf(p.half2()) }

func (p *pulse) peak() float64 { return maxOf(p.pressures) }

func maxOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return slices.Max(v)
}

// phase returns the most common phase, the lowest on a tie; nil if masked.
func (p *pulse) phase() *float64 {
	var best *float64
	for v, n := range p.phases {
		if best == nil || n > p.phases[*best] || n == p.phases[*best] && v < *best {
			best = &v
		}
	}
	return best
}

// spChange is a change of the setpoint, with the sample before it.
type spChange struct {
	prev, at Sample
	brk      int
	// span is how long the new value then held; open while it still holds.
	end  Sample
	open bool
}

// modeSpan is a stretch of one mode.
type modeSpan struct {
	mode               string
	first, last        Sample
	openStart, openEnd bool
	before, after      *Sample // nearest samples outside with PID constants
	heatOn             []time.Time
	lost               []time.Duration // interruptions inside
}

// Detector collects samples and frames; Runs finds the programs in them.
// It is not safe for concurrent use.
type Detector struct {
	have    bool // prev is valid: no Break since
	prev    Sample
	cur     *pulse
	pulses  []*pulse
	changes []spChange
	tunes   []*modeSpan
	tune    *modeSpan // the open autotune

	brk, modes, setpoints, resets int
	ticks, mlPerTick, ml          *float64
	sp                            *Sample // last sample with a setpoint
	pid                           *Sample // last sample with PID constants
	breaks                        []time.Time
	frames                        []lcdFrame
	settings                      Settings
}

// NewDetector returns an empty Detector.
func NewDetector() *Detector { return &Detector{} }

// Break says the stream was interrupted: a disconnect or another source.
func (d *Detector) Break() {
	if !d.have {
		return
	}
	d.breaks = append(d.breaks, d.prev.At)
	if d.cur != nil {
		d.cur.openEnd = true
		d.closePulse()
	}
	if d.tune != nil {
		d.tune.openEnd = d.prev.Mode == "ATN"
		d.tune = nil
	}
	if n := len(d.changes); n > 0 {
		d.changes[n-1].open = false
	}
	d.have = false
	d.brk++
	d.ticks, d.ml, d.sp, d.pid = nil, nil, nil, nil
}

// Sample feeds the next report.
func (d *Detector) Sample(s Sample) {
	start := !d.have
	if !start && s.Mode != d.prev.Mode {
		d.modes++
	}
	d.setpoint(s)
	d.autotune(s, start)
	if d.cur != nil && (!s.Pump || s.At.Sub(d.prev.At) >= pulseGap) {
		if !s.Pump {
			d.cur.after = s.Pressure
		}
		d.closePulse()
	}
	if s.Pump && d.cur == nil {
		d.open(s, start)
	} else if d.ticks != nil && s.Ticks != nil && *s.Ticks < *d.ticks && !s.Pump {
		d.resets++
	}
	d.volume(s)
	if s.Pump {
		d.cur.add(s)
		d.cur.endML = d.ml
	} else if n := len(d.pulses); n > 0 && d.ml != nil {
		p := d.pulses[n-1]
		if p.brk == d.brk && p.endML != nil && *d.ml > *p.endML && s.At.Sub(p.last.At) <= coast {
			p.endML = d.ml
		}
	}
	d.ticks = s.Ticks
	if s.Kc != nil {
		d.pid = &s
	}
	d.prev, d.have = s, true
}

func (d *Detector) open(s Sample, start bool) {
	p := &pulse{
		first: s, phases: map[float64]int{}, openStart: start,
		brk: d.brk, modes: d.modes, setpoints: d.setpoints, resets: d.resets, startML: d.ml,
	}
	if !start {
		p.before = d.prev.Pressure
	}
	if d.ticks != nil && *d.ticks != 0 && s.Ticks != nil {
		c := *s.Ticks >= *d.ticks
		p.cont = &c
	}
	d.cur = p
}

func (d *Detector) closePulse() {
	d.pulses = append(d.pulses, d.cur)
	d.cur = nil
}

// volume keeps the volume counter, derived from the ticks while Volume is
// masked (above 999 ml).
func (d *Detector) volume(s Sample) {
	switch {
	case s.Volume != nil:
		d.ml = s.Volume
		if s.Ticks != nil && *s.Ticks > 0 && *s.Volume >= minRatioML {
			r := *s.Volume / *s.Ticks
			d.mlPerTick = &r
		}
	case s.Ticks != nil && d.mlPerTick != nil:
		v := *s.Ticks * *d.mlPerTick
		d.ml = &v
	default:
		d.ml = nil
	}
}

func (d *Detector) setpoint(s Sample) {
	if s.Setpoint == nil {
		return
	}
	if d.sp != nil && *d.sp.Setpoint != *s.Setpoint {
		if n := len(d.changes); n > 0 {
			d.changes[n-1].open = false
		}
		d.changes = append(d.changes, spChange{prev: *d.sp, at: s, brk: d.brk, open: true})
		d.setpoints++
	}
	if n := len(d.changes); n > 0 && d.changes[n-1].open {
		d.changes[n-1].end = s
	}
	d.sp = &s
}

func (d *Detector) autotune(s Sample, start bool) {
	if s.Mode != "ATN" {
		if d.tune != nil && s.Kc != nil {
			d.tune.after = &s
			d.tune = nil
		}
		return
	}
	if n := len(d.tunes); start && n > 0 && d.tunes[n-1].openEnd && s.At.Sub(d.tunes[n-1].last.At) <= tuneGap {
		d.tune = d.tunes[n-1]
		d.tune.openEnd = false
		d.tune.lost = append(d.tune.lost, s.At.Sub(d.tune.last.At))
	} else if d.tune == nil || d.prev.Mode != "ATN" {
		d.tune = &modeSpan{mode: s.Mode, first: s, openStart: start, before: d.pid}
		d.tunes = append(d.tunes, d.tune)
	}
	if s.Heat != nil && *s.Heat > 0 && d.have && d.prev.Heat != nil && *d.prev.Heat == 0 {
		d.tune.heatOn = append(d.tune.heatOn, s.At)
	}
	d.tune.last = s
}

// allPulses returns the pulses, one still open last and marked openEnd.
func (d *Detector) allPulses() []*pulse {
	ps := slices.Clone(d.pulses)
	if d.cur != nil {
		c := *d.cur
		c.openEnd = true
		ps = append(ps, &c)
	}
	return ps
}

// rampSpan is a falling setpoint ramp.
type rampSpan struct {
	first, last int    // indexes into Detector.changes
	jump        *int   // the rise just before it
	setpoints   int    // distinct values
	end         Sample // last sample of the final value
}

// ramps finds the setpoint ramps: falling changes in mode PID, at least
// rampValues values in every rampWindow at its start, rampShortest long.
func (d *Detector) ramps() []rampSpan {
	var out []rampSpan
	falling := func(c spChange) bool {
		return c.at.Mode == "PID" && c.prev.Mode == "PID" && *c.at.Setpoint < *c.prev.Setpoint
	}
	for i := 0; i < len(d.changes); {
		j := i
		for j < len(d.changes) && falling(d.changes[j]) &&
			(j == i || d.changes[j].brk == d.changes[i].brk && d.changes[j].at.At.Sub(d.changes[j-1].at.At) <= rampWindow) {
			j++
		}
		if j == i {
			i++
			continue
		}
		c := d.changes[i:j]
		dense := false
		for k := 0; k+rampValues-2 < len(c); k++ {
			// rampValues values are rampValues-1 changes.
			dense = dense || c[k+rampValues-2].at.At.Sub(c[k].at.At) <= rampWindow
		}
		if dense && c[len(c)-1].at.At.Sub(c[0].at.At) >= rampShortest {
			r := rampSpan{first: i, last: j - 1, setpoints: len(c) + 1, end: c[len(c)-1].end}
			if p := i - 1; p >= 0 && d.changes[p].brk == c[0].brk && *d.changes[p].at.Setpoint > *d.changes[p].prev.Setpoint {
				r.jump = &p
			}
			out = append(out, r)
		}
		i = j
	}
	return out
}
