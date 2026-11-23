package program

import (
	"math"
	"testing"
)

func only(t *testing.T, name string, s *seq) Run {
	t.Helper()
	rs := s.d.Runs()
	if len(rs) != 1 {
		t.Fatalf("%s: %d runs %v, want 1", name, len(rs), kinds(rs))
	}
	return rs[0]
}

func TestOutcome(t *testing.T) {
	bar := func(p float64) float64 { return (120 - p) / 12 }
	for _, c := range []struct {
		name string
		f    func(*seq)
		want Outcome
	}{
		{"pressure test", func(s *seq) { s.cold(); s.sweep(120, 0, -5, bar) }, Finished},
		{"pressure test stopped", func(s *seq) { s.cold(); s.sweep(120, 60, -5, bar) }, Aborted},
		{"flow test", func(s *seq) { s.cold(); s.sweep(0, 110, 5, flat(0.1)) }, Unknown},
		{"autotune", (*seq).autotune, Finished},
		{"autotune, same constants", func(s *seq) {
			s.autotune()
			s.st.Mode = "PID"
		}, Finished},
		{"warm-up", (*seq).warmup, Finished},
		{"descale", func(s *seq) {
			s.st.Setpoint = fp(0)
			s.idle(1)
			s.reset()
			s.rhythm(3)
			s.st.Setpoint = fp(90)
		}, Finished},
		{"descale without its end", func(s *seq) { s.st.Setpoint = fp(0); s.idle(1); s.reset(); s.rhythm(3) }, Unknown},
		{"backflush", func(s *seq) { s.backflush(3) }, Unknown},
		{"purge", func(s *seq) { s.cold(); s.idle(1); s.reset(); s.rhythm(3) }, Unknown},
	} {
		s := newSeq()
		s.used()
		s.idle(30)
		c.f(s)
		s.idle(90)
		if r := only(t, c.name, s); r.Outcome != c.want {
			t.Errorf("%s: outcome %s, want %s", c.name, r.Outcome, c.want)
		}
	}
}

func TestAutotuneUnchanged(t *testing.T) {
	s := newSeq()
	s.st.Kc, s.st.Ti, s.st.Td = fp(1), fp(20), fp(5)
	s.idle(5)
	s.st.Mode, s.st.Kc, s.st.Ti, s.st.Td = "ATN", nil, nil, nil
	s.idle(60)
	s.st.Mode, s.st.Kc, s.st.Ti, s.st.Td = "PID", fp(1), fp(20), fp(5)
	s.idle(60)
	if r := only(t, "autotune", s); r.Outcome != Unknown {
		t.Errorf("outcome %s, want unknown: same constants before and after", r.Outcome)
	}
}

func TestCut(t *testing.T) {
	start := func(f func(*seq)) *seq {
		s := newSeq()
		f(s)
		s.idle(120)
		return s
	}
	s := start(func(s *seq) { s.cold(); s.used(); s.pump(5, 0, nil); s.idle(20); s.rhythm(2) })
	if r := only(t, "starts mid-program", s); r.Outcome != Cut || !r.OpenStart {
		t.Errorf("starts mid-program: %+v", r)
	}
	s = newSeq()
	s.used()
	s.idle(5)
	s.backflush(3)
	if r := only(t, "input ends", s); r.Outcome != Cut || !r.OpenEnd {
		t.Errorf("input ends: %+v", r)
	}
	s = newSeq()
	s.used()
	s.idle(5)
	s.backflush(3)
	s.d.Break()
	s.idle(600)
	if r := only(t, "break", s); r.Outcome != Cut {
		t.Errorf("break: %s, want cut", r.Outcome)
	}
	s = newSeq()
	s.cold()
	s.idle(5)
	s.sweep(120, 60, -5, flat(1))
	s.st.Pump, s.st.Phase = true, fp(60)
	s.sample()
	s.d.Break()
	if r := only(t, "test broken off", s); r.Outcome != Cut {
		t.Errorf("test broken off: %s, want cut", r.Outcome)
	}
}

func TestFigures(t *testing.T) {
	s := newSeq()
	s.cold()
	s.idle(5)
	s.st.Pump = true
	for v := 120.0; v >= 0; v-- {
		s.st.Phase, s.st.Pressure = fp(v), fp(120-v)
		hold := 2
		if v >= 50 && int(v)%2 == 0 || v < 50 && int(v)%5 == 0 {
			hold = 16
		}
		for range hold {
			s.sample()
		}
	}
	s.st.Phase, s.st.Pressure = fp(180), fp(0)
	s.sample()
	s.idle(90)
	r := only(t, "pressure test", s)
	steps, _ := r.Figures["steps"].([][2]float64)
	if len(steps) != 46 || steps[0] != [2]float64{120, 0} || steps[45] != [2]float64{0, 120} {
		t.Errorf("%d steps %v, want 46 from 120 to 0", len(steps), steps)
	}

	s = newSeq()
	s.cold()
	s.idle(5)
	s.st.Flow = fp(7)
	s.sweep(0, 110, 5, flat(0.1))
	s.idle(90)
	steps, _ = only(t, "flow test", s).Figures["steps"].([][2]float64)
	if len(steps) != 23 || steps[1] != [2]float64{5, 7} {
		t.Errorf("flow steps %v", steps)
	}

	s = newSeq()
	s.autotune()
	f := only(t, "autotune", s).Figures
	if p, _ := f["periods_s"].([]float64); len(p) != 3 || p[0] != 100 {
		t.Errorf("periods %v, want 3 of 100 s", p)
	}
	b, _ := f["before"].(map[string]float64)
	a, _ := f["after"].(map[string]float64)
	if b["kc"] != 1 || b["ti"] != 20 || b["td"] != 5 || a["kc"] != 2 || a["ti"] != 30 || a["td"] != 7.5 {
		t.Errorf("constants %v -> %v", b, a)
	}

	s = newSeq()
	s.idle(5)
	s.warmup()
	sched, _ := only(t, "warm-up", s).Figures["schedule"].([][3]float64)
	if len(sched) == 0 || sched[0][2] != 50 || sched[0][0] < 9 || sched[0][0] > 12 || math.Abs(sched[0][1]-1.75) > 0.01 {
		t.Errorf("schedule %v", sched)
	}

	s = newSeq()
	s.used()
	s.idle(5)
	s.backflush(3)
	s.st.Pressure = fp(9)
	s.idle(60)
	s.st.Pressure = fp(7)
	s.idle(0.25)
	s.backflush(1)
	s.idle(90)
	f = only(t, "backflush", s).Figures
	if h, _ := f["hold_bar"].([]float64); len(h) != 4 || h[0] < 9 {
		t.Errorf("hold_bar %v", h)
	}
	if l, _ := f["leak_bar"].([]float64); len(l) != 2 || l[0] <= l[1] || l[1] != 7 {
		t.Errorf("leak_bar %v, want falling to 7", l)
	}
}
