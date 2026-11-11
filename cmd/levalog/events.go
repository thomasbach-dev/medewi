package main

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/logview"
	"github.com/thomasbach-dev/medewi/systime"
)

// eventKinds are the values of --event.
var eventKinds = []logview.Kind{
	logview.KindReport, logview.KindFrame, logview.KindCmd, logview.KindReply, logview.KindBanner,
	logview.KindNotice, logview.KindNote, logview.KindFragment, logview.KindPartial, logview.KindRedacted,
}

// runSet is the --run selection: numbers and ranges, kept as ranges so
// "1-1000000" costs nothing.
type runSet []struct{ lo, hi int }

func (rs runSet) has(n int) bool {
	for _, r := range rs {
		if r.lo <= n && n <= r.hi {
			return true
		}
	}
	return false
}

// parseRuns parses --run: numbers and ranges, comma-separated ("12",
// "3,5", "7-9", "1,4-6").
func parseRuns(s string) (runSet, error) {
	var out runSet
	for _, p := range splitList(s) {
		lo, hi, isRange := strings.Cut(p, "-")
		a, err := strconv.Atoi(lo)
		b := a
		if err == nil && isRange {
			b, err = strconv.Atoi(hi)
		}
		if err != nil || a < 1 || b < a {
			return nil, fmt.Errorf("bad run %q (want N, N,M or N-M with 1 <= N <= M)", p)
		}
		out = append(out, struct{ lo, hi int }{a, b})
	}
	return out, nil
}

// eventsOpts are the flags of events beyond the input.
type eventsOpts struct {
	run, runKind, event, fields string
	margin                      time.Duration
	skipStandby, hideMasked     bool
	changes, rawValues          bool
	showDumps                   bool
}

// events prints the selected events of the input.
func events(args []string, xmlPath string, stdout, stderr io.Writer) int {
	var in input
	var o eventsOpts
	fs := subFlags("events", stderr)
	in.register(fs)
	fs.StringVar(&o.run, "run", "", "only the windows of these runs, by number: 12, 3,5 or 7-9")
	fs.StringVar(&o.runKind, "run-kind", "", "only the windows of runs of these kinds: shot, flush, draw")
	fs.Var(systime.NewSpan(&o.margin), "margin", "widen each run window on both sides (systemd.time span, default 0; needs --run or --run-kind)")
	fs.StringVar(&o.event, "event", "", "only these event kinds, comma-separated: "+kindList())
	fs.BoolVar(&o.skipStandby, "skip-standby", false, "drop reports whose pump marker is ?")
	fs.StringVar(&o.fields, "fields", "", "only these columns, by key (see levalog fields), in this order")
	fs.BoolVar(&o.hideMasked, "hide-masked", false, "text: omit masked report fields")
	fs.BoolVar(&o.changes, "changes", false, "print a report only when a projected report field changed (each run window starts anew)")
	fs.BoolVar(&o.rawValues, "raw-values", false, "print the raw substrings instead of parsed values")
	fs.BoolVar(&o.showDumps, "show-dumps", false, "print MCu/MCm dump lines instead of redacting them")
	if code, done := parse(fs, args, stderr); done {
		return code
	}
	if code := in.check(fs, stderr); code != 0 {
		return code
	}

	var sel logview.Selection
	runSel, err := parseRuns(o.run)
	if err != nil {
		return fail(stderr, 2, "--run: %v", err)
	}
	code := 0
	if sel.RunKinds, code = parseRunKinds(o.runKind, "--run-kind", stderr); code != 0 {
		return code
	}
	for _, k := range splitList(o.event) {
		if !slices.Contains(eventKinds, logview.Kind(k)) {
			return fail(stderr, 2, "--event: unknown event kind %q (want %s)", k, kindList())
		}
		if in.f == logview.FormatCSV && logview.Kind(k) != logview.KindReport {
			return fail(stderr, 2, "--event: csv holds reports only, not %s", k)
		}
		sel.Events = append(sel.Events, logview.Kind(k))
	}
	keys := splitList(o.fields)
	if xmlPath == "" {
		for _, c := range []struct {
			set  bool
			flag string
		}{
			{o.fields != "", "--fields"},
			{o.run != "", "--run"},
			{o.runKind != "", "--run-kind"},
			{o.changes, "--changes"},
			{in.f == logview.FormatCSV, "--format csv"},
		} {
			if c.set {
				return needXML(stderr, c.flag)
			}
		}
	}
	sel.Since, sel.Until, sel.Margin = in.since.T, in.until.T, o.margin
	sel.SkipStandby, sel.Changes = o.skipStandby, o.changes
	byRun := len(runSel) > 0 || len(sel.RunKinds) > 0
	if o.margin != 0 && !byRun {
		return fail(stderr, 2, "--margin needs --run or --run-kind")
	}
	if o.hideMasked && in.f != logview.FormatText {
		return fail(stderr, 2, "--hide-masked applies to --format text only")
	}

	// A selected run is printed whole: with its margins, from files before
	// and after the window.
	since, until := in.since.T, in.until.T
	if byRun {
		if !since.IsZero() {
			since = since.Add(-o.margin)
		}
		if !until.IsZero() {
			until = until.Add(o.margin)
		}
	}
	paths, code := in.paths(since, until, stderr)
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
	if cat == nil {
		_, _ = fmt.Fprintln(stderr, "levalog: no vendor XML (--xml / MEDEWI_XML_PATH); reports shown raw")
	}
	keep := func(r logview.Run) bool {
		return runSel.has(r.N) || slices.Contains(sel.RunKinds, r.Kind)
	}
	// Text shows run and run_t only with a run selection; jsonl and csv
	// always carry them. Pass 1 finds the runs (and the specs --fields is
	// checked against); pass 2 decodes again and writes. Without either,
	// one pass streams.
	detect := cat != nil && (byRun || in.f != logview.FormatText)
	var sc scanned
	if detect || len(keys) > 0 {
		if sc, err = scan(paths, in.dir, detect, byRun && in.fromDir(), cat, o.showDumps, in.since.T, in.until.T, keep); err != nil {
			return fail(stderr, 1, "%v", err)
		}
		paths = sc.paths
	}
	for _, r := range sc.runs {
		if runSel.has(r.N) {
			sel.Runs = append(sel.Runs, r.N)
		}
	}
	if bad := unknownKeys(keys, sc.specs, cat); len(bad) > 0 {
		return fail(stderr, 2, "--fields: unknown keys %s (see levalog fields)", strings.Join(bad, ", "))
	}
	for _, k := range keys {
		if k == "pump" || !isMedewiKey(k) {
			sel.ChangeKeys = append(sel.ChangeKeys, k)
		}
	}

	out := bufio.NewWriter(stdout)
	w := logview.NewWriter(out, in.f, logview.WriterOptions{
		Fields: keys, RawValues: o.rawValues, HideMasked: o.hideMasked, RunCols: byRun, Loc: time.Local,
	})
	selector := logview.NewSelector(sc.runs, sel)
	problems := sc.problems
	if !byRun || len(sel.Runs) > 0 || len(sel.RunKinds) > 0 { // --run naming no run prints nothing
		var notes string
		s := logview.Stream{
			Catalog: cat, ShowDumps: o.showDumps,
			OnSource: func(si logview.SourceInfo) error {
				notes = si.Notes
				return nil
			},
			OnEntry: func(e *logview.Entry) error {
				r, ok := selector.Select(e)
				if !ok {
					return nil
				}
				if notes != "" && in.f == logview.FormatText {
					if _, err := fmt.Fprintf(out, "# notes: %s\n", notes); err != nil {
						return err
					}
					notes = ""
				}
				return w.Write(r)
			},
		}
		err = s.Add(paths...)
		problems = s.Problems
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if ferr := out.Flush(); err == nil {
		err = ferr
	}
	warn(stderr, problems)
	warn(stderr, sc.warns)
	warn(stderr, w.Warnings())
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}

func kindList() string {
	s := make([]string, len(eventKinds))
	for i, k := range eventKinds {
		s[i] = string(k)
	}
	return strings.Join(s, ", ")
}

func isMedewiKey(k string) bool {
	return slices.ContainsFunc(logview.MedewiColumns, func(c logview.Column) bool { return c.Key == k })
}

// unknownKeys returns the keys that are neither medewi's nor in the XML of
// any of the report specs of the input or the catalog's default spec.
func unknownKeys(keys []string, specs map[*leva.RecordSpec]bool, cat *logview.Catalog) []string {
	known := map[string]bool{}
	all := maps.Clone(specs)
	if all == nil {
		all = map[*leva.RecordSpec]bool{}
	}
	if spec, _ := cat.Lookup(""); spec != nil {
		all[spec] = true
	}
	for spec := range all {
		cols, _ := logview.Columns(spec) // a clash surfaces when writing
		for _, c := range cols {
			known[c.Key] = true
		}
	}
	var bad []string
	for _, k := range keys {
		if !known[k] && !isMedewiKey(k) {
			bad = append(bad, k)
		}
	}
	return bad
}
