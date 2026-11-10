package logview

import (
	"strings"
	"testing"
)

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
