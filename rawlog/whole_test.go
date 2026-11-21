package rawlog

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// testReport is a 289-byte report without the trailing checksum.
func testReport() []byte {
	b := bytes.Repeat([]byte(" "), 289)
	b[0], b[288] = '{', '}'
	b[2] = '*'
	copy(b[7:], "  93.5")
	return b
}

// sumReport is testReport with a valid trailing "~ HH}" checksum.
func sumReport() []byte {
	b := testReport()
	var s byte
	for _, c := range b[:284] {
		s += c
	}
	copy(b[284:], fmt.Sprintf("~ %02X}", -s))
	return b
}

func testFrame() string {
	return "<112" + "Line one        " + "Line two        " + "Line three      " + "Line four       " + ">"
}

func TestIsWholeLine(t *testing.T) {
	rep, frame := string(sumReport()), testFrame()
	for in, want := range map[string]bool{
		rep:                                   true,
		frame:                                 true,
		"$id: leva! (3.2)":                    true,
		"[READY]":                             true,
		rep + rep:                             true, // glued, both whole
		rep + "$id: leva! (3.2)":              true,
		frame + rep:                           true,
		rep[:100] + rep:                       false, // a cut-off report piece
		string(testReport()):                  false, // no checksum
		"{ * 93.5|0001 secret}":               false,
		"$id: leva! (3.2)|0001 secret":        false,
		"[READY]|secret":                      false,
		rep + "$id: |0001 secret":             false,
		frame[:30] + "|0001 secret":           false,
		"<" + strings.Repeat("a", 2000) + ">": false,
		"":                                    false,
	} {
		if got := IsWholeLine([]byte(in)); got != want {
			t.Errorf("IsWholeLine(%.60q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsWholeBanner(t *testing.T) {
	for in, want := range map[string]bool{
		"$id: leva! (3.2)":        true,
		"$id: leva! (10.12)":      true,
		"$id: leva! (3)":          true,
		"$id: leva! (3.2)|0001 x": false,
		"$id: leva! (3.2) ":       false,
		"$id: leva! ()":           false,
		"$id: leva! (3.x)":        false,
		"$id: leva! (3.2":         false,
		"$id: |0001 secret":       false,
		"$id: other (3.2)":        false,
		"":                        false,
	} {
		if got := isWholeBanner([]byte(in)); got != want {
			t.Errorf("isWholeBanner(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsWholeNotice(t *testing.T) {
	for in, want := range map[string]bool{
		"[READY]":                           true,
		"[READY]|secret":                    false,
		"[a:b]":                             false,
		"[a|b]":                             false,
		"[READY":                            false,
		"[" + strings.Repeat("x", 30) + "]": true,
		"[" + strings.Repeat("x", 31) + "]": false,
	} {
		if got := isWholeNotice([]byte(in)); got != want {
			t.Errorf("isWholeNotice(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestReportSumOK(t *testing.T) {
	good := sumReport()
	if !reportSumOK(good) {
		t.Error("checksummed report rejected")
	}
	if reportSumOK(testReport()) {
		t.Error("report without checksum accepted")
	}
	bad := append([]byte(nil), good...)
	bad[10] ^= 1
	if reportSumOK(bad) {
		t.Error("corrupted report accepted")
	}
	for _, in := range []string{"{ * 93.5|0001 secret}", "~ 00}", "{~ 00}", ""} {
		if reportSumOK([]byte(in)) {
			t.Errorf("reportSumOK(%q) = true", in)
		}
	}
}
