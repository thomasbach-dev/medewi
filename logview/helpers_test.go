package logview

import (
	"time"

	"github.com/thomasbach-dev/medewi/brew"
)

// loaded is a whole input held in memory, for tests: what a Stream hands
// on, collected.
type loaded struct {
	Entries  []Entry
	Sources  []SourceInfo
	Problems []string
}

// loadAll streams paths into a loaded.
func loadAll(paths []string, cat *Catalog) (loaded, error) {
	var l loaded
	s := Stream{
		Catalog:  cat,
		OnSource: func(si SourceInfo) error { l.Sources = append(l.Sources, si); return nil },
		OnEntry:  func(e *Entry) error { l.Entries = append(l.Entries, *e); return nil },
	}
	err := s.Add(paths...)
	l.Problems = s.Problems
	return l, err
}

// findRuns feeds l to a RunFinder, source by source.
func findRuns(l loaded, cfg brew.Config, since, until time.Time) []Run {
	f := NewRunFinder(cfg)
	k := 0
	for _, si := range l.Sources {
		f.Source(si)
		for ; k < len(l.Entries) && l.Entries[k].Source == si.Index; k++ {
			f.Entry(&l.Entries[k])
		}
	}
	return f.Runs(since, until)
}

// selectRows runs the entries of l through a Selector.
func selectRows(l loaded, runs []Run, sel Selection) []Row {
	s := NewSelector(runs, sel)
	var out []Row
	for i := range l.Entries {
		if r, ok := s.Select(&l.Entries[i]); ok {
			out = append(out, r)
		}
	}
	return out
}
