package logview

import (
	"fmt"
	"sort"
)

// RunStats aggregates report fields per run, over each run's window from
// its first sample to its last (no margin).
type RunStats struct {
	keys    []string
	aggs    []Agg
	sel     *Selector
	byRun   map[*Run]int // run -> index in accs
	byN     map[int]int  // Run.N -> index in accs
	accs    [][]Acc      // per run, per key
	missing map[string]int
}

// NewRunStats returns a RunStats over runs (as RunFinder.Runs returns
// them) for keys and aggs.
func NewRunStats(runs []Run, keys []string, aggs []Agg) *RunStats {
	s := &RunStats{
		keys: keys, aggs: aggs,
		byRun: map[*Run]int{}, byN: map[int]int{}, missing: map[string]int{},
		accs: make([][]Acc, len(runs)),
	}
	all := make([]int, len(runs))
	for i := range runs {
		all[i] = runs[i].N
		s.byRun[&runs[i]], s.byN[runs[i].N] = i, i
		s.accs[i] = make([]Acc, len(keys))
		for j := range keys {
			s.accs[i][j] = NewAcc(aggs)
		}
	}
	s.sel = NewSelector(runs, Selection{Runs: all})
	return s
}

// Entry counts e when it is a non-standby report inside a run window.
func (s *RunStats) Entry(e *Entry) {
	if e.Kind != KindReport || pumpByte(e) == '?' {
		return
	}
	row, ok := s.sel.Select(e)
	if !ok || row.Run == nil {
		return
	}
	accs := s.accs[s.byRun[row.Run]]
	src := srcRef(e.Path, e.Src)
	for i, k := range s.keys {
		v, ok := FieldOf(e, k)
		if !ok {
			s.missing[k]++
			continue
		}
		accs[i].Add(v, *row.RunT, src)
	}
}

// Header returns the column names, <key>_<agg>, keys first.
func (s *RunStats) Header() []string {
	out := make([]string, 0, len(s.keys)*len(s.aggs))
	for _, k := range s.keys {
		for _, a := range s.aggs {
			out = append(out, k+"_"+a.Name)
		}
	}
	return out
}

// Values returns the aggregates of run n in Header order.
func (s *RunStats) Values(n int) []AggValue {
	var accs []Acc
	if i, ok := s.byN[n]; ok {
		accs = s.accs[i]
	} else {
		accs = make([]Acc, len(s.keys))
	}
	out := make([]AggValue, 0, len(s.keys)*len(s.aggs))
	for i := range s.keys {
		for _, a := range s.aggs {
			out = append(out, accs[i].Result(a))
		}
	}
	return out
}

// Missing lists the keys that some run reports lacked (no XML for the
// report or its XML lacks the key), one line per key with a count.
func (s *RunStats) Missing() []string {
	out := make([]string, 0, len(s.missing))
	for k, n := range s.missing {
		out = append(out, fmt.Sprintf("key %s: %d reports whose XML lacks it", k, n))
	}
	sort.Strings(out)
	return out
}
