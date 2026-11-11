package logview

import (
	"slices"
	"sort"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
)

// RunKind classifies a run.
type RunKind string

// The run kinds: pump runs (shot, flush) and wand draws.
const (
	RunShot  RunKind = "shot"
	RunFlush RunKind = "flush"
	RunDraw  RunKind = "draw"
)

// Point locates an entry of the input without holding it.
type Point struct {
	Index   int // Entry.Index
	Source  int // Entry.Source
	Path    string
	Src     int // Entry.Src
	Offset  time.Duration
	Wall    time.Time
	HasWall bool
}

// PointOf returns where e is.
func PointOf(e *Entry) Point {
	return Point{Index: e.Index, Source: e.Source, Path: e.Path, Src: e.Src, Offset: e.Offset, Wall: e.Wall, HasWall: e.HasWall}
}

// at is the time of p on its timeline: wall clock for raw logs, offset for
// captures.
func (p Point) at() time.Duration {
	if p.HasWall {
		return time.Duration(p.Wall.UnixNano())
	}
	return p.Offset
}

// Run is one pump run or wand draw found in an input.
type Run struct {
	N         int // from 1, in input order, among the runs in the window
	Kind      RunKind
	Truncated bool // cut by an abort while pumping (source gap or end of input)
	OpenAtEnd bool // still open at the end of the input
	// First and Last are the first and last pumping report of a shot or
	// flush, the reports of the first and last volume increment of a draw.
	First, Last Point
	Duration    time.Duration
	PeakBar     *float64 // nil for draws
	VolumeML    float64
	XML         string // session XML of the first sample
}

// RunFinder finds runs in a decoded input, entry by entry: it feeds every
// decoded report to a brew.Recorder and keeps only the runs. Raw logs are
// timed by wall clock; each capture is its own timeline on its offsets. The
// recorder is aborted between two sources unless both are raw logs and the
// next starts at most cfg.CoastMax after the last event of the previous
// one. Call Source before the entries of each source (Stream.OnSource) and
// Entry for each entry (Stream.OnEntry).
type RunFinder struct {
	cfg  brew.Config
	rec  *brew.Recorder
	runs []Run
	cur  *Run        // the open shot or flush
	fed  []fedSample // samples of an open draw, to place it

	src, prev SourceInfo // the current source; that of the last fed report
	fedAny    bool
}

// NewRunFinder returns a RunFinder with recorder settings cfg.
func NewRunFinder(cfg brew.Config) *RunFinder {
	return &RunFinder{cfg: cfg, rec: brew.NewRecorder(cfg)}
}

// Source announces the source whose entries follow.
func (f *RunFinder) Source(s SourceInfo) { f.src = s }

// Entry feeds e when it is a decoded report.
func (f *RunFinder) Entry(e *Entry) {
	if e.Record == nil || e.Kind != KindReport {
		return
	}
	if f.fedAny && f.src.Index != f.prev.Index && !continues(f.prev, f.src, f.cfg.CoastMax) {
		f.abort(false)
	}
	f.prev, f.fedAny = f.src, true
	f.feed(e)
}

// Runs returns the runs found so far; a run still open is closed as at the
// end of the input and marked OpenAtEnd, without changing f, so more
// entries may follow. Only runs whose first sample lies in [since, until)
// are returned (a zero bound is open; runs in captures are always kept),
// numbered from 1 in input order.
func (f *RunFinder) Runs(since, until time.Time) []Run {
	c := *f
	c.rec = f.rec.Clone()
	c.runs = slices.Clone(f.runs)
	if f.cur != nil {
		cur := *f.cur
		c.cur = &cur
	}
	c.fed = nil
	c.abort(true)

	sort.SliceStable(c.runs, func(i, j int) bool { return c.runs[i].First.Index < c.runs[j].First.Index })
	var out []Run
	for _, r := range c.runs {
		p := r.First
		if p.HasWall && (!since.IsZero() && p.Wall.Before(since) || !until.IsZero() && !p.Wall.Before(until)) {
			continue
		}
		r.N = len(out) + 1
		out = append(out, r)
	}
	return out
}

// continues reports whether source b carries on the recorder of source a.
func continues(a, b SourceInfo, coastMax time.Duration) bool {
	if !a.HasWall || !b.HasWall || a.Events == 0 || b.Events == 0 {
		return false
	}
	gap := b.Start.Add(b.First).Sub(a.Start.Add(a.Last))
	return gap <= coastMax
}

type fedSample struct {
	at  time.Time
	p   Point
	xml string
}

// captureBase anchors a capture's offsets on a timeline.
var captureBase = time.Unix(0, 0).UTC()

func (f *RunFinder) feed(e *Entry) {
	at := captureBase.Add(e.Offset)
	if e.HasWall {
		at = e.Wall
	}
	p := PointOf(e)
	f.fed = append(f.fed, fedSample{at, p, e.XML})
	res := f.rec.Feed(brew.SampleFrom(*e.Record, e.Raw, at))
	if res.Water != nil {
		f.draw(*res.Water)
	}
	if res.Closed != nil {
		f.close(*res.Closed, false)
	}
	if res.Opened {
		f.cur = &Run{First: p, XML: e.XML}
	}
	if len(res.Points) > 0 && f.cur != nil {
		f.cur.Last = p
	}
	if !f.rec.Drawing() {
		// A draw starts at a later sample, if at all.
		f.fed = f.fed[:0]
	}
}

// draw records a finished wand draw, placed on the first fed samples at its
// start and end times.
func (f *RunFinder) draw(w brew.WaterDraw) {
	first := -1
	for k, s := range f.fed {
		if first < 0 && s.at.Equal(w.StartedAt) {
			first = k
		}
		if first >= 0 && s.at.Equal(w.EndedAt) {
			r := Run{Kind: RunDraw, First: f.fed[first].p, Last: s.p, Duration: w.Duration, VolumeML: w.VolumeML}
			r.XML = f.fed[first].xml
			f.runs = append(f.runs, r)
			break
		}
	}
	f.fed = f.fed[:0]
}

func (f *RunFinder) close(s brew.Summary, atEnd bool) {
	if f.cur == nil {
		return
	}
	r := *f.cur
	f.cur = nil
	r.Kind = RunFlush
	if s.Normal == brew.Shot {
		r.Kind = RunShot
	}
	r.Truncated = s.Kind.Truncated()
	r.OpenAtEnd = atEnd
	r.Duration = s.Duration
	peak := s.PeakBar
	r.PeakBar = &peak
	r.VolumeML = s.VolumeML
	f.runs = append(f.runs, r)
	f.fed = f.fed[:0]
}

func (f *RunFinder) abort(atEnd bool) {
	if s := f.rec.Abort(brew.TruncatedLost); s != nil {
		f.close(*s, atEnd)
	}
	f.fed = f.fed[:0]
}
