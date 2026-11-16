package main

import (
	"bufio"
	"io"
	"time"

	"github.com/thomasbach-dev/medewi/logview"
)

// summarySessions lists the raw-log sessions among paths with the part of
// [since, until) each covers. Captures have no wall time, hence no
// sessions: any capture among paths yields none.
func summarySessions(paths []string, since, until time.Time) ([]logview.SessionPart, error) {
	srcs := make([]logview.Source, 0, len(paths))
	for _, p := range paths {
		s, err := logview.Open(p)
		if err != nil {
			return nil, err
		}
		if !s.HasWall {
			return nil, nil
		}
		srcs = append(srcs, s)
	}
	if until.IsZero() {
		until = openEndAt
	}
	sessions := logview.Sessions(srcs)
	var out []logview.SessionPart
	for _, w := range logview.SplitWindow(sessions, since, until) {
		p := logview.SessionPart{Session: sessions[w.Session], Since: w.Since, Until: w.Until}
		if until.Equal(openEndAt) && w.Until.Equal(openEndAt) {
			p.Until = time.Time{}
		}
		out = append(out, p)
	}
	return out, nil
}

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
	if code != 0 || len(paths) == 0 {
		return code
	}
	cat, code := loadCatalog(xmlPath, stderr)
	if code != 0 {
		return code
	}
	// Runs are numbered over the whole input, as runs does, and a run
	// open at the end of the input from --dir is read on, as runs does,
	// so both classify it alike.
	sc := scanned{paths: paths}
	if cat != nil {
		var err error
		keepAll := func(logview.Run) bool { return true }
		if sc, err = scan(paths, in.dir, true, in.fromDir(), cat, false, in.since.T, in.until.T, keepAll); err != nil {
			return fail(stderr, 1, "%v", err)
		}
	}
	ss := logview.NewSummaryScanner()
	ss.Since, ss.Until = in.since.T, in.until.T
	s := logview.Stream{
		Catalog:  cat,
		OnSource: func(si logview.SourceInfo) error { ss.Source(si); return nil },
		OnEntry:  func(e *logview.Entry) error { ss.Entry(e); return nil },
	}
	if err := s.Add(sc.paths...); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	sums := ss.Summaries(sc.runs)
	sessions, err := summarySessions(sc.paths, in.since.T, in.until.T)
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	w := bufio.NewWriter(stdout)
	err = logview.WriteSummariesSessions(w, in.f, sums, sessions, time.Local)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	warn(stderr, in.notes)
	warn(stderr, sc.problems)
	warn(stderr, sc.warns)
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
