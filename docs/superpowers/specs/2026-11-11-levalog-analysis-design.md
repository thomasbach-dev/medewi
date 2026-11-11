# levalog for analysis: fields, runs, filters, formats — design

Builds on [2026-11-10-log-decode-cli-conventions-design.md](2026-11-10-log-decode-cli-conventions-design.md)
(`levalog decode`, `logview/`) and follows
[cli-conventions.md](../../cli-conventions.md).

## Intent

`levalog` becomes the way to get data out of raw logs and captures for
research into how the machine works. Its main users are **agents**; the
barista uses it occasionally to look at the data. The questions ahead:

- Can a heuristic tell pre-infusion and shot apart?
- How does the firmware decide READY?
- How can the warm-up program be improved?
- Can heater control give a steadier temperature during brews?
- Is the pump starting to wear out (compare many shots over weeks)?
- How does the machine behave during specific programs in captures?

Today `levalog decode` prints every report with all 52 fields, raw
substrings, half of them `?`-masked on this machine. An agent analysing a
shot ends up slicing byte offsets in Python; the barista skimming the log
drowns in `??????`. The pump marker (report byte 2), the most useful signal
so far, is not shown at all.

**Agent intuitive** means: an agent that has never seen the tool learns it
from `--help` and `levalog fields` alone, gets parseable output with stable
names and real numbers, narrows to what it needs with flags, and can jump
from any output line back to the raw bytes.

## Goals

1. `--help` explains the output: every column, which ones medewi adds and
   where the machine's raw data starts.
2. `levalog fields` lists every report field with a stable key.
3. `levalog runs` lists the pump runs (shots, flushes) and wand draws in
   the input, numbered.
4. `levalog decode` selects by run, event kind and standby; projects
   fields; hides masked values; prints only changes; writes text, JSON
   Lines or CSV.
5. Every output line can carry its source: file and JSON line number.

## Non-goals

- **No database.** `levalog` reads raw logs and captures only, never
  medewid's brew DB. Run numbers are `levalog`'s own count.
- **No analysis.** No statistics, no READY detection, no pre-infusion
  boundary: those are the research questions, answered by agents on top
  of this output.
- **No program detection** (warm-up, etc.; issue #13). Until it exists, a
  program is selected by time window.
- Remote reading (#30) stays out; the logic stays in `logview` so it can
  reuse it.

## Anti-goals

- Output whose columns shift between rows or between runs of the tool (an
  agent's parser breaks silently).
- Hiding where a value came from: every derived value must be traceable to
  the raw line.
- Committing vendor content: field keys are derived from the XML at
  runtime, never written into code or docs beyond what is already there.

## CLI

```
levalog [--xml F] SUBCOMMAND [OPTIONS] [FILE|DIR ...]

  fields    list the report fields: key, name, unit, factor, offset, source
  runs      list pump runs and wand draws in the input, numbered
  decode    print events, with selection, projection and formats
```

`runs` and `decode` share the input options of today's `decode`:
`FILE|DIR ...`, `--dir D`, `-s/--since T`, `-u/--until T`. With the same
input options they see the same runs with the same numbers. All three take
`--format text|jsonl|csv` (default `text`).

Without the vendor XML, `fields` and `runs` fail with exit 2 (they need
field names and run detection needs `Pressure SP` and `Volume`); `decode`
keeps printing reports raw, as today, and rejects `--fields`, `--run`,
`--run-kind` and parsed-value output with exit 2.

### Columns medewi adds

| Key | Meaning |
| --- | --- |
| `t` | seconds since the start of the file (raw log) or session (capture) |
| `wall` | wall-clock time; raw logs only (file-name start + `t`), empty for captures |
| `src` | `PATH:N`: the input file and the 1-based JSON line (in the decompressed file) of the event that carried the line's first byte |
| `src_end` | the same for the last byte; empty when equal to `src` |
| `dir` | `rx` machine → medewi, `tx` medewi → machine, `note` medewi's annotation |
| `event` | the decoder's classification: `report`, `frame`, `cmd`, `reply`, `banner`, `notice`, `note`, `fragment`, `partial`, `redacted`; a glued piece is marked (today's `+`) |
| `run` | number of the run whose window (run ± `--margin`) holds the line (see `runs`); empty otherwise |
| `run_t` | seconds since that run's first sample: negative in the margin before it, beyond its duration in the margin after |
| `pump` | report byte 2: `*` leva! drives the pump, `-` not, `?` standby |

Everything after these is the machine's data: report fields from the XML
(and `mode`, below), frame fields, or the raw text of other lines. The
`--help` of `decode` contains this table in short form.

`src` matters because a printed line is not one raw-log line: rx bytes
arrive in chunks, one chunk per JSON line, so a report is often assembled
from two or three events, and a glued line splits into two entries that
share an event.

### `levalog fields`

```
KEY                 NAME                  UNIT  FACTOR  AT   LEN  SOURCE
t                   (offset)              s     -       -    -    medewi
...
pump                (pump marker)         -     -       2    1    medewi
mode                -                     -     -       4    3    xml
temperature_pid1    Temperature PID1      °C    -       7    6    xml
flow_rate_ml_s      Flow rate             ml/s  0.0166  160  3    xml
flow_rate_g_s       Flow rate             g/s   -       231  5    xml
```

Keys: the XML export name lowercased, every run of non-alphanumerics
replaced by `_`, trimmed of `_`. A name that occurs more than once gets
its unit appended the same way (`flow_rate_ml_s`). The unnamed field at
offset 4 is `mode` (its meaning is observed: `PID`, `ECO`, …); other
unnamed fields are `field_N` with N the XML value index. medewi's column
keys are reserved; an XML key colliding with one is a load error that
names both.

### `levalog runs`

```
levalog runs [-s T] [-u T] [--kind shot,flush,draw] [--format …] [FILE|DIR ...]

RUN  KIND   START                    T        DURATION  PEAK_BAR  VOLUME_ML  TRUNCATED  SRC
1    flush  2026-11-10 07:58:12.300  712.300  4.2       2.1       38         no         …:2841
2    shot   2026-11-10 08:03:40.120  1040.12  31.5      9.4       52         no         …:4102
3    draw   2026-11-10 08:05:02.900  1122.90  12.0      -         144        no         …:4410
```

- One numbering sequence across all kinds and all inputs; `--kind` only
  filters the rows shown, never renumbers.
- Numbering covers the runs that **start** inside the `--since/--until`
  window (all runs, without a window), from 1, in input order: the order
  `decode` already processes sources in (captures first, in argument
  order, each on its own offsets; then raw logs by start time). *(Builder
  check; my call.)*
- `START`/`T`/`SRC` refer to the first sample of the run: the first
  pumping report (shot, flush) or the first volume increment (draw).
- Peak bar is blank for draws (brew does not track it).

### `levalog decode`

```
levalog decode [OPTIONS] [FILE|DIR ...]

  input        --dir, -s/--since, -u/--until        (as today)
  selection    --run LIST        runs by number: 12 | 3,5 | 7-9
               --run-kind K[,K]  runs of these kinds: shot, flush, draw
               --margin SPAN     widen each selected run window on both sides
                                 (default 0)
               --event K[,K]     only these event kinds (default: all)
               --skip-standby    drop reports whose pump marker is `?`
  projection   --fields K[,K]    only these columns, by key (medewi's and
                                 the XML's), in this order
               --hide-masked     text: omit masked fields from the line
               --changes         print a report only when a projected
                                 report field differs from the last
                                 printed report (t, wall, src, src_end,
                                 run_t ignored)
  output       --format text|jsonl|csv
               --raw-values      print the raw substrings instead of
                                 parsed numbers
               --show-dumps      (as today)
```

`--run` and `--run-kind` combine as a union; with either, only events
inside a selected run window (run ± margin) are printed. A run window is
[first sample − margin, last sample + margin]; the last sample of a shot
or flush is its last pumping report, so the coast is only visible with a
margin.

*(Builder check; my calls:)*

- **Runs are printed whole.** `--since/--until` decide which runs exist
  and get numbers; a selected run's window is printed in full even where
  it (or its margin) crosses the window edge. File selection extends to
  the neighbouring raw logs as needed: the file before when a margin
  reaches back past the first selected file, and following files while a
  selected run is still open at the end of the last one.
- **Run detection always runs** when the XML is loaded, so `run` and
  `run_t` are filled in JSON and CSV also without a run selection. A line
  in no run window has both empty.
- **Unparseable values.** A numeric field whose value is neither a
  number nor all `?` (partly masked, blank, text) prints like a masked
  one (`null` / empty; text shows the raw substring) and is counted in
  one warning on stderr at the end, naming the field. `--raw-values`
  shows it as is.

**Values.** By default every format prints parsed values: numbers with
the XML factor applied (`091.00` → `91`, `Flow rate` raw `060` → `0.996`),
strings trimmed. `--raw-values` prints the raw substrings.
Masked (`?`) is `?` in text, `null` in JSON, empty in CSV. `pump` is
`*`/`-`/`?` in text, `true`/`false`/`null` in JSON, `1`/`0`/empty in CSV.

**Text** (default) keeps today's layout — `T WALL DIR EVENT TEXT` — with
report fields printed as `key=value` (keys, not XML names, so what an
agent reads in text it can pass to `--fields`) and `pump` and `mode`
added as the first report fields. `run` and `run_t` are added after
`EVENT` when a run selection is active. `src` appears only when asked for
via `--fields`.

**JSON Lines**: one object per event; keys are the column keys. `src` is
always included. Reports carry their fields at the top level; frames
carry `bkl`, `key`, `layout`, `rows` (array of four strings); other events
carry `text`. A report that failed to decode carries `error` and `text`.

**CSV** (RFC 4180, header row): reports only (`--event` with any other
kind is a usage error). Columns: `t, wall, src, src_end, run, run_t, pump`
then all report fields, or exactly `--fields` in that order. A report that
failed to decode is skipped and counted in one warning on stderr at the
end.

Example agent session:

```sh
levalog runs -s yesterday --kind shot
levalog decode -s yesterday --run 2 --margin 5s --format csv \
  --fields run_t,pump,pressure,pressure_sp,temperature_pid1,heat_pid,volume
```

## How runs are found

`brew.Recorder` (already pure: no clock, no I/O) is fed every decoded
report in time order, across all input files. The record → sample code is
today `daemon.sample` (`daemon/daemon.go`); it moves into `brew` (e.g.
`brew.SampleFrom(rec, payload, at)`) and both the daemon and `logview` use
it. `brew.WaterDraw` gains the times of its first and last increment (the
daemon may ignore them).

- **Time base.** Raw logs use wall time. Captures have none; each capture
  is its own timeline on its offsets, and the recorder is aborted between
  two inputs that are not consecutive raw logs.
- **File boundaries.** Raw-log files rotate hourly and split at each
  reconnect. Consecutive files continue the same recorder; if the gap
  between the last event of one file and the first of the next exceeds
  the recorder's `CoastMax`, the recorder is aborted first. An aborted
  pumping run is listed with its normal kind (shot/flush, from what was
  seen) and `TRUNCATED yes`; this needs `Abort` (or a sibling) to report
  the normal kind plus a truncated flag instead of only a truncated kind.
- **Window.** With `--since/--until`, the recorder sees all reports of
  the selected files (whole files, as `logview.Select` already picks
  them), so a run started before the window is still recognised; only
  runs starting in the window are numbered.
- Thresholds: `brew.DefaultConfig()`, the same as the daemon.

## Changes by package

- `brew`: `SampleFrom`, draw times, truncated-with-kind on abort.
- `daemon`: use `brew.SampleFrom`; behaviour unchanged.
- `logview`: entries carry `src`/`src_end` (event index → line number);
  field keys; run detection over a sequence of sources; selection,
  projection and the three formatters. Kept out of `main` for #30.
- `cmd/levalog`: the `fields` and `runs` subcommands, the new flags, the
  help text.
- Docs: `docs/architecture.md` (logview's new role), the levalog part of
  `docs/runbooks/` if one covers it.

## Testing

TDD as usual. The committed `testdata/fixtures/shot-genlever` is the main
real-data fixture: its run list, a CSV projection and the `src` of known
lines are pinned in tests. Field-key derivation is tested on a synthetic
spec (no vendor names beyond those already in committed code). File
boundary, gap abort and window numbering get synthetic multi-file tests.

## Left to the builder

- Internal API shape in `logview` (streaming vs. slices; today's
  `Decode` returns a slice per source).
- Exact column widths and alignment of text tables.
- Which run wins `run`/`run_t` when two margins overlap.
- How the text format shows `src` when selected (column position).
- Wall-time format in JSON/CSV: RFC 3339 with milliseconds; UTC or local
  offset is the builder's call, stated in `--help`.
