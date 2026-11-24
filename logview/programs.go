package logview

import (
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/program"
)

// Program is a program run found in an input.
type Program struct {
	program.Run
	N               int // from 1, by start, among the programs in the window
	FirstPt, LastPt Point
	XML             string // session XML of the first sample
	// Began is the wall time of FirstPt, as Run.Start; zero when unknown.
	Began time.Time
}

// Window returns p as a Run, for a Selector.
func (p Program) Window() Run {
	return Run{
		N: p.N, Kind: RunKind(p.Kind), First: p.FirstPt, Last: p.LastPt,
		Duration: p.End.Sub(p.Start), XML: p.XML, Start: p.Began,
	}
}

// programRef is the Ref of the samples a ProgramFinder feeds.
type programRef struct {
	p    Point
	xml  string
	wall time.Time
}

// ProgramFinder finds program runs in a decoded input, entry by entry, as
// RunFinder finds runs: call Source before the entries of each source and
// Entry for each entry. Captures have no common clock, so each gets its own
// stretch of time, before any raw log.
type ProgramFinder struct {
	det       *program.Detector
	src, prev SourceInfo
	fedAny    bool
}

// NewProgramFinder returns an empty ProgramFinder.
func NewProgramFinder() *ProgramFinder { return &ProgramFinder{det: program.NewDetector()} }

// SetSettings gives the machine settings of the whole input.
func (f *ProgramFinder) SetSettings(s program.Settings) { f.det.SetSettings(s) }

// captureSpan separates the captures of an input on the finder's timeline.
const captureSpan = 1000 * time.Hour

// captureEpoch starts the captures' timeline, long before any raw log.
var captureEpoch = time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)

// Source announces the source whose entries follow.
func (f *ProgramFinder) Source(s SourceInfo) { f.src = s }

// Entry feeds e when it is a decoded report or a display frame.
func (f *ProgramFinder) Entry(e *Entry) {
	if e.Record == nil && e.Frame == nil {
		return
	}
	if f.fedAny && f.src.Index != f.prev.Index &&
		(f.src.SessionStart || !continues(f.prev, f.src, brew.DefaultConfig().CoastMax)) {
		f.det.Break()
	}
	f.prev, f.fedAny = f.src, true
	at := e.Wall
	if !e.HasWall {
		at = captureEpoch
		for range f.src.Index {
			at = at.Add(captureSpan)
		}
		at = at.Add(e.Offset)
	}
	if e.Frame != nil {
		f.det.Frame(at, e.Frame.Rows)
		return
	}
	p := PointOf(e)
	ref := programRef{p: p, xml: e.XML, wall: wallOf(f.src, p)}
	f.det.Sample(program.SampleFrom(*e.Record, e.Raw, at, ref))
}

// Programs returns the programs found so far that start in [since, until)
// (a zero bound is open; programs in captures are always kept), numbered
// from 1. It does not change f, so more entries may follow.
func (f *ProgramFinder) Programs(since, until time.Time) []Program {
	var out []Program
	for _, r := range f.det.Runs() {
		first, last := r.First.(programRef), r.Last.(programRef)
		if w := first.p.Wall; first.p.HasWall && (!since.IsZero() && w.Before(since) || !until.IsZero() && !w.Before(until)) {
			continue
		}
		out = append(out, Program{Run: r, N: len(out) + 1, FirstPt: first.p, LastPt: last.p, XML: first.xml, Began: first.wall})
	}
	return out
}
