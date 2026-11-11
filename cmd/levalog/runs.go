package main

import (
	"bufio"
	"io"
	"slices"
	"time"

	"github.com/thomasbach-dev/medewi/logview"
)

// parseRunKinds parses a comma-separated list of run kinds.
func parseRunKinds(s, flag string, stderr io.Writer) ([]logview.RunKind, int) {
	var out []logview.RunKind
	for _, k := range splitList(s) {
		rk := logview.RunKind(k)
		if rk != logview.RunShot && rk != logview.RunFlush && rk != logview.RunDraw {
			return nil, fail(stderr, 2, "%s: unknown run kind %q (want shot, flush or draw)", flag, k)
		}
		out = append(out, rk)
	}
	return out, 0
}

// runsCmd lists the runs of the input.
func runsCmd(args []string, xmlPath string, stdout, stderr io.Writer) int {
	var in input
	var kind string
	fs := subFlags("runs", stderr)
	in.register(fs)
	fs.StringVar(&kind, "kind", "", "only runs of these kinds, comma-separated: shot, flush, draw (numbers stay)")
	if code, done := parse(fs, args, stderr); done {
		return code
	}
	if code := in.check(fs, stderr); code != 0 {
		return code
	}
	kinds, code := parseRunKinds(kind, "--kind", stderr)
	if code != 0 {
		return code
	}
	if xmlPath == "" {
		return needXML(stderr, "runs")
	}
	paths, code := in.paths(in.since.T, in.until.T, stderr)
	if code != 0 {
		return code
	}
	if len(paths) == 0 {
		return fail(stderr, 0, "no raw logs for this window in %s", in.dir)
	}
	cat, code := loadCatalog(xmlPath, stderr)
	if code != 0 {
		return code
	}
	keep := func(r logview.Run) bool { return len(kinds) == 0 || slices.Contains(kinds, r.Kind) }
	sc, err := scan(paths, in.dir, true, in.fromDir(), cat, false, in.since.T, in.until.T, keep)
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	runs := slices.DeleteFunc(sc.runs, func(r logview.Run) bool { return !keep(r) })
	w := bufio.NewWriter(stdout)
	err = logview.WriteRuns(w, in.f, runs, time.Local)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	warn(stderr, sc.problems)
	warn(stderr, sc.warns)
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}
