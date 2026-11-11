package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/spf13/pflag"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/logview"
	"github.com/thomasbach-dev/medewi/rawlog"
	"github.com/thomasbach-dev/medewi/systime"
)

// maxExtend caps how many raw logs are appended to finish an open run.
const maxExtend = 24

// input holds the input flags all subcommands share.
type input struct {
	dir          string
	since, until systime.Timestamp
	format       string
	f            logview.Format
	args         []string
}

func (in *input) register(fs *pflag.FlagSet) {
	in.dir = envOr(defaultRawDir, "MEDEWID_RAW_DIR")
	fs.StringVar(&in.dir, "dir", in.dir, "raw-log directory used without FILE (env MEDEWID_RAW_DIR)")
	fs.VarP(&in.since, "since", "s", "from this time on (inclusive)")
	fs.VarP(&in.until, "until", "u", "before this time (exclusive)")
	fs.StringVar(&in.format, "format", "text", "output format: text, jsonl or csv")
}

// check validates the flags after parsing; code 0 means ok.
func (in *input) check(fs *pflag.FlagSet, stderr io.Writer) int {
	f, err := logview.ParseFormat(in.format)
	if err != nil {
		return fail(stderr, 2, "--format: %v", err)
	}
	in.f, in.args = f, fs.Args()
	return 0
}

func (in *input) window() bool { return !in.since.T.IsZero() || !in.until.T.IsZero() }

// fromDir reports whether the input is the raw logs of --dir.
func (in *input) fromDir() bool { return len(in.args) == 0 }

// paths resolves the input files, selecting raw logs for [since, until).
// Code 0 means ok; otherwise the error is printed.
func (in *input) paths(since, until time.Time, stderr io.Writer) ([]string, int) {
	srcs, err := sourcePaths(in.args, in.dir, since, until, in.window())
	if err != nil {
		return nil, fail(stderr, 1, "%v", err)
	}
	out := make([]string, 0, len(srcs))
	for _, s := range srcs {
		if in.window() && !s.raw {
			return nil, fail(stderr, 2, "--since/--until need raw logs with wall time; %s is a capture", s.path)
		}
		out = append(out, s.path)
	}
	return out, 0
}

// loadCatalog opens --xml; nil without one. Code 0 means ok.
func loadCatalog(xmlPath string, stderr io.Writer) (*logview.Catalog, int) {
	if xmlPath == "" {
		return nil, 0
	}
	cat, err := logview.LoadCatalog(xmlPath)
	if err != nil {
		return nil, fail(stderr, 1, "--xml: %v", err)
	}
	return cat, 0
}

// scanned is what the first pass over an input keeps: no entries, only the
// runs and the report specs in use.
type scanned struct {
	paths    []string // the input, with the raw logs appended to finish runs
	runs     []logview.Run
	specs    map[*leva.RecordSpec]bool
	problems []string
	warns    []string
}

// scan decodes paths one file at a time and collects the report specs and,
// when detect is set, the runs that start in [since, until). When extend is
// set too (input from dir), it decodes the next raw log of dir while a run
// that keep selects is open at the end, up to maxExtend files.
func scan(paths []string, dir string, detect, extend bool, cat *logview.Catalog, showDumps bool,
	since, until time.Time, keep func(logview.Run) bool,
) (scanned, error) {
	sc := scanned{paths: slices.Clone(paths), specs: map[*leva.RecordSpec]bool{}}
	finder := logview.NewRunFinder(brew.DefaultConfig())
	s := logview.Stream{
		Catalog: cat, ShowDumps: showDumps,
		OnSource: func(si logview.SourceInfo) error {
			finder.Source(si)
			return nil
		},
		OnEntry: func(e *logview.Entry) error {
			if e.Spec != nil {
				sc.specs[e.Spec] = true
			}
			if detect {
				finder.Entry(e)
			}
			return nil
		},
	}
	err := s.Add(paths...)
	sc.problems = s.Problems
	if err != nil || !detect {
		return sc, err
	}
	var all []string
	next := -1
	for extra := 0; ; extra++ {
		sc.runs = finder.Runs(since, until)
		if !extend || len(sc.paths) == 0 || !slices.ContainsFunc(sc.runs, func(r logview.Run) bool { return r.OpenAtEnd && keep(r) }) {
			return sc, nil
		}
		if all == nil {
			if all, err = logview.Select(dir, time.Time{}, time.Time{}); err != nil {
				return sc, err
			}
			next = slices.Index(all, sc.paths[len(sc.paths)-1]) + 1
		}
		if next <= 0 || next >= len(all) {
			return sc, nil
		}
		if extra == maxExtend {
			sc.warns = []string{fmt.Sprintf("a run is still open after %d more raw logs; stopped reading at %s",
				maxExtend, sc.paths[len(sc.paths)-1])}
			return sc, nil
		}
		// Only the appended file is decoded; the XML carries on.
		sc.paths = append(sc.paths, all[next])
		err = s.Add(all[next])
		sc.problems = s.Problems
		if err != nil {
			return sc, err
		}
		next++
	}
}

// source is one file to decode: a raw log (start from its name) or a
// capture log.jsonl.
type source struct {
	path  string
	raw   bool
	start time.Time
}

// sourcePaths resolves the FILE|DIR arguments, or the raw logs in dir that
// overlap the window when there are none, and sorts them by start time
// (captures, which have none, first and in argument order). A directory
// argument holding log.jsonl is a capture; any other is a raw-log
// directory. With neighbours, the raw logs just before and after those a
// directory selects are added too, so run detection sees a run that
// crosses into or out of the window from a file outside it.
func sourcePaths(args []string, dir string, since, until time.Time, neighbours bool) ([]source, error) {
	var paths []string
	if len(args) == 0 {
		args = []string{dir}
	}
	for _, a := range args {
		fi, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			paths = append(paths, a)
			continue
		}
		capture := filepath.Join(a, "log.jsonl")
		if _, err := os.Stat(capture); err == nil {
			paths = append(paths, capture)
			continue
		}
		sel, err := logview.Select(a, since, until)
		if err != nil {
			return nil, err
		}
		if neighbours && len(sel) > 0 {
			if sel, err = withNeighbours(a, sel); err != nil {
				return nil, err
			}
		}
		paths = append(paths, sel...)
	}
	out := make([]source, 0, len(paths))
	for _, p := range paths {
		start, ok := rawlog.FileStart(filepath.Base(p))
		out = append(out, source{path: p, raw: ok, start: start})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out, nil
}

// withNeighbours adds the raw logs of dir just before and after sel, a
// contiguous selection of it.
func withNeighbours(dir string, sel []string) ([]string, error) {
	all, err := logview.Select(dir, time.Time{}, time.Time{})
	if err != nil {
		return nil, err
	}
	i, j := slices.Index(all, sel[0]), slices.Index(all, sel[len(sel)-1])
	if i < 0 || j < 0 {
		return sel, nil
	}
	return all[max(i-1, 0):min(j+2, len(all))], nil
}
