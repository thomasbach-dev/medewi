package main

import (
	"bufio"
	"fmt"
	"io"
	"time"

	"github.com/thomasbach-dev/medewi/logview"
)

// programsCmd lists the program runs of the input.
func programsCmd(args []string, xmlPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	in := input{stdin: stdin}
	fs := subFlags("programs", stderr)
	in.register(fs)
	if code, done := parse(fs, args, stderr); done {
		return code
	}
	if code := in.check(fs, stderr); code != 0 {
		return code
	}
	if xmlPath == "" {
		return needXML(stderr, "programs")
	}
	paths, code := in.paths(in.since.T, in.until.T, stderr)
	if code != 0 || len(paths) == 0 {
		return code
	}
	cat, code := loadCatalog(xmlPath, stderr)
	if code != 0 {
		return code
	}
	keep := func(logview.Run) bool { return true }
	sc, err := scan(paths, in.stdinData, in.dir, true, in.fromDir(), cat, false, in.since.T, in.until.T, keep, logview.NewProgramFinder())
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	w := bufio.NewWriter(stdout)
	err = logview.WritePrograms(w, in.f, sc.programs, time.Local)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	warn(stderr, in.notes)
	warn(stderr, sc.problems)
	warn(stderr, sc.warns)
	if len(sc.programs) == 0 {
		warn(stderr, []string{noPrograms(sc)})
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}

// noPrograms says why programs found none.
func noPrograms(sc scanned) string {
	if sc.decoded == 0 && len(sc.noXML) > 0 {
		return "no programs: no report decoded (no XML for " + noXMLNames(sc) + ")"
	}
	return fmt.Sprintf("no programs in %d reports (pump marker * in %d)", sc.decoded, sc.pumping)
}

// useWindows makes the programs of sc its runs, for a Selector.
func (sc *scanned) useWindows() {
	sc.runs = nil
	for _, p := range sc.programs {
		sc.runs = append(sc.runs, p.Window())
	}
}
