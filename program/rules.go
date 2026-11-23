package program

import (
	"fmt"
	"slices"
	"time"
)

const (
	// backflushBar is the firmware's own threshold (manual p. 37: ERR:OPEN
	// below 1.0 bar). No flush reaches it in its second half.
	backflushBar = 1.0
	// backflushGap covers EMPTY, where the program waits for a key press.
	backflushGap = 10 * time.Minute
	// sweepPhases and sweepNet make a pulse a phase sweep.
	sweepPhases = 8
	sweepNet    = 0.8
	// settle is how long the setpoint holds after a warm-up's ramp.
	settle = 20 * time.Second
	// cutMargin: an interruption this soon after the last pulse cuts a run.
	cutMargin = 60 * time.Second
)

// found is a run while it is being put together.
type found struct {
	kind    Kind
	pulses  []*pulse
	stages  []string
	first   Sample
	last    Sample
	open    bool // start unseen
	openEnd bool
	ramp    *rampSpan
	tune    *modeSpan
	lcd     bool // named by the LCD only
}

// Runs returns the program runs seen so far, by start. It does not change
// d, so more samples may follow.
func (d *Detector) Runs() []Run {
	fs := d.find()
	out := make([]Run, 0, len(fs))
	for _, f := range fs {
		out = append(out, d.run(f))
	}
	return out
}

func (d *Detector) find() []*found {
	ps := d.allPulses()
	taken := make([]bool, len(ps))
	var out []*found
	for _, r := range d.ramps() {
		out = append(out, d.warmUp(r, ps, taken))
	}
	for _, t := range d.tunes {
		last := t.openEnd || t == d.tune && d.have && d.prev.Mode == "ATN"
		out = append(out, &found{kind: Autotune, first: t.first, last: t.last, open: t.openStart, openEnd: last, tune: t})
	}
	for i, p := range ps {
		if taken[i] || p.shot {
			continue
		}
		kind := Kind("")
		switch n := sweep(p); {
		case n < -sweepNet:
			kind = PressureTest
		case n > sweepNet:
			kind = FlowTest
		}
		if kind != "" {
			taken[i] = true
			out = append(out, d.group(kind, ps[i:i+1], nil))
		}
	}
	for i := 0; i < len(ps); {
		if j := backflushRun(ps, taken, i); j-i >= 2 {
			out = append(out, d.group(Backflush, ps[i:j], nil))
			for ; i < j; i++ {
				taken[i] = true
			}
			continue
		}
		f, j := d.rinse(ps, taken, i)
		if f != nil {
			out = append(out, f)
			for k := i; k < j; k++ {
				taken[k] = true
			}
		}
		i = max(j, i+1)
	}
	out = append(out, d.lcdOnly(ps, taken)...)
	slices.SortStableFunc(out, func(a, b *found) int { return a.first.At.Compare(b.first.At) })
	return out
}

// sweep is the net direction of a pulse's phase steps, -1..1; 0 for a pulse
// with few phases.
func sweep(p *pulse) float64 {
	if len(p.phases) <= sweepPhases {
		return 0
	}
	net := 0
	for k := 1; k < len(p.steps); k++ {
		if p.steps[k].phase > p.steps[k-1].phase {
			net++
		} else {
			net--
		}
	}
	return float64(net) / float64(len(p.steps)-1)
}

func (d *Detector) warmUp(r rampSpan, ps []*pulse, taken []bool) *found {
	f := &found{kind: WarmUp, first: d.changes[r.first].prev, last: r.end, ramp: &r}
	if r.jump != nil {
		f.first = d.changes[*r.jump].at
	} else {
		f.open = true
	}
	lastChange := d.changes[r.last].at.At
	if f.last.At.After(lastChange.Add(settle)) {
		f.last = d.changes[r.last].at
	} else {
		f.openEnd = d.changes[r.last].brk < d.brk || d.changes[r.last].open
	}
	brk := d.changes[r.first].brk
	for i, p := range ps {
		if p.brk == brk && !p.shot && !p.first.At.Before(f.first.At) && !p.first.At.After(lastChange.Add(settle)) {
			taken[i] = true
			f.pulses = append(f.pulses, p)
			if p.last.At.After(f.last.At) {
				f.last = p.last
			}
		}
	}
	return f
}

// fullPower reports whether p can belong to a full-power program.
func fullPower(p *pulse) bool {
	ph := p.phase()
	return !p.shot && ph != nil && *ph == 0
}

// backflushRun returns the end of the backflush pulses starting at i.
func backflushRun(ps []*pulse, taken []bool, i int) int {
	j := i
	for j < len(ps) && !taken[j] && fullPower(ps[j]) && ps[j].half2Max() >= backflushBar &&
		(j == i || ps[j].brk == ps[i].brk && ps[j].modes == ps[i].modes &&
			ps[j].first.At.Sub(ps[j-1].last.At) <= backflushGap) {
		j++
	}
	return j
}

// rinse finds a descale or purge starting at pulse i: full-power pulses
// whose tick counter goes on. A descale's setpoint stays at its value over
// a counter reset, between its descaler and its purge stage. It returns the
// index after the group; nil if there is none.
func (d *Detector) rinse(ps []*pulse, taken []bool, i int) (*found, int) {
	if taken[i] || !fullPower(ps[i]) {
		return nil, i + 1
	}
	zero := func(p *pulse) bool {
		return p.first.Mode != "CLD" && p.first.Setpoint != nil && *p.first.Setpoint == 0
	}
	stages := []string{stageDescaler}
	purging := false
	j := i + 1
	for ; j < len(ps) && !taken[j] && fullPower(ps[j]) && ps[j].brk == ps[i].brk && ps[j].modes == ps[i].modes; j++ {
		goesOn := ps[j].cont != nil && *ps[j].cont
		same := zero(ps[i]) && zero(ps[j]) && ps[j].setpoints == ps[i].setpoints
		if !goesOn && !same {
			break
		}
		if !goesOn || ps[j].resets != ps[j-1].resets {
			purging = true
		}
		stage := stageDescaler
		if purging {
			stage = stagePurge
		}
		stages = append(stages, stage)
	}
	if j-i < 2 {
		return nil, j
	}
	if slices.ContainsFunc(ps[i:j], zero) {
		return d.group(Descale, ps[i:j], stages), j
	}
	return d.group(Purge, ps[i:j], nil), j
}

func (d *Detector) group(kind Kind, ps []*pulse, stages []string) *found {
	f := &found{kind: kind, pulses: ps, stages: stages, first: ps[0].first, last: ps[len(ps)-1].last}
	f.open = ps[0].openStart
	if kind == Descale || kind == Purge {
		f.open = f.open || ps[0].cont != nil && *ps[0].cont
	}
	end := ps[len(ps)-1]
	f.openEnd = end.openEnd
	if kind == PressureTest || kind == FlowTest {
		return f // one pulse: nothing can follow
	}
	for _, b := range d.breaks {
		f.openEnd = f.openEnd || !b.Before(end.last.At) && b.Sub(end.last.At) < cutMargin
	}
	if d.have && end.brk == d.brk {
		f.openEnd = f.openEnd || d.prev.At.Sub(end.last.At) < cutMargin
	}
	return f
}

func (d *Detector) run(f *found) Run {
	if d.profileDescale(f) {
		f.kind = Descale
	}
	r := Run{
		Kind: f.kind, Start: f.first.At, End: f.last.At, First: f.first.Ref, Last: f.last.Ref,
		OpenStart: f.open, OpenEnd: f.openEnd,
		Outcome: d.outcome(f), Figures: d.figures(f),
	}
	r.OpenAtEnd = f.openEnd && d.have && !slices.ContainsFunc(d.breaks, func(b time.Time) bool { return !b.Before(r.End) })
	for k, p := range f.pulses {
		pl := Pulse{Start: p.first.At, End: p.last.At, PeakBar: p.peak(), Phase: p.phase(), VolumeML: ownVolume(p)}
		if f.stages != nil {
			pl.Stage = f.stages[k]
		}
		r.Pulses = append(r.Pulses, pl)
	}
	if f.tune != nil {
		for _, g := range f.tune.lost {
			r.Notes = append(r.Notes, fmt.Sprintf("connection lost for %.0f s", g.Seconds()))
		}
	}
	if d.judge(&r); f.lcd {
		r.Status, r.Evidence = LCD, []string{"lcd"}
	}
	d.compare(f, &r)
	return r
}

// ownVolume is the volume p pumped itself: the counter's growth when it
// went on, else the counter.
func ownVolume(p *pulse) *float64 {
	if p.endML == nil {
		return nil
	}
	v := *p.endML
	if p.cont != nil && *p.cont {
		if p.startML == nil {
			return nil
		}
		v -= *p.startML
	}
	return &v
}
