package logview

import (
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
)

// barSteps are pumping steps of a shot with these pressures, volume rising
// by 4 from v.
func barSteps(v float64, bars ...float64) []step {
	var out []step
	for i, b := range bars {
		out = append(out, step{pump: '*', bar: b, sp: "9.0", volume: v + float64(i)*4})
	}
	return out
}

// twoShots is a raw-log source with run 1 (pressures 1, 9, 8 at steps 4-6)
// and run 2 (2, 3 at steps 23-24).
func twoShots(t *testing.T) (loaded, []Run) {
	t.Helper()
	var l loaded
	addSource(t, &l, runT0, true, concat(
		idle(4, 0, 0), barSteps(0, 1, 9, 8), idle(16, 8, 0),
		barSteps(8, 2, 3), idle(16, 12, 0),
	))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 2 || runs[0].Kind != RunShot || runs[1].Kind != RunShot {
		t.Fatalf("runs = %+v", runs)
	}
	return l, runs
}

func feed(s *RunStats, l loaded) {
	for i := range l.Entries {
		s.Entry(&l.Entries[i])
	}
}

func aggStrings(vs []AggValue) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		switch {
		case v.Num != nil:
			out[i] = FormatNum(*v.Num)
		case v.Str != nil:
			out[i] = *v.Str
		default:
			out[i] = "?"
		}
	}
	return out
}

func TestRunStatsPerRun(t *testing.T) {
	l, runs := twoShots(t)
	aggs, err := ParseAggs("n,max,tmax,srcmax")
	if err != nil {
		t.Fatal(err)
	}
	s := NewRunStats(runs, []string{"pressure"}, [][]Agg{aggs})
	feed(s, l)
	if h := strings.Join(s.Header(), " "); h != "pressure_n pressure_max pressure_tmax pressure_srcmax" {
		t.Errorf("header %q", h)
	}
	if got := strings.Join(aggStrings(s.Values(1)), " "); got != "3 9 0.25 src0:6" {
		t.Errorf("run 1 = %q", got)
	}
	if got := strings.Join(aggStrings(s.Values(2)), " "); got != "2 3 0.25 src0:25" {
		t.Errorf("run 2 = %q", got)
	}
	if m := s.Missing(); len(m) != 0 {
		t.Errorf("missing = %q", m)
	}
}

func TestRunStatsMaskedAndStandby(t *testing.T) {
	l, runs := twoShots(t)
	// Mask the pressure of the 9 bar report (step 5) and put the 8 bar one
	// (step 6) into standby.
	masked := &l.Entries[5]
	masked.Raw = append([]byte(nil), masked.Raw...)
	copy(masked.Raw[7:], "?????")
	rec, err := runSpec.Decode(masked.Raw)
	if err != nil {
		t.Fatal(err)
	}
	masked.Record = &rec
	standby := &l.Entries[6]
	standby.Raw = append([]byte(nil), standby.Raw...)
	standby.Raw[2] = '?'

	aggs, _ := ParseAggs("n,max")
	s := NewRunStats(runs, []string{"pressure"}, [][]Agg{aggs})
	feed(s, l)
	if got := strings.Join(aggStrings(s.Values(1)), " "); got != "1 1" {
		t.Errorf("run 1 = %q, want only the 1 bar report", got)
	}

	// All values masked: n is 0, max empty.
	s = NewRunStats(runs, []string{"pressure"}, [][]Agg{aggs})
	for i := range l.Entries {
		if i != 4 {
			s.Entry(&l.Entries[i])
		}
	}
	if got := strings.Join(aggStrings(s.Values(1)), " "); got != "0 ?" {
		t.Errorf("run 1 without values = %q", got)
	}
}

func TestRunStatsMissingKey(t *testing.T) {
	l, runs := twoShots(t)
	// A second raw log shown raw (no spec), with a report inside run 1.
	in := l.Entries[5]
	in.Source, in.Path, in.Record, in.Spec, in.XML = 1, "raw1", nil, nil, ""
	aggs, _ := ParseAggs("n")
	s := NewRunStats(runs, []string{"pressure"}, [][]Agg{aggs})
	feed(s, l)
	s.Entry(&in)
	if got := aggStrings(s.Values(1)); got[0] != "3" {
		t.Errorf("run 1 n = %s, want 3", got[0])
	}
	m := s.Missing()
	if len(m) != 1 || !strings.Contains(m[0], "pressure") || !strings.Contains(m[0], "1 report") {
		t.Errorf("missing = %q", m)
	}
}
