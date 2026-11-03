package leva

import (
	"bytes"
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
	if len(spec.Values) != 6 {
		t.Fatalf("got %d values, want 6", len(spec.Values))
	}
	want := map[int]ValueSpec{
		0: {Index: 0, Name: "", At: 4, Len: 3, Type: String, Factor: 1},
		1: {Index: 1, Name: "Temperature PID1", At: 7, Len: 6, Type: Float, Unit: "°C", Factor: 1},
		2: {Index: 2, Name: "Setpoint PID", At: 13, Len: 5, Type: Float, Unit: "°C", Factor: 1},
		4: {Index: 4, Name: "Heat PID", At: 18, Len: 3, Type: Int, Unit: "%", Factor: 1},
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

func TestParseSpecLatin1Content(t *testing.T) {
	doc := []byte("<cfg><values><value at=\"1\" len=\"2\" export=\"T\xb0\" type=\"string\">x\xb0y</value></values></cfg>")
	spec, err := ParseSpec(bytes.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Values[0].Name; got != "T\u00b0" {
		t.Errorf("name = %q", got)
	}
}

func TestParseSpecStructuralErrors(t *testing.T) {
	cases := map[string]string{
		"no values":    "<cfg version=\"4\"></cfg>",
		"zero len":     "<cfg><values><value at=\"1\" len=\"0\" type=\"int\"/></values></cfg>",
		"negative at":  "<cfg><values><value at=\"-1\" len=\"2\" type=\"int\"/></values></cfg>",
		"missing at":   "<cfg><values><value len=\"2\" type=\"int\"/></values></cfg>",
		"unknown type": "<cfg><values><value at=\"1\" len=\"2\" type=\"blob\"/></values></cfg>",
	}
	for name, doc := range cases {
		if _, err := ParseSpec(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	_, err := ParseSpec(strings.NewReader("<cfg><values><value at=\"1\" len=\"2\" type=\"int\"/><value at=\"3\" len=\"0\" type=\"int\"/></values></cfg>"))
	if err == nil || !strings.Contains(err.Error(), "value 1") {
		t.Errorf("want error naming value 1, got %v", err)
	}
}

func TestParseSpecDeclaredLatin1NotDoubleDecoded(t *testing.T) {
	doc := []byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><cfg><values><value at=\"1\" len=\"2\" export=\"T\xb0\" type=\"string\"/></values></cfg>")
	spec, err := ParseSpec(bytes.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	got := spec.Values[0].Name
	if got == "T\u00c2\u00b0" {
		t.Fatal("double-decoded")
	}
	if got != "T\u00b0" {
		t.Errorf("name = %q", got)
	}
}

func TestParseSpecFactor(t *testing.T) {
	doc := `<cfg><values>` +
		`<value at="1" len="3" export="Flow rate" factor="0.0166" type="int"/>` +
		`<value at="4" len="3" export="Volume" type="int"/>` +
		`</values></cfg>`
	spec, err := ParseSpec(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Values[0].Factor; got != 0.0166 {
		t.Errorf("Flow rate Factor = %v, want 0.0166", got)
	}
	if got := spec.Values[1].Factor; got != 1 {
		t.Errorf("absent Factor = %v, want 1", got)
	}
	for _, bad := range []string{"x", "0", "-1"} {
		doc := `<cfg><values><value at="1" len="3" factor="` + bad + `" type="int"/></values></cfg>`
		if _, err := ParseSpec(strings.NewReader(doc)); err == nil {
			t.Errorf("factor %q: expected error", bad)
		}
	}
}

func TestRealSpecFlowFactor(t *testing.T) {
	path := os.Getenv("MEDEWI_XML_PATH")
	if path == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	spec, err := LoadSpecFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range spec.Values {
		if v.Name == "Flow rate" {
			if v.Index != 30 || v.Factor != 0.0166 {
				t.Errorf("Flow rate: Index %d Factor %v, want 30 and 0.0166", v.Index, v.Factor)
			}
			return
		}
	}
	t.Error("Flow rate not found")
}
