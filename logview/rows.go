package logview

import (
	"slices"
	"strings"
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
func entryTime(e Entry) time.Duration {
	if e.HasWall {
		return time.Duration(e.Wall.UnixNano())
	}
	return e.Offset
}

// Rows returns the entries of l that sel keeps, in order. Run and RunT come
// from the earliest run whose window [first-margin, last+margin] holds the
// entry on the same timeline: raw logs share the wall clock, a capture is
// its own timeline.
func Rows(l Loaded, runs []Run, sel Selection) []Row {
	byRun := len(sel.Runs) > 0 || len(sel.RunKinds) > 0
	cols := map[*leva.RecordSpec][]Column{}
	var lastSpec *leva.RecordSpec
	var lastSig string
	var out []Row
	for _, e := range l.Entries {
		row := Row{Entry: e}
		for i := range runs {
			if t, ok := runOffset(l, &runs[i], e, sel.Margin); ok {
				row.Run, row.RunT = &runs[i], &t
				break
			}
		}
		if byRun {
			// Overlapping windows: the entry may sit in a selected run
			// that lost to an earlier unselected one.
			row.Run, row.RunT = nil, nil
			for i := range runs {
				if !slices.Contains(sel.Runs, runs[i].N) && !slices.Contains(sel.RunKinds, runs[i].Kind) {
					continue
				}
				if t, ok := runOffset(l, &runs[i], e, sel.Margin); ok {
					row.Run, row.RunT = &runs[i], &t
					break
				}
			}
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
				sig := changeSig(e, sel.ChangeKeys, cols)
				if e.Spec == lastSpec && sig == lastSig {
					continue
				}
				lastSpec, lastSig = e.Spec, sig
			}
		}
		out = append(out, row)
	}
	return out
}

// runOffset returns the time of e relative to the run's first entry when e
// lies in the run's window on the run's timeline.
func runOffset(l Loaded, r *Run, e Entry, margin time.Duration) (time.Duration, bool) {
	first, last := l.Entries[r.First], l.Entries[r.Last]
	if first.HasWall != e.HasWall || (!first.HasWall && first.Source != e.Source) {
		return 0, false
	}
	t, t0 := entryTime(e), entryTime(first)
	if t < t0-margin || t > entryTime(last)+margin {
		return 0, false
	}
	return t - t0, true
}

// changeSig joins the compared values of a report into one string.
func changeSig(e Entry, keys []string, cache map[*leva.RecordSpec][]Column) string {
	cs, ok := cache[e.Spec]
	if !ok {
		cs, _ = Columns(e.Spec)
		cache[e.Spec] = cs
	}
	if len(keys) == 0 {
		keys = []string{"pump"}
		for _, c := range cs {
			if c.Source == "xml" {
				keys = append(keys, c.Key)
			}
		}
	}
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		if k == "pump" {
			if len(e.Raw) > 2 {
				b.WriteByte(e.Raw[2])
			}
		} else {
			for _, c := range cs {
				if c.Source != "xml" || c.Key != k {
					continue
				}
				for _, f := range e.Record.Fields {
					if f.Spec.Index != c.Index {
						continue
					}
					v := ParseField(f)
					switch {
					case v.Num != nil:
						b.WriteString(FormatNum(*v.Num))
					case v.Str != nil:
						b.WriteString(*v.Str)
					default:
						b.WriteString(v.Raw)
					}
				}
			}
		}
		b.WriteByte(0)
	}
	return b.String()
}
