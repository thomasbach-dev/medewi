package main

import (
	"bufio"
	"io"
	"time"

	"github.com/thomasbach-dev/medewi/logview"
)

// sessionParts are the parts of [since, until) that sessions cover, an
// open end (zero until) kept open.
func sessionParts(sessions []logview.Session, since, until time.Time) []logview.SessionPart {
	if until.IsZero() {
		until = openEndAt
	}
	var out []logview.SessionPart
	for _, w := range logview.SplitWindow(sessions, since, until) {
		p := logview.SessionPart{Session: sessions[w.Session], Since: w.Since, Until: w.Until}
		if until.Equal(openEndAt) && w.Until.Equal(openEndAt) {
			p.Until = time.Time{}
		}
		out = append(out, p)
	}
	return out
}

// summaryCmd prints what each source of the input holds.
func summaryCmd(args []string, xmlPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	in := input{stdin: stdin}
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
		if sc, err = scan(paths, in.stdinData, in.dir, true, in.fromDir(), cat, false, in.since.T, in.until.T, keepAll, nil); err != nil {
			return fail(stderr, 1, "%v", err)
		}
	}
	ss := logview.NewSummaryScanner()
	ss.Since, ss.Until = in.since.T, in.until.T
	// The sessions are gathered in the same pass, one file at a time:
	// captures have no wall time, hence no sessions.
	var sb logview.SessionBuilder
	s := logview.Stream{
		Catalog:  cat,
		Stdin:    in.stdinData,
		OnSource: func(si logview.SourceInfo) error { ss.Source(si); sb.Source(si); return nil },
		OnEntry:  func(e *logview.Entry) error { ss.Entry(e); sb.Entry(e); return nil },
	}
	if err := s.Add(sc.paths...); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	sums := ss.Summaries(sc.runs)
	sessions := sessionParts(sb.Sessions(), in.since.T, in.until.T)
	w := bufio.NewWriter(stdout)
	err := logview.WriteSummariesSessions(w, in.f, sums, sessions, time.Local)
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
