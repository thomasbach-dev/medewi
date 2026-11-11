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
	srcs, err := sourcePaths(in.args, in.dir, since, until)
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

// loadRuns loads paths and finds the runs that start in [since, until).
// When extend is set (input from dir), it appends the next raw log of dir
// while a run that keep selects is open at the end, up to maxExtend files.
func loadRuns(paths []string, dir string, extend bool, cat *logview.Catalog, showDumps bool,
	since, until time.Time, keep func(logview.Run) bool,
) (logview.Loaded, []logview.Run, []string, error) {
	var all []string
	next := -1
	for extra := 0; ; extra++ {
		l, err := logview.Load(paths, cat, showDumps)
		if err != nil {
			return logview.Loaded{}, nil, nil, err
		}
		if cat == nil {
			return l, nil, nil, nil
		}
		runs := logview.FindRuns(l, brew.DefaultConfig(), since, until)
		if !extend || len(paths) == 0 || !slices.ContainsFunc(runs, func(r logview.Run) bool { return r.OpenAtEnd && keep(r) }) {
			return l, runs, nil, nil
		}
		if all == nil {
			if all, err = logview.Select(dir, time.Time{}, time.Time{}); err != nil {
				return logview.Loaded{}, nil, nil, err
			}
			next = slices.Index(all, paths[len(paths)-1]) + 1
		}
		if next <= 0 || next >= len(all) {
			return l, runs, nil, nil
		}
		if extra == maxExtend {
			return l, runs, []string{fmt.Sprintf("a run is still open after %d more raw logs; stopped reading at %s",
				maxExtend, paths[len(paths)-1])}, nil
		}
		paths = append(paths, all[next])
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
// directory.
func sourcePaths(args []string, dir string, since, until time.Time) ([]source, error) {
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
