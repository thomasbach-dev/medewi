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
