package leva

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// buildLine makes a 54-byte line (the synthetic spec's last window ends at
// offset 54) and plants values at the synthetic spec's offsets.
func buildLine() []byte {
	line := []byte(strings.Repeat(" ", 54))
	line[0] = '{'
	copy(line[4:], "1:%")
	copy(line[7:], " 93.50")
	copy(line[18:], " 42")
	copy(line[36:], " 9.10")
	copy(line[50:], "Out ")
	return line
}

func loadSynthetic(t *testing.T) *RecordSpec {
	t.Helper()
	spec, err := LoadSpecFile("../testdata/spec/synthetic.xml")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestDecode(t *testing.T) {
	rec, err := loadSynthetic(t).Decode(buildLine())
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := rec.Float("Temperature PID1"); !ok || v != 93.5 {
		t.Errorf("Temperature PID1 = %v, %v", v, ok)
	}
	if v, ok := rec.Float("Pressure"); !ok || v != 9.1 {
		t.Errorf("Pressure = %v, %v", v, ok)
	}
	f, ok := rec.Field("Heat PID")
	if !ok || f.Raw != "42" || f.Spec.Unit != "%" {
		t.Errorf("Heat PID = %+v, %v", f, ok)
	}
	if _, ok := rec.Field("nope"); ok {
		t.Error("unknown field found")
	}
	if _, ok := rec.Float("nope"); ok {
		t.Error("unknown float found")
	}
}

func TestDecodeShortLine(t *testing.T) {
	_, err := loadSynthetic(t).Decode(buildLine()[:40])
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Pressure") {
		t.Errorf("error does not name truncated value: %v", err)
	}
}

func TestDecodeBinaryBytes(t *testing.T) {
	line := buildLine()
	copy(line[50:], "\x00\xDF  ")
	rec, err := loadSynthetic(t).Decode(line)
	if err != nil {
		t.Fatal(err)
	}
	f := rec.Fields[4]
	if f.Raw != "\x00\xDF" {
		t.Errorf("Raw = %q, want NUL+0xDF", f.Raw)
	}
}

func TestChecksumAbsent(t *testing.T) {
	line := []byte("{hello}")
	payload, err := VerifyChecksum(line)
	if err != nil || !bytes.Equal(payload, line) {
		t.Errorf("payload %q err %v", payload, err)
	}
	if _, _, present := SplitChecksum(line); present {
		t.Error("checksum reported present")
	}
}

func withSum(payload string) []byte {
	var s byte
	for _, b := range []byte(payload) {
		s += b
	}
	return []byte(fmt.Sprintf("%s~%02X", payload, ^s))
}

func TestChecksumValid(t *testing.T) {
	line := withSum("{hello\x00\xDF}")
	payload, err := VerifyChecksum(line)
	if err != nil || string(payload) != "{hello\x00\xDF}" {
		t.Errorf("payload %q err %v", payload, err)
	}
}

func TestChecksumInvalid(t *testing.T) {
	line := withSum("{hello}")
	line[1] = 'j'
	if _, err := VerifyChecksum(line); err == nil {
		t.Error("expected error")
	}
}
