package main

import (
	"bufio"
	"io"
	"time"

	"github.com/thomasbach-dev/medewi/logview"
)

// summaryCmd prints what each source of the input holds.
func summaryCmd(args []string, xmlPath string, stdout, stderr io.Writer) int {
	var in input
	fs := subFlags("summary", stderr)
	in.register(fs)
	if code, done := parse(fs, args, stderr); done {
		return code
	}
	if code := in.check(fs, stderr); code != 0 {
		return code
	}
	paths, code := in.paths(in.since.T, in.until.T, stderr)
	if code != 0 {
		return code
	}
	cat, code := loadCatalog(xmlPath, stderr)
	if code != 0 {
		return code
	}
	// Runs are numbered over the whole input, as runs does.
	sc := scanned{paths: paths}
	if cat != nil {
		var err error
		keepAll := func(logview.Run) bool { return true }
		if sc, err = scan(paths, in.dir, true, false, cat, false, in.since.T, in.until.T, keepAll); err != nil {
			return fail(stderr, 1, "%v", err)
		}
	}
	ss := logview.NewSummaryScanner()
	s := logview.Stream{
		Catalog:  cat,
		OnSource: func(si logview.SourceInfo) error { ss.Source(si); return nil },
		OnEntry:  func(e *logview.Entry) error { ss.Entry(e); return nil },
	}
	if err := s.Add(sc.paths...); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	sums := ss.Summaries(sc.runs)
	w := bufio.NewWriter(stdout)
	err := logview.WriteSummaries(w, in.f, sums, time.Local)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	warn(stderr, sc.problems)
	if cat == nil {
		warn(stderr, []string{"no vendor XML (--xml / MEDEWI_XML_PATH): modes, runs and masked are left empty"})
	} else {
		for _, sum := range sums {
			if !sum.Decoded {
				warn(stderr, []string{sum.Src + ": no XML for its reports: modes, runs and masked are left empty"})
			}
		}
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}
