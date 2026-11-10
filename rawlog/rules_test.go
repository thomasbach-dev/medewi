package rawlog

import (
	"strings"
	"testing"
	"time"
)

func TestRules(t *testing.T) {
	at, ok := FileStart("20261110T094200.123Z.jsonl.gz")
	if want := time.Date(2026, 11, 10, 9, 42, 0, 123_000_000, time.UTC); !ok || !at.Equal(want) {
		t.Errorf("FileStart = %v, %v; want %v, true", at, ok, want)
	}
	for _, name := range []string{"foo.jsonl.gz", "20261110T094200.123Z.jsonl", "20261110T094200.123Z.txt", ""} {
		if _, ok := FileStart(name); ok {
			t.Errorf("FileStart(%q) ok, want false", name)
		}
	}

	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"MCu", true}, {"MCm", true}, {"MCu1", true}, {"MCmx", true},
		{"MCr", false}, {"MC ", false}, {"MCh", false}, {"", false},
	} {
		if got := IsDumpCommand([]byte(tc.in)); got != tc.want {
			t.Errorf("IsDumpCommand(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"MCr", true}, {"MC ", true}, {"MC@", true},
		{"MCu", false}, {"MCr1", false}, {"MC", false}, {"", false},
	} {
		if got := IsStreamToggle([]byte(tc.in)); got != tc.want {
			t.Errorf("IsStreamToggle(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"|abc", true}, {":10000000", true}, {"{ -1", false}, {"", false}, {"P", false},
	} {
		if got := IsDumpLine([]byte(tc.in)); got != tc.want {
			t.Errorf("IsDumpLine(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"{ -1PID}", true}, {"<102x>", true}, {"$id: leva! (3.2)", true},
		{"[READY]", true},
		{"[" + strings.Repeat("x", 30) + "]", true},  // 32 bytes
		{"[" + strings.Repeat("x", 31) + "]", false}, // 33 bytes
		{"P 91.0", false}, {"", false}, {"|x", false},
	} {
		if got := IsStreamLine([]byte(tc.in)); got != tc.want {
			t.Errorf("IsStreamLine(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if DefaultReplyWindow != 5*time.Second || DefaultDumpQuiet != 60*time.Second {
		t.Errorf("defaults = %v, %v", DefaultReplyWindow, DefaultDumpQuiet)
	}
}
