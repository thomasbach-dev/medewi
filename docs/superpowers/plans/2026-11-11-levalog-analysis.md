# levalog analysis Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `levalog` into an agent-friendly analysis tool: `fields`, `runs` and `events` (renamed `decode`) with run selection, projection, three output formats, a source reference per line and one vendor XML per session.

**Architecture:** All logic lives in `logview/` (reused later by #30); `cmd/levalog` only parses flags and wires stages: load sources → decode (per-session XML) → find runs (`brew.Recorder`) → select rows → write. `brew` gains the record → sample builder the daemon has today; `rawlog` repeats the session's XML name at the top of every file.

**Tech Stack:** Go (pure, no cgo), `spf13/pflag`, `encoding/json`, `encoding/csv`, existing `leva`, `brew`, `rawlog`, `systime`.

**Spec:** [docs/superpowers/specs/2026-11-11-levalog-analysis-design.md](../specs/2026-11-11-levalog-analysis-design.md)

## Global Constraints

- Work on branch `docs-pressure-band` in the worktree `../medewi-pressure-band`; commit with `git -c commit.gpgsign=false commit`; conventional commits; trailers `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_01BwcWPUNaZSEfvzzZKpmyEF`.
- Run `nix flake check -L` before every commit (`git add` new files first). Tests needing `MEDEWI_XML_PATH` skip in the sandbox: run them with `nix develop --command go test -race ./PKG/ -run NAME` (the local `.envrc` of the main checkout sets the variable: `source ../medewi/.envrc`).
- Pure Go, no cgo. TDD: failing test first.
- Never commit vendor content: no XML from the vendor, no field names beyond those already in committed code (`daemon/daemon.go` field constants, `brew/fixture_test.go`). Unit tests use `testdata/spec/synthetic.xml` or Go-built `leva.RecordSpec`s.
- Never contact the machine; nothing here needs it.
- The raw log must never contain `MCu`/`MCm` reply lines; the `rawlog` allowlist stays untouched except for the new note.
- CLI per `docs/cli-conventions.md`: pflag long options, `-s/--since`, `-u/--until` via `systime`, env vars as defaults, exit codes 0 ok / 1 read error / 2 usage error, errors as `levalog: message`.
- Medewi column keys (reserved, in this order): `t, wall, src, src_end, dir, event, run, run_t, pump, xml`.
- Masked = `?` in text, `null` in JSON, empty in CSV. `pump`: `*`/`-`/`?` text, `true`/`false`/`null` JSON, `1`/`0`/empty CSV.
- Wall time in JSON/CSV: RFC 3339 with milliseconds in UTC (`2026-11-10T07:58:12.300Z`); text keeps local `2006-01-02 15:04:05.000`. *(Plan's call on the spec's open point; say it in `--help`.)*
- Parsed numbers are rounded to 6 decimal places and printed shortest (`strconv.FormatFloat(v, 'f', -1, 64)`), so `060`×0.0166 prints `0.996`. *(Plan's call.)*

## Review Focus

1. A raw log rotated mid-session from before the XML note existed: the XML must come from the handshake in an earlier file of the directory, not "unknown". Test in Task 4 (`TestLoadLooksBackForXML`).
2. A report whose bytes span two or three events, and a glued line split in two: `src`/`src_end` must name the right JSON lines. Test in Task 3 (`TestDecodeSrcSpansEvents`, `TestDecodeSrcGlued`).
3. A shot crossing an hourly file boundary selected with `--run`: printed whole, and the same number in `runs` and `events`. Test in Task 10 (`TestEventsRunAcrossFiles`).
4. Factor-scaled values and Latin-1 bytes (`°`, profile labels) in JSON/CSV: no float noise, valid UTF-8. Tests in Task 6 and Task 9.
5. A capture without `MC?` with `--xml DIR` holding two XMLs: reports raw plus one warning, exit 0, no crash. Test in Task 2 and Task 10.

---

### Task 1: brew owns the sample builder, draw times and the normal kind

**Files:**
- Create: `brew/sample.go`, `brew/sample_test.go`
- Modify: `brew/recorder.go` (`WaterDraw`, `Summary`, `closeDraw`, `closeBrew`, `Abort`), `brew/fixture_test.go` (use `SampleFrom`), `daemon/daemon.go:39-48,526-545` (drop the local builder and field constants)
- Test: `brew/sample_test.go`, `brew/recorder_test.go`

**Interfaces:**
- Produces:
  - `brew.FieldNames []string` — the seven export names now in `daemon/daemon.go` (same order); the daemon uses it where it uses `fieldNames`.
  - `func SampleFrom(rec leva.Record, payload []byte, at time.Time) Sample` — body of today's `daemon.sample`.
  - `WaterDraw{StartedAt, EndedAt time.Time; Duration time.Duration; VolumeML float64}` — first and last increment.
  - `Summary.Normal Kind` — always `Shot` or `Flush` (what the run looked like); `Kind` unchanged (truncated kinds from `Abort` stay, so the daemon's behaviour is unchanged).

- [ ] **Step 1: Write the failing tests**

```go
func TestSampleFrom(t *testing.T)            // synthetic Record with "Pressure"=9.0, payload[2]='*' → Pumping, *Pressure==9, Volume nil
func TestSampleFromNotPumping(t *testing.T)  // payload[2]='-' and a 2-byte payload → Pumping false, no panic
func TestDrawTimes(t *testing.T)             // feed idle samples Volume 10,20,30 at 0s,1s,2s then quiet 3s → Water.StartedAt==0s, EndedAt==2s
func TestAbortKeepsNormalKind(t *testing.T)  // pumping with PressureSP set, Abort(TruncatedLost) → Kind==TruncatedLost, Normal==Shot
func TestCloseSetsNormal(t *testing.T)       // a normal flush close → Kind==Flush, Normal==Flush
```

- [ ] **Step 2: Run** `nix develop --command go test -race ./brew/` — Expected: FAIL (undefined `SampleFrom`, `StartedAt`, `Normal`).
- [ ] **Step 3: Implement** in `brew/sample.go` and `brew/recorder.go`; replace `daemon.sample` and the duplicate in `brew/fixture_test.go` with `SampleFrom`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./brew/ ./daemon/` and, with the XML, `go test -race ./brew/ -run TestFixtureRuns` — Expected: PASS.
- [ ] **Step 5: Commit** `refactor(brew): sample builder, draw times and normal kind for offline use`

### Task 2: rawlog repeats the session's XML; the daemon sets it

**Files:**
- Modify: `rawlog/rawlog.go` (`Session`, `item`, `writer.open`/`write`), `daemon/daemon.go:332-343`
- Test: `rawlog/rawlog_test.go`, `daemon/rawlog_test.go`

**Interfaces:**
- Produces:
  - `func (s *Session) SetXML(name string)` — nil-safe, no I/O; enqueues a note `xml: NAME` now and makes every later file of this session start with that note (at `t=0`, before the triggering event). Calling it again with another name replaces it.
  - `const rawlog.XMLNotePrefix = "xml: "` — used by `logview` to read it back.
- Daemon: right after `d.xmlName` succeeds (before the `SpecName` comparison) call `rs.SetXML(name)`, so a mismatched session is labelled too.

- [ ] **Step 1: Write the failing tests**

```go
func TestSetXMLNoteNow(t *testing.T)             // SetXML("leva!-TEST") → file has note "xml: leva!-TEST"
func TestSetXMLRepeatedAfterRotation(t *testing.T) // RotateEvery 1h, fake Now; SetXML; tap a report at +61min → second file's first event is the note at t=0
func TestSetXMLOtherSessionUnaffected(t *testing.T) // next Session's files carry no note until it calls SetXML
func TestSetXMLNilSafe(t *testing.T)
// daemon/rawlog_test.go
func TestRawLogNamesXML(t *testing.T)            // daemon against the sim → raw file contains a note "xml: " + the sim's XML name
```

- [ ] **Step 2: Run** `nix develop --command go test -race ./rawlog/ ./daemon/ -run 'XML'` — Expected: FAIL.
- [ ] **Step 3: Implement** — the item carries the name; the writer keeps it per current session and writes the note first when it opens a file.
- [ ] **Step 4: Run** the same — Expected: PASS; then `go test -race ./rawlog/ ./daemon/`.
- [ ] **Step 5: Commit** `feat(rawlog): repeat the session's XML name at the top of every file`

### Task 3: logview catalog, per-session XML, source references

**Files:**
- Create: `logview/catalog.go`, `logview/catalog_test.go`
- Modify: `logview/source.go` (`Source.Lines`), `logview/decode.go` (`Entry`, `Options`, `Decode`, decoder), `logview/decode_test.go`, `logview/decode_glue_test.go`
- Test: `logview/catalog_test.go`, `logview/decode_test.go`

**Interfaces:**
- Produces:
  - `type Catalog struct{…}`; `func LoadCatalog(path string) (*Catalog, error)` — `path` is a file (used for every session) or a directory of `NAME.xml` files (loaded lazily, cached). Errors: unreadable path, invalid file XML.
  - `func (c *Catalog) Lookup(name string) (spec *leva.RecordSpec, problem string)` — file mode: `name==""` or equal to the file's base name without `.xml` → spec; else `problem` = `log names NAME, --xml is FILE`. Dir mode: `NAME.xml` present → spec; missing → `NAME.xml not in DIR`; `name==""` → the only XML if the directory holds exactly one, else `no XML name in the log and N XMLs in DIR`. A nil `*Catalog` returns `nil, ""` (reports raw, no warning).
  - `Source.Lines []int` — 1-based line number in the (decompressed) file of each event.
  - `Entry` gains `Path string; Src, SrcEnd int /* line numbers of first/last byte's event */; XML string; Spec *leva.RecordSpec`.
  - `Options{Catalog *Catalog; XML string /* in effect at start */; ShowDumps bool}` — `Spec`, `Since`, `Until` are removed (window filtering moves to Task 8).
  - `func Decode(src Source, opt Options) Decoded`; `type Decoded struct{Entries []Entry; XML string /* in effect at end */; Problems []string /* one per session that hit a Lookup problem */}`.
- Session XML changes on an rx reply line `XML=NAME` (`leva.XMLName`) and on a note starting with `rawlog.XMLNotePrefix`; the spec is looked up at each change.

- [ ] **Step 1: Write the failing tests**

```go
func TestCatalogFile(t *testing.T)             // synthetic.xml as file: Lookup("") and Lookup("synthetic") → spec; Lookup("other") → nil, "log names other, --xml is …synthetic.xml"
func TestCatalogDir(t *testing.T)              // dir with a.xml, b.xml (copies of synthetic): Lookup("b") → spec; Lookup("c") → "c.xml not in DIR"; Lookup("") → "no XML name in the log and 2 XMLs in DIR"
func TestCatalogDirSingle(t *testing.T)        // one XML: Lookup("") → spec
func TestDecodeSrcSpansEvents(t *testing.T)    // report split over events 1-3 (lines 1-3) → Src==1, SrcEnd==3
func TestDecodeSrcGlued(t *testing.T)          // one event with two glued reports → both entries Src==SrcEnd==that line
func TestDecodeSwitchesXMLOnReply(t *testing.T) // catalog dir {a,b}; reports, reply "XML=b", reports → entries before: XML "a" from Options.XML, after: "b"; Spec set accordingly
func TestDecodeXMLNote(t *testing.T)           // note "xml: a" sets XML
func TestDecodeXMLProblemOnce(t *testing.T)    // file catalog, reply "XML=other", 3 reports → Record nil on all, len(Problems)==1
func TestOpenLines(t *testing.T)               // capture with a blank line in the middle → Lines skip it
```

- [ ] **Step 2: Run** `nix develop --command go test -race ./logview/` — Expected: FAIL (compile).
- [ ] **Step 3: Implement**; adapt existing tests to `Options{Catalog: …}` and `Decoded.Entries`; move `TestDecodeWindow` to Task 8 (delete here, recreate there).
- [ ] **Step 4: Run** `nix develop --command go test -race ./logview/` — Expected: PASS. (`cmd/levalog` breaks to compile; fix it minimally in this task by building a file `Catalog` from `--xml` and dropping the window from `Options` — the window comes back in Task 8.)
- [ ] **Step 5: Commit** `feat(logview): per-session XML from a file or directory, source line references`

### Task 4: loading a sequence of sources

**Files:**
- Create: `logview/load.go`, `logview/load_test.go`

**Interfaces:**
- Consumes: `Open`, `Decode`, `Decoded` (Task 3).
- Produces:
  - `func LastXML(dir string, before time.Time) (string, error)` — scans raw logs in `dir` that start before `before`, newest first, decoding each until one yields a session XML; returns the XML in effect at that file's end, `""` if none.
  - `type Loaded struct{Entries []Entry; Sources []Source; Problems []string}` — every entry has `Entry.Source int` (index into `Sources`; add the field in this task).
  - `func Load(paths []string, cat *Catalog, showDumps bool) (Loaded, error)` — paths in the order given (callers sort as `sourcePaths` does today). The XML carries over between consecutive raw logs of the same directory; for the first raw log of a directory run (or after a capture), it starts from `LastXML(dir, start)`; captures start from `""`.

- [ ] **Step 1: Write the failing tests**

```go
func TestLoadCarriesXML(t *testing.T)      // two raw logs; handshake reply only in the first → second file's reports decoded, XML set
func TestLoadLooksBackForXML(t *testing.T) // three raw logs in a dir, handshake only in the first; Load only the third → its reports decoded with the first's XML
func TestLoadCaptureNoXML(t *testing.T)    // capture without MC?, catalog dir {a,b} → reports raw, one Problem
func TestLoadSourceIndex(t *testing.T)     // entries carry Source 0 then 1
```

- [ ] **Step 2: Run** `nix develop --command go test -race ./logview/ -run TestLoad` — Expected: FAIL.
- [ ] **Step 3: Implement** `logview/load.go`.
- [ ] **Step 4: Run** the same — Expected: PASS.
- [ ] **Step 5: Commit** `feat(logview): load a sequence of sources, XML carried and looked back`

### Task 5: field keys

**Files:**
- Create: `logview/columns.go`, `logview/columns_test.go`

**Interfaces:**
- Produces:
  - `type Column struct{Key, Name, Unit string; Factor float64; At, Len, Index int; Source string /* "medewi" | "xml" */}` — `Index` is the XML value index, `-1` for medewi columns.
  - `var MedewiColumns []Column` — the ten reserved keys from Global Constraints, in that order; `pump` has `At: 2, Len: 1`, the others `At/Len -1`.
  - `func Key(name string) string` — lowercase; every run of non-`[a-z0-9]` becomes `_`; trim `_`.
  - `func Columns(spec *leva.RecordSpec) ([]Column, error)` — `MedewiColumns` then one per XML value in spec order: empty name at `At==4` → `mode`; other empty names → `field_N`; duplicate key → append `_` + `Key(unit)` to every duplicate; still duplicate → error; a key equal to a medewi key → error naming both.

- [ ] **Step 1: Write the failing tests**

```go
func TestKey(t *testing.T)               // "Heat PID (P)"→"heat_pid_p", "dT/dt PID1"→"dt_dt_pid1", "Temperature PID1"→"temperature_pid1", "Pressure SP"→"pressure_sp"
func TestColumnsSynthetic(t *testing.T)  // synthetic.xml → keys after medewi: mode, temperature_pid1, setpoint_pid, pressure, heat_pid, field_5
func TestColumnsDuplicate(t *testing.T)  // Go spec with two "Flow rate" (ml/s, g/s) → flow_rate_ml_s, flow_rate_g_s
func TestColumnsReservedClash(t *testing.T) // a value named "Run" → error mentions "run"
```

- [ ] **Step 2: Run** `nix develop --command go test -race ./logview/ -run 'TestKey|TestColumns'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same — Expected: PASS.
- [ ] **Step 5: Commit** `feat(logview): stable field keys`

### Task 6: parsed values

**Files:**
- Create: `logview/values.go`, `logview/values_test.go`

**Interfaces:**
- Produces:
  - `type Value struct{Raw string; Num *float64; Str *string; Masked, Bad bool}`
  - `func ParseField(f leva.Field) Value` — `Masked`: `Raw` non-empty and all `?`. String type → `Str` (Latin-1 bytes converted to runes, as `rawText` does). Float/Int → `Num`, factor applied, rounded to 6 decimals; anything else (partly masked, blank, text) → `Bad`.
  - `func FormatNum(v float64) string` — shortest form per Global Constraints.

- [ ] **Step 1: Write the failing tests**

```go
func TestParseFieldNumber(t *testing.T)  // "091.00" Float → 91; "060" Int factor 0.0166 → FormatNum == "0.996"
func TestParseFieldMasked(t *testing.T)  // "?????" → Masked, Num nil
func TestParseFieldBad(t *testing.T)     // "09?.0", "", "abc" on a Float → Bad, not Masked
func TestParseFieldLatin1(t *testing.T)  // String raw "SOAK 1.0\xb0" → *Str is valid UTF-8 with "°"
```

- [ ] **Step 2–4:** run `nix develop --command go test -race ./logview/ -run TestParseField`, FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(logview): parse report values`

### Task 7: runs

**Files:**
- Create: `logview/runs.go`, `logview/runs_test.go`

**Interfaces:**
- Consumes: `Loaded` (Task 4), `brew.SampleFrom`, `brew.Recorder`, `Summary.Normal`, `WaterDraw.StartedAt/EndedAt` (Task 1).
- Produces:
  - `type RunKind string` with `RunShot = "shot"`, `RunFlush = "flush"`, `RunDraw = "draw"`.
  - `type Run struct{N int; Kind RunKind; Truncated, OpenAtEnd bool; First, Last int /* entry indices of first/last sample */; Duration time.Duration; PeakBar *float64 /* nil for draws */; VolumeML float64; XML string}`
  - `func FindRuns(l Loaded, cfg brew.Config, since, until time.Time) []Run` — feeds every decoded report in order (sample time: `Wall` for raw logs, a per-source timeline on `Offset` for captures). Aborts the recorder (`Abort(brew.TruncatedLost)`) between sources unless both are raw logs and the gap from the last event of one to the first of the next is ≤ `cfg.CoastMax`; at end of input an open run is closed the same way with `OpenAtEnd` set. Returns only runs whose first sample is in [since, until) (all if both zero; captures: all), numbered from 1 in input order. A draw's `First`/`Last` are the reports at `StartedAt`/`EndedAt` (first match in that run's span).

- [ ] **Step 1: Write the failing tests** (synthetic specs built in Go with `Pressure`, `Pressure SP`, `Volume`, pump marker byte 2)

```go
func TestFindRunsKinds(t *testing.T)        // flush, shot, draw in one capture → N 1,2,3 kinds flush,shot,draw; draw PeakBar nil
func TestFindRunsAcrossFiles(t *testing.T)  // shot spans two raw logs with a 250 ms gap → one run, not truncated
func TestFindRunsGapAborts(t *testing.T)    // same with a 10 s gap → run Truncated, Kind shot (from Normal)
func TestFindRunsWindowNumbering(t *testing.T) // three shots, since between 1st and 2nd start → N 1,2 for 2nd,3rd
func TestFindRunsOpenAtEnd(t *testing.T)    // input ends mid-shot → OpenAtEnd, Truncated
func TestFindRunsFixture(t *testing.T)      // MEDEWI_XML_PATH (skip without): shot-genlever → flush 73 ml, shot 101 ml peak 8.9–9.1; steam-eco → one draw 144 ml
```

- [ ] **Step 2–4:** `nix develop --command go test -race ./logview/ -run TestFindRuns` FAIL → implement → PASS (the fixture test with `source ../medewi/.envrc`).
- [ ] **Step 5: Commit** `feat(logview): number pump runs and wand draws`

### Task 8: selecting rows

**Files:**
- Create: `logview/rows.go`, `logview/rows_test.go`

**Interfaces:**
- Consumes: `Loaded`, `Run`, `ParseField`.
- Produces:
  - `type Row struct{Entry Entry; Run *Run; RunT *time.Duration}`
  - `type Selection struct{Since, Until time.Time; Runs []int; RunKinds []RunKind; Margin time.Duration; Events []Kind; SkipStandby, Changes bool; ChangeKeys []string}`
  - `func Rows(l Loaded, runs []Run, sel Selection, cols []Column) []Row`
- Rules (from the spec): every row gets `Run`/`RunT` from the run whose window [first−margin, last+margin] holds it (earlier run wins on overlap — *plan's call*). With `Runs`/`RunKinds` (union) only rows in selected windows pass and `Since/Until` do not cut them; otherwise `Since/Until` filter by wall time (entries without wall pass). `Events` filters by `Kind` (a glued piece counts as its kind). `SkipStandby` drops reports with raw byte 2 `?`. `Changes` drops a report whose `ChangeKeys` values (parsed, `pump` and `mode` included; empty keys = all XML keys) equal the last printed report's; other events always pass.

- [ ] **Step 1: Write the failing tests**

```go
func TestRowsWindow(t *testing.T)          // former TestDecodeWindow, now on Rows
func TestRowsRunWithMargin(t *testing.T)   // run 2 selected, margin 1s → rows from first−1s to last+1s; RunT negative before; Run set in margins
func TestRowsRunIgnoresUntil(t *testing.T) // until cuts mid-run → whole run printed
func TestRowsRunKindUnion(t *testing.T)    // Runs [1], RunKinds [draw] → run 1 and all draws
func TestRowsEvents(t *testing.T)          // Events [report] → no cmd/banner rows
func TestRowsSkipStandby(t *testing.T)
func TestRowsChanges(t *testing.T)         // three reports, pressure 1,1,2 with ChangeKeys [pressure] → 2 rows; t differences ignored
```

- [ ] **Step 2–4:** `nix develop --command go test -race ./logview/ -run TestRows` FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(logview): select rows by run, event kind, standby and change`

### Task 9: writers (text, jsonl, csv)

**Files:**
- Modify: `logview/format.go` (text moves behind the writer), `logview/format_test.go`
- Create: `logview/write.go`, `logview/write_test.go`

**Interfaces:**
- Consumes: `Row`, `Column`, `Value`.
- Produces:
  - `type Format string` — `FormatText = "text"`, `FormatJSONL = "jsonl"`, `FormatCSV = "csv"`; `func ParseFormat(s string) (Format, error)`.
  - `type WriterOptions struct{Fields []string; RawValues, HideMasked, RunCols bool; Loc *time.Location}`
  - `type Writer interface{ Write(Row) error; Close() error; Warnings() []string }`; `func NewWriter(w io.Writer, f Format, opt WriterOptions) Writer`.
  - `func WriteColumns(w io.Writer, f Format, xml string, cols []Column) error` and `func WriteRuns(w io.Writer, f Format, l Loaded, runs []Run, loc *time.Location) error` for the `fields` and `runs` subcommands (columns exactly as in the spec tables; runs adds `XML` before `SRC`).
- Text: today's `T WALL DIR EVENT` prefix, then `RUN RUN_T` when `RunCols`, then any of `src, src_end, xml` named in `Fields`; reports as `key=value` with `pump` and `mode` first, only `Fields` when given, masked omitted with `HideMasked`; a `# PATH` line when the source changes and `# xml: NAME` when the XML changes.
- JSONL: one object per row; medewi keys as named (`t` number of seconds, `wall` UTC string or null, `src` `"PATH:N"` always, `src_end` null when equal, `glued` bool, `run`, `run_t`), report fields at top level (`Fields` limits them; `src` and `event` always kept), frames `bkl, key, layout, rows`, others `text`, decode error `error` + `text`.
- CSV: reports only; header `t, wall, src, src_end, run, run_t, pump` + the first report's XML keys, or exactly `Fields`; a later report whose spec has a different key set (no `Fields`) → `Write` returns an error telling to use `--fields`; a key the row's spec lacks → empty + a warning; decode errors skipped + a warning. `Bad` values print as masked plus a per-key warning in every format (text shows the raw substring).

- [ ] **Step 1: Write the failing tests**

```go
func TestTextKeysAndPump(t *testing.T)      // report line contains "pump=* mode=… temperature_pid1=93.5"
func TestTextHideMasked(t *testing.T)
func TestTextHeaders(t *testing.T)          // "# PATH" and "# xml: NAME" on change only
func TestJSONLReport(t *testing.T)          // unmarshal: pump==true, pressure==9, masked==nil, src=="p:3", wall ends "Z"
func TestJSONLFrameAndCmd(t *testing.T)     // frame has rows[4]; cmd has text
func TestJSONLFields(t *testing.T)          // Fields [pressure] → keys exactly {src, event, pressure}
func TestCSVHeaderAndRows(t *testing.T)     // Fields [run_t,pump,pressure] → header "run_t,pump,pressure"; pump "1"; masked ""
func TestCSVSpecChangeError(t *testing.T)
func TestCSVLatin1Valid(t *testing.T)       // profile string with 0xb0 → utf8.Valid output
func TestBadValueWarning(t *testing.T)      // Warnings() names the key once with a count
func TestWriteRunsText(t *testing.T)        // header "RUN  KIND  START …", draw PEAK_BAR "-"
```

- [ ] **Step 2–4:** `nix develop --command go test -race ./logview/ -run 'TestText|TestJSONL|TestCSV|TestBad|TestWriteRuns'` FAIL → implement → PASS.
- [ ] **Step 5: Commit** `feat(logview): text, JSON Lines and CSV writers`

### Task 10: levalog fields / runs / events

**Files:**
- Modify: `cmd/levalog/main.go` (split into `main.go`, `input.go`, `events.go`, `fields.go`, `runs.go` if it grows past ~300 lines), `cmd/levalog/main_test.go`
- Test: `cmd/levalog/main_test.go`

**Interfaces:**
- Consumes: everything above.
- Subcommands `fields`, `runs`, `events` (no `decode`; unknown subcommand → exit 2). Shared input flags: `--dir`, `-s/--since`, `-u/--until`, `--format`. `runs`: `--kind`. `events`: `--run`, `--run-kind`, `--margin` (systime span), `--event`, `--skip-standby`, `--fields`, `--hide-masked`, `--changes`, `--raw-values`, `--show-dumps`. `--xml` (global) accepts a file or directory, same env vars.
- Input: as today's `sourcePaths`; when files come from the dir and a run selection is active, select with `since − margin`, and while a selected run has `OpenAtEnd` and a later raw log exists in the dir, append the next file and reload.
- `fields` reads the same input and lists the columns of the XML in effect at the end of the last source (`Loaded`'s last entry's `Spec`/`XML`); with `--xml FILE` and no readable input it lists that file's columns.
- Without `--xml`: `fields`/`runs` exit 2; `events` exit 2 for `--fields`, `--run`, `--run-kind`, `--changes`, `--format csv`; otherwise raw as today with the existing warning. Unknown key in `--fields` → exit 2 listing it. `Problems` and writer `Warnings()` go to stderr after output.
- Help: the usage text explains the columns (the spec's "Columns medewi adds" table in short form: what medewi adds, that the machine's data starts after them, `src` meaning), the three subcommands, `--xml` file vs. directory, wall time zones, exit codes.

- [ ] **Step 1: Write the failing tests** (synthetic raw logs as today's tests build them)

```go
func TestEventsRenamed(t *testing.T)          // "decode" → exit 2 "unknown subcommand"
func TestFieldsSynthetic(t *testing.T)        // `--xml synthetic.xml fields` → lines for t … xml, then mode, temperature_pid1 …
func TestFieldsNoXML(t *testing.T)            // exit 2
func TestRunsAndEventsAgree(t *testing.T)     // runs --format jsonl numbers == events --run N rows' run
func TestEventsRunAcrossFiles(t *testing.T)   // shot spans two hourly files, -u inside the shot → whole shot printed
func TestEventsCSV(t *testing.T)              // header and one row exact
func TestEventsUsageWithoutXML(t *testing.T)  // --fields / --run / --format csv → exit 2
func TestEventsXMLDirTwoNoName(t *testing.T)  // capture without MC?, --xml DIR {a,b} → exit 0, raw reports, one stderr warning
func TestHelpExplainsColumns(t *testing.T)    // help mentions "src", "run_t", "medewi adds", "fields", "runs", "events"
func TestEventsFixtureCSV(t *testing.T)       // MEDEWI_XML_PATH (skip without): shot-genlever --run 2 --format csv --fields run_t,pump,pressure → first row run_t "0", pump "1"
```

- [ ] **Step 2: Run** `nix develop --command go test -race ./cmd/levalog/` — Expected: FAIL.
- [ ] **Step 3: Implement**; update the existing `TestDecodeCmd*` tests to `events` (keep their assertions where the format did not change; `Temperature PID1=` becomes `temperature_pid1=`).
- [ ] **Step 4: Run** the same (and the fixture test with the XML) — Expected: PASS.
- [ ] **Step 5: Commit** `feat(levalog): fields, runs and events subcommands`

### Task 11: docs and nix follow the rename

**Files:**
- Modify: `README.md:101-117`, `docs/architecture.md:67-70,113-122`, `docs/deployment.md:265-270`, `docs/cli-conventions.md:18,28,53`, `docs/runbooks/troubleshoot-connection.md:52-59`, `nix/module.nix:209`, `nix/vm-test.nix:104-108`

- [ ] **Step 1:** Replace `levalog decode` with `levalog events` everywhere listed; README gets a short `fields`/`runs`/`events` example block (the spec's agent session); architecture names logview's new stages and `rawlog`'s XML note; deployment mentions `--xml DIR` for keeping old XMLs after a firmware update. The VM test greps `'temperature_pid1='`.
- [ ] **Step 2: Run** `nix flake check -L` — Expected: all checks pass (the VM test included).
- [ ] **Step 3: Commit** `docs: levalog events, fields and runs; XML per session`
