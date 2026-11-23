package program

// The outcomes. Finished and Aborted are only given on evidence; most
// aborts look like ends.
const (
	Finished Outcome = "finished"
	Aborted  Outcome = "aborted"
	Cut      Outcome = "cut"
	Unknown  Outcome = "unknown"
)

// outcome says how f ended: an unseen start is a cut; proof of the
// program's own end beats an unseen end.
func (d *Detector) outcome(f *found) Outcome {
	switch {
	case f.lcd:
		return Unknown
	case f.open:
		return Cut
	case d.ended(f):
		return Finished
	case f.openEnd:
		return Cut
	case f.kind == PressureTest:
		return Aborted
	}
	return Unknown
}

// ended reports whether the stream shows the program's own end.
func (d *Detector) ended(f *found) bool {
	switch f.kind {
	case PressureTest:
		_, ok := f.pulses[0].phases[0]
		return ok
	case FlowTest:
		return d.flowTestDone(f)
	case Autotune:
		b, a := f.tune.before, f.tune.after
		return b != nil && a != nil && !sameConstants(*b, *a)
	case WarmUp:
		c := d.changes[f.ramp.last]
		return f.ramp.end.At.Sub(c.at.At) >= settle
	case Descale:
		end := f.pulses[len(f.pulses)-1]
		for _, c := range d.changes {
			if c.brk == end.brk && c.at.At.After(end.last.At) {
				return *c.prev.Setpoint == 0 && *c.at.Setpoint > 0
			}
		}
	}
	return false
}

func sameConstants(a, b Sample) bool {
	same := func(x, y *float64) bool { return x == nil || y == nil || *x == *y }
	return same(a.Kc, b.Kc) && same(a.Ti, b.Ti) && same(a.Td, b.Td)
}
