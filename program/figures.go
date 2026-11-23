package program

import (
	"slices"
	"time"
)

// stepHold leaves out the phases a sweep only passes through.
const stepHold = time.Second

// figures are the numbers of f for wear tracking, by kind; nil for none.
func (d *Detector) figures(f *found) map[string]any {
	out := map[string]any{}
	switch f.kind {
	case PressureTest:
		out["steps"] = steps(f.pulses[0], func(s step) *float64 { return s.pressure })
	case FlowTest:
		out["steps"] = steps(f.pulses[0], func(s step) *float64 { return s.flow })
	case Autotune:
		if f.tune.before != nil {
			out["before"] = constants(*f.tune.before)
		}
		if f.tune.after != nil {
			out["after"] = constants(*f.tune.after)
		}
		periods := []float64{}
		for k := 1; k < len(f.tune.heatOn); k++ {
			periods = append(periods, f.tune.heatOn[k].Sub(f.tune.heatOn[k-1]).Seconds())
		}
		out["periods_s"] = periods
	case WarmUp:
		out["schedule"] = d.schedule(f)
	case Backflush:
		hold := []float64{}
		for _, p := range f.pulses {
			hold = append(hold, median(p.half2()))
		}
		out["hold_bar"] = hold
		if k := longestGap(f.pulses); k > 0 {
			if a, b := f.pulses[k-1].after, f.pulses[k].before; a != nil && b != nil {
				out["leak_bar"] = []float64{*a, *b}
			}
		}
	default:
		return nil
	}
	return out
}

// steps lists the phases p held and the value at the end of each.
func steps(p *pulse, value func(step) *float64) [][2]float64 {
	out := [][2]float64{}
	for _, s := range p.steps {
		if v := value(s); v != nil && s.end.Sub(s.start) >= stepHold {
			out = append(out, [2]float64{s.phase, *v})
		}
	}
	return out
}

// schedule lists the pulses of a warm-up: start after the ramp's first
// change and duration (s), phase.
func (d *Detector) schedule(f *found) [][3]float64 {
	start := d.changes[f.ramp.first].at.At
	out := [][3]float64{}
	for _, p := range f.pulses {
		ph := -1.0
		if v := p.phase(); v != nil {
			ph = *v
		}
		out = append(out, [3]float64{p.first.At.Sub(start).Seconds(), p.last.At.Sub(p.first.At).Seconds(), ph})
	}
	return out
}

// longestGap returns the index of the pulse after the longest pause, 0 with
// fewer than two pulses.
func longestGap(ps []*pulse) int {
	best := 0
	gap := func(k int) time.Duration { return ps[k].first.At.Sub(ps[k-1].last.At) }
	for k := 1; k < len(ps); k++ {
		if best == 0 || gap(k) > gap(best) {
			best = k
		}
	}
	return best
}

func constants(s Sample) map[string]float64 {
	out := map[string]float64{}
	for k, v := range map[string]*float64{"kc": s.Kc, "ti": s.Ti, "td": s.Td} {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(v))
	if n := len(s); n%2 == 0 {
		return (s[n/2-1] + s[n/2]) / 2
	}
	return s[len(s)/2]
}
