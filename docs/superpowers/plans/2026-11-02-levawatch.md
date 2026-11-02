# levawatch (Issue #6, notifications only) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This plan is written to be executable by a small model: every decision is pinned; do not improvise beyond it.

**Goal:** A standalone observer binary, `cmd/levawatch`, that follows the machine's report stream and fires user-visible notifications for READY, STEAM WINDOW and ECO — the observation half of issue #6.

**Scope fence:** OBSERVATION ONLY. No write commands of any kind (no `MCa`, no setpoint changes); the steam-assist write feature stays in issue #6 for the phase-2 daemon. levawatch sends exactly: nothing before `WaitReady`, then `MCr`, and `MC ` on clean shutdown.

**Architecture:** a pure detector (`watch/` package: Records in → Events out, no I/O, no clocks) wrapped by a thin binary that owns the connection (Dial → WaitReady → StartReports → decode → feed) and executes a user-supplied shell command per event. Notification transport is therefore the user's business (`notify-send`, `curl` to a push service, anything).

**Tech Stack:** unchanged (Go, stdlib only; existing gate).

**Spec:** GitHub issue #6 plus docs/procedure.md (steps 1, 9, 12). Empirical anchors: READY ≈ temp settled at the brew setpoint; steam window 113–119 °C at the sensor while the thermostat (not PID1) heats; eco = setpoint drop below the brew range, fields may be `?`-masked.

## Global Constraints

- Everything in the repo CLAUDE.md's Hard rules (2323, whitelist, double-gated machine tests, no vendor content, pure Go, TDD, gate before commit).
- `watch/` imports only stdlib + `github.com/thomasbach-dev/medewi/leva`.
- Commit trailers: `Co-Authored-By:` and `Claude-Session:` lines exactly as configured for the working session.

## Review Focus

1. `?`-masked eco lines (unparsable floats) must neither crash nor false-fire any event — pinned test in Task 2.
2. A reconnect must not stall event flow: detector state resets, and a re-fired READY after reconnect is accepted, documented behavior — pinned test in Task 3.
3. A failing `-exec` command is logged and never fatal — pinned test in Task 3.
4. Clean shutdown sends `MC ` exactly once and leaks no goroutines — pinned test in Task 3.
5. The steam window must not fire during a normal brew-temperature overshoot (temp must exceed setpoint + 5 °C) — pinned test in Task 2.

---

### Task 1: Setpoint field in the synthetic spec

**Files:**
- Modify: `testdata/spec/synthetic.xml`, `leva/spec_test.go`, `conformance/conformance_test.go` (value-count assertions only)

**Interfaces:**
- Produces: the synthetic spec gains `<value at="13" len="5" export="Setpoint PID" type="float" unit="&#xb0;C"></value>` as the entry AFTER "Temperature PID1" (document order: index 2). `at=13 len=5` lies inside the existing 54-byte synthetic lines (bytes there are spaces today, which decode to an empty Raw — existing fixtures stay valid).

- [ ] **Step 1: Amend the XML** (keep CRLF line endings and the raw 0xB0 comment byte intact — edit carefully, verify with `file testdata/spec/synthetic.xml` still reporting ISO-8859 + CRLF).
- [ ] **Step 2: Run** `nix develop --command go test -race ./leva/ ./conformance/ 2>&1 | tail -5` — expect FAILs only in value-count assertions (5 vs 6) and index spot-checks.
- [ ] **Step 3: Fix those assertions** (5 → 6; the spot-check of the entry previously at index 2+ shifts by one).
- [ ] **Step 4: Full gate** (CLAUDE.md command) — green.
- [ ] **Step 5: Commit** — `test: add Setpoint PID to the synthetic spec`.

### Task 2: watch package — the pure detector

**Files:**
- Create: `watch/detector.go`, `watch/detector_test.go`

**Interfaces:**
- Produces:
  - `type Kind int` with `const (Ready Kind = iota; SteamWindow; Eco)` and `func (k Kind) String() string` returning `"READY"`, `"STEAM_WINDOW"`, `"ECO"`.
  - `type Event struct { Kind Kind; Temp, Setpoint float64 }`
  - `type Config struct { ReadyBand float64; ReadyCount int; SteamLow, SteamHigh float64; BrewSetpointMin float64 }` with `func DefaultConfig() Config` returning `{0.5, 10, 113, 119, 85}`.
  - `func NewDetector(cfg Config) *Detector`; `func (d *Detector) Feed(rec leva.Record) []Event` — pure state machine, no time, no I/O.
- Field access: `rec.Float("Temperature PID1")` and `rec.Float("Setpoint PID")`; if EITHER returns ok=false (masked `?` fields, short line), `Feed` returns nil and MUST NOT change episode state.
- Rules (pin exactly):
  - **READY:** fires when setpoint ≥ BrewSetpointMin AND |temp−setpoint| ≤ ReadyBand for ReadyCount consecutive fed records. Fires once per episode; the episode re-arms when |temp−setpoint| > 2.0 OR setpoint < BrewSetpointMin.
  - **STEAM_WINDOW:** fires when temp ≥ SteamLow AND temp > previous fed temp (rising) AND temp > setpoint + 5. Once per episode; re-arms when temp < SteamLow − 3.
  - **ECO:** fires when setpoint transitions from ≥ BrewSetpointMin (previous fed record) to < BrewSetpointMin. Once per transition.
  - One `Feed` call may return multiple events only if genuinely simultaneous; otherwise a single-element or nil slice.

- [ ] **Step 1: Write failing tests** — `TestReadyFiresOnceAndRearms` (ramp 79→91, hold ReadyCount in band → exactly one Ready; dip to 85 and return → second Ready), `TestReadyNeedsConsecutive` (band interrupted at count 9 → none), `TestSteamWindowOnRisingEdgeOnly` (climb 91→115 with setpoint 91 → one SteamWindow at ≥113; continued climb to 119 → no second; fall to 100, re-climb → second), `TestSteamNotDuringBrewOvershoot` (temp 93, setpoint 91 → nothing, even at count; Review Focus 5 needs temp>setpoint+5 — craft 95.5 vs 91 just below 113? pin: temp 114 with setpoint 110 — NOT > setpoint+5 → nothing), `TestEcoOnSetpointDrop` (91→79 → one Eco; 79→79 → none), `TestMaskedLinesIgnored` (records whose Float returns false between ramp records: no crash, counters unchanged — Review Focus 1). Build records via a local helper constructing `leva.Record{Fields: …}` directly.
- [ ] **Step 2: Run** `nix develop --command go test -race ./watch/ -v` — FAIL (undefined symbols).
- [ ] **Step 3: Implement** `watch/detector.go`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./watch/ -v` — PASS.
- [ ] **Step 5: Commit** — `feat(watch): pure ready/steam/eco detector`.

### Task 3: cmd/levawatch — connection loop and exec hook

**Files:**
- Create: `cmd/levawatch/main.go`, `cmd/levawatch/main_test.go`
- Modify: `README.md` (one short usage block under Build and test)

**Interfaces:**
- Consumes: `leva.Dial`, `leva.WaitReady` (settle 1500 ms package var, shrinkable in tests), `Conn.StartReports/StopReports/Close/Err`, `leva.LoadSpecFile`, `leva.VerifyChecksum` + `RecordSpec.Decode`, `watch.NewDetector/Feed`, `sim` for tests.
- Produces: binary `levawatch`; flags `-addr` (default `$MEDEWI_MACHINE_ADDR`), `-xml` (default `$MEDEWI_XML_PATH`, REQUIRED — exit 2 with a message if empty: events need temperatures), `-exec CMD` (optional; run via `sh -c` with env `MEDEWI_EVENT`, `MEDEWI_TEMP`, `MEDEWI_SETPOINT`; stdout/stderr inherited; a non-zero exit is logged to stderr, never fatal), `-steam-low`/`-steam-high` (defaults 113/119, override `watch.Config`).
- Behavior: testable `run(args []string, stdout, stderr io.Writer, done <-chan struct{}) int` (same shape as levasim). Loop: Dial (2323 refused by leva) → WaitReady → StartReports → for each report line: VerifyChecksum, Decode, Feed; print each event as one line `event READY temp=91.0 setpoint=91.0` to stdout and run `-exec`. On connection error: log, wait 5 s, reconnect with a FRESH detector (documented: READY may re-fire after reconnect). On `done`/SIGINT: StopReports (context.WithoutCancel + 2 s), Close, return 0.
- NEVER send anything except via WaitReady-gated StartReports/StopReports. No new command strings.

- [ ] **Step 1: Write failing tests** against `sim` (BannerInterval 30 ms; stream crafted on the synthetic spec with Setpoint at 13): `TestEmitsReadyFromRamp` (ramp lines → stdout contains `event READY`), `TestExecHookRuns` (`-exec` appends `$MEDEWI_EVENT` to a temp file → file contains `READY`; then an `-exec` that `exit 1`s → levawatch keeps running, stderr notes it — Review Focus 3), `TestReconnectAfterSimRestart` (stop sim, restart on same port… pin the simpler variant: close the client via sim Stop, start a second sim, point levawatch at the new addr is NOT possible with fixed flags — therefore pin: restart sim on the SAME addr (`127.0.0.1:0` is random; use an explicit `127.0.0.1:PORT` chosen by binding-then-closing a listener) and assert a second READY appears — Review Focus 2), `TestCleanShutdownStopsReports` (close `done`; sim received exactly one `MC ` — count via sim Responses? `MC ` is intercepted by the sim as stream-stop, not a canned response; assert instead that reporting stopped: after shutdown, sim's reporting flag is off — if not observable, assert via a second client that no reports flow — pin the second-client variant — Review Focus 4).
- [ ] **Step 2: Run** `nix develop --command go test -race ./cmd/levawatch/ -v` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: README usage block** (3 lines: what it does, one invocation example with `-exec 'notify-send "$MEDEWI_EVENT"'`).
- [ ] **Step 5: Full gate** (CLAUDE.md command, pipefail included) — green.
- [ ] **Step 6: Commit** — `feat(levawatch): ready/steam/eco notifications via exec hook`.
