package program

import (
	"testing"
	"time"
)

func TestPulseGap(t *testing.T) {
	for _, c := range []struct {
		name string
		mid  func(*seq)
		want int
	}{
		{"10 s gap", func(s *seq) { s.skip(10) }, 1},
		{"20 s gap", func(s *seq) { s.skip(20) }, 2},
		{"idle sample", func(s *seq) { s.idle(0.25) }, 2},
		{"break", func(s *seq) { s.d.Break() }, 2},
	} {
		s := newSeq()
		s.idle(2)
		s.pump(3, 0, nil)
		c.mid(s)
		s.pump(3, 0, nil)
		s.idle(2)
		if got := len(s.d.allPulses()); got != c.want {
			t.Errorf("%s: %d pulses, want %d", c.name, got, c.want)
		}
	}
}

func TestContinuation(t *testing.T) {
	for _, c := range []struct {
		name        string
		before, now *float64
		brk         bool
		want        string
	}{
		{"goes on", fp(100), fp(101), false, "true"},
		{"reset", fp(100), fp(2), false, "false"},
		{"from zero", fp(0), fp(1), false, "unknown"},
		{"masked", fp(100), nil, false, "unknown"},
		{"after break", fp(100), fp(101), true, "unknown"},
	} {
		s := newSeq()
		s.st.Ticks = c.before
		s.idle(1)
		if c.brk {
			s.d.Break()
		}
		s.st.Pump, s.st.Phase, s.st.Ticks = true, fp(0), c.now
		s.sample()
		s.idle(1)
		ps := s.d.allPulses()
		if len(ps) != 1 {
			t.Fatalf("%s: %d pulses", c.name, len(ps))
		}
		got := "unknown"
		if p := ps[0]; p.cont != nil {
			got = map[bool]string{true: "true", false: "false"}[*p.cont]
		}
		if got != c.want {
			t.Errorf("%s: continued %s, want %s", c.name, got, c.want)
		}
	}
}

func ramp(s *seq, from, to float64, every time.Duration) {
	for v := from; v >= to-0.01; v -= 0.1 {
		s.st.Setpoint = fp(v)
		s.idle(every.Seconds())
	}
}

func TestRamp(t *testing.T) {
	count := func(f func(*seq)) int {
		s := newSeq()
		s.idle(5)
		f(s)
		s.idle(60)
		return len(s.d.ramps())
	}
	if n := count(func(s *seq) { ramp(s, 100, 95, 2*time.Second) }); n != 1 {
		t.Errorf("100 s ramp: %d ramps, want 1", n)
	}
	if n := count(func(s *seq) { ramp(s, 100, 99, 2*time.Second) }); n != 0 {
		t.Errorf("20 s ramp (Ramp & Soak flush): %d ramps, want 0", n)
	}
	if n := count(func(s *seq) { s.st.Setpoint = fp(79); s.st.Mode = "ECO" }); n != 0 {
		t.Errorf("eco: %d ramps, want 0", n)
	}
	if n := count(func(s *seq) { s.st.Mode = "ATN"; ramp(s, 100, 95, 2*time.Second); s.st.Mode = "PID" }); n != 0 {
		t.Errorf("ramp in ATN: %d ramps, want 0", n)
	}
	if n := count(func(s *seq) {
		for v := 90.0; v < 95; v += 0.1 {
			s.st.Setpoint = fp(v)
			s.idle(2)
		}
	}); n != 0 {
		t.Errorf("rising ramp: %d ramps, want 0", n)
	}
}

func TestHalf2(t *testing.T) {
	s := newSeq()
	s.idle(1)
	s.pump(8, 85, func(t float64) float64 { return max(0.1, 6-2*t) })
	s.idle(1)
	p := s.d.allPulses()[0]
	if m := p.half2Max(); m >= 1 {
		t.Errorf("second-half maximum %v, want < 1.0", m)
	}
	if p.peak() != 6 {
		t.Errorf("peak %v, want 6", p.peak())
	}
}

func TestPhase(t *testing.T) {
	s := newSeq()
	s.idle(1)
	s.pump(0.5, 0, nil)
	s.idle(1)
	if p := s.d.allPulses(); len(p) != 1 || p[0].phase() == nil || *p[0].phase() != 0 {
		t.Errorf("pulses = %+v", p)
	}
	s.st.Pump, s.st.Phase = true, nil
	s.sample()
	s.idle(1)
	if p := s.d.allPulses(); len(p) != 2 || p[1].phase() != nil {
		t.Errorf("masked phase: pulses = %+v", p)
	}
}
