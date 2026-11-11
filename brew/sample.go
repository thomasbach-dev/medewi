package brew

import (
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// Export names from the vendor XML. A name missing from the spec leaves the
// value unknown (nil in the Sample).
const (
	fieldTemp       = "Temperature PID1"
	fieldSetpoint   = "Setpoint PID"
	fieldHeat       = "Heat PID"
	fieldPressure   = "Pressure"
	fieldPressureSP = "Pressure SP" // without it every brew is a flush
	fieldFlow       = "Flow rate"   // factor-scaled to ml/s
	fieldVolume     = "Volume"
)

// FieldNames lists the export names SampleFrom reads.
var FieldNames = []string{fieldTemp, fieldSetpoint, fieldHeat, fieldPressure, fieldPressureSP, fieldFlow, fieldVolume}

// SampleFrom builds a Sample from a decoded record; Pumping is the pump
// marker, payload byte 2.
func SampleFrom(rec leva.Record, payload []byte, at time.Time) Sample {
	f := func(name string) *float64 {
		if v, ok := rec.Float(name); ok {
			return &v
		}
		return nil
	}
	return Sample{
		At:         at,
		Pumping:    len(payload) > 2 && payload[2] == '*',
		Temp:       f(fieldTemp),
		Setpoint:   f(fieldSetpoint),
		Pressure:   f(fieldPressure),
		PressureSP: f(fieldPressureSP),
		Flow:       f(fieldFlow),
		Heat:       f(fieldHeat),
		Volume:     f(fieldVolume),
	}
}
