package logview

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// Row is one entry selected for output, with the run it belongs to.
type Row struct {
	Entry *Entry
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
	// last printed report in the same run (or also in no run). Empty
	// ChangeKeys means pump plus every XML key. Entries that are not
	// reports always pass and do not reset the comparison.
	Changes    bool
	ChangeKeys []string
}

// window is a run's precomputed time window on its timeline.
type window struct {
	run        *Run
	t0, lo, hi time.Duration
}

// timeline names the timeline of an entry or point: -1 for the wall clock
// of raw logs, the source index for a capture.
func timeline(hasWall bool, source int) int {
	if hasWall {
		return -1
	}
	return source
}

// Selector picks the rows of an input entry by entry. Run and RunT come
// from the earliest run whose window [first-margin, last+margin] holds the
// entry on the same timeline: raw logs share the wall clock, a capture is
// its own timeline. When the Selection selects runs, only selected runs
// count.
type Selector struct {
	sel   Selection
	byRun bool
	wins  map[int][]window // by timeline, in run order

	cache    map[*leva.RecordSpec]*specKeys
	lastSpec *leva.RecordSpec
	lastRun  *Run // the run of the last printed report
	lastSig  []byte
	sig      []byte

	seen  int
	drops [nFilters]int
}

// The filters of a Selection, in the order they apply.
const (
	filterWindow = iota
	filterRuns
	filterEvents
	filterStandby
	filterChanges
	nFilters
)

// Drop is what one filter of a Selection dropped: the entries that
// survived the filters before it and not this one.
type Drop struct {
	Filter string // the filter as its flags read, e.g. "--event report"
	N      int
}

// Seen is the number of entries Select and SelectAll were given.
func (s *Selector) Seen() int { return s.seen }

// Drops lists what each active filter dropped, in the order they apply:
// window, run selection, event kinds, standby, changes.
func (s *Selector) Drops() []Drop {
	sel := &s.sel
	var out []Drop
	add := func(f int, active bool, name string) {
		if active {
			out = append(out, Drop{Filter: name, N: s.drops[f]})
		}
	}
	var window []string
	if !sel.Since.IsZero() {
		window = append(window, "--since")
	}
	if !sel.Until.IsZero() {
		window = append(window, "--until")
	}
	add(filterWindow, !s.byRun && len(window) > 0, strings.Join(window, "/"))
	var runs []string
	if len(sel.Runs) > 0 {
		ns := make([]string, len(sel.Runs))
		for i, n := range sel.Runs {
			ns[i] = strconv.Itoa(n)
		}
		runs = append(runs, "--run "+strings.Join(ns, ","))
	}
	if len(sel.RunKinds) > 0 {
		ks := make([]string, len(sel.RunKinds))
		for i, k := range sel.RunKinds {
			ks[i] = string(k)
		}
		runs = append(runs, "--run-kind "+strings.Join(ks, ","))
	}
	add(filterRuns, s.byRun, strings.Join(runs, " "))
	ks := make([]string, len(sel.Events))
	for i, k := range sel.Events {
		ks[i] = string(k)
	}
	add(filterEvents, len(sel.Events) > 0, "--event "+strings.Join(ks, ","))
	add(filterStandby, sel.SkipStandby, "--skip-standby")
	add(filterChanges, sel.Changes, "--changes")
	return out
}

// NewSelector returns a Selector for runs, as RunFinder.Runs returns them
// (in input order); rows point into runs.
func NewSelector(runs []Run, sel Selection) *Selector {
	s := &Selector{
		sel:   sel,
		byRun: len(sel.Runs) > 0 || len(sel.RunKinds) > 0,
		wins:  map[int][]window{},
		cache: map[*leva.RecordSpec]*specKeys{},
	}
	for i := range runs {
		r := &runs[i]
		if s.byRun && !slices.Contains(sel.Runs, r.N) && !slices.Contains(sel.RunKinds, r.Kind) {
			continue
		}
		tl := timeline(r.First.HasWall, r.First.Source)
		t0 := r.First.at()
		s.wins[tl] = append(s.wins[tl], window{run: r, t0: t0, lo: t0 - sel.Margin, hi: r.Last.at() + sel.Margin})
	}
	return s
}

// window returns the earliest window holding e. Runs follow each other on
// a timeline, so windows are ordered by lo and by hi alike: the first one
// whose hi reaches e is the only candidate.
func (s *Selector) window(e *Entry) (*window, time.Duration) {
	ws := s.wins[timeline(e.HasWall, e.Source)]
	t := PointOf(e).at()
	k := sort.Search(len(ws), func(i int) bool { return ws[i].hi >= t })
	if k == len(ws) || ws[k].lo > t {
		return nil, t
	}
	return &ws[k], t
}

// Select returns the row for e and whether the Selection keeps it.
func (s *Selector) Select(e *Entry) (Row, bool) {
	s.seen++
	row := Row{Entry: e}
	if w, t := s.window(e); w != nil {
		d := t - w.t0
		row.Run, row.RunT = w.run, &d
	}
	if s.byRun && row.Run == nil {
		s.drops[filterRuns]++
		return Row{}, false
	}
	if !s.keep(e, row.Run) {
		return Row{}, false
	}
	return row, true
}

// SelectAll returns one row per selected run window holding e (several
// where margins overlap), in run order, when the Selection keeps e.
// Without run selection it returns the row Select gives, or none.
// Changes compares within the earliest of those runs.
func (s *Selector) SelectAll(e *Entry) []Row {
	if !s.byRun {
		if r, ok := s.Select(e); ok {
			return []Row{r}
		}
		return nil
	}
	s.seen++
	ws := s.wins[timeline(e.HasWall, e.Source)]
	t := PointOf(e).at()
	var out []Row
	// Windows are ordered by lo and hi alike: those holding t are a
	// contiguous range starting at the first whose hi reaches t.
	for k := sort.Search(len(ws), func(i int) bool { return ws[i].hi >= t }); k < len(ws) && ws[k].lo <= t; k++ {
		d := t - ws[k].t0
		out = append(out, Row{Entry: e, Run: ws[k].run, RunT: &d})
	}
	if len(out) == 0 {
		s.drops[filterRuns]++
		return nil
	}
	if !s.keep(e, out[0].Run) {
		return nil
	}
	return out
}

// keep applies the filters other than run selection to e, in run (or
// nil), and counts what each drops.
func (s *Selector) keep(e *Entry, run *Run) bool {
	sel := &s.sel
	if !s.byRun && e.HasWall && (!sel.Since.IsZero() && e.Wall.Before(sel.Since) || !sel.Until.IsZero() && !e.Wall.Before(sel.Until)) {
		s.drops[filterWindow]++
		return false
	}
	if len(sel.Events) > 0 && !slices.Contains(sel.Events, e.Kind) {
		s.drops[filterEvents]++
		return false
	}
	if e.Kind == KindReport {
		if sel.SkipStandby && len(e.Raw) > 2 && e.Raw[2] == '?' {
			s.drops[filterStandby]++
			return false
		}
		if sel.Changes && e.Spec != nil && e.Record != nil {
			sk := s.cache[e.Spec]
			if sk == nil {
				sk = newSpecKeys(e.Spec, sel.ChangeKeys)
				s.cache[e.Spec] = sk
			}
			s.sig = sk.sig(s.sig[:0], e)
			if e.Spec == s.lastSpec && run == s.lastRun && string(s.sig) == string(s.lastSig) {
				s.drops[filterChanges]++
				return false
			}
			s.lastSpec, s.lastRun = e.Spec, run
			s.lastSig = append(s.lastSig[:0], s.sig...)
		}
	}
	return true
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
