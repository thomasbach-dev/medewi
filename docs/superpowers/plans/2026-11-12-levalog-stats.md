# levalog statistics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `levalog summary`, `runs --stats/--agg`, `events --every/--agg`, stderr reasons for empty results, capture-parent directories, examples in `--help`, `levalog` on PATH in `nix develop`.

**Architecture:** One aggregator type in `logview` (`agg.go`) feeds two groupings: per run (`runstats.go`) and per time bucket (`bucket.go`). `summary.go` is a third consumer of the existing `Stream` callbacks. All stay streaming: groups hold only their own values. `cmd/levalog` wires flags and passes, as `events` does today (pass 1 `scan` for runs, pass 2 `Stream` for output).

**Tech Stack:** Go, `pflag`, `systime`, existing `logview`/`brew`/`fixture`; Nix flake.

**Spec:** `docs/superpowers/specs/2026-11-12-levalog-stats-design.md` (addendum to `docs/superpowers/specs/2026-11-11-levalog-analysis-design.md`). Read both.

## Global Constraints

- Branch `docs-pressure-band`, worktree `/home/thomas/thomasbach-dev/toying/medewi-pressure-band`. Commit with `git -c commit.gpgsign=false commit`; conventional commits; end each message with the two trailer lines `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_01BwcWPUNaZSEfvzzZKpmyEF`.
- TDD: failing test first. Pure Go, no cgo, no new dependencies.
- Gate before every commit: `nix develop --command go test -race ./logview/ ./cmd/levalog/` while iterating; before committing `nix build .#checks.x86_64-linux.{fmt,vet,lint,test,tidy,levalog} -L` (git add new files first; the flake sees only tracked files).
- Tests use `testdata/spec/synthetic.xml` and the run spec helpers in `cmd/levalog/main_test.go`; tests on `testdata/fixtures/shot-genlever` need `MEDEWI_XML_PATH` and skip without it (follow the existing skip at `cmd/levalog/main_test.go:727`). Never commit vendor XML content; field keys in tests, help and docs: only `pressure`, `temperature_pid1`, `setpoint_pid`, `heat_pid`, `heat_pid_average`, `pressure_sp`, `pressure_band_min`, `pressure_band_max`, `mode`, `pump`, or keys of the synthetic spec.
- Aggregate column name `<key>_<agg>`, key verbatim, lowercase in every format including text headers. Masked: `?` text, `null` jsonl, empty csv. No units in values.
- Exit codes: 0 ok (also for empty results), 1 read error, 2 usage error. Stderr lines start with `levalog: ` and come after the output.
- Never contact the machine; never send anything to port 2323.

## Review Focus

1. A capture where a field is masked in some reports (eco/standby): aggregates must skip those values and `n` must say so — pinned in Task 1 and Task 2.
2. `events --every` over a raw log that spans a file boundary: two rows for the cut bucket, each with its own `reports` — pinned in Task 3.
3. `runs --stats` on input with no XML for one source: no crash, `n` 0, one stderr warning — pinned in Task 2.
4. `levalog summary captures/` on a directory with an interrupted capture (`log.partial.jsonl` only): skipped with a note, others listed — pinned in Task 5.
5. Negative `run_t` buckets with `--margin`: floor, not truncation (`-0.3s` at `--every 1s` is bucket `-1`) — pinned in Task 3.

---

### Task 1: Aggregators

**Files:**
- Create: `logview/agg.go`, `logview/agg_test.go`

**Interfaces:**
- Produces:
  - `type Agg struct { Name string; P int }` — `Name` as written (`"p90"`, `"median"`); `P` the percentile for `pNN`/`median` (50), 0 otherwise.
  - `func ParseAggs(s string) ([]Agg, error)` — comma-separated; error text names the valid ones: `n, first, last, min, max, mean, median, pNN (1-99), sd, tmin, tmax, srcmin, srcmax`.
  - `func (a Agg) Numeric() bool` — false only for `n`, `first`, `last`.
  - `type Acc struct` (zero value usable) with `func (c *Acc) Add(v Value, t time.Duration, src string)` and `func (c *Acc) Result(a Agg) AggValue`.
  - `type AggValue struct { Num *float64; Str *string }` — both nil means empty (masked output).
  - `func FormatTime(d time.Duration) string` if no equivalent exists (seconds, as `events` prints `t`); reuse `seconds()` from `write.go` if it fits.

- [ ] **Step 1: Write the failing tests** in `logview/agg_test.go`:
  - `TestParseAggs`: `"n,min,p90,median"` → names `n min p90 median`, P `0 0 90 50`; `"p0"`, `"p100"`, `"avg"`, `""` → error; error for `"avg"` contains `"tmax"`.
  - `TestAccNumbers`: add values 5, 1, 3, 2, 4 at t = 1s…5s, src `"f:1"`…`"f:5"`; then: `n`=5, `first`=5, `last`=4, `min`=1, `max`=5, `mean`=3, `median`=3, `p90`=5, `p10`=1, `sd`=√2 (population, within 1e-9), `tmin`=2s, `tmax`=1s, `srcmin`=`"f:2"`, `srcmax`=`"f:1"`.
  - `TestAccNearestRank`: values 1…10: `p25`=3, `p50`=5, `p75`=8 (rank = ceil(P/100·n)).
  - `TestAccMaskedAndBad`: a masked and a bad Value are not counted; with only those, `n`=0 and every other aggregate is empty (both pointers nil).
  - `TestAccFirstOccurrence`: values 2, 7, 7 → `tmax` is the first 7's time.
  - `TestAccText`: Str values `"PID"`, `"ECO"` → `first`=`PID`, `last`=`ECO`, `n`=2.
- [ ] **Step 2: Run** `nix develop --command go test -race ./logview/ -run 'TestParseAggs|TestAcc' -v` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `logview/agg.go`. `Acc` keeps the numbers in a slice (percentiles need them), first/last value, and the time/src of the first min and max. Percentile and median sort a copy.
- [ ] **Step 4: Run** the same command — Expected: PASS.
- [ ] **Step 5: Commit** `feat(logview): aggregators for run stats and buckets`.

### Task 2: `runs --stats`

**Files:**
- Create: `logview/fieldvalue.go` (+ test), `logview/runstats.go`, `logview/runstats_test.go`
- Modify: `logview/listings.go` (`WriteRuns`), `cmd/levalog/runs.go`, `cmd/levalog/main_test.go`, callers of `WriteRuns`

**Interfaces:**
- Consumes: Task 1 (`Agg`, `Acc`, `AggValue`, `ParseAggs`), `NewSelector`, `RunFinder` output.
- Produces:
  - `func FieldOf(e *Entry, key string) (Value, bool)` — `pump` from report byte 2 as a Str Value (`?` is Masked); XML keys via the entry's spec (cache key positions per spec, as `specKeys` does in `rows.go`); false when the key is not in this entry's spec or the entry has no Record.
  - `func IsTextKey(spec *leva.RecordSpec, key string) bool` — `pump` and `leva.String` fields.
  - `type RunStats struct`; `func NewRunStats(runs []Run, keys []string, aggs []Agg) *RunStats`; `func (s *RunStats) Entry(e *Entry)` (feed every entry; non-reports and standby reports are ignored); `func (s *RunStats) Header() []string` (`<key>_<agg>` in keys order, then aggs order); `func (s *RunStats) Values(n int) []AggValue` (for run N); `func (s *RunStats) Missing() []string` (warnings, one per key not found in some source, with a count).
  - `WriteRuns(w io.Writer, f Format, runs []Run, opt RunsOptions) error` with `type RunsOptions struct { Loc *time.Location; Stats *RunStats }`; stats columns appended after `src` in all three formats.
- Run windows: first sample to `Last` of each run (no margin), on the run's timeline — use `NewSelector(runs, Selection{Runs: all N})` and `Row.Run`.

- [ ] **Step 1: Write the failing tests:**
  - `logview/runstats_test.go` `TestRunStatsPerRun`: two runs from synthetic reports (pump `*` with pressure 1, 9, 8 in run 1; 2, 3 in run 2), keys `pressure`, aggs `n,max,tmax,srcmax`: run 1 → `3, 9, <run_t of the 9>, <its src>`; run 2 → `2, 3, …`.
  - `TestRunStatsMaskedAndStandby`: a pressure `?????` report inside run 1 and a standby report (marker `?`) → not counted (`n` unchanged).
  - `TestRunStatsMissingKey`: a source decoded raw (no spec) inside a run → `n` counts only decoded reports; `Missing()` has one line naming the key.
  - `logview` writer test `TestWriteRunsStats`: header text `RUN … SRC  pressure_n  pressure_max` (stats headers lowercase), jsonl keys `pressure_n`, `pressure_max` after `src`, csv header ends `,pressure_n,pressure_max`; an empty aggregate is `?`/`null`/empty.
  - `cmd/levalog/main_test.go` `TestRunsStats`: `runs --stats pressure --agg max` on a synthetic capture with one shot → output has the `pressure_max` column with the expected value; `TestRunsStatsUsage`: `--agg max` without `--stats` → exit 2; `--stats nosuch` → exit 2 naming valid keys hint `levalog fields`; `--stats mode --agg max` → exit 2 (text field with numeric aggregator); `--stats mode --agg last` → ok.
  - `TestRunsStatsGenlever` (skips without `MEDEWI_XML_PATH`): `runs --kind shot --stats pressure --agg n,max,tmax,srcmax testdata/fixtures/shot-genlever` — pin the values the first run prints (record them from the implementation, then check `max` against `PEAK_BAR`: they must be equal).
- [ ] **Step 2: Run** `nix develop --command go test -race ./logview/ ./cmd/levalog/ -run 'RunStats|WriteRunsStats|TestRunsStats' -v` — Expected: FAIL.
- [ ] **Step 3: Implement** `FieldOf`, `IsTextKey`, `RunStats`, `RunsOptions`. In `runsCmd`: flags `--stats KEYS` and `--agg LIST` (default `n,min,max,mean`); validate keys against `sc.specs` like `unknownKeys` in `events.go`; with `--stats`, run a second `Stream` pass over `sc.paths` feeding `RunStats.Entry`; warn `Missing()` after the output.
- [ ] **Step 4: Run** the same command — Expected: PASS. Then the full `go test -race ./...`.
- [ ] **Step 5: Commit** `feat(levalog): runs --stats with per-run aggregates`.

### Task 3: `events --every`

**Files:**
- Create: `logview/bucket.go`, `logview/bucket_test.go`, `logview/write_bucket.go` (+ test)
- Modify: `logview/rows.go` (`SelectAll`), `cmd/levalog/events.go`, `cmd/levalog/main_test.go`

**Interfaces:**
- Consumes: Tasks 1–2 (`Acc`, `FieldOf`, `IsTextKey`), `Selector`, `SourceInfo`.
- Produces:
  - `func (s *Selector) SelectAll(e *Entry) []Row` — one row per selected run window holding e (overlapping margins give several), in run order; without run selection the same single row `Select` gives (or none). `Select` is unchanged.
  - `type Bucketer struct`; `func NewBucketer(every time.Duration, cols []BucketCol, byRun bool) *Bucketer`; `func (b *Bucketer) Source(si SourceInfo) []BucketRow` (closes open buckets when not byRun); `func (b *Bucketer) Add(r Row) []BucketRow` (returns buckets closed by r); `func (b *Bucketer) Close() []BucketRow`.
  - `type BucketCol struct { Key string; Agg Agg; Plain bool }` — `Plain` prints the column as `Key` (the default `last`).
  - `type BucketRow struct { Source SourceInfo; Run *Run; Start time.Duration /* t of the bucket start on the source base */; Wall time.Time; HasWall bool; RunT *time.Duration; Reports int; Values []AggValue }`.
  - `func NewBucketWriter(w io.Writer, f Format, cols []BucketCol, runCols bool, loc *time.Location) BucketWriter` with `Write(BucketRow) error`, `Close() error`. Columns: `t`, `wall`, then `run`, `run_t` when runCols, then `reports`, then the value columns. Text: tab-aligned table, lowercase header; csv header row; jsonl objects. `wall` follows the `events` rule for each format (empty/absent for captures as `events` does).
- Bucket rules (spec): with runs, one bucket set per run from `run_t` = 0, rows per run in run order — buffer per run and emit at `Close`, or emit when the run window ends; without runs, raw logs align `floor(wall/every)` on the Unix epoch, captures `floor(offset/every)`; floor for negatives; empty buckets omitted; only non-standby reports.
- `Acc.Add` gets `run_t` as the time with run selection, else the report's `t` on its source base, so `tmin`/`tmax` match the row's time columns. Keys missing from a source's spec warn after the output as in Task 2 (`Missing()`-style, one line per key).
- Column set when `--fields` is empty: `pump`, then the XML keys of the first spec seen (writer order: mode first). Default (no `--agg`): every column `Plain` with `last`. With `--agg`: numeric fields get each aggregator as `<key>_<agg>`; text fields get `<key>_last` only when implied, usage error when named.

- [ ] **Step 1: Write the failing tests** (`bucket_test.go`, synthetic entries):
  - `TestBucketCaptureAlign`: capture reports at 0.2, 0.7, 1.1, 2.6 s, every 1s → buckets 0 (2 reports), 1 (1), 2 (1); none for an empty second when reports skip 3–4 s.
  - `TestBucketRawEpochAlign`: raw-log reports at wall 10:00:29.9 and 10:00:30.1, every 30s → two buckets starting 10:00:00 and 10:00:30; `Start` = bucket wall − source start.
  - `TestBucketFileBoundary`: one bucket cut by a second raw-log source → two rows, each with its own `Reports`.
  - `TestBucketRunNegative`: with run selection and margin, a report at run_t −0.3 s, every 1s → bucket run_t −1 s.
  - `TestBucketOverlap`: two runs whose margins overlap; a report in both windows counts in both runs' buckets (via `SelectAll`).
  - `TestBucketDefaultLast`: default columns print plain keys and the last value of the bucket.
  - `TestBucketSkipsStandby`: a bucket holding only standby reports is omitted.
  - `write_bucket_test.go`: header and one row in each format, masked aggregate shown as `?`/`null`/empty.
  - `cmd/levalog/main_test.go` `TestEventsEvery`: `events --every 1s --fields pressure --agg max` on a synthetic capture → expected header `t … reports pressure_max` and rows; `TestEventsEveryUsage`: `--every 1s --event notice` → 2; `--every 1s --changes` → 2; `--agg max` without `--every` → 2; `--every 1s --fields mode --agg max` → 2; `--every 0` → 2.
  - `TestEventsEveryGenlever` (skips without `MEDEWI_XML_PATH`): `events --run 2 --every 1s --fields pressure testdata/fixtures/shot-genlever` — pin row count and first row.
- [ ] **Step 2: Run** `nix develop --command go test -race ./logview/ ./cmd/levalog/ -run 'Bucket|EventsEvery' -v` — Expected: FAIL.
- [ ] **Step 3: Implement.** In `events`: flags `--every SPAN` (`systime.NewSpan`) and `--agg LIST`; `--every` implies `--skip-standby` and reports only; output goes through the BucketWriter instead of `Writer`. Without an XML, `--every` → `needXML`.
- [ ] **Step 4: Run** the same command, then `go test -race ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(levalog): events --every resamples reports into buckets`.

### Task 4: `levalog summary`

**Files:**
- Create: `logview/summary.go`, `logview/summary_test.go`, `cmd/levalog/summary.go`
- Modify: `logview/source.go` (manifest fields), `cmd/levalog/main.go` (dispatch), `cmd/levalog/main_test.go`

**Interfaces:**
- Consumes: `Stream`, `RunFinder`/`scan` runs, `FieldOf`.
- Produces:
  - `Source` and `SourceInfo` gain `Recorded time.Time` and `Firmware string` from the capture manifest (`fixture.Manifest.Recorded`, `.FirmwareVersion`).
  - `type Summary struct { Src, Kind string; Start time.Time; HasStart bool; Duration time.Duration; Firmware string; XML []string; Notes string; Events map[Kind]int; Modes map[string]time.Duration; Ready []time.Duration; Runs map[RunKind]int; RunNums []int; Masked []string; GapMax time.Duration; Gaps int; Decoded bool }`
  - `type SummaryScanner struct`; `func NewSummaryScanner() *SummaryScanner`; `Source(SourceInfo)`, `Entry(*Entry)`, `func (s *SummaryScanner) Summaries(runs []Run) []Summary` (runs assigned by `First.Source`, global numbers).
  - `func WriteSummaries(w io.Writer, f Format, ss []Summary, loc *time.Location) error` — keys and order as the spec's table; text blocks `key: value`, blank line between; jsonl one object (`events`, `modes`, `runs` as objects, `ready` as an array of seconds, `xml`/`masked` arrays); csv one row, lists space-joined, maps as `k=v` space-joined (keys sorted; event kinds in `eventKinds` order).
- Rules (spec): firmware from the manifest, else the text of the first banner entry; modes: each non-standby report counts until the next report, pauses > 2 s left out (they count in `Gaps`, and `GapMax` is the longest pause); standby (marker `?`) is mode `standby`; a masked mode field is `?`; ready = notices whose text is `[READY]`; masked = XML keys masked in every non-standby report of the source (empty with none); without an XML (`Decoded` false) `modes`, `runs`, `masked` are empty and the command warns.

- [ ] **Step 1: Write the failing tests:**
  - `TestSummaryCounts` (synthetic capture with manifest: 4 reports, 1 banner `$id: leva! (3.2)`, 1 notice `[READY]` at 1.5 s, a 3 s pause between reports 3 and 4): `Events` report=4 banner=1 notice=1, `Ready` [1.5 s], `Gaps`=1, `GapMax`=3 s, `Firmware` from the manifest, `Start` = manifest `recorded`.
  - `TestSummaryModes`: reports PID, PID, ECO, standby at 0, 0.25, 0.5, 0.75 s, last at 1.0 s → PID 0.5 s, ECO 0.25 s, standby 0.25 s.
  - `TestSummaryMasked`: a key `?`-masked in every non-standby report is listed; one masked only in standby is not.
  - `TestSummaryRunsGlobal`: two raw-log sources, runs 1–2 start in the first, run 3 in the second → second summary `runs` shot/flush counts and `RunNums` [3].
  - `TestSummaryFirmwareFromBanner`: raw log without manifest → firmware = first banner text.
  - writer test: one summary in each format (text block, jsonl keys, csv header `src,kind,start,duration,firmware,xml,notes,events,modes,ready,runs,masked,gap_max,gaps`).
  - `cmd/levalog/main_test.go` `TestSummaryCmd`: `summary DIR` on a synthetic capture prints a block with `firmware: $id: leva! (3.2)`; without XML → exit 0, reduced record, stderr warning; `TestSummaryGenlever` (skips without XML): pin `runs:` and `events:` lines for `testdata/fixtures/shot-genlever`.
- [ ] **Step 2: Run** `nix develop --command go test -race ./logview/ ./cmd/levalog/ -run 'Summary' -v` — Expected: FAIL.
- [ ] **Step 3: Implement.** `summary` subcommand with the shared input flags (`input.register`), `scan` for runs (detect only with an XML), a second `Stream` pass feeding `SummaryScanner`. Add `summary` to `usage()`'s subcommand list (one line: "what each source holds: firmware, XML, events, modes, READY, runs, masked fields, gaps").
- [ ] **Step 4: Run** the same command, then `go test -race ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(levalog): summary of each source`.

### Task 5: Input directories and empty-result reasons

**Files:**
- Modify: `cmd/levalog/input.go`, `cmd/levalog/events.go`, `cmd/levalog/runs.go`, `cmd/levalog/summary.go`, `logview/rows.go` (drop counts), `cmd/levalog/main_test.go`, `logview/rows_test.go`

**Interfaces:**
- Produces:
  - `sourcePaths` detection in this order: DIR with `log.jsonl` → capture; DIR with `*.jsonl.gz` (non-empty `logview.Select(dir, zero, zero)`) → raw-log dir; else its direct subdirectories that hold `log.jsonl`, in name order; other subdirectories → a returned note `levalog: skipped DIR/NAME: not a capture` (and for `log.partial.jsonl` only: `levalog: skipped DIR/NAME: interrupted capture (log.partial.jsonl)`); none at all → error message `no logs in DIR (no log.jsonl, no *.jsonl.gz, no capture subdirectories)`, exit 0. The message names the directory actually read, never the `--dir` default unless that is the one read.
  - `func (s *Selector) Drops() []Drop` with `type Drop struct { Filter string; N int }` — in the order window (`--since/--until`), run selection (`--run/--run-kind`), `--event`, `--skip-standby`, `--changes`; each counts entries dropped among those that survived the earlier filters; only active filters listed. `Selector` also counts entries seen.
  - `events` with zero rows printed: `levalog: N events read; --event report: K dropped; …; 0 left`. A `--run` naming no run keeps its current message form: `levalog: --run 5: no such run (runs 1-3)` (change the existing "no run N in this input" text to this; `(no runs)` when there are none).
  - `runs` with zero runs: `levalog: no runs in N reports (pump marker * in M)`; with no report decoded because the XML is missing for every source: `levalog: no runs: no report decoded (no XML for NAME)`.
  - `runs --kind` filtering all rows away is not empty input: `levalog: no runs of kind shot (of N runs)`.

- [ ] **Step 1: Write the failing tests:**
  - `TestCaptureParentDir`: a temp dir with captures `b/` and `a/` (each `log.jsonl`), a dir `c/` with `log.partial.jsonl` only, and a dir `d/` empty → `events` reads `a` then `b`; stderr has the two skip notes.
  - `TestNoLogsNamesDir`: an empty temp dir as argument → stderr names that dir, exit 0.
  - `TestCaptureWithSubdirs`: a capture dir that also has capture subdirectories → read as the capture only.
  - `TestRowsDrops` (`rows_test.go`): 10 entries (6 reports, 2 standby among them, 4 notices) with `Events: [report]`, `SkipStandby` → drops `--event report: 4`, `--skip-standby: 2`.
  - `TestEventsEmptyReason`: `events --event notice` on a capture without notices → stderr `levalog: N events read; --event notice: N dropped; 0 left`.
  - `TestRunsEmptyReason`: idle-only capture → `levalog: no runs in N reports (pump marker * in 0)`; `--kind draw` on a capture with one shot → `levalog: no runs of kind draw (of 1 runs)`.
  - Update the existing test that expects `no run N in this input` to the new form.
- [ ] **Step 2: Run** `nix develop --command go test -race ./logview/ ./cmd/levalog/ -v -run 'CaptureParent|NoLogs|CaptureWithSubdirs|Drops|EmptyReason'` — Expected: FAIL.
- [ ] **Step 3: Implement.** Report counts for `runs` come from `scan` (count decoded reports and those with marker `*`).
- [ ] **Step 4: Run** the same command, then `go test -race ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(levalog): capture-parent directories and reasons for empty results`.

### Task 6: Help examples, PATH, docs

**Files:**
- Modify: `cmd/levalog/main.go` (`usage`), `cmd/levalog/main_test.go`, `flake.nix` (devShell), `CLAUDE.md`, `docs/architecture.md` (if it lists levalog's subcommands)

**Interfaces:**
- Produces: the usage text names four subcommands and documents `--stats`/`--agg`/`--every` and the aggregator list in one short paragraph each, then ends with:

```
Examples:
  levalog summary captures/                         # what each capture holds
  levalog events --event notice captures/warmup-ready   # find [READY]
  levalog runs --kind shot --stats pressure --agg max,tmax,srcmax
  levalog events --run 2 --every 1s --fields pressure,pressure_sp
  levalog events --every 60s --agg min,max --fields temperature_pid1,heat_pid_average
  levalog events --run 2 --format csv --fields run_t,pressure | awk -F, '$2>=8.5{print $1; exit}'
                                                    # time to 8.5 bar: pipe to any tool
```

  (at most eight command lines; exact wording may change, the commands may not.)
- Dev shell: a `levalog` on PATH that builds the working tree's `./cmd/levalog` (from the repo root found via `git rev-parse --show-toplevel`, falling back to `$MEDEWI_REPO`, which the devShell's `shellHook` exports as the directory `nix develop` started in) into `${XDG_CACHE_HOME:-$HOME/.cache}/medewi/levalog` with `go build`, then `exec`s it with the original arguments and working directory. Implement with `pkgs.writeShellScriptBin "levalog" …` in `devShells.default.packages`.
- CLAUDE.md Commands block gains: `levalog summary captures/   # in nix develop: levalog runs the working tree's code`.

- [ ] **Step 1: Write the failing test** `TestUsageExamples`: `levalog --help` output contains `Examples:`, `summary captures/`, `--event notice`, `--stats pressure`, `--every 1s`, and every key it names in examples is one of the allowed keys in Global Constraints.
- [ ] **Step 2: Run** `nix develop --command go test -race ./cmd/levalog/ -run TestUsageExamples -v` — Expected: FAIL.
- [ ] **Step 3: Implement** the usage text, the dev-shell wrapper, the doc lines.
- [ ] **Step 4: Verify:** test PASS; `nix develop --command bash -c 'cd /tmp && levalog --help | tail -3'` prints the examples (proves PATH and a non-repo working directory); full gate `nix build .#checks.x86_64-linux.{fmt,vet,lint,test,tidy,levalog} -L` green.
- [ ] **Step 5: Commit** `feat(levalog): help examples; levalog on PATH in nix develop`.
