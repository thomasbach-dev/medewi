package brew

import (
	"testing"

	"github.com/thomasbach-dev/medewi/leva"
)

func pressureRecord() leva.Record {
	return leva.Record{Fields: []leva.Field{
		{Spec: leva.ValueSpec{Name: "Pressure"}, Raw: "9.0"},
	}}
}

func TestSampleFrom(t *testing.T) {
	s := SampleFrom(pressureRecord(), []byte("{ *rest"), t0)
	if !s.Pumping {
		t.Error("Pumping = false, want true for '*'")
	}
	if s.Pressure == nil || *s.Pressure != 9 {
		t.Errorf("Pressure = %v, want 9", s.Pressure)
	}
	if s.Volume != nil {
		t.Errorf("Volume = %v, want nil", *s.Volume)
	}
	if !s.At.Equal(t0) {
		t.Errorf("At = %v, want %v", s.At, t0)
	}
}

// The flow-meter tick counter has no export name in the XML; it is the
// field whose unit is "ticks" (issue #67).
func TestSampleFromTicks(t *testing.T) {
	rec := leva.Record{Fields: []leva.Field{
		{Spec: leva.ValueSpec{Name: "Volume", Unit: "ml"}, Raw: "???"},
		{Spec: leva.ValueSpec{Index: 31, Unit: "ticks"}, Raw: " 4494"},
	}}
	s := SampleFrom(rec, []byte("{ -rest"), t0)
	if s.Volume != nil {
		t.Errorf("Volume = %v, want nil (masked)", *s.Volume)
	}
	if s.Ticks == nil || *s.Ticks != 4494 {
		t.Errorf("Ticks = %v, want 4494", s.Ticks)
	}
	if s := SampleFrom(pressureRecord(), []byte("{ -rest"), t0); s.Ticks != nil {
		t.Errorf("Ticks = %v without a ticks field, want nil", *s.Ticks)
	}
	named := leva.Record{Fields: []leva.Field{{Spec: leva.ValueSpec{Name: "Other", Unit: "ticks"}, Raw: "7"}}}
	if s := SampleFrom(named, []byte("{ -rest"), t0); s.Ticks != nil {
		t.Errorf("a named ticks field was taken for the flow meter: %v", *s.Ticks)
	}
}

func TestSampleFromNotPumping(t *testing.T) {
	if s := SampleFrom(pressureRecord(), []byte("{ -rest"), t0); s.Pumping {
		t.Error("Pumping = true for '-'")
	}
	if s := SampleFrom(pressureRecord(), []byte("{ "), t0); s.Pumping {
		t.Error("Pumping = true for a 2-byte payload")
	}
}

func TestFieldNames(t *testing.T) {
	if len(FieldNames) != 7 {
		t.Errorf("FieldNames has %d entries, want 7", len(FieldNames))
	}
}
