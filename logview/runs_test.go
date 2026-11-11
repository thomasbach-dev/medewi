package logview

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

// runSpec is a Go-built spec with the fields brew reads for runs.
var runSpec = &leva.RecordSpec{Values: []leva.ValueSpec{
	{Index: 0, Name: "Pressure", At: 7, Len: 5, Type: leva.Float},
	{Index: 1, Name: "Pressure SP", At: 13, Len: 5, Type: leva.Float},
	{Index: 2, Name: "Volume", At: 19, Len: 5, Type: leva.Float},
}}

// step is one synthetic report: pump marker, pressure, setpoint ("" masks
// it) and volume.
type step struct {
	pump   byte
	bar    float64
	sp     string
	volume float64
}

func runReport(s step) []byte {
	b := bytes.Repeat([]byte(" "), 289)
	b[0], b[288] = '{', '}'
	b[2] = s.pump
	copy(b[7:], fmt.Sprintf("%5.2f", s.bar))
	sp := "?????"
	if s.sp != "" {
		sp = fmt.Sprintf("%5s", s.sp)
	}
	copy(b[13:], sp)
	copy(b[19:], fmt.Sprintf("%5.1f", s.volume))
	return b
}

// pumping returns n pumping steps, volume rising from 0 by perStep.
func pumping(n int, sp string, bar, perStep float64) []step {
	var out []step
	for i := range n {
		out = append(out, step{pump: '*', bar: bar, sp: sp, volume: float64(i) * perStep})
	}
	return out
}

// idle returns n idle steps, volume rising from v by perStep.
func idle(n int, v, perStep float64) []step {
	var out []step
	for i := range n {
		out = append(out, step{pump: '-', volume: v + float64(i)*perStep})
	}
	return out
}

func concat(parts ...[]step) []step {
	var out []step
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// addSource appends a source whose steps come every 250 ms; raw logs start
// at start (wall time), captures have none.
func addSource(t *testing.T, l *loaded, start time.Time, raw bool, steps []step) {
	t.Helper()
	i := len(l.Sources)
	src := Source{Path: fmt.Sprintf("src%d", i), HasWall: raw}
	if raw {
		src.Start = start
	}
	for k, s := range steps {
		off := time.Duration(k) * 250 * time.Millisecond
		line := runReport(s)
		src.Events = append(src.Events, fixture.Event{T: off.Milliseconds(), Dir: fixture.RX, Data: append(line, '\r', '\n')})
		rec, err := runSpec.Decode(line)
		if err != nil {
			t.Fatal(err)
		}
		e := Entry{
			Offset: off, Dir: fixture.RX, Kind: KindReport, Raw: line, Record: &rec,
			Path: src.Path, Source: i, Index: len(l.Entries), Src: k + 1, SrcEnd: k + 1, XML: "x", Spec: runSpec,
		}
		if raw {
			e.Wall, e.HasWall = start.Add(off), true
		}
		l.Entries = append(l.Entries, e)
	}
	l.Sources = append(l.Sources, src.Info(i))
}

var runT0 = time.Date(2026, 11, 10, 7, 58, 0, 0, time.UTC)

func TestFindRunsKinds(t *testing.T) {
	var l loaded
	addSource(t, &l, time.Time{}, false, concat(
		idle(4, 0, 0),            // 0-3
		pumping(8, "", 2, 3),     // 4-11 flush, 21 ml
		idle(12, 21, 0),          // 12-23 coast closes
		pumping(10, "9.0", 9, 4), // 24-33 shot, 36 ml
		idle(14, 36, 0),          // 34-47
		idle(10, 36, 2),          // 48-57 draw: 18 ml
		idle(12, 54, 0),          // 58-69 closes the draw
	))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 3 {
		t.Fatalf("runs = %+v, want 3", runs)
	}
	want := []struct {
		kind        RunKind
		first, last int
		ml          float64
	}{
		{RunFlush, 4, 11, 21},
		{RunShot, 24, 33, 36},
		{RunDraw, 49, 57, 18},
	}
	for i, w := range want {
		r := runs[i]
		if r.N != i+1 || r.Kind != w.kind || r.First.Index != w.first || r.Last.Index != w.last || r.VolumeML != w.ml || r.Truncated || r.OpenAtEnd || r.XML != "x" {
			t.Errorf("run %d = %+v, want N %d %s [%d,%d] %v ml", i, r, i+1, w.kind, w.first, w.last, w.ml)
		}
	}
	if runs[0].PeakBar == nil || *runs[0].PeakBar != 2 || runs[1].PeakBar == nil || *runs[1].PeakBar != 9 {
		t.Errorf("peaks = %v, %v, want 2, 9", runs[0].PeakBar, runs[1].PeakBar)
	}
	if runs[2].PeakBar != nil {
		t.Errorf("draw peak = %v, want nil", *runs[2].PeakBar)
	}
	if runs[1].Duration != 9*250*time.Millisecond || runs[2].Duration != 8*250*time.Millisecond {
		t.Errorf("durations = %v, %v", runs[1].Duration, runs[2].Duration)
	}
}

// splitShot puts a 12-sample shot across two raw logs, the second starting
// gap after the first one's last event.
func splitShot(t *testing.T, gap time.Duration) loaded {
	t.Helper()
	shot := pumping(12, "9.0", 9, 3)
	var l loaded
	addSource(t, &l, runT0, true, concat(idle(4, 0, 0), shot[:6]))
	last := runT0.Add(9 * 250 * time.Millisecond)
	addSource(t, &l, last.Add(gap), true, concat(shot[6:], idle(14, 33, 0)))
	return l
}

func TestFindRunsAcrossFiles(t *testing.T) {
	runs := findRuns(splitShot(t, 250*time.Millisecond), brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want 1", runs)
	}
	if r := runs[0]; r.Kind != RunShot || r.Truncated || r.OpenAtEnd || r.First.Index != 4 || r.Last.Index != 15 || r.VolumeML != 33 {
		t.Errorf("run = %+v, want one shot [4,15] 33 ml", r)
	}
}

func TestFindRunsGapAborts(t *testing.T) {
	runs := findRuns(splitShot(t, 10*time.Second), brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want 2", runs)
	}
	if r := runs[0]; r.Kind != RunShot || !r.Truncated || r.OpenAtEnd || r.First.Index != 4 || r.Last.Index != 9 {
		t.Errorf("first = %+v, want truncated shot [4,9]", r)
	}
	if r := runs[1]; r.N != 2 || r.First.Index != 10 || r.Truncated {
		t.Errorf("second = %+v, want N 2 from 10", r)
	}
}

func TestFindRunsWindowNumbering(t *testing.T) {
	var l loaded
	one := concat(idle(4, 0, 0), pumping(8, "9.0", 9, 3), idle(16, 21, 0)) // 28 steps, 7 s
	addSource(t, &l, runT0, true, concat(one, one, one))
	since := runT0.Add(5 * time.Second) // after the first start (1 s), before the second (8 s)
	runs := findRuns(l, brew.DefaultConfig(), since, time.Time{})
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want 2", runs)
	}
	for i, first := range []int{32, 60} {
		if r := runs[i]; r.N != i+1 || r.First.Index != first {
			t.Errorf("run %d = %+v, want N %d from %d", i, r, i+1, first)
		}
	}
	runs = findRuns(l, brew.DefaultConfig(), since, runT0.Add(10*time.Second))
	if len(runs) != 1 || runs[0].First.Index != 32 || runs[0].N != 1 {
		t.Errorf("window runs = %+v, want only the second as N 1", runs)
	}
}

func TestFindRunsOpenAtEnd(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, true, concat(idle(4, 0, 0), pumping(6, "9.0", 9, 3)))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want 1", runs)
	}
	if r := runs[0]; !r.OpenAtEnd || !r.Truncated || r.Kind != RunShot || r.First.Index != 4 || r.Last.Index != 9 {
		t.Errorf("run = %+v, want open, truncated shot [4,9]", r)
	}
}

func TestFindRunsFixture(t *testing.T) {
	path := os.Getenv("MEDEWI_XML_PATH")
	if path == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	cat, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	load := func(t *testing.T, name string) []Run {
		t.Helper()
		l, err := loadAll([]string{"../testdata/fixtures/" + name}, cat)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Problems) != 0 {
			t.Fatalf("problems = %v", l.Problems)
		}
		return findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	}

	t.Run("shot-genlever", func(t *testing.T) {
		runs := load(t, "shot-genlever")
		if len(runs) != 2 {
			t.Fatalf("runs = %+v, want flush then shot", runs)
		}
		if r := runs[0]; r.Kind != RunFlush || r.VolumeML != 73 || r.Truncated {
			t.Errorf("first = %+v, want flush 73 ml", r)
		}
		r := runs[1]
		if r.Kind != RunShot || r.VolumeML != 101 || r.PeakBar == nil || *r.PeakBar < 8.9 || *r.PeakBar > 9.1 {
			t.Errorf("second = %+v, want shot 101 ml at about 9 bar", r)
		}
	})

	t.Run("steam-eco", func(t *testing.T) {
		runs := load(t, "steam-eco")
		if len(runs) != 1 || runs[0].Kind != RunDraw || runs[0].VolumeML != 144 {
			t.Errorf("runs = %+v, want one 144 ml draw", runs)
		}
	})
}
