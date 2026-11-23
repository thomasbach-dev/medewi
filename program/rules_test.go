package program

import (
	"slices"
	"testing"
)

func kinds(rs []Run) []Kind {
	var out []Kind
	for _, r := range rs {
		out = append(out, r.Kind)
	}
	return out
}

func wantKinds(t *testing.T, name string, s *seq, want ...Kind) []Run {
	t.Helper()
	rs := s.d.Runs()
	if !slices.Equal(kinds(rs), want) {
		t.Errorf("%s: kinds %v, want %v", name, kinds(rs), want)
	}
	return rs
}

var kindSeqs = []struct {
	kind Kind
	f    func(*seq)
}{
	{WarmUp, (*seq).warmup},
	{Autotune, (*seq).autotune},
	{PressureTest, func(s *seq) { s.cold(); s.sweep(120, 0, -5, func(p float64) float64 { return (120 - p) / 12 }) }},
	{FlowTest, func(s *seq) { s.cold(); s.sweep(0, 110, 5, flat(0.1)) }},
	{Backflush, func(s *seq) { s.backflush(6); s.idle(80); s.backflush(3) }},
	{Descale, func(s *seq) { s.st.Setpoint = fp(0); s.idle(1); s.reset(); s.rhythm(4) }},
	{Purge, func(s *seq) { s.cold(); s.idle(1); s.reset(); s.rhythm(4) }},
}

func TestKinds(t *testing.T) {
	for _, c := range kindSeqs {
		s := newSeq()
		s.used()
		s.idle(30)
		s.flush(20, 85, nil)
		c.f(s)
		s.idle(30)
		rs := wantKinds(t, string(c.kind), s, c.kind)
		if len(rs) == 1 && (rs[0].First == nil || rs[0].Last == nil || !rs[0].End.After(rs[0].Start)) {
			t.Errorf("%s: run %+v", c.kind, rs[0])
		}
	}
}

func TestNotPrograms(t *testing.T) {
	for _, c := range []struct {
		name string
		f    func(*seq)
	}{
		{"flush", func(s *seq) { s.flush(20, 85, flat(0.1)) }},
		{"full-power flushes", func(s *seq) {
			for range 3 {
				s.flush(20, 0, flat(0.1))
			}
		}},
		{"one blind-basket pulse", func(s *seq) { s.backflush(1) }},
		{"flush after a shot", func(s *seq) {
			for range 2 {
				s.flush(20, 0, func(t float64) float64 { return max(0.1, 6-2*t) })
			}
		}},
		{"short pulse", func(s *seq) { s.flush(0.5, 0, nil) }},
		{"shots", func(s *seq) {
			s.st.PressureSP = fp(9)
			s.backflush(3)
			s.st.PressureSP = nil
		}},
		{"blind pulses half an hour apart", func(s *seq) { s.backflush(1); s.idle(1800); s.backflush(1) }},
	} {
		s := newSeq()
		s.used()
		s.idle(10)
		c.f(s)
		s.idle(10)
		wantKinds(t, c.name, s)
	}
}

func TestBreakSplits(t *testing.T) {
	s := newSeq()
	s.cold()
	s.used()
	s.idle(5)
	s.reset()
	s.rhythm(2)
	s.d.Break()
	s.rhythm(2)
	rs := s.d.Runs()
	total := 0
	for _, r := range rs {
		total += len(r.Pulses)
		if len(r.Pulses) > 2 {
			t.Errorf("a run spans the break: %d pulses", len(r.Pulses))
		}
	}
	if len(rs) != 2 || total != 4 {
		t.Errorf("%d runs with %d pulses, want 2 with 4", len(rs), total)
	}
}

func TestCounterReset(t *testing.T) {
	descale := func(sp *float64, mode string) *seq {
		s := newSeq()
		s.used()
		s.st.Setpoint = fp(0)
		s.idle(5)
		s.reset()
		s.rhythm(3)
		s.st.Mode, s.st.Setpoint = mode, sp
		s.idle(30)
		s.reset()
		s.idle(5)
		s.rhythm(3)
		return s
	}
	rs := wantKinds(t, "same setpoint", descale(fp(0), "PID"), Descale)
	var stages []string
	for _, p := range rs[0].Pulses {
		stages = append(stages, p.Stage)
	}
	if !slices.Equal(stages, []string{"descaler", "descaler", "descaler", "purge", "purge", "purge"}) {
		t.Errorf("stages %v", stages)
	}
	wantKinds(t, "cold purge after", descale(nil, "CLD"), Descale, Purge)
}

func TestWaitAnyLength(t *testing.T) {
	s := newSeq()
	s.used()
	s.st.Setpoint = fp(0)
	s.idle(5)
	s.reset()
	s.rhythm(2)
	s.idle(15 * 60)
	s.rhythm(2)
	wantKinds(t, "15 min wait", s, Descale)
}

func TestMaskedTicks(t *testing.T) {
	s := newSeq()
	s.cold()
	s.used()
	s.idle(5)
	s.reset()
	s.rhythm(2)
	s.st.Ticks = nil
	s.rhythm(1)
	rs := wantKinds(t, "masked", s, Purge)
	if len(rs) == 1 && len(rs[0].Pulses) != 2 {
		t.Errorf("%d pulses, want 2", len(rs[0].Pulses))
	}
}

// A program's first pulses with volume counting on give each pulse its own
// volume.
func TestPulseVolume(t *testing.T) {
	s := newSeq()
	s.cold()
	s.used()
	s.idle(5)
	s.reset()
	s.rhythm(3)
	for k, p := range wantKinds(t, "purge", s, Purge)[0].Pulses {
		if p.VolumeML == nil || *p.VolumeML < 39 || *p.VolumeML > 41 {
			t.Errorf("pulse %d: volume %v, want 40", k, p.VolumeML)
		}
	}
}

func TestRunsIsRepeatable(t *testing.T) {
	s := newSeq()
	s.used()
	s.idle(5)
	s.backflush(3)
	a, b := s.d.Runs(), s.d.Runs()
	if len(a) != 1 || len(b) != 1 || len(a[0].Pulses) != len(b[0].Pulses) {
		t.Errorf("runs differ: %+v %+v", a, b)
	}
	s.backflush(2)
	if rs := s.d.Runs(); len(rs) != 1 || len(rs[0].Pulses) != 5 {
		t.Errorf("after more pulses: %+v", rs)
	}
}

// The counter adds a little after the pump stops; that belongs to the pulse.
func TestPulseVolumeCoast(t *testing.T) {
	s := newSeq()
	s.cold()
	s.used()
	s.idle(5)
	s.reset()
	for range 3 {
		s.pump(10, 0, nil)
		s.st.Ticks, s.st.Volume = fp(*s.st.Ticks+6), fp(*s.st.Volume+3)
		s.idle(1)
		s.idle(20)
		s.st.Ticks, s.st.Volume = fp(*s.st.Ticks+20), fp(*s.st.Volume+10) // a wand draw
		s.idle(1)
	}
	for k, p := range wantKinds(t, "purge", s, Purge)[0].Pulses {
		if p.VolumeML == nil || *p.VolumeML != 43 {
			t.Errorf("pulse %d: volume %v, want 43", k, *p.VolumeML)
		}
	}
}
