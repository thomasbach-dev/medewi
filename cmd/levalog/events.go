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
var eventKinds = logview.EventKinds

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
	agg                         string
	margin, every               time.Duration
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
	fs.Var(systime.NewSpan(&o.every), "every", "one row per bucket of this systemd.time span, aggregating the non-standby reports")
	fs.StringVar(&o.agg, "agg", "last", "aggregates per bucket for --every (columns key_agg; default plain keys, last value): "+
		"n, first, last, min, max, mean, median, pNN, sd, tmin, tmax, srcmin, srcmax")
	if code, done := parse(fs, args, stderr); done {
		return code
	}
	if code := in.check(fs, stderr); code != 0 {
		return code
	}

	every := fs.Changed("every")
	aggGiven := fs.Changed("agg")
	aggs, code := checkEvery(&o, every, aggGiven, stderr)
	if code != 0 {
		return code
	}

	var sel logview.Selection
	runSel, err := parseRuns(o.run)
	if err != nil {
		return fail(stderr, 2, "--run: %v", err)
	}
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
	keys := dedup(splitList(o.fields))
	if xmlPath == "" {
		for _, c := range []struct {
			set  bool
			flag string
		}{
			{o.fields != "", "--fields"},
			{o.run != "", "--run"},
			{o.runKind != "", "--run-kind"},
			{o.changes, "--changes"},
			{every, "--every"},
			{in.f == logview.FormatCSV, "--format csv"},
		} {
			if c.set {
				return needXML(stderr, c.flag)
			}
		}
	}
	sel.Since, sel.Until, sel.Margin = in.since.T, in.until.T, o.margin
	sel.SkipStandby, sel.Changes = o.skipStandby || every, o.changes
	if every {
		sel.Events = []logview.Kind{logview.KindReport}
	}
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
	if code != 0 || len(paths) == 0 {
		return code
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
	if detect || len(keys) > 0 || every {
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
	noRun := missingRuns(runSel, sel.Runs, sc.runs)
	if bad := unknownKeys(keys, sc.specs, cat); len(bad) > 0 {
		return fail(stderr, 2, "--fields: unknown keys %s (see levalog fields)", strings.Join(bad, ", "))
	}
	for _, k := range keys {
		if k == "pump" || !isMedewiKey(k) {
			sel.ChangeKeys = append(sel.ChangeKeys, k)
		}
	}
	if every {
		cols, code := bucketCols(keys, aggs, aggGiven, sc, cat, stderr)
		if code != 0 {
			return code
		}
		return eventsEvery(paths, cat, sc, sel, o, cols, byRun, in.f, in.notes, noRun, stdout, stderr)
	}

	out := bufio.NewWriter(stdout)
	w := logview.NewWriter(out, in.f, logview.WriterOptions{
		Fields: keys, RawValues: o.rawValues, HideMasked: o.hideMasked, RunCols: byRun, Loc: time.Local,
	})
	selector := logview.NewSelector(sc.runs, sel)
	problems := sc.problems
	rows := 0
	if !byRun || len(sel.Runs) > 0 || len(sel.RunKinds) > 0 { // --run naming no run prints nothing
		s := logview.Stream{
			Catalog: cat, ShowDumps: o.showDumps,
			OnSource: func(si logview.SourceInfo) error {
				w.Source(si)
				return nil
			},
			OnEntry: func(e *logview.Entry) error {
				if r, ok := selector.Select(e); ok {
					rows++
					return w.Write(r)
				}
				return nil
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
	warn(stderr, in.notes)
	warn(stderr, problems)
	warn(stderr, sc.warns)
	warn(stderr, w.Warnings())
	warn(stderr, noRun)
	if rows == 0 && len(noRun) == 0 && err == nil {
		warn(stderr, []string{dropReason(selector, o, false)})
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}

// missingRuns says, for each --run item that names no run of the input,
// that there is no such run, with the runs there are.
func missingRuns(runSel runSet, selected []int, runs []logview.Run) []string {
	var out []string
	for _, rr := range runSel {
		if slices.ContainsFunc(selected, func(n int) bool { return rr.lo <= n && n <= rr.hi }) {
			continue
		}
		name := strconv.Itoa(rr.lo)
		if rr.hi != rr.lo {
			name += "-" + strconv.Itoa(rr.hi)
		}
		have := "no runs"
		switch {
		case len(runs) == 1:
			have = "run " + strconv.Itoa(runs[0].N)
		case len(runs) > 1:
			have = fmt.Sprintf("runs %d-%d", runs[0].N, runs[len(runs)-1].N)
		}
		out = append(out, fmt.Sprintf("--run %s: no such run (%s)", name, have))
	}
	return out
}

// dropReason says why events printed no row: the events read, what each
// filter dropped and what was left. With every, the filters --every
// implies are named after it.
func dropReason(s *logview.Selector, o eventsOpts, every bool) string {
	parts := []string{fmt.Sprintf("%d events read", s.Seen())}
	left := s.Seen()
	for _, d := range s.Drops() {
		name := d.Filter
		switch {
		case every && name == "--event report" && o.event == "":
			name = "--every (reports only)"
		case every && name == "--skip-standby" && !o.skipStandby:
			name = "--every (no standby)"
		}
		parts = append(parts, fmt.Sprintf("%s: %d dropped", name, d.N))
		left -= d.N
	}
	return strings.Join(append(parts, fmt.Sprintf("%d left", left)), "; ")
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

// checkEvery validates --every, --agg and the flags --every excludes; it
// returns the aggregates of --agg.
func checkEvery(o *eventsOpts, every, aggGiven bool, stderr io.Writer) ([]logview.Agg, int) {
	if !every {
		if aggGiven {
			return nil, fail(stderr, 2, "--agg needs --every")
		}
		return nil, 0
	}
	if o.every <= 0 {
		return nil, fail(stderr, 2, "--every: want a span above 0, not %q", systime.FormatSpan(o.every))
	}
	for _, k := range splitList(o.event) {
		if logview.Kind(k) != logview.KindReport {
			return nil, fail(stderr, 2, "--event %s: --every aggregates reports only", k)
		}
	}
	for _, c := range []struct {
		set  bool
		flag string
	}{{o.changes, "--changes"}, {o.hideMasked, "--hide-masked"}, {o.rawValues, "--raw-values"}} {
		if c.set {
			return nil, fail(stderr, 2, "%s does not apply to --every", c.flag)
		}
	}
	aggs, err := logview.ParseAggs(o.agg)
	if err != nil {
		return nil, fail(stderr, 2, "--agg: %v", err)
	}
	return aggs, 0
}

// bucketCols are the columns of events --every: each key with each
// aggregate (plain keys with the default last). Without keys they are
// pump and the XML keys of the first report; a text field then gets
// only last.
func bucketCols(keys []string, aggs []logview.Agg, aggGiven bool, sc scanned, cat *logview.Catalog, stderr io.Writer) ([]logview.BucketCol, int) {
	implied := len(keys) == 0
	if implied {
		keys = []string{"pump"}
		if sc.first != nil {
			var err error
			if keys, err = logview.ReportKeys(sc.first); err != nil {
				return nil, fail(stderr, 1, "%v", err)
			}
		}
	} else if code := checkStats("--fields", keys, aggs, sc.specs, cat, stderr); code != 0 {
		return nil, code
	}
	last := logview.Agg{Name: "last"}
	var cols []logview.BucketCol
	for _, k := range keys {
		switch {
		case !aggGiven:
			cols = append(cols, logview.BucketCol{Key: k, Agg: last, Plain: true})
		case implied && isTextKey(k, sc.specs, cat):
			cols = append(cols, logview.BucketCol{Key: k, Agg: last})
		default:
			for _, a := range aggs {
				cols = append(cols, logview.BucketCol{Key: k, Agg: a})
			}
		}
	}
	return cols, 0
}

// eventsEvery streams the input into buckets and writes them.
func eventsEvery(paths []string, cat *logview.Catalog, sc scanned, sel logview.Selection, o eventsOpts,
	cols []logview.BucketCol, byRun bool, f logview.Format, notes, noRun []string, stdout, stderr io.Writer,
) int {
	out := bufio.NewWriter(stdout)
	w := logview.NewBucketWriter(out, f, cols, byRun, time.Local)
	b := logview.NewBucketer(o.every, cols, byRun)
	n := 0
	write := func(rows []logview.BucketRow) error {
		for _, r := range rows {
			if err := w.Write(r); err != nil {
				return err
			}
			n++
		}
		return nil
	}
	selector := logview.NewSelector(sc.runs, sel)
	problems := sc.problems
	var err error
	if !byRun || len(sel.Runs) > 0 || len(sel.RunKinds) > 0 { // --run naming no run prints nothing
		s := logview.Stream{
			Catalog: cat,
			OnSource: func(si logview.SourceInfo) error {
				return write(b.Source(si))
			},
			OnEntry: func(e *logview.Entry) error {
				for _, r := range selector.SelectAll(e) {
					if err := write(b.Add(r)); err != nil {
						return err
					}
				}
				return nil
			},
		}
		err = s.Add(paths...)
		problems = s.Problems
	}
	if err == nil {
		err = write(b.Close())
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if ferr := out.Flush(); err == nil {
		err = ferr
	}
	warn(stderr, notes)
	warn(stderr, problems)
	warn(stderr, sc.warns)
	warn(stderr, b.Missing())
	warn(stderr, noRun)
	if n == 0 && len(noRun) == 0 && err == nil {
		warn(stderr, []string{dropReason(selector, o, true)})
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}
