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
