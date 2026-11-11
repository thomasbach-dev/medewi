package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/logview"
)

// fields lists the columns of the XML in effect at the end of the input:
// the last entry's, else the one its file starts with. With --xml FILE and
// no readable --dir input it lists that file.
func fields(args []string, xmlPath string, stdout, stderr io.Writer) int {
	var in input
	fs := subFlags("fields", stderr)
	in.register(fs)
	if code, done := parse(fs, args, stderr); done {
		return code
	}
	if code := in.check(fs, stderr); code != 0 {
		return code
	}
	if xmlPath == "" {
		return needXML(stderr, "fields")
	}
	cat, code := loadCatalog(xmlPath, stderr)
	if code != 0 {
		return code
	}
	fi, err := os.Stat(xmlPath)
	if err != nil {
		return fail(stderr, 1, "--xml: %v", err)
	}

	var name string
	var spec *leva.RecordSpec
	var problems []string
	srcs, err := sourcePaths(in.args, in.dir, in.since.T, in.until.T, false)
	switch {
	case (err != nil || len(srcs) == 0) && in.fromDir() && !fi.IsDir():
		name = strings.TrimSuffix(filepath.Base(xmlPath), ".xml")
		spec, _ = cat.Lookup("")
	case err != nil:
		return fail(stderr, 1, "%v", err)
	case len(srcs) == 0:
		return fail(stderr, 1, "no raw logs for this window in %s", in.dir)
	default:
		for _, s := range srcs {
			if in.window() && !s.raw {
				return fail(stderr, 2, "--since/--until need raw logs with wall time; %s is a capture", s.path)
			}
		}
		// The last source alone: Load looks back through its directory for
		// the XML in effect when it starts.
		last := srcs[len(srcs)-1].path
		l, err := logview.Load([]string{last}, cat, false)
		if err != nil {
			return fail(stderr, 1, "%v", err)
		}
		problems = l.Problems
		if n := len(l.Entries); n > 0 {
			name, spec = l.Entries[n-1].XML, l.Entries[n-1].Spec
		} else if src := l.Sources[0]; src.HasWall {
			var warns []string
			if name, warns, err = logview.LastXML(filepath.Dir(src.Path), src.Start); err != nil {
				return fail(stderr, 1, "%v", err)
			}
			problems = append(problems, warns...)
		}
		if spec == nil {
			var prob string
			if spec, prob = cat.Lookup(name); spec == nil {
				msg := last + ": " + prob
				warn(stderr, slices.DeleteFunc(problems, func(p string) bool { return p == msg }))
				return fail(stderr, 1, "%s", msg)
			}
		}
	}
	cols, err := logview.Columns(spec)
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	w := bufio.NewWriter(stdout)
	err = logview.WriteColumns(w, in.f, name, cols)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	warn(stderr, problems)
	return 0
}
