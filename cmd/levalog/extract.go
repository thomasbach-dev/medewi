package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/logview"
	"github.com/thomasbach-dev/medewi/systime"
)

// extractAddr is the machine address of every extracted fixture.
const extractAddr = "machine.example:23"

// wallLayout formats wall times in extract's notes and messages.
const wallLayout = "2006-01-02 15:04:05.000"

// provenanceLayout formats the wall times of the manifest's provenance
// line: with the zone offset, as the manifest outlives the local zone.
const provenanceLayout = wallLayout + " -07:00"

// extractOpts are the flags of extract beyond the input.
type extractOpts struct {
	run                  int
	margin               time.Duration
	out, scenario, notes string
	split                bool
}

// extractCmd cuts a window out of the raw logs and writes it as a capture
// directory (log.jsonl + manifest.json), or log.jsonl's lines to stdout
// with --out -.
func extractCmd(args []string, xmlPath string, stdout, stderr io.Writer) int {
	var in input
	var o extractOpts
	fs := subFlags("extract", stderr)
	in.registerWindow(fs)
	fs.IntVar(&o.run, "run", 0, "the window of this run, by number as runs numbers it (needs the XML); -s/-u then pick the input")
	fs.Var(systime.NewSpan(&o.margin), "margin", "widen the -s/-u or run window on both sides (systemd.time span, default 0; needs -s/-u or --run)")
	fs.StringVar(&o.out, "out", "", "capture directory to write (must not exist or be empty), or - for log.jsonl's lines on stdout (required)")
	fs.StringVar(&o.scenario, "scenario", "extract", "the manifest's scenario")
	fs.StringVar(&o.notes, "notes", "", "free text for the manifest, before the provenance line")
	fs.BoolVar(&o.split, "split-sessions", false, "write one capture per raw-log session part of the window into DIR/01, DIR/02, ... (needs --out DIR)")
	if code, done := parse(fs, args, stderr); done {
		return code
	}
	in.args = fs.Args()
	byRun := fs.Changed("run")
	since, until := in.since.T, in.until.T
	switch {
	case o.out == "":
		return fail(stderr, 2, "extract needs --out DIR or --out -")
	case byRun && o.run < 1:
		return fail(stderr, 2, "--run: bad run %d (want N >= 1)", o.run)
	case fs.Changed("margin") && !byRun && !in.window():
		return fail(stderr, 2, "--margin needs a window (-s/-u) or --run")
	case !byRun && !in.window():
		return fail(stderr, 2, "extract needs a window: -s/--since and -u/--until, or --run N")
	case !since.IsZero() && !until.IsZero() && !until.After(since):
		return fail(stderr, 2, "--until must be after --since")
	case byRun && xmlPath == "":
		return needXML(stderr, "--run")
	case o.split && o.out == "-":
		return fail(stderr, 2, "--split-sessions needs --out DIR, not --out -")
	}
	if o.out != "-" {
		if code := checkOut(o.out, stderr); code != 0 {
			return code
		}
	}

	// A run is cut whole, with its margins, from files before and after
	// the window, as events --run prints it.
	// An explicit -s/-u window is widened the same way.
	if !since.IsZero() {
		since = since.Add(-o.margin)
	}
	if !until.IsZero() {
		until = until.Add(o.margin)
	}
	if !byRun {
		in.since.T, in.until.T = since, until
	}
	srcs, notes, empty, err := sourcePaths(in.args, in.dir, since, until, in.window())
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	paths := make([]string, 0, len(srcs))
	for _, s := range srcs {
		if !s.raw {
			return fail(stderr, 2, "extract reads raw logs only; %s is a capture", s.path)
		}
		paths = append(paths, s.path)
	}
	warn(stderr, notes)
	if len(paths) == 0 {
		warn(stderr, empty)
		return fail(stderr, 1, "no raw logs to extract from")
	}

	if byRun {
		cat, code := loadCatalog(xmlPath, stderr)
		if code != 0 {
			return code
		}
		keep := func(r logview.Run) bool { return r.N == o.run }
		sc, err := scan(paths, in.dir, true, in.fromDir(), cat, false, in.since.T, in.until.T, keep)
		if err != nil {
			return fail(stderr, 1, "%v", err)
		}
		warn(stderr, sc.problems)
		warn(stderr, sc.warns)
		i := slices.IndexFunc(sc.runs, keep)
		if i < 0 {
			warn(stderr, missingRuns(runSet{{o.run, o.run}}, nil, sc.runs))
			return 1
		}
		r := sc.runs[i]
		// The run window is [First - margin, Last + margin]; until is
		// exclusive, and raw-log times are whole milliseconds.
		since, until = r.First.Wall.Add(-o.margin), r.Last.Wall.Add(o.margin+time.Millisecond)
		paths = sc.paths
	}
	if until.IsZero() {
		until = openEndAt
	}

	opened, extNotes, err := openSession(paths, extendDirs(in))
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	warn(stderr, extNotes)
	// The parts of the window per session, for --split-sessions and the
	// boundary refusal; the first starts no earlier than the first raw log.
	parts := logview.SplitWindow(logview.Sessions(opened), since, until)
	noteClamp := func() {
		if len(parts) > 0 && !since.IsZero() && parts[0].Since.After(since) {
			warn(stderr, []string{"the window starts before the first raw log; part 1 starts at " +
				parts[0].Since.In(time.Local).Format(wallLayout)})
		}
	}
	if o.split {
		noteClamp()
		return extractParts(opened, parts, since, until, xmlPath, o, stdout, stderr)
	}
	ex, err := logview.Extract(opened, since, until)
	var sbe *logview.SessionBoundaryError
	if errors.As(err, &sbe) {
		noteClamp()
		return refuseBoundaries(stderr, parts)
	}
	if errors.Is(err, logview.ErrNoEvents) {
		return fail(stderr, 1, "no events in %s", fmtWindow(since, until))
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	warn(stderr, ex.Notes)

	if o.out == "-" {
		_, _ = fmt.Fprintln(stderr, "levalog: no manifest with --out -")
		w := bufio.NewWriter(stdout)
		err := fixture.WriteLog(w, ex.Events)
		if ferr := w.Flush(); err == nil {
			err = ferr
		}
		if err != nil {
			return fail(stderr, 1, "%v", err)
		}
		return 0
	}
	return writeExtract(o.out, ex, xmlPath, o, stdout, stderr)
}

// maxParts is the most parts --split-sessions numbers (two digits).
const maxParts = 99

// openEndAt stands in for an open --until.
var openEndAt = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// fmtWindow formats [since, until) in local time; openEndAt reads as
// "open end".
func fmtWindow(since, until time.Time) string {
	end := "open end"
	if !until.Equal(openEndAt) {
		end = until.In(time.Local).Format(wallLayout)
	}
	return fmt.Sprintf("[%s, %s)", since.In(time.Local).Format(wallLayout), end)
}

// refuseBoundaries names every session boundary inside the window and
// prints the -s/-u pair of each part, as the window's refusal.
func refuseBoundaries(stderr io.Writer, parts []logview.Window) int {
	at := make([]string, 0, len(parts)-1)
	for _, w := range parts[1:] {
		at = append(at, w.Since.In(time.Local).Format(wallLayout))
	}
	noun := "boundaries"
	if len(at) == 1 {
		noun = "boundary"
	}
	_, _ = fmt.Fprintf(stderr, "levalog: the window crosses %d raw-log session %s (%s); extract each part:\n",
		len(at), noun, strings.Join(at, ", "))
	for _, w := range parts {
		_, _ = fmt.Fprintf(stderr, "  -s '%s'", w.Since.In(time.Local).Format(wallLayout))
		if !w.Until.Equal(openEndAt) {
			_, _ = fmt.Fprintf(stderr, " -u '%s'", w.Until.In(time.Local).Format(wallLayout))
		}
		_, _ = fmt.Fprintln(stderr)
	}
	return fail(stderr, 1, "or write every part at once with --split-sessions --out DIR")
}

// extractParts writes one capture per session part of the window into
// out/01, out/02, ... in time order; a part without events is skipped
// with a note and takes no number.
func extractParts(opened []logview.Source, parts []logview.Window, since, until time.Time, xmlPath string,
	o extractOpts, stdout, stderr io.Writer,
) int {
	if len(parts) > maxParts {
		return fail(stderr, 1, "the window holds %d session parts, more than --split-sessions numbers (%d); narrow it with -s/-u",
			len(parts), maxParts)
	}
	n := 0
	for i, w := range parts {
		ex, err := logview.Extract(opened, w.Since, w.Until)
		if errors.Is(err, logview.ErrNoEvents) {
			warn(stderr, []string{fmt.Sprintf("part %d %s has no events; skipped", i+1, fmtWindow(w.Since, w.Until))})
			continue
		}
		if err != nil {
			return fail(stderr, 1, "part %d %s: %v", i+1, fmtWindow(w.Since, w.Until), err)
		}
		for _, note := range ex.Notes {
			warn(stderr, []string{fmt.Sprintf("part %d: %s", i+1, note)})
		}
		n++
		if code := writeExtract(filepath.Join(o.out, fmt.Sprintf("%02d", n)), ex, xmlPath, o, stdout, stderr); code != 0 {
			return code
		}
	}
	if n == 0 {
		return fail(stderr, 1, "no events in %s", fmtWindow(since, until))
	}
	return 0
}

// writeExtract writes ex as a capture into dir and lists it on stdout.
func writeExtract(dir string, ex logview.Extracted, xmlPath string, o extractOpts, stdout, stderr io.Writer) int {
	sum, sumNotes, err := xmlSHA(xmlPath, ex.XMLName)
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	warn(stderr, sumNotes)
	provenance := fmt.Sprintf("extracted from %s %s – %s by levalog extract", strings.Join(ex.Files, ", "),
		ex.First.In(time.Local).Format(provenanceLayout), ex.Last.In(time.Local).Format(provenanceLayout))
	mNotes := provenance
	if o.notes != "" {
		mNotes = o.notes + "\n" + provenance
	}
	m := fixture.Manifest{
		Scenario:        o.scenario,
		FirmwareVersion: ex.Firmware,
		XMLName:         ex.XMLName,
		XMLSHA256:       sum,
		MachineAddr:     extractAddr,
		Notes:           mNotes,
		Recorded:        ex.Last,
	}
	if err := fixture.WriteCapture(dir, "log.jsonl", "manifest.json", ex.Events, m); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	_, _ = fmt.Fprintf(stdout, "%s: %d events (%d reports, %d frames, %d notes)\n%s\n",
		filepath.Join(dir, "log.jsonl"), len(ex.Events), ex.Reports, ex.Frames, ex.NoteEvents,
		filepath.Join(dir, "manifest.json"))
	return 0
}

// checkOut refuses an --out that exists and is not an empty directory.
func checkOut(out string, stderr io.Writer) int {
	fi, err := os.Stat(out)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return 0
	case err != nil:
		return fail(stderr, 1, "--out: %v", err)
	case !fi.IsDir():
		return fail(stderr, 1, "--out %s exists and is not a directory", out)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		return fail(stderr, 1, "--out: %v", err)
	}
	if len(entries) > 0 {
		return fail(stderr, 1, "--out %s is not empty", out)
	}
	return 0
}

// extendDirs are the raw-log directories of the input, cleaned: --dir
// without FILE|DIR, else the DIR arguments. The session's first file is
// looked for there; FILE arguments are taken as given.
func extendDirs(in input) []string {
	if in.fromDir() {
		return []string{filepath.Clean(in.dir)}
	}
	var dirs []string
	for _, a := range in.args {
		if fi, err := os.Stat(a); err == nil && fi.IsDir() {
			dirs = append(dirs, filepath.Clean(a))
		}
	}
	return dirs
}

// openSession opens paths, raw logs in start order, and, when the first one
// lies in one of dirs, prepends the earlier raw logs of that directory back
// to the session's first file (logview.SessionStart), at most maxExtend.
// The result stays in start order, as logview.Extract requires.
func openSession(paths, dirs []string) ([]logview.Source, []string, error) {
	srcs := make([]logview.Source, 0, len(paths))
	for _, p := range paths {
		s, err := logview.Open(p)
		if err != nil {
			return nil, nil, err
		}
		srcs = append(srcs, s)
	}
	dir := filepath.Dir(paths[0])
	if !slices.Contains(dirs, dir) {
		return srcs, nil, nil
	}
	var all []string
	for extra := 0; !logview.SessionStart(srcs[0]); extra++ {
		if all == nil {
			var err error
			if all, err = logview.Select(dir, time.Time{}, time.Time{}); err != nil {
				return nil, nil, err
			}
		}
		i := slices.Index(all, srcs[0].Path)
		if i <= 0 {
			break // the session began before the oldest file here
		}
		if extra == maxExtend {
			return srcs, []string{fmt.Sprintf("the session's first file is not within %d raw logs before %s; stopped reading back",
				maxExtend, filepath.Base(srcs[0].Path))}, nil
		}
		s, err := logview.Open(all[i-1])
		if err != nil {
			return nil, nil, err
		}
		srcs = append([]logview.Source{s}, srcs...)
	}
	return srcs, nil, nil
}

// xmlSHA returns the sha256 of the XML named name in effect: xmlPath if it
// is a file named NAME.xml, NAME.xml in it if it is a directory; "" with a
// note otherwise, and "" without xmlPath or name.
func xmlSHA(xmlPath, name string) (string, []string, error) {
	if xmlPath == "" || name == "" {
		return "", nil, nil
	}
	fi, err := os.Stat(xmlPath)
	if err != nil {
		return "", nil, fmt.Errorf("--xml: %w", err)
	}
	p := xmlPath
	if fi.IsDir() {
		p = filepath.Join(xmlPath, name+".xml")
	} else if filepath.Base(xmlPath) != name+".xml" {
		return "", []string{fmt.Sprintf("xml_sha256 left empty: --xml %s is not %s.xml", filepath.Base(xmlPath), name)}, nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", []string{fmt.Sprintf("xml_sha256 left empty: no %s.xml in --xml %s", name, xmlPath)}, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("--xml: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil, nil
}
