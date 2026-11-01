package leva

import (
	"os"
	"strings"
	"testing"
)

func TestParseSpecSynthetic(t *testing.T) {
	f, err := os.Open("../testdata/spec/synthetic.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	spec, err := ParseSpec(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Values) != 5 {
		t.Fatalf("got %d values, want 5", len(spec.Values))
	}
	want := map[int]ValueSpec{
		0: {Index: 0, Name: "", At: 4, Len: 3, Type: String},
		1: {Index: 1, Name: "Temperature PID1", At: 7, Len: 6, Type: Float, Unit: "°C"},
		3: {Index: 3, Name: "Heat PID", At: 18, Len: 3, Type: Int, Unit: "%"},
	}
	for i, w := range want {
		if got := spec.Values[i]; got != w {
			t.Errorf("value %d = %+v, want %+v", i, got, w)
		}
	}
}

func TestParseSpecRejectsGarbage(t *testing.T) {
	if _, err := ParseSpec(strings.NewReader("this is not xml")); err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadSpecFileRealXML(t *testing.T) {
	path := os.Getenv("MEDEWI_XML_PATH")
	if path == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	spec, err := LoadSpecFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d values", len(spec.Values))
	if len(spec.Values) < 40 {
		t.Fatalf("got %d values, want >= 40", len(spec.Values))
	}
	for _, v := range spec.Values {
		if v.Name == "Temperature PID1" {
			return
		}
	}
	t.Error("Temperature PID1 not found")
}
