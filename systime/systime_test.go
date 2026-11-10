package systime

import (
	"testing"
	"time"
	_ "time/tzdata" // the nix sandbox has no zoneinfo

	"github.com/spf13/pflag"
)

func TestParseSpan(t *testing.T) {
	ok := []struct {
		in   string
		want time.Duration
	}{
		{"0", 0},
		{"90", 90 * time.Second},
		{"1h30min", 90 * time.Minute},
		{"1h 30min", 90 * time.Minute},
		{"500ms", 500 * time.Millisecond},
		{"10us", 10 * time.Microsecond},
		{"10µs", 10 * time.Microsecond},
		{"2d", 48 * time.Hour},
		{"1w", 168 * time.Hour},
		{"1.5h", 90 * time.Minute},
		{"720h", 720 * time.Hour},
		{"1h0m0s", time.Hour},
		{"5 minutes", 5 * time.Minute},
		{"2 hours", 2 * time.Hour},
		{"3sec", 3 * time.Second},
	}
	for _, c := range ok {
		got, err := ParseSpan(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseSpan(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "M", "1M", "1y", "-5s", "1x", "h", "9999999999999h", "1 month", "1year"} {
		if got, err := ParseSpan(in); err == nil {
			t.Errorf("ParseSpan(%q) = %v, want error", in, got)
		}
	}
}

func TestFormatSpan(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{90 * time.Minute, "1h 30min"},
		{0, "0"},
		{500 * time.Millisecond, "500ms"},
		{48 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := FormatSpan(c.d); got != c.want {
			t.Errorf("FormatSpan(%v) = %q, want %q", c.d, got, c.want)
		}
	}
	for _, d := range []time.Duration{0, 1, 1500 * time.Microsecond, 90 * time.Minute, 9*24*time.Hour + 3*time.Second + 7*time.Millisecond} {
		s := FormatSpan(d)
		back, err := ParseSpan(s)
		if err != nil || back != d {
			t.Errorf("round trip %v -> %q -> %v, %v", d, s, back, err)
		}
	}
}

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestParseTimestamp(t *testing.T) {
	loc := berlin(t)
	d := func(y int, m time.Month, day, h, mi, s int) time.Time {
		return time.Date(y, m, day, h, mi, s, 0, loc)
	}
	now := d(2026, 11, 10, 14, 0, 0)
	ok := []struct {
		in   string
		want time.Time
	}{
		{"now", now},
		{"today", d(2026, 11, 10, 0, 0, 0)},
		{"yesterday", d(2026, 11, 9, 0, 0, 0)},
		{"tomorrow", d(2026, 11, 11, 0, 0, 0)},
		{"2026-11-10 10:42", d(2026, 11, 10, 10, 42, 0)},
		{"2026-11-10 10:42:05", d(2026, 11, 10, 10, 42, 5)},
		{"2026-11-10", d(2026, 11, 10, 0, 0, 0)},
		{"10:42", d(2026, 11, 10, 10, 42, 0)},
		{"+1h", d(2026, 11, 10, 15, 0, 0)},
		{"-10min", d(2026, 11, 10, 13, 50, 0)},
		{"10min ago", d(2026, 11, 10, 13, 50, 0)},
		{"2026-11-10 10:42 UTC", d(2026, 11, 10, 11, 42, 0)},
		// DST ends in Berlin on 2026-10-25: 02:30 happens twice. Pin what
		// time.Date picks (the second one, CET, = 01:30 UTC).
		{"2026-10-25 02:30", time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)},
	}
	for _, c := range ok {
		got, err := ParseTimestamp(c.in, now, loc)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("ParseTimestamp(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	noon, err1 := ParseTimestamp("2026-10-25 12:00", now, loc)
	mid, err2 := ParseTimestamp("2026-10-25 00:00", now, loc)
	if err1 != nil || err2 != nil || noon.Sub(mid) != 13*time.Hour {
		t.Errorf("DST day: noon-midnight = %v (%v, %v), want 13h", noon.Sub(mid), err1, err2)
	}
	for _, in := range []string{"", "10:61", "2026-13-01", "next week", "1h", "@"} {
		if got, err := ParseTimestamp(in, now, loc); err == nil {
			t.Errorf("ParseTimestamp(%q) = %v, want error", in, got)
		}
	}
}

func TestFlagValues(t *testing.T) {
	loc := berlin(t)
	now := time.Date(2026, 11, 10, 14, 0, 0, 0, loc)
	var d time.Duration
	ts := &Timestamp{Now: func() time.Time { return now }, Loc: loc}
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	fs.Var(NewSpan(&d), "every", "span")
	fs.VarP(ts, "since", "s", "timestamp")
	if ts.String() != "" {
		t.Errorf("unset Timestamp.String() = %q, want empty", ts.String())
	}
	if err := fs.Parse([]string{"--every", "1h30min", "-s", "10:42"}); err != nil {
		t.Fatal(err)
	}
	if d != 90*time.Minute {
		t.Errorf("span = %v", d)
	}
	if want := time.Date(2026, 11, 10, 10, 42, 0, 0, loc); !ts.T.Equal(want) {
		t.Errorf("timestamp = %v, want %v", ts.T, want)
	}
	if got := NewSpan(&d).String(); got != "1h 30min" {
		t.Errorf("Span.String() = %q", got)
	}
	if NewSpan(&d).Type() != "span" || ts.Type() != "timestamp" {
		t.Error("wrong Type()")
	}
	if err := fs.Parse([]string{"--every", "bogus"}); err == nil {
		t.Error("want error for bogus span")
	}
}
