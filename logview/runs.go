package logview

import (
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

// Run is one pump run or wand draw found in a Loaded.
type Run struct {
	N         int // from 1, in input order, among the runs in the window
	Kind      RunKind
	Truncated bool // cut by an abort while pumping (source gap or end of input)
	OpenAtEnd bool // still open at the end of the input
	// First and Last index Loaded.Entries: the first and last pumping
	// report of a shot or flush, the reports of the first and last volume
	// increment of a draw.
	First, Last int
	Duration    time.Duration
	PeakBar     *float64 // nil for draws
	VolumeML    float64
	XML         string // session XML of the first sample
}

// FindRuns feeds every decoded report of l in order to a brew.Recorder and
// returns the runs it finds. Raw logs are timed by wall clock; each capture
// is its own timeline on its offsets. The recorder is aborted between two
// sources unless both are raw logs and the next starts at most
// cfg.CoastMax after the last event of the previous one; a run still open
// at the end is closed the same way and marked OpenAtEnd. Only runs whose
// first sample lies in [since, until) are returned (a zero bound is open;
// runs in captures are always kept), numbered from 1.
func FindRuns(l Loaded, cfg brew.Config, since, until time.Time) []Run {
	f := &runFinder{l: l, rec: brew.NewRecorder(cfg)}
	prev := -1
	for i, e := range l.Entries {
		if e.Record == nil || e.Kind != KindReport {
			continue
		}
		if prev >= 0 && e.Source != prev && !continues(l.Sources, prev, e.Source, cfg.CoastMax) {
			f.abort(false)
		}
		prev = e.Source
		f.feed(i)
	}
	f.abort(true)

	sort.SliceStable(f.runs, func(i, j int) bool { return f.runs[i].First < f.runs[j].First })
	var out []Run
	for _, r := range f.runs {
		e := l.Entries[r.First]
		if e.HasWall && (!since.IsZero() && e.Wall.Before(since) || !until.IsZero() && !e.Wall.Before(until)) {
			continue
		}
		r.N = len(out) + 1
		out = append(out, r)
	}
	return out
}

// continues reports whether source b carries on the recorder of source a.
func continues(srcs []Source, a, b int, coastMax time.Duration) bool {
	if a < 0 || b < 0 || a >= len(srcs) || b >= len(srcs) {
		return false
	}
	sa, sb := srcs[a], srcs[b]
	if !sa.HasWall || !sb.HasWall || len(sa.Events) == 0 || len(sb.Events) == 0 {
		return false
	}
	gap := sb.Wall(sb.Events[0]).Sub(sa.Wall(sa.Events[len(sa.Events)-1]))
	return gap <= coastMax
}

type fedSample struct {
	at  time.Time
	idx int
}

type runFinder struct {
	l    Loaded
	rec  *brew.Recorder
	runs []Run
	cur  *Run        // the open shot or flush
	fed  []fedSample // samples since the last close or abort, to place draws
}

// captureBase anchors a capture's offsets on a timeline.
var captureBase = time.Unix(0, 0).UTC()

func (f *runFinder) feed(i int) {
	e := f.l.Entries[i]
	at := captureBase.Add(e.Offset)
	if e.HasWall {
		at = e.Wall
	}
	f.fed = append(f.fed, fedSample{at, i})
	res := f.rec.Feed(brew.SampleFrom(*e.Record, e.Raw, at))
	if res.Water != nil {
		f.draw(*res.Water)
	}
	if res.Closed != nil {
		f.close(*res.Closed, false)
	}
	if res.Opened {
		f.cur = &Run{First: i, XML: e.XML}
	}
	if len(res.Points) > 0 && f.cur != nil {
		f.cur.Last = i
	}
}

// draw records a finished wand draw, placed on the first fed samples at its
// start and end times.
func (f *runFinder) draw(w brew.WaterDraw) {
	first := -1
	for k, s := range f.fed {
		if first < 0 && s.at.Equal(w.StartedAt) {
			first = k
		}
		if first >= 0 && s.at.Equal(w.EndedAt) {
			r := Run{Kind: RunDraw, First: f.fed[first].idx, Last: s.idx, Duration: w.Duration, VolumeML: w.VolumeML}
			r.XML = f.l.Entries[r.First].XML
			f.runs = append(f.runs, r)
			break
		}
	}
	f.fed = f.fed[:0]
}

func (f *runFinder) close(s brew.Summary, atEnd bool) {
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

func (f *runFinder) abort(atEnd bool) {
	if s := f.rec.Abort(brew.TruncatedLost); s != nil {
		f.close(*s, atEnd)
	}
	f.fed = f.fed[:0]
}
