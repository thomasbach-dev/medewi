// Command levalog decodes medewid's raw traffic logs and levacap captures
// into one text line per event. It only reads files; it never talks to the
// machine.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/pflag"

	"github.com/thomasbach-dev/medewi/logview"
	"github.com/thomasbach-dev/medewi/rawlog"
	"github.com/thomasbach-dev/medewi/systime"
)

const defaultRawDir = "/var/lib/medewid/raw"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `usage: levalog [--xml F] <subcommand>

  decode [--dir D] [-s|--since T] [-u|--until T] [--show-dumps] [FILE|DIR ...]
      print one line per event of raw logs (*.jsonl.gz) or captures
      (log.jsonl, plain or gzip). A DIR is either a capture directory
      (containing log.jsonl) or a raw-log directory. Without FILE or DIR,
      the raw logs in --dir that overlap the window. T is a systemd.time(7)
      timestamp in local time: "2026-11-10 10:40", "10:40", "-5min",
      "yesterday", with an optional " UTC". --since/--until need raw logs.

--xml defaults to $MEDEWID_XML_PATH, then $MEDEWI_XML_PATH; without it
reports are shown raw. --dir defaults to $MEDEWID_RAW_DIR, then
`+defaultRawDir+`. Exit codes: 0 ok, 1 read error, 2 usage error.`)
}

// envOr returns the first non-empty environment variable of keys, else def.
func envOr(def string, keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return def
}

func run(args []string, stdout, stderr io.Writer) int {
	xmlPath := envOr("", "MEDEWID_XML_PATH", "MEDEWI_XML_PATH")
	fs := pflag.NewFlagSet("levalog", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.SetInterspersed(false) // "levalog --xml F decode ..." stops at the subcommand
	fs.Usage = func() { usage(stderr) }
	fs.StringVar(&xmlPath, "xml", xmlPath, "vendor Status-Monitor XML (env MEDEWID_XML_PATH, MEDEWI_XML_PATH)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "levalog: %v\n", err)
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		usage(stderr)
		return 2
	}
	if rest[0] != "decode" {
		_, _ = fmt.Fprintf(stderr, "levalog: unknown subcommand %q\n", rest[0])
		usage(stderr)
		return 2
	}
	return decode(rest[1:], xmlPath, stdout, stderr)
}

func decode(args []string, xmlPath string, stdout, stderr io.Writer) int {
	dir := envOr(defaultRawDir, "MEDEWID_RAW_DIR")
	var since, until systime.Timestamp
	var showDumps bool
	fs := pflag.NewFlagSet("decode", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		usage(stderr)
		_, _ = fmt.Fprintln(stderr, "\ndecode flags:")
		fs.PrintDefaults()
	}
	fs.StringVar(&dir, "dir", dir, "raw-log directory used without FILE (env MEDEWID_RAW_DIR)")
	fs.VarP(&since, "since", "s", "show events at or after this time")
	fs.VarP(&until, "until", "u", "show events before this time")
	fs.BoolVar(&showDumps, "show-dumps", false, "print MCu/MCm dump lines instead of redacting them")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "levalog: %v\n", err)
		return 2
	}
	window := !since.T.IsZero() || !until.T.IsZero()

	paths, err := sourcePaths(fs.Args(), dir, since.T, until.T)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "levalog: %v\n", err)
		return 1
	}
	for _, p := range paths {
		if window && !p.raw {
			_, _ = fmt.Fprintf(stderr, "levalog: --since/--until need raw logs with wall time; %s is a capture\n", p.path)
			return 2
		}
	}
	if len(paths) == 0 {
		_, _ = fmt.Fprintf(stderr, "levalog: no raw logs for this window in %s\n", dir)
		return 0
	}

	var cat *logview.Catalog
	if xmlPath == "" {
		_, _ = fmt.Fprintln(stderr, "levalog: no vendor XML (--xml / MEDEWI_XML_PATH); reports shown raw")
	} else if cat, err = logview.LoadCatalog(xmlPath); err != nil {
		_, _ = fmt.Fprintf(stderr, "levalog: --xml: %v\n", err)
		return 1
	}

	opt := logview.Options{Catalog: cat, ShowDumps: showDumps}
	w := bufio.NewWriter(stdout)
	for _, p := range paths {
		src, err := logview.Open(p.path)
		if err != nil {
			_ = w.Flush()
			_, _ = fmt.Fprintf(stderr, "levalog: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(w, "# %s\n", src.Path)
		if src.Notes != "" {
			_, _ = fmt.Fprintf(w, "# notes: %s\n", src.Notes)
		}
		dec := logview.Decode(src, opt)
		opt.XML = dec.XML // a session continues in the next file
		for _, prob := range dec.Problems {
			_, _ = fmt.Fprintf(stderr, "levalog: %s\n", prob)
		}
		for _, e := range dec.Entries {
			if err := logview.Format(w, e, time.Local); err != nil {
				_ = w.Flush()
				_, _ = fmt.Fprintf(stderr, "levalog: %v\n", err)
				return 1
			}
		}
	}
	if err := w.Flush(); err != nil {
		_, _ = fmt.Fprintf(stderr, "levalog: %v\n", err)
		return 1
	}
	return 0
}

// source is one file to decode: a raw log (start from its name) or a
// capture log.jsonl.
type source struct {
	path  string
	raw   bool
	start time.Time
}

// sourcePaths resolves the FILE|DIR arguments, or the raw logs in dir that
// overlap the window when there are none, and sorts them by start time
// (captures, which have none, first and in argument order). A directory
// argument holding log.jsonl is a capture; any other is a raw-log
// directory.
func sourcePaths(args []string, dir string, since, until time.Time) ([]source, error) {
	var paths []string
	if len(args) == 0 {
		args = []string{dir}
	}
	for _, a := range args {
		fi, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			paths = append(paths, a)
			continue
		}
		capture := filepath.Join(a, "log.jsonl")
		if _, err := os.Stat(capture); err == nil {
			paths = append(paths, capture)
			continue
		}
		sel, err := logview.Select(a, since, until)
		if err != nil {
			return nil, err
		}
		paths = append(paths, sel...)
	}
	out := make([]source, 0, len(paths))
	for _, p := range paths {
		start, ok := rawlog.FileStart(filepath.Base(p))
		out = append(out, source{path: p, raw: ok, start: start})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out, nil
}
