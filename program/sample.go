package program

import (
	"strconv"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// Sample is one decoded report line. Nil means the field was masked or
// missing.
type Sample struct {
	At            time.Time
	Ref           any    // the caller's, returned in Run.First and Run.Last
	Pump, Standby bool   // pump marker (payload byte 2) '*', '?'
	Mode          string // payload bytes 4-6

	Temp, Setpoint, Heat, Phase, Pressure, PressureSP, Flow, Volume, Ticks, Kc, Ti, Td *float64
}

// SampleFrom builds a Sample from a decoded record.
func SampleFrom(rec leva.Record, payload []byte, at time.Time, ref any) Sample {
	f := func(name string) *float64 {
		if v, ok := rec.Float(name); ok {
			return &v
		}
		return nil
	}
	s := Sample{
		At: at, Ref: ref,
		Temp: f("Temperature PID1"), Setpoint: f("Setpoint PID"), Heat: f("Heat PID"),
		Phase: f("Phase"), Pressure: f("Pressure"), PressureSP: f("Pressure SP"),
		Flow: f("Flow rate"), Volume: f("Volume"), Ticks: ticks(rec),
		Kc: f("Kc PID"), Ti: f("Ti PID"), Td: f("Td PID"),
	}
	if len(payload) > 2 {
		s.Pump, s.Standby = payload[2] == '*', payload[2] == '?'
	}
	if len(payload) >= 7 {
		s.Mode = string(payload[4:7])
	}
	return s
}

// ticks returns the flow-meter tick counter: the field without an export
// name whose unit is "ticks" (issue #67).
func ticks(rec leva.Record) *float64 {
	for _, fl := range rec.Fields {
		if fl.Spec.Name != "" || fl.Spec.Unit != "ticks" {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(fl.Raw), 64); err == nil {
			return &v
		}
		return nil
	}
	return nil
}
