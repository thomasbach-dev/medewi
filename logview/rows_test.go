package logview

import (
	"fmt"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

func TestRowsWindow(t *testing.T) {
	start := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	var l loaded
	for i, s := range []string{"a", "b", "c", "d"} {
		l.Entries = append(l.Entries, Entry{
			Offset: time.Duration(i) * time.Second, Wall: start.Add(time.Duration(i) * time.Second),
			HasWall: true, Kind: KindReply, Raw: []byte(s), Dir: fixture.RX,
		})
	}
	rows := selectRows(l, nil, Selection{Since: start.Add(time.Second), Until: start.Add(3 * time.Second)})
	if len(rows) != 2 || string(rows[0].Entry.Raw) != "b" || string(rows[1].Entry.Raw) != "c" {
		t.Fatalf("rows = %+v", rows)
	}
	// Entries without wall time pass the bounds.
	for i := range l.Entries {
		l.Entries[i].HasWall = false
	}
	if rows = selectRows(l, nil, Selection{Since: start.Add(time.Second)}); len(rows) != 4 {
		t.Errorf("len = %d, want 4", len(rows))
	}
}

// runsLoaded has idle 0-3, shot 4-13, idle 14-27, shot 28-37, idle 38-51.
func runsLoaded(t *testing.T, raw bool) (loaded, []Run) {
	t.Helper()
	var l loaded
	addSource(t, &l, runT0, raw, concat(
		idle(4, 0, 0), pumping(10, "9.0", 9, 4), idle(14, 40, 0),
		pumping(10, "9.0", 9, 4), idle(14, 40, 0),
	))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	return l, runs
}

func TestRowsRunWithMargin(t *testing.T) {
	l, runs := runsLoaded(t, true)
	rows := selectRows(l, runs, Selection{Runs: []int{2}, Margin: time.Second})
	first, last := runs[1].First.Index, runs[1].Last.Index
	if len(rows) != last-first+1+8 {
		t.Fatalf("len = %d, want %d", len(rows), last-first+9)
	}
	if rows[0].Entry.Offset != l.Entries[first-4].Offset || rows[0].RunT == nil || *rows[0].RunT != -time.Second || rows[0].Run == nil || rows[0].Run.N != 2 {
		t.Errorf("first row = %+v", rows[0])
	}
	if rows[4].RunT == nil || *rows[4].RunT != 0 {
		t.Errorf("run_t at first = %v", rows[4].RunT)
	}
	if rl := rows[len(rows)-1]; *rl.RunT != runs[1].Duration+time.Second {
		t.Errorf("last run_t = %v", *rl.RunT)
	}
}

func TestRowsRunIgnoresUntil(t *testing.T) {
	l, runs := runsLoaded(t, true)
	until := l.Entries[runs[0].First.Index+2].Wall
	rows := selectRows(l, runs, Selection{Runs: []int{1}, Until: until})
	if len(rows) != runs[0].Last.Index-runs[0].First.Index+1 {
		t.Errorf("len = %d, want whole run", len(rows))
	}
}

func TestRowsRunKindUnion(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, true, concat(
		pumping(10, "9.0", 9, 4), idle(14, 40, 0), // shot
		idle(10, 40, 2), idle(12, 58, 0), // draw
		pumping(10, "9.0", 9, 4), idle(14, 40, 0), // shot 2
	))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 3 || runs[1].Kind != RunDraw {
		t.Fatalf("runs = %+v", runs)
	}
	rows := selectRows(l, runs, Selection{Runs: []int{1}, RunKinds: []RunKind{RunDraw}})
	seen := map[int]bool{}
	for _, r := range rows {
		if r.Run == nil {
			t.Fatal("row without run")
		}
		seen[r.Run.N] = true
	}
	if !seen[1] || !seen[2] || seen[3] {
		t.Errorf("runs seen = %v", seen)
	}
}

func TestRowsCaptureTimeline(t *testing.T) {
	l, runs := runsLoaded(t, false)
	addSource(t, &l, runT0, true, idle(30, 0, 0))
	rows := selectRows(l, runs, Selection{Runs: []int{1}})
	for _, r := range rows {
		if r.Entry.Source != 0 {
			t.Fatalf("row from source %d matched a capture run", r.Entry.Source)
		}
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
}

func TestRowsEvents(t *testing.T) {
	l, _ := runsLoaded(t, true)
	l.Entries = append(l.Entries[:2:2], append([]Entry{{Kind: KindCmd, Raw: []byte("MCh")}, {Kind: KindBanner}}, l.Entries[2:4]...)...)
	rows := selectRows(l, nil, Selection{Events: []Kind{KindReport}})
	if len(rows) != 4 {
		t.Fatalf("len = %d, want 4", len(rows))
	}
	for _, r := range rows {
		if r.Entry.Kind != KindReport {
			t.Errorf("kind = %s", r.Entry.Kind)
		}
	}
}

func TestRowsSkipStandby(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, true, []step{{pump: '-'}, {pump: '?'}, {pump: '*'}})
	rows := selectRows(l, nil, Selection{SkipStandby: true})
	if len(rows) != 2 || rows[0].Entry.Raw[2] != '-' || rows[1].Entry.Raw[2] != '*' {
		t.Errorf("rows = %d", len(rows))
	}
}

func TestRowsChanges(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, true, []step{
		{pump: '-', bar: 1}, {pump: '-', bar: 1}, {pump: '-', bar: 2},
	})
	l.Entries = append(l.Entries[:2:2], append([]Entry{{Kind: KindCmd}}, l.Entries[2:]...)...)
	rows := selectRows(l, nil, Selection{Changes: true, ChangeKeys: []string{"pressure"}})
	if len(rows) != 3 { // report 1, the cmd, report 3
		t.Fatalf("len = %d, want 3", len(rows))
	}
	if rows[1].Entry.Kind != KindCmd || rows[2].Entry.Record == nil {
		t.Errorf("rows = %+v", rows)
	}
	// Pump alone: no change.
	if rows = selectRows(l, nil, Selection{Changes: true, ChangeKeys: []string{"pump"}}); len(rows) != 2 {
		t.Errorf("pump len = %d, want 2", len(rows))
	}
	// All keys by default: pressure differs in the last report.
	if rows = selectRows(l, nil, Selection{Changes: true}); len(rows) != 3 {
		t.Errorf("default len = %d, want 3", len(rows))
	}
}

// modeReport builds a report on modeSpecT; modeSpecT has a mode field (offset 4), a pressure and a maskable setpoint.
func modeReport(mode, sp string) Entry {
	line := runReport(step{pump: '-', bar: 1})
	copy(line[4:7], mode)
	copy(line[7:12], " 1.00")
	copy(line[13:18], sp)
	rec, _ := modeSpecT.Decode(line)
	return Entry{Kind: KindReport, Raw: line, Record: &rec, Spec: modeSpecT}
}

var modeSpecT = &leva.RecordSpec{Values: []leva.ValueSpec{
	{Index: 0, Name: "", At: 4, Len: 3, Type: leva.String},
	{Index: 1, Name: "Pressure", At: 7, Len: 5, Type: leva.Float},
	{Index: 2, Name: "Pressure SP", At: 13, Len: 5, Type: leva.Float},
}}

func TestRowsChangesDefaultKeys(t *testing.T) {
	l := loaded{Entries: []Entry{
		modeReport("RUN", "?????"),
		modeReport("RUN", "?????"), // identical: dropped
		modeReport("ECO", "?????"), // mode only
		modeReport("ECO", " 9.00"), // masked -> value
		modeReport("ECO", " 9.00"), // identical: dropped
	}}
	rows := selectRows(l, nil, Selection{Changes: true})
	if len(rows) != 3 {
		t.Fatalf("len = %d, want 3", len(rows))
	}
}

func TestRowsOverlapSelectedWins(t *testing.T) {
	l, runs := runsLoaded(t, true)
	// Margin spans the 14 idle reports between the runs: windows overlap.
	rows := selectRows(l, runs, Selection{Runs: []int{2}, Margin: 3 * time.Second})
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range rows {
		if r.Run == nil || r.Run.N != 2 {
			t.Fatalf("row %v carries run %+v, want 2", r.Entry.Src, r.Run)
		}
	}
	// The first row lies inside run 1's window (its last + 3 s = 12 reports).
	if rows[0].Entry.Src > runs[0].Last.Index+12+1 {
		t.Errorf("overlap rows missing: first src %d", rows[0].Entry.Src)
	}
}

func TestRowsChangesResetPerRun(t *testing.T) {
	l, runs := runsLoaded(t, true)
	// Both shots report the same pressure: each run's first report stays.
	rows := selectRows(l, runs, Selection{Runs: []int{1, 2}, Changes: true, ChangeKeys: []string{"pressure"}})
	var got []int
	for _, r := range rows {
		got = append(got, r.Run.N)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("runs of the rows = %v, want [1 2]", got)
	}
	// Into a run from no run: the volume (0) is unchanged at the first
	// pumping report, which is printed all the same.
	rows = selectRows(l, runs, Selection{Changes: true, ChangeKeys: []string{"volume"}})
	found := false
	for _, r := range rows {
		found = found || r.Entry.Index == runs[0].First.Index
	}
	if !found {
		t.Error("first report of run 1 dropped")
	}
}

func TestRowsDrops(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, true, []step{{pump: '-'}, {pump: '?'}, {pump: '*'}, {pump: '?'}, {pump: '-'}, {pump: '-'}})
	for range 4 {
		l.Entries = append(l.Entries, Entry{Kind: KindNotice, Raw: []byte("x"), Index: len(l.Entries)})
	}
	s := NewSelector(nil, Selection{Events: []Kind{KindReport}, SkipStandby: true})
	kept := 0
	for i := range l.Entries {
		if _, ok := s.Select(&l.Entries[i]); ok {
			kept++
		}
	}
	want := []Drop{{"--event report", 4}, {"--skip-standby", 2}}
	if kept != 4 || s.Seen() != 10 || fmt.Sprint(s.Drops()) != fmt.Sprint(want) {
		t.Errorf("kept %d, seen %d, drops %v; want 4, 10, %v", kept, s.Seen(), s.Drops(), want)
	}

	// The window, then run selection through SelectAll; only active
	// filters are listed.
	l, runs := runsLoaded(t, true)
	s = NewSelector(runs, Selection{Since: runT0.Add(time.Hour)})
	for i := range l.Entries {
		s.SelectAll(&l.Entries[i])
	}
	if want := []Drop{{"--since", len(l.Entries)}}; fmt.Sprint(s.Drops()) != fmt.Sprint(want) {
		t.Errorf("window drops %v, want %v", s.Drops(), want)
	}
	s = NewSelector(runs, Selection{Runs: []int{1}, RunKinds: []RunKind{RunDraw}, Changes: true, ChangeKeys: []string{"pressure"}})
	for i := range l.Entries {
		s.SelectAll(&l.Entries[i])
	}
	inRun := runs[0].Last.Index - runs[0].First.Index + 1
	want = []Drop{{"--run 1 --run-kind draw", len(l.Entries) - inRun}, {"--changes", inRun - 1}}
	if fmt.Sprint(s.Drops()) != fmt.Sprint(want) {
		t.Errorf("run drops %v, want %v", s.Drops(), want)
	}
}
