package logview

import (
	"testing"

	"github.com/thomasbach-dev/medewi/leva"
)

func TestFieldOf(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, false, []step{{pump: '*', bar: 9, sp: "9.0", volume: 3}})
	e := &l.Entries[0]
	v, ok := FieldOf(e, "pressure")
	if !ok || v.Num == nil || *v.Num != 9 {
		t.Errorf("pressure = %+v, %v", v, ok)
	}
	v, ok = FieldOf(e, "pressure_sp")
	if !ok || v.Num == nil || *v.Num != 9 {
		t.Errorf("pressure_sp = %+v, %v", v, ok)
	}
	v, ok = FieldOf(e, "pump")
	if !ok || v.Str == nil || *v.Str != "*" {
		t.Errorf("pump = %+v, %v", v, ok)
	}
	if _, ok := FieldOf(e, "nosuch"); ok {
		t.Error("nosuch: want false")
	}

	standby := *e
	standby.Raw = append([]byte(nil), e.Raw...)
	standby.Raw[2] = '?'
	if v, ok := FieldOf(&standby, "pump"); !ok || !v.Masked {
		t.Errorf("standby pump = %+v, %v", v, ok)
	}

	raw := *e
	raw.Record, raw.Spec = nil, nil
	for _, k := range []string{"pressure", "pump"} {
		if _, ok := FieldOf(&raw, k); ok {
			t.Errorf("%s of an undecoded report: want false", k)
		}
	}
}

func TestIsTextKey(t *testing.T) {
	spec := &leva.RecordSpec{Values: []leva.ValueSpec{
		{Index: 0, At: 4, Len: 3, Type: leva.String},
		{Index: 1, Name: "Pressure", At: 7, Len: 5, Type: leva.Float},
	}}
	for k, want := range map[string]bool{"pump": true, "mode": true, "pressure": false, "nosuch": false} {
		if got := IsTextKey(spec, k); got != want {
			t.Errorf("IsTextKey(%q) = %v, want %v", k, got, want)
		}
	}
	if !IsTextKey(nil, "pump") || IsTextKey(nil, "mode") {
		t.Error("nil spec: only pump is text")
	}
}
