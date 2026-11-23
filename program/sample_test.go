package program

import (
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func TestSampleFrom(t *testing.T) {
	rec := leva.Record{Fields: []leva.Field{
		{Spec: leva.ValueSpec{Name: "Phase"}, Raw: "085"},
		{Spec: leva.ValueSpec{Name: "Setpoint PID"}, Raw: "?????"},
		{Spec: leva.ValueSpec{Name: "Kc PID"}, Raw: "1.5"},
		{Spec: leva.ValueSpec{Name: "Ti PID"}, Raw: "20"},
		{Spec: leva.ValueSpec{Name: "Td PID"}, Raw: "5"},
		{Spec: leva.ValueSpec{Name: "Flow rate", Unit: "ml/s", Factor: 0.5}, Raw: "10"},
		{Spec: leva.ValueSpec{Name: "Flow rate", Unit: "g/s"}, Raw: "99"},
		{Spec: leva.ValueSpec{Index: 31, Unit: "ticks"}, Raw: "4494"},
	}}
	s := SampleFrom(rec, []byte("{ *1CLDrest"), t0, "ref")
	if !s.Pump || s.Standby || s.Mode != "CLD" || s.Ref != "ref" || !s.At.Equal(t0) {
		t.Errorf("sample = %+v", s)
	}
	for name, c := range map[string]struct {
		got  *float64
		want float64
	}{
		"phase": {s.Phase, 85}, "kc": {s.Kc, 1.5}, "ti": {s.Ti, 20}, "td": {s.Td, 5},
		"ticks": {s.Ticks, 4494}, "flow": {s.Flow, 5},
	} {
		if c.got == nil || *c.got != c.want {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
	}
	if s.Setpoint != nil || s.Pressure != nil {
		t.Errorf("masked and missing fields must be nil: %v %v", s.Setpoint, s.Pressure)
	}
	if s := SampleFrom(rec, []byte("{ ?1???rest"), t0, nil); !s.Standby || s.Pump {
		t.Errorf("standby sample = %+v", s)
	}
	if s := SampleFrom(rec, []byte("{ "), t0, nil); s.Pump || s.Mode != "" {
		t.Errorf("short payload = %+v", s)
	}
}
