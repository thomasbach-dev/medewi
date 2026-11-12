package main

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
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
	var kind, stats, agg string
	fs := subFlags("runs", stderr)
	in.register(fs)
	fs.StringVar(&kind, "kind", "", "only runs of these kinds, comma-separated: shot, flush, draw (numbers stay)")
	fs.StringVar(&stats, "stats", "", "add aggregates of these report fields per run window, by key (see levalog fields)")
	fs.StringVar(&agg, "agg", "n,min,max,mean", "aggregates for --stats: n, first, last, min, max, mean, median, pNN, sd, tmin, tmax, srcmin, srcmax")
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
	statKeys := dedup(splitList(stats))
	if fs.Changed("agg") && len(statKeys) == 0 {
		return fail(stderr, 2, "--agg needs --stats")
	}
	aggs, err := logview.ParseAggs(agg)
	if err != nil {
		return fail(stderr, 2, "--agg: %v", err)
	}
	if xmlPath == "" {
		return needXML(stderr, "runs")
	}
	paths, code := in.paths(in.since.T, in.until.T, stderr)
	if code != 0 || len(paths) == 0 {
		return code
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
	opt := logview.RunsOptions{Loc: time.Local}
	var textNotes []string
	if len(statKeys) > 0 {
		if code := checkStats("--stats", statKeys, sc.specs, cat, stderr); code != 0 {
			return code
		}
		var keyAggs [][]logview.Agg
		keyAggs, textNotes = aggsPerKey(statKeys, aggs, sc.specs, cat)
		opt.Stats = logview.NewRunStats(runs, statKeys, keyAggs)
		s := logview.Stream{Catalog: cat, OnEntry: func(e *logview.Entry) error {
			opt.Stats.Entry(e)
			return nil
		}}
		if err := s.Add(sc.paths...); err != nil {
			return fail(stderr, 1, "%v", err)
		}
	}
	w := bufio.NewWriter(stdout)
	err = logview.WriteRuns(w, in.f, runs, opt)
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	warn(stderr, in.notes)
	warn(stderr, sc.problems)
	warn(stderr, sc.warns)
	warn(stderr, textNotes)
	if opt.Stats != nil {
		warn(stderr, opt.Stats.Missing())
	}
	if len(runs) == 0 {
		warn(stderr, []string{noRuns(sc, kinds)})
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}

// noRuns says why runs found none: no run at all in the reports, or none
// of the kinds asked for.
func noRuns(sc scanned, kinds []logview.RunKind) string {
	switch {
	case len(sc.runs) > 0:
		ks := make([]string, len(kinds))
		for i, k := range kinds {
			ks[i] = string(k)
		}
		return fmt.Sprintf("no runs of kind %s (of %d runs)", strings.Join(ks, ","), len(sc.runs))
	case sc.decoded == 0 && len(sc.noXML) > 0:
		names := make([]string, len(sc.noXML))
		for i, n := range sc.noXML {
			names[i] = n
			if n == "" {
				names[i] = "a session that names none"
			}
		}
		return "no runs: no report decoded (no XML for " + strings.Join(names, ", ") + ")"
	default:
		return fmt.Sprintf("no runs in %d reports (pump marker * in %d)", sc.decoded, sc.pumping)
	}
}

// checkStats validates the keys of flag (--stats, --fields): report fields
// of the input's XML, or pump.
func checkStats(flag string, keys []string, specs map[*leva.RecordSpec]bool, cat *logview.Catalog, stderr io.Writer) int {
	var bad []string
	for _, k := range keys {
		if k != "pump" && isMedewiKey(k) {
			bad = append(bad, k)
		}
	}
	bad = append(bad, unknownKeys(keys, specs, cat)...)
	if len(bad) > 0 {
		return fail(stderr, 2, "%s: unknown report fields %s (see levalog fields)", flag, strings.Join(bad, ", "))
	}
	return 0
}

// aggsPerKey returns the aggregates of each key: aggs, except that a text
// field gets only last when aggs holds a numeric aggregate. notes names
// each such field, for after the output.
func aggsPerKey(keys []string, aggs []logview.Agg, specs map[*leva.RecordSpec]bool, cat *logview.Catalog) (perKey [][]logview.Agg, notes []string) {
	numeric := slices.ContainsFunc(aggs, logview.Agg.Numeric)
	for _, k := range keys {
		if numeric && isTextKey(k, specs, cat) {
			perKey = append(perKey, []logview.Agg{{Name: "last"}})
			notes = append(notes, fmt.Sprintf("%s is text: only %s_last", k, k))
			continue
		}
		perKey = append(perKey, aggs)
	}
	return perKey, notes
}

// isTextKey reports whether key is a text field in any of specs or the
// catalog's default spec.
func isTextKey(key string, specs map[*leva.RecordSpec]bool, cat *logview.Catalog) bool {
	if key == "pump" {
		return true
	}
	for spec := range specs {
		if logview.IsTextKey(spec, key) {
			return true
		}
	}
	spec, _ := cat.Lookup("")
	return spec != nil && logview.IsTextKey(spec, key)
}

// dedup drops repeated keys, keeping the first.
func dedup(keys []string) []string {
	var out []string
	for _, k := range keys {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}
