package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/backup"
	"github.com/thomasbach-dev/medewi/logview"
	"github.com/thomasbach-dev/medewi/program"
)

// programsCmd lists the program runs of the input.
func programsCmd(args []string, xmlPath string, stdin io.Reader, stdout, stderr io.Writer) int {
	in := input{stdin: stdin}
	fs := subFlags("programs", stderr)
	var settings string
	in.register(fs)
	fs.StringVar(&settings, "settings", "", "a settings backup (MCu dump, see levabak) that was current for the whole input; it confirms or contradicts programs")
	if code, done := parse(fs, args, stderr); done {
		return code
	}
	if code := in.check(fs, stderr); code != 0 {
		return code
	}
	if xmlPath == "" {
		return needXML(stderr, "programs")
	}
	finder := logview.NewProgramFinder()
	var notes []string
	if fs.Changed("settings") {
		st, missing, code := loadSettings(settings, stderr)
		if code != 0 {
			return code
		}
		if len(missing) > 0 {
			notes = []string{"--settings: not found in the backup: " + strings.Join(missing, ", ")}
		}
		finder.SetSettings(st)
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
	sc, err := scan(paths, in.stdinData, in.dir, true, in.fromDir(), cat, false, in.since.T, in.until.T, keep, finder)
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	w := bufio.NewWriter(stdout)
	err = logview.WritePrograms(w, in.f, sc.programs, time.Local)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	warn(stderr, in.notes)
	warn(stderr, notes)
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

// loadSettings reads the program settings from the backup at path; missing
// names the sections it lacks. Code 0 means ok.
func loadSettings(path string, stderr io.Writer) (st program.Settings, missing []string, code int) {
	bks, _, err := backup.Resolve([]string{path})
	if err != nil {
		return st, nil, fail(stderr, 1, "--settings: %v", err)
	}
	if len(bks) != 1 {
		return st, nil, fail(stderr, 2, "--settings: want one backup, %s holds %d", path, len(bks))
	}
	if err := bks[0].Load(); err != nil {
		return st, nil, fail(stderr, 1, "--settings: %v", err)
	}
	u := bks[0].MCu
	if u == nil {
		return st, nil, fail(stderr, 2, "--settings: %s is no MCu dump", path)
	}
	if len(u.Problems) > 0 {
		p := u.Problems[0]
		return st, nil, fail(stderr, 2, "--settings: damaged backup, line %d: %s (levabak check shows all)", p.Line, p.Text)
	}
	st, missing = program.SettingsFrom(u)
	return st, missing, 0
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
