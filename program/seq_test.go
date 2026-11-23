package program

import (
	"time"
)

func fp(v float64) *float64 { return &v }

// seq feeds a Detector samples every 250 ms. st is the sample's state; its
// fields are copied into every sample until changed.
type seq struct {
	d   *Detector
	at  time.Time
	st  Sample
	n   int // samples fed: their Ref
	mlT float64
}

func newSeq() *seq {
	s := &seq{d: NewDetector(), at: t0, mlT: 0.5}
	s.st = Sample{Mode: "PID", Setpoint: fp(90), Temp: fp(90), Heat: fp(0), Pressure: fp(0.1), Volume: fp(0), Ticks: fp(0)}
	return s
}

func (s *seq) sample() {
	c := s.st
	c.At, c.Ref = s.at, s.n
	s.n++
	s.d.Sample(c)
	s.at = s.at.Add(250 * time.Millisecond)
}

// idle feeds sec seconds with the pump off.
func (s *seq) idle(sec float64) {
	s.st.Pump, s.st.Phase = false, nil
	for range int(sec * 4) {
		s.sample()
	}
}

// pump feeds sec seconds with the pump on at phase, adding 2 ticks (1 ml)
// per sample; bar gives the pressure by time into the pulse.
func (s *seq) pump(sec, phase float64, bar func(t float64) float64) {
	s.st.Pump, s.st.Phase = true, fp(phase)
	for k := range int(sec * 4) {
		if bar != nil {
			s.st.Pressure = fp(bar(float64(k) / 4))
		}
		if s.st.Ticks != nil {
			s.st.Ticks = fp(*s.st.Ticks + 2)
			s.st.Volume = fp(*s.st.Ticks * s.mlT)
			if *s.st.Volume > 999 {
				s.st.Volume = nil
			}
		}
		s.sample()
	}
	s.st.Pump, s.st.Phase = false, nil
}

func (s *seq) skip(sec float64) { s.at = s.at.Add(time.Duration(sec * float64(time.Second))) }

// reset zeroes the counters, as a pump start outside a program does.
func (s *seq) reset() { s.st.Ticks, s.st.Volume = fp(0), fp(0) }

func flat(v float64) func(float64) float64 { return func(float64) float64 { return v } }

// used sets the counters to a value left over from earlier use.
func (s *seq) used() { s.st.Ticks, s.st.Volume = fp(400), fp(200) }

// flush is one pump run at phase with a counter reset at its start.
func (s *seq) flush(sec, phase float64, bar func(float64) float64) {
	s.idle(5)
	s.reset()
	s.pump(sec, phase, bar)
	s.idle(5)
}

// rhythm is n pulses of 10 s at full power, 20 s apart, counting on.
func (s *seq) rhythm(n int) {
	for range n {
		s.pump(10, 0, flat(0.1))
		s.idle(20)
	}
}

func (s *seq) cold() { s.st.Mode, s.st.Setpoint = "CLD", nil }

func (s *seq) backflush(n int) {
	for range n {
		s.reset()
		s.pump(6, 0, func(t float64) float64 { return min(10, 3*t) })
		s.idle(10)
	}
}

// sweep is one pulse through the phases from..to, 2 s each.
func (s *seq) sweep(from, to, by float64, bar func(phase float64) float64) {
	s.st.Pump = true
	for v := from; (by > 0 && v <= to) || (by < 0 && v >= to); v += by {
		s.st.Phase, s.st.Pressure = fp(v), fp(bar(v))
		for range 8 {
			s.sample()
		}
	}
	s.st.Pump, s.st.Phase = false, nil
}

// warmup is a 90 → 110 jump, then a ramp back with three pulses.
func (s *seq) warmup() {
	s.st.Setpoint = fp(110)
	s.idle(60)
	for k := range 100 {
		s.st.Setpoint = fp(110 - 0.2*float64(k+1))
		if k%30 == 5 {
			s.pump(2, 50, flat(0.2))
		} else {
			s.idle(2)
		}
	}
	s.idle(60)
}

func (s *seq) autotune() {
	s.st.Kc, s.st.Ti, s.st.Td = fp(1), fp(20), fp(5)
	s.idle(5)
	s.st.Mode, s.st.Kc, s.st.Ti, s.st.Td = "ATN", nil, nil, nil
	for range 4 {
		s.st.Heat = fp(0)
		s.idle(60)
		s.st.Heat = fp(20)
		s.idle(40)
	}
	s.st.Heat = fp(0)
	s.idle(60)
	s.st.Mode = "PID"
	s.st.Kc, s.st.Ti, s.st.Td = fp(2), fp(30), fp(7.5)
	s.idle(60)
}
