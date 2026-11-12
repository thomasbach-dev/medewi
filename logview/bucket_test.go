package logview

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
)

// bucketReport is a report of runSpec in source src at offset off; a zero
// start makes it a capture's, else a raw log's starting at start.
func bucketReport(t *testing.T, src int, start time.Time, off time.Duration, s step) Entry {
	t.Helper()
	line := runReport(s)
	rec, err := runSpec.Decode(line)
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{
		Offset: off, Dir: fixture.RX, Kind: KindReport, Raw: line, Record: &rec,
		Path: fmt.Sprintf("src%d", src), Source: src, Src: 1, SrcEnd: 1, XML: "x", Spec: runSpec,
	}
	if !start.IsZero() {
		e.Wall, e.HasWall = start.Add(off), true
	}
	return e
}

func lastCols(keys ...string) []BucketCol {
	out := make([]BucketCol, len(keys))
	for i, k := range keys {
		out[i] = BucketCol{Key: k, Agg: Agg{Name: "last"}, Plain: true}
	}
	return out
}

// bucketAll feeds the rows of each source to b and returns every bucket.
func bucketAll(b *Bucketer, sources []SourceInfo, entries []Entry) []BucketRow {
	var out []BucketRow
	k := 0
	for _, si := range sources {
		out = append(out, b.Source(si)...)
		for ; k < len(entries) && entries[k].Source == si.Index; k++ {
			out = append(out, b.Add(Row{Entry: &entries[k]})...)
		}
	}
	return append(out, b.Close()...)
}

type startCount struct {
	start   time.Duration
	reports int
}

func startsOf(rows []BucketRow) []startCount {
	out := make([]startCount, len(rows))
	for i, r := range rows {
		out[i] = startCount{r.Start, r.Reports}
	}
	return out
}

func TestBucketCaptureAlign(t *testing.T) {
	var es []Entry
	for _, ms := range []int{200, 700, 1100, 2600, 5100} {
		es = append(es, bucketReport(t, 0, time.Time{}, time.Duration(ms)*time.Millisecond, step{pump: '-'}))
	}
	b := NewBucketer(time.Second, lastCols("pressure"), false)
	got := startsOf(bucketAll(b, []SourceInfo{{Index: 0, Path: "src0"}}, es))
	want := []startCount{{0, 2}, {time.Second, 1}, {2 * time.Second, 1}, {5 * time.Second, 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("buckets %v, want %v", got, want)
	}
}

func TestBucketRawEpochAlign(t *testing.T) {
	start := time.Date(2026, 11, 10, 10, 0, 20, 0, time.UTC)
	es := []Entry{
		bucketReport(t, 0, start, 9900*time.Millisecond, step{pump: '-'}),  // 10:00:29.9
		bucketReport(t, 0, start, 10100*time.Millisecond, step{pump: '-'}), // 10:00:30.1
	}
	b := NewBucketer(30*time.Second, lastCols("pressure"), false)
	rows := bucketAll(b, []SourceInfo{{Index: 0, Path: "src0", Start: start, HasWall: true}}, es)
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	for i, want := range []time.Time{
		time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 10, 10, 0, 30, 0, time.UTC),
	} {
		r := rows[i]
		if !r.HasWall || !r.Wall.Equal(want) || r.Start != want.Sub(start) || r.Reports != 1 {
			t.Errorf("row %d: wall %v start %v reports %d; want wall %v start %v", i, r.Wall, r.Start, r.Reports, want, want.Sub(start))
		}
	}
}

func TestBucketFileBoundary(t *testing.T) {
	s0 := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	s1 := s0.Add(500 * time.Millisecond)
	es := []Entry{
		bucketReport(t, 0, s0, 200*time.Millisecond, step{pump: '-'}),
		bucketReport(t, 0, s0, 300*time.Millisecond, step{pump: '-'}),
		bucketReport(t, 1, s1, 100*time.Millisecond, step{pump: '-'}),
	}
	b := NewBucketer(time.Second, lastCols("pressure"), false)
	rows := bucketAll(b, []SourceInfo{
		{Index: 0, Path: "src0", Start: s0, HasWall: true},
		{Index: 1, Path: "src1", Start: s1, HasWall: true},
	}, es)
	got := startsOf(rows)
	want := []startCount{{0, 2}, {-500 * time.Millisecond, 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("buckets %v, want %v", got, want)
	}
	if rows[0].Source.Index != 0 || rows[1].Source.Index != 1 || !rows[1].Wall.Equal(s0) {
		t.Errorf("rows %+v", rows)
	}
}

// bucketRuns feeds l through a Selector's SelectAll into b and returns the
// buckets Add gave and those Close gave.
func bucketRuns(l loaded, runs []Run, sel Selection, b *Bucketer) (added, closed []BucketRow) {
	s := NewSelector(runs, sel)
	k := 0
	for _, si := range l.Sources {
		added = append(added, b.Source(si)...)
		for ; k < len(l.Entries) && l.Entries[k].Source == si.Index; k++ {
			for _, r := range s.SelectAll(&l.Entries[k]) {
				added = append(added, b.Add(r)...)
			}
		}
	}
	return added, b.Close()
}

func TestBucketRunNegative(t *testing.T) {
	l, runs := twoShots(t) // run 1: steps 4-6 (1.0-1.5 s)
	b := NewBucketer(time.Second, lastCols("pressure"), true)
	added, closed := bucketRuns(l, runs, Selection{Runs: []int{1}, Margin: 300 * time.Millisecond}, b)
	rows := slices.Concat(added, closed)
	// Window 0.7-1.8 s: step 3 at run_t -0.25, steps 4-7 at 0-0.75.
	if len(rows) != 2 {
		t.Fatalf("rows %+v", rows)
	}
	r := rows[0]
	if r.Run == nil || r.Run.N != 1 || r.RunT == nil || *r.RunT != -time.Second || r.Reports != 1 {
		t.Errorf("first row %+v", r)
	}
	if !r.Wall.Equal(runT0) || r.Start != 0 || !r.HasWall {
		t.Errorf("first row wall %v start %v, want %v and 0", r.Wall, r.Start, runT0)
	}
	if *rows[1].RunT != 0 || rows[1].Reports != 4 {
		t.Errorf("second row run_t %v reports %d", *rows[1].RunT, rows[1].Reports)
	}
}

func TestBucketOverlap(t *testing.T) {
	l, runs := twoShots(t) // run 1: steps 4-6, run 2: steps 23-24
	sel := Selection{Runs: []int{1, 2}, Margin: 2500 * time.Millisecond}
	s := NewSelector(runs, sel)
	if rs := s.SelectAll(&l.Entries[14]); len(rs) != 2 || rs[0].Run.N != 1 || rs[1].Run.N != 2 {
		t.Fatalf("SelectAll(step 14) = %+v, want runs 1 and 2", rs)
	}
	if rs := s.SelectAll(&l.Entries[0]); len(rs) != 1 || rs[0].Run.N != 1 {
		t.Fatalf("SelectAll(step 0) = %+v, want run 1", rs)
	}
	b := NewBucketer(time.Second, lastCols("pressure"), true)
	added, closed := bucketRuns(l, runs, sel, b)
	reports := map[int]int{}
	last := 0
	for _, r := range append(added, closed...) {
		if r.Run.N < last {
			t.Errorf("run %d after run %d", r.Run.N, last)
		}
		last = r.Run.N
		reports[r.Run.N] += r.Reports
	}
	// Run 1: -1.5..4.0 s holds steps 0-16; run 2: 3.25..8.5 s steps 13-34.
	if reports[1] != 17 || reports[2] != 22 {
		t.Errorf("reports per run %v, want 1:17 2:22", reports)
	}
	if len(added) == 0 || added[len(added)-1].Run.N != 1 {
		t.Errorf("run 1 not emitted before Close: %d rows from Add", len(added))
	}
}

func TestSelectAllWithoutRuns(t *testing.T) {
	l, runs := twoShots(t)
	s := NewSelector(runs, Selection{})
	rs := s.SelectAll(&l.Entries[5])
	if len(rs) != 1 || rs[0].Run == nil || rs[0].Run.N != 1 {
		t.Errorf("SelectAll = %+v, want the single row of Select", rs)
	}
	s = NewSelector(runs, Selection{Events: []Kind{KindNote}})
	if rs := s.SelectAll(&l.Entries[5]); len(rs) != 0 {
		t.Errorf("SelectAll kept a filtered report: %+v", rs)
	}
}

func TestBucketDefaultLast(t *testing.T) {
	var es []Entry
	for i, bar := range []float64{1, 2, 3, 4} {
		es = append(es, bucketReport(t, 0, time.Time{}, time.Duration(i)*400*time.Millisecond, step{pump: '*', bar: bar}))
	}
	cols := lastCols("pump", "pressure")
	b := NewBucketer(time.Second, cols, false)
	rows := bucketAll(b, []SourceInfo{{Index: 0, Path: "src0"}}, es)
	var buf bytes.Buffer
	w := NewBucketWriter(&buf, FormatText, cols, false, time.UTC)
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ls := lines(buf.String())
	got := make([]string, len(ls))
	for i, l := range ls {
		got[i] = strings.Join(strings.Fields(l), " ")
	}
	want := []string{"t wall reports pump pressure", "0.000 - 3 * 3", "1.000 - 1 * 4"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBucketSkipsStandby(t *testing.T) {
	es := []Entry{
		bucketReport(t, 0, time.Time{}, 100*time.Millisecond, step{pump: '-'}),
		bucketReport(t, 0, time.Time{}, 1100*time.Millisecond, step{pump: '?'}),
		bucketReport(t, 0, time.Time{}, 1600*time.Millisecond, step{pump: '?'}),
		bucketReport(t, 0, time.Time{}, 2100*time.Millisecond, step{pump: '-'}),
	}
	b := NewBucketer(time.Second, lastCols("pressure"), false)
	got := startsOf(bucketAll(b, []SourceInfo{{Index: 0, Path: "src0"}}, es))
	want := []startCount{{0, 1}, {2 * time.Second, 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("buckets %v, want %v", got, want)
	}
}

func TestBucketMissingKey(t *testing.T) {
	es := []Entry{bucketReport(t, 0, time.Time{}, 0, step{pump: '-'})}
	es = append(es, es[0])
	es[1].Spec, es[1].Record, es[1].Offset, es[1].Index = nil, nil, 100*time.Millisecond, 1
	b := NewBucketer(time.Second, []BucketCol{{Key: "pressure", Agg: Agg{Name: "n"}}}, false)
	rows := bucketAll(b, []SourceInfo{{Index: 0, Path: "src0"}}, es)
	if len(rows) != 1 || rows[0].Reports != 2 || *rows[0].Values[0].Num != 1 {
		t.Errorf("rows %+v", rows)
	}
	if m := b.Missing(); len(m) != 1 || !strings.Contains(m[0], "key pressure: 1 report") {
		t.Errorf("missing %q", m)
	}
}

// A capture between two raw logs: rows of raw run 1 that the margin takes
// from the second raw log come after the capture's run 2, and must land
// in run 1's buckets, not in a second set of them.
func TestBucketRunAcrossCapture(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, true, concat(idle(4, 0, 0), pumping(10, "9.0", 9, 4), idle(14, 40, 0)))
	addSource(t, &l, time.Time{}, false, concat(idle(4, 0, 0), pumping(10, "9.0", 9, 4), idle(14, 40, 0)))
	addSource(t, &l, runT0.Add(6900*time.Millisecond), true, idle(8, 40, 0))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 2 || runs[0].First.Source != 0 || runs[1].First.Source != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	b := NewBucketer(time.Second, lastCols("pressure"), true)
	added, closed := bucketRuns(l, runs, Selection{Runs: []int{1, 2}, Margin: 4 * time.Second}, b)
	seen := map[string]bool{}
	last, reports := 0, 0
	for _, r := range append(added, closed...) {
		if r.Run.N < last {
			t.Errorf("run %d after run %d", r.Run.N, last)
		}
		last = r.Run.N
		k := fmt.Sprint(r.Run.N, *r.RunT)
		if seen[k] {
			t.Errorf("run %d bucket %v twice", r.Run.N, *r.RunT)
		}
		seen[k] = true
		if r.Run.N == 1 {
			reports += r.Reports
		}
	}
	// Run 1 (t0 1 s, last 3.25 s): all 28 reports of the first raw log and
	// those of the second up to 7.25 s (6.9 and 7.15 s).
	if reports != 30 {
		t.Errorf("run 1 reports %d, want 30", reports)
	}
}

// A report in two overlapping run windows lacks a key once, not twice.
func TestBucketMissingKeyOverlap(t *testing.T) {
	l, runs := twoShots(t)
	sel := Selection{Runs: []int{1, 2}, Margin: 2500 * time.Millisecond}
	b := NewBucketer(time.Second, []BucketCol{{Key: "absent", Agg: Agg{Name: "n"}}}, true)
	bucketRuns(l, runs, sel, b)
	// Steps 0-34 lie in a window; 13-16 in both.
	if m := b.Missing(); len(m) != 1 || m[0] != "key absent: 35 reports whose XML lacks it" {
		t.Errorf("missing %q", m)
	}
}

// A capture is one source: its runs are over once a raw log follows, and
// come out before Close.
func TestBucketCaptureRunClosesOnNextSource(t *testing.T) {
	var l loaded
	addSource(t, &l, time.Time{}, false, concat(idle(4, 0, 0), pumping(10, "9.0", 9, 4), idle(14, 40, 0)))
	addSource(t, &l, runT0, true, concat(idle(4, 0, 0), pumping(10, "9.0", 9, 4), idle(14, 40, 0)))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 2 || runs[0].First.Source != 0 || runs[1].First.Source != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	b := NewBucketer(time.Second, lastCols("pressure"), true)
	added, closed := bucketRuns(l, runs, Selection{Runs: []int{1, 2}}, b)
	if len(added) == 0 || added[0].Run.N != 1 || len(closed) == 0 || closed[0].Run.N != 2 {
		t.Errorf("run 1 not emitted by Add: %d rows from Add, %d from Close", len(added), len(closed))
	}
}
