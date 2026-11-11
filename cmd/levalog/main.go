// Command levalog reads medewid's raw traffic logs and levacap captures: it
// lists the report fields (fields), the pump runs and wand draws (runs) and
// the events (events), as text, JSON Lines or CSV. It only reads files; it
// never talks to the machine.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

const defaultRawDir = "/var/lib/medewid/raw"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `usage: levalog [--xml F] SUBCOMMAND [OPTIONS] [FILE|DIR ...]

  fields   list the report fields: key, name, unit, factor, offset, source
  runs     list pump runs (shot, flush) and wand draws, numbered
  events   print events, with selection, projection and formats

Input, for all three: FILE|DIR ... are raw logs (*.jsonl.gz) or captures
(log.jsonl, plain or gzip). A DIR is either a capture directory (containing
log.jsonl) or a raw-log directory. Without FILE or DIR, the raw logs in
--dir that overlap the window. -s/--since T (inclusive) and -u/--until T
(exclusive) take a systemd.time(7) timestamp in local time: "2026-11-10
10:40", "10:40", "-5min", "yesterday", with an optional " UTC"; they need
raw logs. --format is text (default), jsonl or csv. fields lists the XML in
effect at the end of the input (with --xml FILE and no readable input, that
file). runs numbers the runs that start in the window, from 1; events
--run N names the same run. Run detection also reads the raw log just before
and after the window, and a selected run is printed whole.

runs prints RUN KIND START T DURATION PEAK_BAR VOLUME_ML TRUNCATED XML SRC,
all of the run's first sample (START is empty for captures, PEAK_BAR for
draws). events --margin SPAN (default 0) widens each run window on both
sides and needs --run or --run-kind.

events text: T WALL DIR EVENT, then RUN RUN_T with --run/--run-kind, then
src=, src_end=, xml= when --fields names them, then report fields as
key=value (pump and mode first) or the line's text. Comment lines: "# PATH"
when the source changes, followed by "# notes: ..." for a capture with
manifest notes, and "# xml: NAME" when the XML changes. jsonl: one object
per event, always with src, event and glued (also with --fields); csv:
reports only, with a header row.

Columns medewi adds; everything after them is the machine's data (report
fields from the XML, frame fields, or the raw text of other lines):
  t        seconds since the start of the file (raw log) or session (capture)
  wall     wall clock, raw logs only: local time in text, UTC (RFC 3339 with
           milliseconds) in jsonl and csv
  src      PATH:N, the input file and the 1-based JSON line (decompressed) of
           the event that carried the line's first byte
  src_end  the same for the last byte; empty when equal to src
  dir      rx machine to medewi, tx medewi to machine, note medewi's annotation
  event    report, frame, cmd, reply, banner, notice, note, fragment, partial,
           redacted; a piece of a glued line is marked (text "+", jsonl glued)
  run      number of the run whose window (run +- --margin) holds the line
  run_t    seconds since that run's first sample, negative in the margin before
  pump     report byte 2: * leva! drives the pump, - not, ? standby
  xml      the session's vendor XML name
Report values are parsed (XML factor applied, --raw-values for the raw
text); masked is ? in text, null in jsonl, empty in csv.

--xml is a file, used for every source, or a directory of NAME.xml files,
one per XML name a session reports (MC? XML=NAME). It defaults to
$MEDEWID_XML_PATH, then $MEDEWI_XML_PATH. Without it events shows reports
raw; fields and runs need it. --dir defaults to $MEDEWID_RAW_DIR, then
`+defaultRawDir+`. Problems (a missing XML, unparseable values) go to stderr
as "levalog: ..." lines after the output and do not change the exit code.
Exit codes: 0 ok, 1 read error, 2 usage error.`)
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
	fs.SetInterspersed(false) // "levalog --xml F events ..." stops at the subcommand
	fs.Usage = func() { usage(stderr) }
	fs.StringVar(&xmlPath, "xml", xmlPath, "vendor Status-Monitor XML file or directory (env MEDEWID_XML_PATH, MEDEWI_XML_PATH)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return fail(stderr, 2, "%v", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		usage(stderr)
		return 2
	}
	cmds := map[string]func([]string, string, io.Writer, io.Writer) int{
		"fields": fields,
		"runs":   runsCmd,
		"events": events,
	}
	cmd, ok := cmds[rest[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "levalog: unknown subcommand %q\n", rest[0])
		usage(stderr)
		return 2
	}
	return cmd(rest[1:], xmlPath, stdout, stderr)
}

// fail prints "levalog: MESSAGE" to stderr and returns code.
func fail(stderr io.Writer, code int, format string, a ...any) int {
	_, _ = fmt.Fprintf(stderr, "levalog: "+format+"\n", a...)
	return code
}

// warn prints each message as a "levalog: " line.
func warn(stderr io.Writer, msgs []string) {
	for _, m := range msgs {
		_, _ = fmt.Fprintf(stderr, "levalog: %s\n", m)
	}
}

// subFlags returns the flag set of subcommand name, printing the usage and
// its own flags on --help.
func subFlags(name string, stderr io.Writer) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		usage(stderr)
		_, _ = fmt.Fprintf(stderr, "\n%s flags:\n", name)
		fs.PrintDefaults()
	}
	return fs
}

// parse parses args into fs; done is true when the caller returns code.
func parse(fs *pflag.FlagSet, args []string, stderr io.Writer) (code int, done bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0, true
		}
		return fail(stderr, 2, "%v", err), true
	}
	return 0, false
}

// splitList splits a comma-separated flag value, dropping empty items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// needXML is the usage error of a subcommand or flag that needs the XML.
func needXML(stderr io.Writer, what string) int {
	return fail(stderr, 2, "%s needs the vendor XML (--xml / MEDEWI_XML_PATH)", what)
}
