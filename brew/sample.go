package brew

import (
	"strconv"
	"strings"
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

// unitTicks is the XML unit of the flow-meter tick counter, a field without
// an export name (field 31 in leva!-65FCB201).
const unitTicks = "ticks"

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
		Ticks:      ticks(rec),
	}
}

// ticks returns the flow-meter tick counter: the field without an export
// name whose unit is "ticks"; nil if there is none or it is masked.
func ticks(rec leva.Record) *float64 {
	for _, fl := range rec.Fields {
		if fl.Spec.Name != "" || fl.Spec.Unit != unitTicks {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(fl.Raw), 64)
		if err != nil {
			return nil
		}
		return &v
	}
	return nil
}
