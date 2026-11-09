package main

import (
	"testing"
	"time"
)

func TestParseUntil(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ok := []struct {
		in   string
		want time.Duration
	}{
		{"+30min", 30 * time.Minute},
		{"+1h30min", 90 * time.Minute},
		{"+90s", 90 * time.Second},
		{"+2h", 2 * time.Hour},
		{"+1min30s", 90 * time.Second},
		{"+1h1h", 2 * time.Hour},
		{"+30s1h", time.Hour + 30*time.Second},
	}
	for _, c := range ok {
		got, err := parseUntil(c.in, now)
		if err != nil || !got.Equal(now.Add(c.want)) {
			t.Errorf("parseUntil(%q) = %v, %v; want %v", c.in, got, err, now.Add(c.want))
		}
	}
	for _, in := range []string{"", "+", "30min", "-5min", "+0s", "+5m", "+1d", "+1h 30min", "+1.5h", "+h"} {
		if got, err := parseUntil(in, now); err == nil {
			t.Errorf("parseUntil(%q) = %v, want error", in, got)
		}
	}
}
