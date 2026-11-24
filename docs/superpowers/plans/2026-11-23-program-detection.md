# Program Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `levalog programs` lists the leva! program runs in raw logs and captures, with status, evidence and figures.

**Architecture:** New pure package `program/` collects pulses and autotune windows from samples, then classifies them all in `Runs()`. `logview.ProgramFinder` feeds it entries; `cmd/levalog` adds the subcommand, `--settings` and `events --program`. The core rules decide; LCD and settings only change status (plus `lcd`-only runs).

**Tech Stack:** Go, pflag, existing `leva`, `logview`, `backup`, `systime` packages. No new dependency.

**Spec:** [2026-11-23-program-detection-design.md](../specs/2026-11-23-program-detection-design.md)

## Global Constraints

- Pure Go, no cgo. `program/` has no I/O and no clock.
- No machine contact, no change to `daemon/`, `brew/`, `store/`, `rawlog/` or `levalog runs` output.
- No machine settings or readings in committed files: synthetic values only, plus the already committed fixtures.
- TDD, conventional commits ending in the usual trailer, `git -c commit.gpgsign=false commit`; `nix develop --command gate` before every commit.
- cli-conventions: pflag, exit codes 0/1/2, errors as `levalog: …`.
- `const RuleVersion = 1` in `program/`; every rule change bumps it.

## Review Focus

- Log starts or ends mid-program: run is listed, outcome `cut`, no invented start. (Task 3)
- Full-power flushes in a row at an open group: no program. (Task 2)
- A disconnect between two purge pulses: two groups, never one. (Task 2)
- `-s/-u` cutting a program: the run is chosen by its start, printed whole. (Task 5)
- Backup that is damaged or from other firmware wording: exit 2 / a note, never a silent `conflict`. (Task 6)

---

### Task 1: Samples, pulses and autotune windows

**Files:** Create `program/doc.go`, `program/sample.go`, `program/pulse.go`; tests `program/sample_test.go`, `program/pulse_test.go`.

**Interfaces — Produces:**

```go
type Sample struct {
	At time.Time
	Ref any            // returned in Run.First/Last
	Pump, Standby bool // payload byte 2 '*', '?'
	Mode string        // payload[4:7]
	Temp, Setpoint, Heat, Phase, Pressure, PressureSP, Volume, Ticks, Kc, Ti, Td *float64
}
func SampleFrom(rec leva.Record, payload []byte, at time.Time, ref any) Sample
type Detector struct{ /* … */ }
func NewDetector() *Detector
func (d *Detector) Sample(s Sample)
func (d *Detector) Frame(at time.Time, rows [4]string)
func (d *Detector) Break() // disconnect or source boundary
// unexported, used by Task 2:
type pulse struct {
	first, last Sample
	phases, half2 []float64 // phase per sample; pressures of the 2nd half
	peak float64; shot, ramp bool
	cont *bool // nil: continuation unknown
	brk int    // break counter at its start
	openStart, openEnd bool // no non-pump sample before / after
}
```

Pulse rules: spec "Detection rules". Same pulse while marker is `*` on both sides, gap < 20 s and no Break. `cont` compares the first ticks with the last unmasked ticks before the pulse; a Break or previous value 0 makes it nil. `ramp`: at least 4 distinct setpoints with mode `PID` in the 20 s up to the pulse's end.

- [ ] **Step 1: failing tests** (a helper `seq` builds samples every 250 ms):
  - `TestSampleFrom`: synthetic XML as in `brew/sample_test.go`; mode, phase, ticks, masked `?` → nil.
  - `TestPulseGap`: 10 s gap with `*` both sides → 1 pulse; 20 s → 2; a `-` sample between → 2; `Break()` between → 2.
  - `TestContinuation`: ticks 100 → 101 true; 100 → 2 false; 0 → 1 nil; masked → nil; after Break nil.
  - `TestRamp`: setpoints 98.0, 97.9, 97.8, 97.7 in 20 s → ramp; 91→79 (eco) → not; same in mode `ATN` → not.
  - `TestHalf2`: pulse starting at 6 bar falling to 0.1 by its middle → `max(half2) < 1.0`.
- [ ] **Step 2:** `nix develop --command go test ./program/` → FAIL (undefined).
- [ ] **Step 3:** implement; field lookups follow `brew/sample.go` (`Phase`, `Kc PID`, `Ti PID`, `Td PID`; ticks = unnamed field with unit `ticks`).
- [ ] **Step 4:** tests PASS with `-race`.
- [ ] **Step 5:** gate, commit `feat(program): samples and pulses`.

### Task 2: Core rules

**Files:** Create `program/rules.go`, `program/run.go`; tests `program/rules_test.go`, `program/fixture_test.go`.

**Interfaces — Produces:**

```go
type Kind string // "warm-up" "autotune" "pressure-test" "flow-test" "backflush" "descale" "purge"
type Pulse struct {
	Start, End time.Time
	VolumeML *float64 // difference to the previous pulse when continued
	PeakBar float64; Phase *float64 // most common phase
	Stage string // "", "descaler", "purge"
}
type Run struct {
	Kind Kind; Start, End time.Time; First, Last any
	Pulses []Pulse; OpenStart, OpenEnd bool
	Outcome Outcome; Status Status; Evidence, Notes []string // Tasks 3, 4
	SettingsMatch string; Figures map[string]any             // Tasks 3, 6
}
func (d *Detector) Runs() []Run // sorted by Start; does not change d
```

Order: shot (has `PressureSP`, dropped), warm-up, pressure test (net fall > 0.8 of phase steps, > 8 distinct), flow test (net rise > 0.8), backflush (≥ 2 pulses, most common phase 0, max(half2) ≥ 1.0, gaps ≤ 10 min, same break counter), descale/purge (≥ 2 pulses phase 0, each later one `cont` true, same break counter; descale if any pulse is not `CLD` with setpoint 0). A non-matching pulse between two members ends the group. Warm-up runs from the setpoint jump before the ramp to the first 20 s of constant setpoint after it. Autotune is the `ATN` mode stretch, without pulses.

A counter reset between a descale group and a purge group with setpoint 0 keeps one descale run; its later pulses get `Stage` `purge`. Within a group, stage changes to `purge` when the ticks reset to 0 while the pump is off.

- [ ] **Step 1: failing tests**
  - `TestKinds`: one synthetic sequence per kind → exactly one run of it.
  - `TestNotPrograms`: 85° flush; three full-power flushes with reset counters at 0.1 bar; one blind-basket pulse; flush after a shot starting at 6 bar; 0.5 s phase-0 pulse; shot → no runs.
  - `TestBreakSplits`: 4 purge pulses with Break after 2 → two runs of 2.
  - `TestCounterReset`: descale then reset then setpoint-0 purge → one run, stages set; the same without setpoint 0 → descale and purge.
  - `TestWaitAnyLength`: 15 min pause between continued pulses → one run.
  - `TestMaskedTicks`: ticks masked for one pulse → group ends there.
  - `TestFixtures` (skips without `MEDEWI_XML_PATH`): `backflush` → 1 backflush with 9 pulses; `descale-tail` → 1 descale with 10 pulses, `OpenStart`; `shot-genlever`, `steam-eco`, `idle-stream` → none.
- [ ] **Step 2:** run → FAIL. **Step 3:** implement. **Step 4:** PASS, also `MEDEWI_XML_PATH` set outside the sandbox.
- [ ] **Step 5:** gate, commit `feat(program): core rules`.

### Task 3: Outcome and figures

**Files:** Create `program/outcome.go`, `program/figures.go`; tests beside them.

**Interfaces — Produces:** `type Outcome string` = `finished`, `aborted`, `cut`, `unknown`; keys of `Run.Figures` as below.

| Kind | finished | aborted | Figures |
|---|---|---|---|
| pressure test | phase reached 0 | sweep ended above 0 | `steps` [[phase, bar]] at each step's end |
| flow test | LCD `Test flow ✓` (Task 4) | – | `steps` [[phase, ml/s]] |
| autotune | Kc/Ti/Td after differ from before | – | `before`, `after` {kc,ti,td}; `periods_s` |
| warm-up | setpoint constant 20 s after ramp | – | `schedule` [[s from ramp start, duration_s, phase]] |
| descale | setpoint above 0 after the last pulse | – | – |
| backflush | – | – | `hold_bar` per pulse (median of the 2nd half); `leak_bar` [start, end] of the longest pump-off gap |
| purge | – | – | – |

Everything else is `unknown`. `cut` wins when the run touches a Break or the input's start or end.

- [ ] **Step 1: failing tests** `TestOutcome` (each table row, both ways), `TestCut` (input starts mid-program; ends mid-program; Break), `TestFigures` (synthetic pressure test 120→50 by 2, 50→0 by 5 → 46 steps; autotune 3 periods).
- [ ] **Step 2:** FAIL. **Step 3:** implement; `Sample` gains `Flow *float64` (field `Flow rate`, unit ml/s). **Step 4:** PASS.
- [ ] **Step 5:** gate, commit `feat(program): outcome and figures`.

### Task 4: LCD cross-check

**Files:** Create `program/lcd.go`, `program/lcd_test.go`; modify `program/run.go`.

**Interfaces — Produces:** `type Status string` = `confirmed`, `inferred`, `conflict`, `lcd`; `func frameKind(rows [4]string) (kind Kind, stage string, ok bool)`; kind `"flush"` is returned for `FLUSH` in row 1.

Matchers: row 1 starts with `WARM-UP` or `FLUSH`; row 3, after trimming a leading space or glyph, starts with `DESCALER ` + digit, `PURGE ` + digit, or is `PUMP` + bar / `SOAK ` + time / `EMPTY` (not `EMPTY TRAY!`) → backflush; `WAIT` in row 3 with `Test flow` or `Test pressure` in row 2 or 3 → that test. Menu screens never match: `Purge`, `Backflush`, `[1] FLUSH`, `>>Pump`, `Flush`.

LCD kinds inside a run: same kind → `confirmed` (evidence `lcd`); `PURGE` inside a descale agrees; any other kind → `conflict`, both kinds in Notes, run's kind stays. No frames → `inferred`. A pulse outside every run whose frames name a program → a run of its own, status `lcd`.

- [ ] **Step 1: failing tests** `TestFrameKind` (every matcher, every menu screen above), `TestStatus` (agree, conflict with `FLUSH`, no frames), `TestLCDOnly` (single purge pulse with `PURGE 250ml`).
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS; `backflush` fixture is now `confirmed`.
- [ ] **Step 5:** gate, commit `feat(program): LCD cross-check`.

### Task 5: `levalog programs` and `events --program`

**Files:** Create `logview/programs.go`, `logview/write_programs.go`, `cmd/levalog/programs.go`; modify `cmd/levalog/{main,help,input,events}.go`; tests beside them.

**Interfaces — Produces:**

```go
type Program struct { program.Run; N int; FirstPt, LastPt Point; XML string }
type ProgramFinder struct{ /* … */ }
func NewProgramFinder() *ProgramFinder
func (f *ProgramFinder) Source(s SourceInfo) // Break unless continues(prev, s, brew.DefaultConfig().CoastMax)
func (f *ProgramFinder) Entry(e *Entry)      // reports and frames
func (f *ProgramFinder) Programs(since, until time.Time) []Program // chosen by start, numbered from 1
func (p Program) Window() Run
func WritePrograms(w io.Writer, f Format, ps []Program, loc *time.Location) error
```

`Ref` is the entry's `Point`. Times as in `RunFinder` (wall clock, else offset; capture start estimated the same way). `scan` gets a `programs bool` and extends the input (to `maxExtend` files) while a listed program is open at the end. A program that began before the first file read is listed with outcome `cut`. Columns in spec order; jsonl also has `pulses_detail`, `figures`, `rule_version`; csv ends with `rule_version`.

`events --program N[,N-M]` uses `parseRuns` and `Window()`, excludes `--run`/`--run-kind` (exit 2); `run`/`run_t` then count from the program, and the help says so.

- [ ] **Step 1: failing tests** `TestProgramsFormats` (text, jsonl, csv headers and one row each on the `backflush` fixture), `TestProgramsWindow` (raw log pair: program crosses `-u` → whole; starts before `-s` → absent), `TestProgramsNone` (stderr `no programs in N reports`, exit 0), `TestEventsProgram` (first row `run_t` 0; with `--run` → exit 2), help text test as for `runs`.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5:** gate, commit `feat(levalog): programs and events --program`.

### Task 6: `--settings`

**Files:** Create `program/settings.go`, `program/settings_test.go`; modify `cmd/levalog/programs.go`, `cmd/levalog/programs_test.go`.

**Interfaces — Consumes:** `backup.Resolve`, `backup.MCu`, `backup.Setting.Path`. **Produces:** `type Settings struct` (warm-up steps {Time, Duration}; backflush Cycles, Soak, Rinse; descale Use, PID 1, Tray, Purge), `func SettingsFrom(u *backup.MCu) (Settings, []string)` (the strings name settings not found), `func (d *Detector) SetSettings(Settings)`.

Comparison: warm-up pulse starts within 5 s of a scheduled one counted from the ramp start, and at least half the scheduled steps seen; backflush pulses before the longest gap = Cycles, pauses = Soak ± 3 s, pump time after it = Rinse ± 10 %; descale setpoint = the used profile's PID 1 (`-` is 0). A purge-looking group at the profile's PID temperature in mode `PID` becomes `descale`. This one reclassification is the spec's; nothing else may create, drop or rename a run.

Agree → `confirmed`, evidence `settings`, `settings_match` `yes`. Differ → `conflict`, `no`, note `settings may be outdated`. Not comparable → empty. `cut` runs are not compared. Pump power is never compared. Tray and Purge are parsed but only used for `notes` (volume reached).

- [ ] **Step 1: failing tests** `TestSettingsFrom` (synthetic MCu via `backup/backuptest`; missing section → named, no panic), `TestSettingsCompare` (agree, differ, cut, for each kind), `TestDescaleProfile` (50° group → descale), `TestProgramsSettingsFlag` (damaged dump → exit 2 naming the first problem; file missing → exit 1).
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5:** gate, commit `feat(levalog): programs --settings`.

### Task 7: Docs, acceptance, usability

**Files:** Modify `docs/machine-behaviour.md`, `README.md`, `CLAUDE.md` (add `program/`), `docs/architecture.md`.

- [ ] **Step 1:** the spec's "Documentation fixes". The EEPROM 0x002F line is marked a hypothesis; no counter values.
- [ ] **Step 2: acceptance** on the private logs (nothing committed): `levalog programs --dir private/logs -s 2026-11-06 -u 2026-11-23` → 3 autotune, 1 pressure test, 1 flow test, 1 backflush, 1 descale, 2 purge, nothing else; `captures/warmup-program` → 1 warm-up. Report status and outcome of each to the operator; any difference is a bug or a logged rule change.
- [ ] **Step 3: usability run:** a fresh Haiku agent answers "which programs ran, and can I trust each?" with `--help` only, logging every step; fix the friction, run again.
- [ ] **Step 4:** `nix flake check -L` passes (new files added to git). Commit `docs: program detection`.
