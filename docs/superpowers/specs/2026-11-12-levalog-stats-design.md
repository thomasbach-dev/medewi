# levalog statistics: summary, run stats, resampling — design addendum

Addendum to [2026-11-11-levalog-analysis-design.md](2026-11-11-levalog-analysis-design.md).
That spec's non-goal "no statistics" is lifted for the small, fixed set
below; everything else there stays in force (the anti-goals in particular).

## Intent

Four agents with no prior knowledge of the repo queried real captures with
`levalog` (2026-11-11). All got correct answers; all ended in ad-hoc Python
for the same few things:

- **What is in this capture?** The `warmup` capture held no warm-up program
  (Opus spent its first steps on that); `coldstart-ready` held no `[READY]`
  line (Haiku had to scan every event to find out); one agent blamed an
  empty `runs` result on a missing XML when the pump had simply never run.
- **Per-run numbers:** peak pressure, temperature range, mean heater output
  of a shot, the time of a peak.
- **A coarser time grid:** one row per 30 s for a 20-minute warm-up instead
  of 4800 reports; per-minute temperature ranges before READY.
- **Discoverability:** `levalog` was not on PATH outside brewhost; no help
  example showed `--event notice` (the way to find `[READY]`); `levalog
  runs captures/` failed naming `/var/lib/medewid/raw`.

The research questions ahead (pre-infusion vs. shot, READY, pump wear over
weeks) need exactly these numbers, over many runs, comparably.

`levalog` keeps the domain-aware part: runs, masking, standby, provenance.
Anything beyond a fixed set of aggregates is the agents' job, through the
CSV/JSONL output and the tools they already know (jq, SQL, pandas).

## Goals

1. `levalog summary`: one record per source saying what is in it.
2. `levalog runs --stats FIELDS`: per-run aggregates as extra columns.
3. `levalog events --every SPAN`: reports resampled into fixed time buckets.
4. One aggregator vocabulary for both, borrowed from Miller/SQL/pandas.
5. An empty or reduced result always says why, on stderr.
6. A directory of captures is an input; `levalog` is on PATH in `nix develop`;
   `--help` ends with examples.

## Non-goals

- **No expression or query language** (`--where 'pressure>8'`, computed
  fields). Filters stay fixed flags; anything else is a pipe to jq/SQL.
- **No threshold or event detection** ("time until 8.5 bar", pre-infusion
  boundary, READY prediction). That is the research itself. The pipe recipe
  goes into the help examples instead.
- **No program detection** (issue #13) and no machine optimisation.
- No extra analysis tools in the dev shell (duckdb, miller): the agents
  used Python and jq, both already there. *(My call.)*
- No database, as before.

## Anti-goals

- **A masked value counted as a number.** `?` never becomes 0 in any
  aggregate; every aggregate states how many values it is over.
- **A default that hides spikes.** Resampling without an explicit
  aggregator must not average a 21 s, 12 bar pulse away.
- **A second output world.** Stats use the same `--format text|jsonl|csv`,
  the same field keys and the same selectors as `events`; numbers carry no
  units (units live in `levalog fields`).
- **Losing provenance.** An extreme value must lead back to its raw line.
- **Silent nothing.** Empty output with exit 0 and no word on stderr.

## Aggregators

One list, used by `runs --agg` and `events --agg`:

| Name | Value |
| --- | --- |
| `n` | number of reports with an unmasked, parseable value |
| `first`, `last` | first / last unmasked value in the group |
| `min`, `max` | smallest / largest value |
| `mean` | arithmetic mean over the `n` values (per report, not time-weighted) |
| `median`, `pNN` | nearest-rank percentile, `NN` 1–99 (`median` = `p50`) |
| `sd` | population standard deviation |
| `tmin`, `tmax` | time of the first smallest / largest value: `run_t` with a run (in `runs`, and in `events` with run selection), else `t` on the same base as the row's `t` |
| `srcmin`, `srcmax` | `src` (`PATH:N`) of that same report |

- Output column name: `<key>_<agg>`, the field key verbatim (lowercase also
  in text headers, so a grep for `pressure_max` works in every format).
- All values masked or unparseable in a group: the aggregate is empty
  (`?` in text, `null` in jsonl, empty in csv); `n` is 0.
- Only reports enter aggregates, and only non-standby ones (marker `*` or
  `-`); standby reports are masked anyway.
- Only numeric fields can be aggregated with `min … sd`; `first`/`last`/`n`
  also work on text fields (`mode`, `pump`, the profile label). A numeric
  aggregator on a text field **named** in `--stats`/`--fields` is a usage
  error (exit 2). When the fields are implied (`events --every` without
  `--fields`), a text field gets only `<key>_last`. *(My call: the warm-up
  example works without `--fields`, and an explicit mistake still fails.)*
- A key missing from a source's XML, or a source shown raw (no XML for it),
  contributes no values (so `n` shows it) and gets the same stderr warning
  `events --fields` gives. *(My call, follows the first spec.)*
- `mean` per report is honest because `n` sits next to it; gaps from LCD
  sampling or a reconnect are visible in `summary`. *(Ruling: sample mean,
  not time-weighted — simpler to check by hand; costs a small bias where
  report spacing is uneven.)*
- Unknown aggregator or field key: usage error naming the valid ones.

## `levalog summary`

```
levalog summary [-s T] [-u T] [--format …] [FILE|DIR ...]
```

One record per source (capture or raw-log file), in the input order
`events` uses. Text: a block of `key: value` lines per source, a blank line
between blocks. jsonl: one object per source. csv: one row per source, list
values joined by spaces.

| Key | Content |
| --- | --- |
| `src` | the source path |
| `kind` | `capture` or `rawlog` |
| `start` | wall-clock start: manifest `recorded` (captures), file start (raw logs) |
| `duration` | seconds from first to last event |
| `firmware` | manifest `firmware_version` (captures), else the first `$id:` banner text; empty if none |
| `xml` | the XML name(s) in effect, in order |
| `notes` | manifest notes (captures) |
| `events` | count per event kind, e.g. `report=4812 frame=1290 notice=1` |
| `modes` | seconds per mode, `standby` for marker `?`: `PID=1040.2 ECO=40.0 standby=12.5` |
| `ready` | count of `[READY]` notices and the `t` of each |
| `runs` | count per kind and the run numbers: `shot=2 flush=11 draw=0 (1-13)` |
| `masked` | keys masked in every non-standby report |
| `gap_max` | the longest pause between two reports, seconds |
| `gaps` | pauses over 2 s between reports |

- Seconds per mode: each report counts until the next one, with pauses over
  2 s left out (they are `gaps`).
- Run numbers are the same as `levalog runs` on the same input (the
  global numbering); a run belongs to the source its first sample is in,
  so a later raw-log file can show `(14-17)`. `summary` needs the run pass
  over the whole input, as `runs` does. *(My call.)*
- Without an XML for a source: a reduced record (`events`, `ready`, `gaps`,
  `gap_max`, `duration`, `firmware`, `xml`, `notes`); `modes`, `runs`,
  `masked` empty, plus a stderr warning; exit 0. *(My call.)*
- `masked` is per source; with no non-standby report it is empty. A
  non-standby report whose mode field is masked counts as mode `?`.
- `--stats` is not offered on `summary`: the per-field view is
  `events --every` with a bucket the size of the window. *(My call.)*

## `levalog runs --stats`

```
levalog runs --stats pressure,temperature_pid1 [--agg min,max,mean] …
```

- `--stats KEYS`: the report fields to aggregate; `--agg LIST` defaults to
  `n,min,max,mean`. `--agg` without `--stats` is a usage error.
- Group: the reports of the run window (first sample to end, the same window
  `events --run N` prints without `--margin`).
- Columns `<key>_<agg>` are appended after the existing ones, in `--stats`
  order, then `--agg` order: `pressure_n pressure_min … temperature_pid1_mean`.
  Existing columns keep their positions.
- Works with `--kind`; draws get stats too (their reports are in the window).

Example: peak pressure and its time for every shot:
`levalog runs --kind shot --stats pressure --agg max,tmax,srcmax`.

## `levalog events --every`

```
levalog events --every 30s [--agg last] [--fields …] [selection flags] …
```

- Turns `events` into one row per bucket of `SPAN` (a systemd.time span).
  Only reports are aggregated; `--event` with anything other than `report`
  is a usage error, as is `--changes` with `--every`.
- `--agg LIST` defaults to `last` (sample and hold: the value as it stood at
  the end of the bucket). With the default, columns keep their plain keys so
  the output looks like `events`; with `--agg` given, they are
  `<key>_<agg>`, even for one aggregator. Projected fields: `--fields`, or
  all report fields when not given (as in `events`).
- Leading columns per row: the bucket start in the same time columns
  `events` prints (`t`, plus `wall` for raw logs; `run`, `run_t` with run
  selection), then `reports` (reports in the bucket; not `n`, which could
  collide with a field key), then the aggregates.
- With run selection, rows go per run, in run order; a report in two
  overlapping windows (`--margin`) counts in both runs' buckets, so each
  run's rows are complete. *(My call.)*
- Without run selection, buckets do not cross sources: each capture and
  each raw-log file is bucketed on its own, so a bucket cut by a file
  boundary shows as two rows with their own `reports`; `t` is the bucket
  start on that source's base. Run windows already span files, so per-run
  buckets do. *(My call.)*
- Bucket alignment:
  - with `--run`/`--run-kind`: per run, from `run_t` = 0 (negative buckets
    in the margin), so shots line up across days — the pump-wear question;
  - raw logs otherwise: multiples of SPAN since the Unix epoch;
  - captures otherwise: multiples of SPAN from `t` = 0.
- Buckets without a non-standby report are omitted; the bucket start shows
  where. *(Ruling: omit rather than emit empty rows — keeps CSV dense;
  costs an agent the step of noticing missing starts, which `summary`'s
  `gaps` makes explicit.)*
- `--skip-standby` is implied.

## Empty and reduced results

Exit codes do not change (empty is not an error). A `levalog: ...` line on
stderr, after the output, whenever:

- `runs` finds none: `levalog: no runs in 4812 reports (pump marker * in 0,
  volume never rose)`; with a missing XML it says that instead of a count.
- `events` prints nothing: events read, then each active filter with what
  it dropped from what survived the filters before it, in this order:
  window (`--since/--until`), run selection, `--event`, `--skip-standby`,
  `--changes`; then what is left. E.g. `levalog: 21034 events read;
  --event report: 20100 dropped; --skip-standby: 934 dropped; 0 left`.
  A missing run is said instead: `levalog: --run 5: no such run (runs
  1-3)`. *(My call.)*
- `--run N` names a run that does not exist (already a message; keep it in
  this form).
- An input directory holds no logs: name **that** directory and what it
  holds, never the `--dir` default.

## Input: a directory of captures

Detection, in this order: a DIR holding `log.jsonl` is a capture (its
subdirectories are ignored); a DIR holding `*.jsonl.gz` is a raw-log
directory; otherwise its direct subdirectories that are captures stand for
it, in name order (one level, not recursive). `levalog summary captures/`
is then the overview of all captures. Other subdirectories, including an
interrupted capture (`log.partial.jsonl` only), are skipped with a stderr
note. A DIR with none of these is the "holds no logs" case above. *(My
call.)*

## Help and PATH

- The top-level `--help` ends with an `Examples:` section, at most eight
  lines, each a real command with a one-line comment. It must cover:
  `summary captures/`, finding `[READY]` (`events --event notice`),
  `runs --kind shot --stats pressure --agg max,tmax`, `events --run 2
  --every 1s --fields pressure`, a warm-up at `--every 60s --agg
  min,max`, and a CSV pipe to an external tool for time-to-threshold.
  Field keys in examples: only ones already in committed docs.
- `nix develop` puts a `levalog` on PATH that runs the working tree's code
  (a wrapper building `./cmd/levalog` from the repo root into a cache,
  then exec'ing it), so it is never stale and works from any directory.
  CLAUDE.md's Commands section names it. *(Shape of the wrapper is the
  builder's.)*

## Changes by package

- `logview`: the aggregator set and grouping (by run, by bucket), the
  summary scan, the capture-parent directory expansion, filter drop
  counts. Streaming as today: aggregates hold only what a percentile needs
  (the group's values), never whole reports.
- `cmd/levalog`: the `summary` subcommand, `runs --stats/--agg`,
  `events --every/--agg`, the stderr reasons, the examples.
- `flake.nix`: the dev-shell wrapper.
- Docs: CLAUDE.md (Commands), `docs/architecture.md` if it lists the
  subcommands. The original spec's "last printed report" wording for
  `--changes` is corrected to the per-run reset that shipped.

## Testing

TDD. On the committed `shot-genlever` fixture: pinned `runs --stats
pressure --agg n,max,tmax,srcmax` values for its shot, a pinned `summary`,
and `events --run N --every 1s` row count and first row. Synthetic tests:
masked values excluded and `n` = 0 giving empty aggregates, nearest-rank
percentiles on a known list, bucket alignment for each of the three cases,
omitted empty buckets, the capture-parent expansion, each stderr reason.

## Left to the builder

- Text-table widths and number formatting (as precise as the field's XML
  factor allows, like `events`).
- Internal types for aggregators and groups.
- How `summary` text wraps long `xml`/`notes` values.
- Usage error for `--agg` without `--stats`; message wording.
