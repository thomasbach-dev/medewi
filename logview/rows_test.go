package logview

import (
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
)

func TestRowsWindow(t *testing.T) {
	start := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	var l Loaded
	for i, s := range []string{"a", "b", "c", "d"} {
		l.Entries = append(l.Entries, Entry{
			Offset: time.Duration(i) * time.Second, Wall: start.Add(time.Duration(i) * time.Second),
			HasWall: true, Kind: KindReply, Raw: []byte(s), Dir: fixture.RX,
		})
	}
	rows := Rows(l, nil, Selection{Since: start.Add(time.Second), Until: start.Add(3 * time.Second)})
	if len(rows) != 2 || string(rows[0].Entry.Raw) != "b" || string(rows[1].Entry.Raw) != "c" {
		t.Fatalf("rows = %+v", rows)
	}
	// Entries without wall time pass the bounds.
	for i := range l.Entries {
		l.Entries[i].HasWall = false
	}
	if rows = Rows(l, nil, Selection{Since: start.Add(time.Second)}); len(rows) != 4 {
		t.Errorf("len = %d, want 4", len(rows))
	}
}

// runsLoaded has idle 0-3, shot 4-13, idle 14-27, shot 28-37, idle 38-51.
func runsLoaded(t *testing.T, raw bool) (Loaded, []Run) {
	t.Helper()
	var l Loaded
	addSource(t, &l, runT0, raw, concat(
		idle(4, 0, 0), pumping(10, "9.0", 9, 4), idle(14, 40, 0),
		pumping(10, "9.0", 9, 4), idle(14, 40, 0),
	))
	runs := FindRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	return l, runs
}

func TestRowsRunWithMargin(t *testing.T) {
	l, runs := runsLoaded(t, true)
	rows := Rows(l, runs, Selection{Runs: []int{2}, Margin: time.Second})
	first, last := runs[1].First, runs[1].Last
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
	until := l.Entries[runs[0].First+2].Wall
	rows := Rows(l, runs, Selection{Runs: []int{1}, Until: until})
	if len(rows) != runs[0].Last-runs[0].First+1 {
		t.Errorf("len = %d, want whole run", len(rows))
	}
}

func TestRowsRunKindUnion(t *testing.T) {
	var l Loaded
	addSource(t, &l, runT0, true, concat(
		pumping(10, "9.0", 9, 4), idle(14, 40, 0), // shot
		idle(10, 40, 2), idle(12, 58, 0), // draw
		pumping(10, "9.0", 9, 4), idle(14, 40, 0), // shot 2
	))
	runs := FindRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 3 || runs[1].Kind != RunDraw {
		t.Fatalf("runs = %+v", runs)
	}
	rows := Rows(l, runs, Selection{Runs: []int{1}, RunKinds: []RunKind{RunDraw}})
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
	rows := Rows(l, runs, Selection{Runs: []int{1}})
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
	rows := Rows(l, nil, Selection{Events: []Kind{KindReport}})
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
	var l Loaded
	addSource(t, &l, runT0, true, []step{{pump: '-'}, {pump: '?'}, {pump: '*'}})
	rows := Rows(l, nil, Selection{SkipStandby: true})
	if len(rows) != 2 || rows[0].Entry.Raw[2] != '-' || rows[1].Entry.Raw[2] != '*' {
		t.Errorf("rows = %d", len(rows))
	}
}

func TestRowsChanges(t *testing.T) {
	var l Loaded
	addSource(t, &l, runT0, true, []step{
		{pump: '-', bar: 1}, {pump: '-', bar: 1}, {pump: '-', bar: 2},
	})
	l.Entries = append(l.Entries[:2:2], append([]Entry{{Kind: KindCmd}}, l.Entries[2:]...)...)
	rows := Rows(l, nil, Selection{Changes: true, ChangeKeys: []string{"pressure"}})
	if len(rows) != 3 { // report 1, the cmd, report 3
		t.Fatalf("len = %d, want 3", len(rows))
	}
	if rows[1].Entry.Kind != KindCmd || rows[2].Entry.Record == nil {
		t.Errorf("rows = %+v", rows)
	}
	// Pump alone: no change.
	if rows = Rows(l, nil, Selection{Changes: true, ChangeKeys: []string{"pump"}}); len(rows) != 2 {
		t.Errorf("pump len = %d, want 2", len(rows))
	}
	// All keys by default: pressure differs in the last report.
	if rows = Rows(l, nil, Selection{Changes: true}); len(rows) != 3 {
		t.Errorf("default len = %d, want 3", len(rows))
	}
}
