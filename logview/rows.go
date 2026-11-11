package logview

import (
	"slices"
	"strconv"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// Row is one entry selected for output, with the run it belongs to.
type Row struct {
	Entry Entry
	Run   *Run           // the run whose window holds the entry, or nil
	RunT  *time.Duration // entry time minus the run's first entry; nil without Run
}

// Selection says which entries Rows keeps.
type Selection struct {
	// Since and Until cut by wall time (since inclusive, until exclusive; a
	// zero bound is open). Entries without wall time always pass. They do
	// not apply when Runs or RunKinds select runs.
	Since, Until time.Time
	// Runs (by Run.N) and RunKinds select whole run windows (a union).
	Runs     []int
	RunKinds []RunKind
	// Margin widens each run window on both sides.
	Margin time.Duration
	// Events keeps only entries of these kinds; empty keeps all.
	Events []Kind
	// SkipStandby drops reports whose pump marker is '?'.
	SkipStandby bool
	// Changes drops a report whose ChangeKeys values equal those of the
	// last printed report. Empty ChangeKeys means pump plus every XML key.
	// Entries that are not reports always pass and do not reset the
	// comparison.
	Changes    bool
	ChangeKeys []string
}

// entryTime is the time of e on its timeline: wall clock for raw logs,
// offset for captures.
func entryTime(e *Entry) time.Duration {
	if e.HasWall {
		return time.Duration(e.Wall.UnixNano())
	}
	return e.Offset
}

// window is a run's precomputed time window on its timeline.
type window struct {
	run        *Run
	t0, lo, hi time.Duration
	wall       bool
	source     int
	selected   bool
}

// at returns the time of e relative to the run's first entry when e lies in
// the window on the run's timeline.
func (w *window) at(e *Entry, t time.Duration) (time.Duration, bool) {
	if w.wall != e.HasWall || (!w.wall && w.source != e.Source) || t < w.lo || t > w.hi {
		return 0, false
	}
	return t - w.t0, true
}

// Rows returns the entries of l that sel keeps, in order. Run and RunT come
// from the earliest run whose window [first-margin, last+margin] holds the
// entry on the same timeline: raw logs share the wall clock, a capture is
// its own timeline. When sel selects runs, only selected runs count.
func Rows(l Loaded, runs []Run, sel Selection) []Row {
	byRun := len(sel.Runs) > 0 || len(sel.RunKinds) > 0
	wins := make([]window, 0, len(runs))
	for i := range runs {
		r := &runs[i]
		first, last := &l.Entries[r.First], &l.Entries[r.Last]
		w := window{run: r, wall: first.HasWall, source: first.Source, t0: entryTime(first)}
		w.lo, w.hi = w.t0-sel.Margin, entryTime(last)+sel.Margin
		w.selected = slices.Contains(sel.Runs, r.N) || slices.Contains(sel.RunKinds, r.Kind)
		if byRun && !w.selected {
			continue
		}
		wins = append(wins, w)
	}
	cache := map[*leva.RecordSpec]*specKeys{}
	var lastSpec *leva.RecordSpec
	var lastSig, sig []byte
	var out []Row
	for i := range l.Entries {
		e := &l.Entries[i]
		row := Row{Entry: *e}
		t := entryTime(e)
		for j := range wins {
			if d, ok := wins[j].at(e, t); ok {
				row.Run, row.RunT = wins[j].run, &d
				break
			}
		}
		if byRun {
			if row.Run == nil {
				continue
			}
		} else if e.HasWall && (!sel.Since.IsZero() && e.Wall.Before(sel.Since) || !sel.Until.IsZero() && !e.Wall.Before(sel.Until)) {
			continue
		}
		if len(sel.Events) > 0 && !slices.Contains(sel.Events, e.Kind) {
			continue
		}
		if e.Kind == KindReport {
			if sel.SkipStandby && len(e.Raw) > 2 && e.Raw[2] == '?' {
				continue
			}
			if sel.Changes && e.Spec != nil && e.Record != nil {
				sk := cache[e.Spec]
				if sk == nil {
					sk = newSpecKeys(e.Spec, sel.ChangeKeys)
					cache[e.Spec] = sk
				}
				sig = sk.sig(sig[:0], e)
				if e.Spec == lastSpec && string(sig) == string(lastSig) {
					continue
				}
				lastSpec = e.Spec
				lastSig = append(lastSig[:0], sig...)
			}
		}
		out = append(out, row)
	}
	return out
}

// specKeys lists what --changes compares for one spec.
type specKeys struct {
	pump bool
	pos  []int // positions in Record.Fields
}

func newSpecKeys(spec *leva.RecordSpec, keys []string) *specKeys {
	cols, _ := Columns(spec)
	pos := map[string]int{}
	var all []string
	for _, c := range cols {
		if c.Source != "xml" {
			continue
		}
		for p, v := range spec.Values {
			if v.Index == c.Index {
				pos[c.Key] = p
				break
			}
		}
		all = append(all, c.Key)
	}
	sk := &specKeys{}
	if len(keys) == 0 {
		keys = append([]string{"pump"}, all...)
	}
	for _, k := range keys {
		if k == "pump" {
			sk.pump = true
		} else if p, ok := pos[k]; ok {
			sk.pos = append(sk.pos, p)
		}
	}
	return sk
}

// sig appends the compared values of e's report to b.
func (sk *specKeys) sig(b []byte, e *Entry) []byte {
	if sk.pump && len(e.Raw) > 2 {
		b = append(b, e.Raw[2])
	}
	b = append(b, 0)
	for _, p := range sk.pos {
		if p < len(e.Record.Fields) {
			v := ParseField(e.Record.Fields[p])
			switch {
			case v.Num != nil:
				b = strconv.AppendFloat(b, *v.Num, 'f', -1, 64)
			case v.Str != nil:
				b = append(b, *v.Str...)
			default:
				b = append(b, v.Raw...)
			}
		}
		b = append(b, 0)
	}
	return b
}
