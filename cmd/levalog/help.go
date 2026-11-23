package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/pflag"
)

// helpHead opens levalog --help: the synopsis and the subcommands.
const helpHead = `usage: levalog [--xml F] SUBCOMMAND [OPTIONS] [FILE|DIR ...]

  fields   list the report fields: key, name, unit, factor, offset, source
  runs     list pump runs (shot, flush) and wand draws, numbered
  programs list program runs (warm-up, backflush, descale, ...), numbered
  events   print events, with selection, projection and formats
  summary  what each source holds: sessions, firmware, XML, events, modes, READY, runs, masked fields, gaps
  extract  cut a window of the raw logs out as a capture (log.jsonl + manifest.json)

levalog SUBCOMMAND --help prints that subcommand's part of this text and its flags.`

// helpShared is what every subcommand shares: the input, the time forms,
// the formats, --xml and --dir, problems and exit codes.
const helpShared = `Input, for all subcommands: FILE|DIR ... are raw logs (*.jsonl.gz) or captures
(log.jsonl, plain or gzip). A DIR is a capture directory (containing
log.jsonl), a raw-log directory, or else the parent of capture directories:
its subdirectories holding log.jsonl, in name order (others are skipped
with a note). A FILE - reads a capture log.jsonl from stdin, once (events,
runs, summary, fields; not extract): no manifest, no wall time, and its
source prints as -. Captures (and -) come before raw logs, in argument
order among themselves; raw logs in start order. Without FILE or DIR, the
raw logs in --dir that overlap the window. -s/--since T (inclusive) and
-u/--until T (exclusive) take a systemd.time(7) timestamp in local time:
"2026-11-10 10:40", "10:40", "-5min", "yesterday", with an optional " UTC";
they need raw logs. --format is text (default), jsonl or csv.

--xml is a file, used for every source, or a directory of NAME.xml files,
one per XML name a session reports (MC? XML=NAME). It defaults to
$MEDEWID_XML_PATH, then $MEDEWI_XML_PATH. Without it events shows reports
raw; fields, runs and programs need it. --dir defaults to $MEDEWID_RAW_DIR, then
` + defaultRawDir + `. Problems (a missing XML, unparseable values) go to stderr
as "levalog: ..." lines after the output and do not change the exit code;
so does the reason for an empty result (what each filter dropped, a --run
that names no run).
Exit codes: 0 ok, 1 read error, 2 usage error.`

// helpXMLPointer names --xml in each pointer line.
const helpXMLPointer = "--xml F before the subcommand (env MEDEWID_XML_PATH, then MEDEWI_XML_PATH): levalog --help"

// subHelp is one subcommand's part of the help.
type subHelp struct {
	name     string
	text     string   // synopsis and paragraphs
	examples []string // "  levalog ...  # what" lines
	rawOnly  bool     // reads raw logs only: no captures, no -, no --format
}

// pointer is the line ending h's SUBCOMMAND --help: the shared rules it
// uses, found in levalog --help.
func (h subHelp) pointer() string {
	if h.rawOnly {
		return "Input FILE|DIR (raw logs or a raw-log directory), the -s/-u time forms, " + helpXMLPointer
	}
	return "Input FILE|DIR (raw logs, captures, a raw-log or capture directory, - for stdin), the -s/-u time forms, --format, " +
		helpXMLPointer
}

var subHelps = []subHelp{
	{
		name: "fields",
		text: `usage: levalog fields [--format F] [FILE|DIR ...]

fields lists the report fields of the XML in effect at the end of the
input (with --xml FILE and no readable input, that file): key, name, unit,
factor, offset, source. Its keys are what events --fields, runs --stats
and the report columns use. It needs the XML.`,
		examples: []string{
			"  levalog fields captures/warmup-ready                           # the keys --fields takes",
		},
	},
	{
		name: "runs",
		text: `usage: levalog runs [--kind K] [--stats KEYS [--agg LIST]] [--format F] [FILE|DIR ...]

runs numbers the runs that start in the window, from 1, counting over the
whole input given; events --run N with the same input names the same run,
so to address a run of one capture, pass that capture. Run detection also
reads the raw log just before and after the window, and a selected run is
printed whole. It needs the XML.

runs prints RUN KIND START T DURATION PEAK_BAR VOLUME_ML TRUNCATED XML SRC,
all of the run's first sample (PEAK_BAR is empty for draws). START is its
wall time; for a capture, the manifest's recorded time (taken when levacap
wrote it, just after its last event) less the time from the run to the
capture's last event; empty for a capture without one. runs --stats KEYS
[--agg LIST] appends key_agg columns aggregated over the non-standby
reports of each run window (first to last sample). --agg takes n, first,
last, min, max, mean, median, pNN, sd, tmin, tmax, srcmin, srcmax (default
n,min,max,mean for runs); tmin/tmax are the run_t of the minimum/maximum,
srcmin/srcmax its src, each of the first occurrence on ties, and n counts
the unmasked values. A text field (pump, mode, ...) named with a numeric
aggregate gets only key_last and a note, here and in events --every.`,
		examples: []string{
			"  levalog runs --kind shot --stats pressure --agg max,tmax,srcmax  # peak, when, where",
		},
	},
	{
		name: "programs",
		text: `usage: levalog programs [--format F] [FILE|DIR ...]

programs numbers the program runs that start in the window, from 1, over
the whole input given: warm-up, autotune, pressure-test, flow-test,
backflush, descale, purge. Detection uses the pump marker, phase, pressure,
setpoint, mode and flow-meter ticks of the reports; the LCD frames, where
the log has them, are a cross-check. It also reads the raw log just before
and after the window, and a program is printed whole. It needs the XML.

programs prints PROGRAM KIND START T DURATION OUTCOME STATUS EVIDENCE
PULSES PUMP_S VOLUME_ML PEAK_BAR SETTINGS_MATCH NOTES XML SRC (START, T, XML
and SRC as in runs). How far to trust a row:
  STATUS   confirmed  the rules match and the LCD agrees
           inferred   the rules match; nothing confirms it
           conflict   the LCD contradicts the rules (see NOTES)
           lcd        only the LCD names the program
  OUTCOME  finished   the log shows the program's own end
           aborted    the log shows it stopped early
           cut        its start or end is not in the log: the input ends,
                      the connection dropped, or
                      a program that began before the input
           unknown    no evidence; most aborts look like ends
EVIDENCE is report, lcd or both. VOLUME_ML is empty when a pulse's volume
is unknown. jsonl adds pulses_detail (t from the program's start), figures
(numbers for wear tracking, by kind) and rule_version, csv rule_version.
A flush, a shot and the valve test are no programs; runs still lists each
pulse of a program as a flush.`,
		examples: []string{
			"  levalog programs -s '2w ago' --format jsonl | jq .figures      # numbers for wear tracking",
		},
	},
	{
		name: "events",
		text: `usage: levalog events [--run N|--run-kind K|--program N] [--margin SPAN] [--event KINDS] [--match TEXT]
       [--fields KEYS] [--every SPAN [--agg LIST]] [--format F] [FILE|DIR ...]

events --run N names a run as runs numbers it, over the same input.
events --program N names a program as programs numbers it; run and run_t are
then the program's number and the time since its first sample.
events --match TEXT keeps only events whose text contains TEXT, ignoring case
(note and notice text, banner and reply lines, a report's raw line, a frame's
rows joined with |); it does not combine with --every. medewictl mark TEXT
writes a note "mark: TEXT"; find marks with
levalog events --event note --match TEXT
events --margin SPAN (default 0) widens each run window on both sides; without
--run/--run-kind it widens the -s/-u window instead (an open side stays open),
and it needs one of them. With --run and -s/-u, -s/-u only pick the input and
the margin goes to the run windows.

events --every SPAN [--agg LIST] prints one row per bucket of SPAN instead:
t (bucket start), wall, src (jsonl and csv: the source path), run and run_t
with a run selection, reports (the non-standby reports in it), then each
field's last value under its plain key, or key_agg columns with --agg. Text
starts a table, header repeated, after each "# PATH" and "# xml: NAME" line.
With a run selection a row's source is that of the run's first sample.
Buckets start at run_t 0 per run with a run selection, else per source: for
captures from t 0; for raw logs at multiples of SPAN since the Unix epoch,
so on UTC: --every 1d starts at 00:00 UTC, and a file's first bucket can
start before the file, at a negative t. Empty buckets are left out. Without
--fields: pump and every report field (with --agg, text fields only as
key_last).

events text: T WALL DIR EVENT, then RUN RUN_T with --run/--run-kind, then
src=, src_end=, xml= when --fields names them, then report fields as
key=value (pump and mode first) or the line's text. Comment lines: "# PATH"
when the source changes, followed by "# notes: ..." for a capture with
manifest notes (one comment line per line of notes), and "# xml: NAME"
when the XML changes. --fields selects the data columns; csv and jsonl
always start with t wall src run run_t
(run and run_t empty or null outside a run window), naming them in
--fields does not repeat them. jsonl: one object per event, always with
event and glued too; csv: reports only, with a header row.

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
text); masked is ? in text, null in jsonl, empty in csv.`,
		examples: []string{
			"  levalog events --event notice captures/warmup-ready            # find [READY]",
			"  levalog events --run 2 --every 1s --fields pressure,pressure_sp  # a shot per second",
			"  levalog events --run 2 --format csv --fields pressure | awk -F, 'NR>1 && $6>=8.5{print $5; exit}'  # time to 8.5 bar",
		},
	},
	{
		name: "summary",
		text: `usage: levalog summary [--format F] [FILE|DIR ...]

summary prints what each source holds: firmware (in this file), XML (in
this file, or in effect when carried over), events, modes, READY, runs,
masked fields and gaps. On raw logs (no capture in the input) it first
prints a sessions: block: one line per raw-log session overlapping the
window, with its start (local, with zone;
"starts before" when its first file is not in the input), its first file,
firmware, XML and the part of the window it covers. jsonl carries the
same as a "sessions" array on every record (start and first_file null
when the first file is not in the input; times UTC); csv has no sessions.
summary reads only the files of the window; extract reads back to the
session's first file, so it can know a firmware that summary shows as
unknown.`,
		examples: []string{
			"  levalog summary captures/                                      # each capture",
		},
	},
	{
		name:    "extract",
		rawOnly: true,
		text: `usage: levalog extract (-s T [-u T] | -u T | --run N) [--margin SPAN] --out DIR|-
       [--scenario NAME] [--notes TEXT] [--split-sessions] [FILE|DIR ...]

extract reads raw logs only and keeps every event in the window verbatim
(the first and last rx bytes cut to whole lines, t rebased to 0, the
session's xml note first); one bound is enough, the other end stays open.
With --run N the window is that run's, as events --run N --margin SPAN
prints it; --margin widens the -s/-u window, or the run window, on both
sides. The window must lie in one raw-log session: at a reconnect or
restart it exits 1 naming every boundary and the -s/-u of each part
(tagged "# no events" or "# overlapping sessions"), or with
--split-sessions writes one capture per part into DIR/01, DIR/02, ... (a
part without events is skipped; at most 99 parts; notes name the
directory and the window part, as in "01 (window part 2): ..."). For the
firmware banner it reads back to the session's first file in the
directory, and on past the window while the session has shown none (a
stream was on since connect). --out DIR must not exist or be empty; the
manifest gets machine_addr machine.example:23, the XML's sha256 when --xml
names it, and the provenance in its notes. Each capture written prints as
its log.jsonl with its event counts (reports, frames, notes, commands,
other rx; they sum to the total) and its manifest.json. --out - prints the
lines log.jsonl would hold, and no manifest: JSON objects whose data is
base64 (the fixture format); decode with jq -r '.data | @base64d', or read
them back from stdin: extract ... --out - | levalog events --event frame -`,
		examples: []string{
			"  levalog extract -s 09:00 -u 09:05 --margin 30s --out backflush --scenario backflush  # a fixture, 30 s wider",
			"  levalog extract -s today --run 3 --margin 10s --out - | levalog events --event frame -  # its LCD frames",
		},
	},
}

// usage prints the whole help: the synopsis, the shared rules, every
// subcommand's part and every example.
func usage(w io.Writer) {
	var b strings.Builder
	b.WriteString(helpHead + "\n\n" + helpShared + "\n")
	var ex []string
	for _, h := range subHelps {
		b.WriteString("\n" + h.text + "\n")
		ex = append(ex, h.examples...)
	}
	b.WriteString("\nExamples:\n" + strings.Join(ex, "\n") + "\n")
	_, _ = io.WriteString(w, b.String())
}

// subUsage prints the help of subcommand name: its part, its examples,
// its flags and a pointer to levalog --help.
func subUsage(w io.Writer, name string, fs *pflag.FlagSet) {
	for _, h := range subHelps {
		if h.name != name {
			continue
		}
		_, _ = fmt.Fprintf(w, "%s\n\nExamples:\n%s\n\n%s flags:\n", h.text, strings.Join(h.examples, "\n"), name)
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(w, "\n%s\n", h.pointer())
		return
	}
}
