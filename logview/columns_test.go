package logview

import (
	"os"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/leva"
)

func TestKey(t *testing.T) {
	for in, want := range map[string]string{
		"Foo Bar (P)":      "foo_bar_p",
		"dX/dt Y1":         "dx_dt_y1",
		"Temperature PID1": "temperature_pid1",
		"Pressure SP":      "pressure_sp",
		"ml/s":             "ml_s",
		"°C":               "c",
		"":                 "",
	} {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestColumnsSynthetic(t *testing.T) {
	f, err := os.Open(syntheticPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	spec, err := leva.ParseSpec(f)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := Columns(spec)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range cols {
		keys = append(keys, c.Key)
	}
	want := "t,wall,src,src_end,dir,event,run,run_t,pump,xml," +
		"mode,temperature_pid1,setpoint_pid,pressure,heat_pid,field_5"
	if got := strings.Join(keys, ","); got != want {
		t.Errorf("keys = %s\nwant   %s", got, want)
	}
	if p := cols[8]; p.At != 2 || p.Len != 1 || p.Index != -1 || p.Source != "medewi" {
		t.Errorf("pump = %+v", p)
	}
	if m := cols[10]; m.Source != "xml" || m.At != 4 || m.Index != 0 {
		t.Errorf("mode = %+v", m)
	}
	if c := cols[11]; c.Unit != "°C" || c.Name != "Temperature PID1" {
		t.Errorf("temperature = %+v", c)
	}
}

func TestColumnsDuplicate(t *testing.T) {
	spec := &leva.RecordSpec{Values: []leva.ValueSpec{
		{Index: 0, Name: "Flow rate", At: 4, Len: 5, Type: leva.Float, Unit: "ml/s"},
		{Index: 1, Name: "Flow rate", At: 10, Len: 5, Type: leva.Float, Unit: "g/s"},
		{Index: 2, Name: "Volume", At: 16, Len: 5, Type: leva.Float, Unit: "ml"},
	}}
	cols, err := Columns(spec)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{cols[10].Key, cols[11].Key, cols[12].Key}
	want := []string{"flow_rate_ml_s", "flow_rate_g_s", "volume"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v", got, want)
	}
	spec.Values[1].Unit = "ml/s"
	if _, err := Columns(spec); err == nil {
		t.Error("still-duplicate keys: want error")
	}
}

func TestColumnsDuplicateNoUnit(t *testing.T) {
	spec := &leva.RecordSpec{Values: []leva.ValueSpec{
		{Index: 0, Name: "Flow rate", At: 4, Len: 5, Type: leva.Float, Unit: "ml/s"},
		{Index: 1, Name: "Flow rate", At: 10, Len: 5, Type: leva.Float, Unit: "%"},
	}}
	cols, err := Columns(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := cols[10].Key + "," + cols[11].Key; got != "flow_rate_ml_s,flow_rate" {
		t.Errorf("keys = %s, want flow_rate_ml_s,flow_rate (no trailing _)", got)
	}
	spec.Values[0].Unit = ""
	if _, err := Columns(spec); err == nil || !strings.Contains(err.Error(), `both map to key "flow_rate"`) {
		t.Errorf("err = %v, want the duplicate error", err)
	}
}

func TestColumnsReservedClash(t *testing.T) {
	spec := &leva.RecordSpec{Values: []leva.ValueSpec{
		{Index: 0, Name: "Run", At: 4, Len: 3, Type: leva.Int},
	}}
	_, err := Columns(spec)
	if err == nil || !strings.Contains(err.Error(), "run") {
		t.Errorf("err = %v, want mention of run", err)
	}
}
