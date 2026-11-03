# Phase 2 Daemon (medewid) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `cmd/medewid`, the always-on daemon that owns the machine connection, records every brew into SQLite, exports Prometheus metrics, offers wake/eco/standby and settings backups over an mTLS API, plus `cmd/medewictl` and a NixOS module.

**Architecture:** Pure cores first — `watch/` (ready/steam/eco detector, from the levawatch plan), `brew/` (pump-episode recorder), `store/` (SQLite) — then `daemon/`, which wires them: one owner goroutine reads reports and updates a mutex-guarded state snapshot; one worker goroutine serialises machine commands (power writes, dumps) so the report loop never blocks. `cmd/medewid` adds config, TLS and listeners; `cmd/medewictl` is a thin API client.

**Tech Stack:** Go (stdlib), `modernc.org/sqlite`, `github.com/prometheus/client_golang`; nix flake.

**Spec:** [docs/design/2026-11-02-phase2-daemon.md](../../design/2026-11-02-phase2-daemon.md) — including its "Planning amendments" section, which replaces the pressure-based episode trigger with the pump marker (report byte 2) and drops the cumulative-volume assumption. Volume-driven profiles are out of scope (issue #10). Tasks 1–2 are Tasks 1–2 of [2026-11-02-levawatch.md](2026-11-02-levawatch.md), executed verbatim.

## Global Constraints

- Everything in CLAUDE.md "Hard rules": no byte to 2323; pure Go, no cgo; TDD; full gate before every commit; no vendor XML content, no real dumps, no private hostnames in committed files.
- The only strings medewid ever sends: `MC?`, `MCr`, `MC ` (stream/handshake), `MCu`, `MCm` (backup), `MCa`, `MCe`, `MCE` (power). Nothing else, anywhere.
- Real vendor XML is never committed: tests needing real field offsets use `os.Getenv("MEDEWI_XML_PATH")` and `t.Skip` when unset (pattern: `conformance/real_fixtures_test.go`). CI-run tests use specs built in Go (`leva.RecordSpec{Values: …}`) with the real export names.
- Field names used (export names from the vendor XML): `Temperature PID1`, `Setpoint PID`, `Heat PID`, `Pressure`, `Pressure SP`, `Flow rate` (index 30, int, `factor="0.0166"`, ml/s), `Volume` (int, ml). The pump marker is raw payload byte `[2]`: `*` = leva! drives the pump. Power state comes from the raw payload bytes `[4:7]` (no export name): `PID` → active, `ECO` → eco, `???` → standby, anything else → unknown.
- Adding a Go dependency changes the flake's `vendorHash` (shared by all packages). The task that adds a dependency updates it in `flake.nix` (set it to `pkgs.lib.fakeHash`, run `nix build .#levacap`, copy the `got:` hash) and verifies with the full gate.
- Commits: `git -c commit.gpgsign=false commit`, conventional messages, trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Full gate (CLAUDE.md, from branch `nix-flake-checks`, which merges first): `nix flake check -L`. It only sees git-tracked files, so `git add` new files before running it. Tests that need `MEDEWI_XML_PATH` skip in the sandbox; run those via `nix develop --command go test …` as the task says.
- Logging: `log/slog`, text handler to stderr.

## Review Focus

1. **Standby after READY:** standby lines are `?`-masked, so the detector ignores them and `Ready()` stays true. The daemon's `ready` must be `detector.Ready() && power == active` — pinned in Task 7.
2. **Water that isn't a brew:** a hot-water draw or a boiler refill after steaming (flow with marker `-`), and static pressure rising to 9.9 bar after the pump stops, must never open or extend a brew — pinned in Task 5.
3. **Disconnect or restart mid-episode:** an open episode is closed on disconnect (classified on what it has); a brew left open by a crash is marked `interrupted` at startup — pinned in Tasks 5–7.
4. **Power request in the wrong situation** (brew open, disconnected, spec mismatch, already in the target state) — each gets its own status code and never sends a byte — pinned in Task 8.
5. **Truncated `MCm` dump** (no `:00000001FF` EOF record) must not produce a 200 or a backup file — pinned in Tasks 8 and 12.

---

### Task 1: Setpoint field in the synthetic spec

Execute levawatch plan Task 1 exactly as written, except that commit trailers follow this plan's Global Constraints.

### Task 2: watch package — the pure detector

Execute levawatch plan Task 2 exactly as written, including `(d *Detector) Ready() bool` and its test, with this plan's commit trailers.

### Task 3: leva — `factor` attribute

**Files:**
- Modify: `leva/spec.go`, `leva/record.go`, `leva/spec_test.go`, `leva/record_test.go`

**Interfaces:**
- Produces: `ValueSpec.Factor float64` (ParseSpec sets 1 when the attribute is absent); `Record.Float(name)` returns the parsed value × `Factor`, treating `Factor == 0` as 1, so Go-built specs and hand-built Records (Task 2's tests, Tasks 5–10) need no Factor.

- [ ] **Step 1: Write failing tests.** `TestParseSpecFactor`: XML with `<value at="1" len="3" export="Flow rate" factor="0.0166" type="int"/>` → `Values[0].Factor == 0.0166`; an entry without the attribute → `Factor == 1`; `factor="x"` and `factor="0"` → error. `TestRecordFloatScaled`: spec field with Factor 0.0166 and raw `"151"` → `Float` ≈ 2.5066 (tolerance 1e-9); `TestRecordFloatZeroFactor`: a field with `Factor` 0 and raw `"91.5"` → 91.5. Existing struct-equality expectations in `spec_test.go` gain `Factor: 1`. `TestRealSpecFlowFactor` (skips without `MEDEWI_XML_PATH`): the first `"Flow rate"` value has Index 30 and Factor 0.0166.
- [ ] **Step 2: Run** `nix develop --command go test -race ./leva/ -v` — FAIL.
- [ ] **Step 3: Implement.** `xmlValue` gains `Factor string \`xml:"factor,attr"\``; parse with `strconv.ParseFloat`; reject values that don't parse or are ≤ 0.
- [ ] **Step 4: Run** the same command — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(leva): scale values by the spec's factor attribute`.

### Task 4: leva — `CommandOpts` and firmware version

**Files:**
- Modify: `leva/conn.go`, `leva/conn_test.go`
- Create: `leva/id.go`, `leva/id_test.go`

**Interfaces:**
- Produces: `func (c *Conn) CommandOpts(ctx context.Context, cmd string, firstLine, idleGap time.Duration) ([]string, error)` — identical semantics to `Command`, with per-call timings; `Command` calls it with the configured values. `func FirmwareVersion(id string) string` — `"$id: leva! (3.2)"` → `"3.2"`; anything without a parenthesised suffix → `""`.

- [ ] **Step 1: Write failing tests.** `TestCommandOptsLongGap` (fakeServer replies `"a\r\n"`, sleeps `3*testGap`, then `"b\r\n"`; `CommandOpts(…, time.Second, 5*testGap)` returns `["a","b"]`, and `Command` on the same reply shape returns `["a"]`). `TestFirmwareVersion` table: `"$id: leva! (3.2)"`→`"3.2"`, `"$id: leva!"`→`""`, `""`→`""`, `"$id: leva! (3.2.1-beta)"`→`"3.2.1-beta"`.
- [ ] **Step 2: Run** `nix develop --command go test -race ./leva/ -v` — FAIL.
- [ ] **Step 3: Implement.** `waitIdle` takes the gap as a parameter instead of reading `c.cfg.idleGap`; the quiet-drain before sending keeps the configured gap.
- [ ] **Step 4: Run** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(leva): per-call command timings; firmware version from banner`.

### Task 5: brew — the pump-run recorder

**Files:**
- Create: `brew/recorder.go`, `brew/recorder_test.go`, `brew/fixture_test.go`
- Modify: `conformance/conformance.go`, `conformance/conformance_test.go` (marker check); the synthetic report sources, which have a space at byte 2 today and would fail the new check: `synthLine` in `conformance/conformance_test.go` and `cmd/levacap/main_test.go` (set `line[2] = '-'`), and `testdata/fixtures/synthetic/log.jsonl` (rewrite byte 2 of each RX report to `-` with a one-off script; leave `T`, `dir` and every other byte unchanged)

**Interfaces:**
- Produces:
  - `type Sample struct { At time.Time; Pumping bool; Temp, Setpoint, Pressure, PressureSP, Flow, Heat, Volume *float64 }` — `Pumping` is payload byte 2 == `*`; nil means masked or missing; `Flow` is already factor-scaled (ml/s).
  - `type Kind string` with `Shot Kind = "shot"`, `Flush Kind = "flush"`, `Interrupted Kind = "interrupted"`.
  - `type Config struct { CoastQuiet, CoastMax, WaterQuiet time.Duration; MinWaterML float64 }`; `func DefaultConfig() Config` → `{2 * time.Second, 3 * time.Second, 2 * time.Second, 5}`, each with an `// ASSUMPTION:` comment (after a run the counter adds 1–2 ml within ≈1.5 s; the captured wand draw grew 144 ml steadily).
  - `type Summary struct { StartedAt, EndedAt time.Time; Kind Kind; Duration time.Duration; PeakBar, VolumeML float64 }`
  - `type WaterDraw struct { Duration time.Duration; VolumeML float64 }`
  - `type Result struct { Opened bool; Points []Sample; Closed *Summary; Water *WaterDraw }`
  - `type Recorder struct`; `func NewRecorder(cfg Config) *Recorder`; `func (r *Recorder) Feed(s Sample) Result`; `func (r *Recorder) Abort() *Summary` (for a disconnect: a brew still pumping closes as `Interrupted`, a coasting one closes with its normal Kind, nil if none; an open wand draw is dropped and the remembered previous Volume is forgotten, so water drawn while disconnected never shows up as a draw); `func (r *Recorder) Open() bool`.
- Rules. The flow field is a decaying rate estimate (it lingers for seconds after the pump stops), so it never decides anything; the Volume counter growing does.
  - **Brew:** a `Pumping` sample while no brew is open opens one (`Opened`); it and every following `Pumping` sample are returned in `Points`. When the marker drops, the brew coasts: non-pumping samples are not points, but Volume growth still counts toward the brew. A `Pumping` sample during coasting continues the same brew, unless its Volume is below the brew's last Volume (the counter resets at every leva! pump start): then the old brew closes and a new one opens in the same `Result`. The brew closes (`Closed`) on the first coasting sample at which Volume hasn't grown for `CoastQuiet` or `CoastMax` has passed since the last pumping sample. Summary: `StartedAt`/`EndedAt` = first/last pumping sample, `PeakBar` = max Pressure over the points, `VolumeML` = max Volume seen during the run and the coast, Kind `Shot` if any point had `PressureSP != nil`, else `Flush`. `// ASSUMPTION:` a wand draw started within the coast (e.g. hot water for an Americano right after a shot) adds what it draws until `CoastMax` to the brew's volume (growth keeps the coast open, so up to 3 s, ≈24 ml at ≈8 ml/s) — accepted.
  - **Wand draw:** while no brew is open or coasting, a non-pumping sample whose Volume is greater than the previous non-nil Volume opens a draw or extends it. `VolumeML` sums these increments; a decrease rebases without counting. The draw closes on the first sample at least `WaterQuiet` after its last increment and is returned in `Water` if `VolumeML ≥ MinWaterML` (smaller ones are coast leftovers and are dropped). `Duration` = last − first increment. A `Pumping` sample closes an open draw first (both results in one `Result`).

- [ ] **Step 1: Write failing tests** on crafted samples at 250 ms spacing:
  - `TestShotRun`: 30 s `Pumping` with PressureSP set and 9 bar → one Opened, one Closed of kind Shot, Duration 30 s, PeakBar 9.
  - `TestFlushRun`: 20 s `Pumping`, PressureSP nil, 0.05 bar → Flush.
  - `TestStaticPressureAfterRun`: a shot, then non-pumping samples while pressure ramps 4→9.9 bar → PeakBar is the shot's peak and none of those samples are points (Review Focus 2).
  - `TestStaticPressureNoRun`: 60 s at 10 bar, not pumping, flow 0 → nothing.
  - `TestWandDrawNotABrew`: 20 s not pumping, Volume 72→216 → no Opened, exactly one Water with VolumeML 144 (Review Focus 2).
  - `TestStaleFlowNoDraw`: after a run, Flow stays at 2 ml/s for 5 s with Volume constant → no Water.
  - `TestCoastVolume`: a run ends at Volume 100, and the counter reaches 101 one second into the coast → VolumeML 101 and no Water.
  - `TestCounterDecreaseRebases`: 72→80, 3, 3→10 → VolumeML 15.
  - `TestVolumeIsRunMax`: Volume 0→101, then holds → 101.
  - `TestAbort`: Abort mid-shot → Interrupted; Abort while coasting after a shot → Shot; Abort when idle → nil; after Abort, a first sample with a higher Volume is not a draw.
  - `TestBackToBackRuns`: a run, the marker drops for 1 s, then `*` again with Volume reset to 0 → two brews.
  - `TestShotThenFlush`: the stretch transcribed from a real coldstart capture (not committed; only these numbers are). Shot tail: `Pumping` with PressureSP set, Volume 82, 83, 83; then marker `-` with Volume 84 ×5, 85 from the 6th coasting sample on. Flush: `Pumping`, PressureSP nil, Volume 0 on its first sample, then +1 per sample to 73; then `-` with Volume 73. Play it twice:
    - with the captured gap (flush starts 9.3 s after the shot's last pumping sample);
    - with the flush starting 2.5 s after it, inside the coast.

    Both variants must give the same result: brews [Shot (VolumeML 85), Flush (VolumeML 73)] and no Water.

  `TestFixtureRuns` in `fixture_test.go` (skips without `MEDEWI_XML_PATH`): join the RX chunks of `testdata/fixtures/shot-genlever` and `steam-eco`, split on CRLF, timestamp each line with the `T` of the chunk that contains its CRLF, decode with the vendor spec, and expect:
  - genlever → brews [Flush (VolumeML 73), Shot (PeakBar in [8.9, 9.1], VolumeML 101)] and no Water.
  - steam-eco → no brews and one Water with VolumeML 144.

  In conformance: the `reports-decode` check fails when a report's byte 2 is not one of `*`, `-`, `?`; its test gets a bad-marker case.
- [ ] **Step 2: Run** `nix develop --command go test -race ./brew/ ./conformance/ -v` — FAIL. Run it again with `MEDEWI_XML_PATH` from the local `.envrc`: the fixture test must also run and fail.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run both variants** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(brew): pump-marker run recorder with wand-draw accounting`.

### Task 6: store — SQLite brew database

**Files:**
- Create: `store/store.go`, `store/store_test.go`
- Modify: `go.mod`, `go.sum`, `flake.nix` (vendorHash)

**Interfaces:**
- Consumes: `brew.Sample`, `brew.Summary`, `brew.Kind`.
- Produces:
  - `func Open(path string) (*Store, error)` — DSN `file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)`, driver `sqlite` (`modernc.org/sqlite`); creates the schema when `schema_version` is empty and stores version 1; refuses (error) a version it doesn't know.
  - `func (s *Store) Close() error`
  - `func (s *Store) BeginBrew(ctx, startedAt time.Time) (int64, error)`
  - `func (s *Store) AddPoints(ctx, brewID int64, startedAt time.Time, pts []brew.Sample) error` — one transaction; `t_ms = pt.At − startedAt` in ms; nil fields → NULL.
  - `func (s *Store) FinishBrew(ctx, id int64, sum brew.Summary) error`
  - `func (s *Store) CloseDangling(ctx) (int64, error)` — sets `kind='interrupted'` and `ended_at=started_at` on rows where `ended_at IS NULL`; returns the count.
  - `type Brew struct { ID int64; StartedAt time.Time; EndedAt *time.Time; Kind string; DurationS, PeakBar, VolumeML float64; Notes string }`; `func (s *Store) Brews(ctx, limit int) ([]Brew, error)` (newest first); `type Point struct { TMs int64; Temp, Setpoint, Pressure, Flow, Heat *float64 }`; `func (s *Store) Points(ctx, brewID int64) ([]Point, error)` (by `seq`; `ErrNotFound` for an unknown brew).
- Schema v1: `brews(id INTEGER PRIMARY KEY, started_at TEXT NOT NULL, ended_at TEXT, kind TEXT, duration_s REAL, peak_bar REAL, volume_ml REAL, notes TEXT NOT NULL DEFAULT '')`, `brew_points(brew_id INTEGER NOT NULL REFERENCES brews(id), seq INTEGER NOT NULL, t_ms INTEGER NOT NULL, temp REAL, setpoint REAL, pressure REAL, flow REAL, heat REAL, PRIMARY KEY(brew_id, seq))` — `seq` counts per brew, because buffered reports drained after a slow write can share a millisecond, `schema_version(version INTEGER NOT NULL)`. Timestamps are RFC 3339 in UTC with nanoseconds.

- [ ] **Step 1: Write failing tests** (temp-dir DB): `TestRoundTrip` (Begin, AddPoints with one nil field and two points with the same `At`, Finish → Brews/Points return all points in order, nil stays nil); `TestReopenKeepsData`; `TestCloseDangling` (an unfinished brew → count 1, kind `interrupted` — Review Focus 3); `TestUnknownSchemaVersion` (set version 99 by hand → Open fails); `TestBrewsLimitNewestFirst`; `TestPointsUnknownBrew` → `ErrNotFound`.
- [ ] **Step 2: Run** `nix develop --command go test -race ./store/ -v` — FAIL.
- [ ] **Step 3: Implement**; `go get modernc.org/sqlite`; update the flake `vendorHash` (Global Constraints).
- [ ] **Step 4: Run** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(store): SQLite brew database`.

### Task 7: daemon — connection owner and state

**Files:**
- Create: `daemon/daemon.go`, `daemon/state.go`, `daemon/events.go`, `daemon/daemon_test.go`, `daemon/helpers_test.go`
- Modify: `sim/sim.go`, `sim/sim_test.go` (stream switching, needed again in Task 8)

**Interfaces:**
- Consumes: `leva.Dial/WaitReady/FirmwareVersion`, `Conn.Command/StartReports/StopReports/ID/Close/Err`, `leva.VerifyChecksum`, `RecordSpec.Decode`, `Record.Float`, `watch.NewDetector/Feed/Ready`, `brew.NewRecorder/Feed/Abort`, `store.*`.
- Produces:
  - `sim.Config.Switch map[string][]fixture.Event` — receiving a command that is a key replaces the replay stream with that value. If reporting is on, the current replayer stops and a new one starts on the new stream; otherwise the new stream is used at the next `MCr`.
  - `sim.Config.Once bool` — replay the stream once instead of looping (after the last event the replayer exits; reporting stays on). Daemon tests use it so "exactly one brew" doesn't depend on timing.
  - `func (s *Server) Received() []string` — every command line received from any client, CR+LF stripped, in order (a copy). Tests assert on it to prove what was or wasn't sent.
  - `type PowerState string` with `PowerActive="active"`, `PowerEco="eco"`, `PowerStandby="standby"`, `PowerUnknown="unknown"`; `func powerFromPayload(p []byte) PowerState`.
  - `type Config struct { Addr string; Spec *leva.RecordSpec; SpecName string; Store *store.Store; Brew brew.Config; Watch watch.Config; EventExec string; Settle, BackoffMin, BackoffMax time.Duration }` (Task 9 adds `Registry prometheus.Registerer`).
  - `func New(cfg Config) *Daemon`; `func (d *Daemon) Run(ctx context.Context) error` — returns when ctx is done, after `StopReports` (bounded by 2 s, via `context.WithoutCancel`), `Close`, and `Recorder.Abort` with any summary stored.
  - `type Status struct { Connected, SpecOK, Ready, BrewOpen bool; Power PowerState; Firmware string; Temp, Setpoint *float64; LastBrew *store.Brew }`; `func (d *Daemon) Status() Status` (a copy under RWMutex).
- Behaviour:
  - Startup: `store.CloseDangling`, then the reconnect loop. Before the first attempt, resolve the field names from Global Constraints against `Spec`; each missing one logs a warning once and that value stays nil (e.g. without `Pressure SP` every brew is a flush) — never fatal. Each attempt: Dial → `WaitReady` with `Settle` (default 1500 ms) bounded by a 10 s context. On timeout (the stream may still be on from a daemon killed without `MC `, and banners are silent while reporting), send `MC `, close, and retry with backoff. Then `Command("MC?")` up to 3 times, 2 s apart, until it returns an `XML=` line (the machine swallows early commands; one unanswered `MC?` is in `warmup-stream`). No `XML=` line after 3 tries: close and reconnect with backoff, `SpecOK` stays false. An `XML=` value that differs from `SpecName`: set `SpecOK=false`, log at error level, don't start reports, and hold the connection until ctx is done or it ends — the only terminal state. On a match: `StartReports`, then for each line: VerifyChecksum → payload; power from the payload; Decode; feed the detector, then the recorder with a `brew.Sample` stamped `time.Now()` (`Pumping` from payload byte 2). `Opened` → `BeginBrew`; `Points` → `AddPoints`; `Closed` → `FinishBrew` and refresh `LastBrew`; `Water` → metrics only (Task 9), plus a debug log line. Store errors are logged, never fatal.
  - Decode errors are counted and logged at debug; the line is skipped.
  - On disconnect, call `Recorder.Abort` (Task 5) before reconnecting.
  - When the connection ends: `Abort` → `FinishBrew` (Interrupted); `Connected=false`, `Power=unknown`, a fresh detector; backoff doubles from `BackoffMin` to `BackoffMax` (defaults 1 s, 30 s) and resets after a successful `StartReports`.
  - `Ready` in Status is `detector.Ready() && Power == PowerActive` (Review Focus 1).
  - Events (`events.go`): each `watch.Event` is logged as `slog.Info("event", "kind", k, "temp", t, "setpoint", sp)`. If `EventExec` is set, it runs via `/bin/sh -c` (absolute path: NixOS services have no shell on PATH) with env `MEDEWI_EVENT`, `MEDEWI_TEMP`, `MEDEWI_SETPOINT` (format `%.1f`), in its own goroutine. A non-zero exit is logged and never fatal.

- [ ] **Step 1: Write failing tests.** `helpers_test.go` builds a Go-side spec with the real export names at synthetic offsets, plus a `line(marker byte, mode string, temp, sp, pressure, psp, flowRaw, volume float64) []byte` helper that renders a report line for that spec (marker at `[2]`, mode at `[4:7]`; a negative psp renders as `?????`), and a `stream(lines ...[]byte) []fixture.Event` at 250 ms spacing. Sim with `BannerInterval: 30ms`, `Responses: {"MC?": {"XML=test"}}`, `Speed: 10`, daemon `Brew` config `{CoastQuiet: 200ms, CoastMax: 300ms, WaterQuiet: 300ms, MinWaterML: 5}`, `Settle: 100ms`. Tests:
  - `TestStatusFollowsStream`: Status reaches Connected, SpecOK, Power active, Temp from the stream.
  - `TestRecordsShotEndToEnd`: a `*` run with psp set → one stored brew of kind shot with points; a following `-` stretch with a growing Volume (wand draw) → still exactly one brew.
  - `TestMCqUnansweredRetries`: a sim with no `MC?` response (retry spacing shrunk via package vars) → SpecOK stays false, the daemon keeps reconnecting, `Received()` shows more than 3 `MC?` and never `MCr`.
  - `TestMissingFieldWarns`: a spec without `Pressure SP` → Run doesn't fail; a `*` run is stored as a flush.
  - `TestSpecMismatchDegrades`: `XML=other` → Connected, SpecOK=false, and Power stays unknown for 1 s (no reports were started).
  - `TestReadyDropsInStandby`: ready ramp, then `???` lines → Ready false (Review Focus 1).
  - `TestDisconnectMidShotInterrupted`: stop the sim mid-episode → stored brew kind interrupted; restart the sim on the same port → Connected again (Review Focus 3).
  - `TestEventExecRuns`: an exec hook appends `$MEDEWI_EVENT` to a temp file → it contains `READY`.
  - `TestDanglingClosedAtStartup`.
  - In `sim_test.go`: `TestSwitchReplacesStream`, `TestOnceDoesNotLoop`, `TestReceivedLogsCommands`.
- [ ] **Step 2: Run** `nix develop --command go test -race ./daemon/ ./sim/ -v` — FAIL.
- [ ] **Step 3: Implement** the sim switch, then the daemon.
- [ ] **Step 4: Run** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(daemon): connection owner, brew recording, state and events`.

### Task 8: daemon — command worker: power and dumps

**Files:**
- Create: `daemon/commands.go`, `daemon/commands_test.go`
- Modify: `daemon/daemon.go`

**Interfaces:**
- Produces:
  - `var ErrBrewOpen, ErrDisconnected, ErrSpecMismatch, ErrVerifyFailed, ErrIncompleteDump error`
  - `func (d *Daemon) SetPower(ctx context.Context, want PowerState) (PowerState, error)` — returns the observed state.
  - `type Dump struct { Lines []string; Firmware string; Done time.Time }`; `func (d *Daemon) DumpMCu(ctx) (Dump, error)`; `func (d *Daemon) DumpMCm(ctx) (Dump, error)`.
- Behaviour: requests go over one channel to a single worker goroutine started by `Run`, which holds the current `*leva.Conn`. Checks happen in this order, before anything is sent: disconnected → `ErrDisconnected`; brew open → `ErrBrewOpen`; for power only, `!SpecOK` → `ErrSpecMismatch` and an already-current state → success with nothing sent. Power: active→`MCa`, eco→`MCe`, standby→`MCE`, via `Command`. Then poll `Status().Power` every 250 ms for up to 10 s (`verifyTimeout`, a package var for tests); a timeout gives `ErrVerifyFailed` together with the observed state. Dumps: `CommandOpts(ctx, cmd, 5*time.Second, 2*time.Second)` (package vars). An `MCm` dump whose last non-empty line isn't `:00000001FF` gives `ErrIncompleteDump`. `Firmware` comes from `leva.FirmwareVersion(conn.ID())` (`"unknown"` if empty); `Done` is `time.Now().UTC()` after collection.

- [ ] **Step 1: Write failing tests** (sim `Switch` maps `MCe` to an `ECO` stream and `MCa` to a `PID` stream; `Responses` has fake `MCu`/`MCm` dumps): `TestSetPowerEcoVerified`; `TestSetPowerVerifyTimeout` (no Switch entry → `ErrVerifyFailed`, verifyTimeout shrunk); `TestSetPowerRefusals` — table for brew open, disconnected, spec mismatch and already-active; each asserts the error and that `sim.Received()` contains no `MCa`/`MCe`/`MCE` (Review Focus 4); `TestDumpMCm` (complete → lines, Firmware `3.2` from banner `$id: leva! (3.2)`); `TestDumpMCmTruncated` → `ErrIncompleteDump` (Review Focus 5); `TestDumpRefusedWhileBrewOpen`.
- [ ] **Step 2: Run** `nix develop --command go test -race ./daemon/ -run 'Power|Dump' -v` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(daemon): verified power commands and settings dumps`.

### Task 9: daemon — Prometheus metrics

**Files:**
- Create: `daemon/metrics.go`, `daemon/metrics_test.go`
- Modify: `daemon/daemon.go`, `go.mod`, `go.sum`, `flake.nix` (vendorHash)

**Interfaces:**
- Produces: `Config.Registry prometheus.Registerer` (nil = no metrics); metrics registered on it, with exactly these names. Gauges: `medewi_machine_connected`, `medewi_spec_mismatch`, `medewi_ready`, `medewi_boiler_celsius`, `medewi_setpoint_celsius`, `medewi_heater_percent`, `medewi_power_state{state}` (1 for the current state, 0 for the other three). Counters: `medewi_shots_total`, `medewi_flushes_total`, `medewi_pump_seconds_total` (summary Duration), `medewi_volume_liters_total` (brew VolumeML/1000 on close, plus wand-draw VolumeML/1000), `medewi_water_draws_total`, `medewi_water_liters_total`, `medewi_report_lines_total`, `medewi_decode_errors_total`, `medewi_reconnects_total`. Interrupted brews don't count as shots or flushes, but do add pump seconds and volume. Temperature gauges keep their last value when a field is masked.

- [ ] **Step 1: Write failing tests** with `prometheus.NewRegistry()` and `testutil.GatherAndCompare` / `testutil.ToFloat64`: `TestMetricsAfterShot` (shots_total 1, pump seconds > 0, volume liters = fixture volume/1000); `TestMetricsWandDraw` (water_draws_total 1, water liters added to volume liters, shots and flushes unchanged); `TestPowerStateOneHot`; `TestNilRegistryNoPanic`.
- [ ] **Step 2: Run** `nix develop --command go test -race ./daemon/ -run Metrics -v` — FAIL.
- [ ] **Step 3: Implement**; `go get github.com/prometheus/client_golang`; update `vendorHash`.
- [ ] **Step 4: Run** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(daemon): prometheus metrics`.

### Task 10: daemon — HTTP API and ops panel

**Files:**
- Create: `daemon/api.go`, `daemon/api_test.go`, `daemon/panel.html`

**Interfaces:**
- Produces: `func NewHandler(d *Daemon, metrics http.Handler) http.Handler` (Go 1.22 mux patterns):
  - `GET /api/status` → `Status` as JSON: `{"connected","spec_ok","ready","brew_open","power","firmware","temp","setpoint","last_brew"}`, where null means unknown.
  - `POST /api/power` with `{"state": "active"|"eco"|"standby"}` → 200 `{"ok":true,"state":…}`. Errors: invalid body or state → 400; `ErrBrewOpen` / `ErrSpecMismatch` → 409; `ErrDisconnected` → 503; `ErrVerifyFailed` → 504 `{"ok":false,"state":<observed>}`.
  - `GET /api/brews?limit=N` (default 20, max 500; invalid → 400) → JSON array of `{"id","started_at","ended_at","kind","duration_s","peak_bar","volume_ml","notes"}`.
  - `GET /api/brews/{id}/points` → JSON array of `{"t_ms","temp","setpoint","pressure","flow","heat"}`; `?format=csv` → `text/csv` with header `t_ms,temp,setpoint,pressure,flow,heat` and empty cells for null; unknown id → 404.
  - `GET /api/backup/mcu`, `GET /api/backup/mcm` → `text/plain`, lines joined with `\n`, headers `X-Medewi-Firmware` and `X-Medewi-Lines`. Errors: `ErrBrewOpen` → 409; `ErrDisconnected` → 503; `ErrIncompleteDump` → 502.
  - `GET /metrics` → `metrics` (omitted if nil); `GET /healthz` → 200 `ok`; `GET /` → the embedded `panel.html`.
  - `panel.html`: no external resources and no build step. It shows mode, temperature vs setpoint, a READY indicator and three buttons (wake / eco / standby) calling `/api/power`, polls `/api/status` every 2 s, and has a viewport meta for phones.
- No `PATCH /api/brews/{id}` in phase 2 (the design's optional item, deferred).

- [ ] **Step 1: Write failing tests** with `httptest` against a daemon on the sim: one per route, plus a status-code mapping table for the power and backup errors (Review Focus 4/5), the CSV null cell, `limit=0`/`limit=x` → 400, and a check that `GET /` contains `/api/power`.
- [ ] **Step 2: Run** `nix develop --command go test -race ./daemon/ -run API -v` — FAIL.
- [ ] **Step 3: Implement** (`//go:embed panel.html`).
- [ ] **Step 4: Run** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(daemon): HTTP API and ops panel`.

### Task 11: cmd/medewid — config, mTLS, listeners

**Files:**
- Create: `cmd/medewid/main.go`, `cmd/medewid/main_test.go`, `cmd/medewid/machine_test.go`

**Interfaces:**
- Produces: `run(args []string, stderr io.Writer, ready chan<- string, done <-chan struct{}) int` (levasim shape; `ready` receives the main listener's bound address, so tests can use `-listen 127.0.0.1:0`). Flags override env vars:

  | flag | env | default |
  |---|---|---|
  | `-machine` | `MEDEWID_MACHINE_ADDR`, else `MEDEWI_MACHINE_ADDR` | required |
  | `-xml` | `MEDEWID_XML_PATH`, else `MEDEWI_XML_PATH` | required |
  | `-db` | `MEDEWID_DB` | `/var/lib/medewid/medewi.db` |
  | `-listen` | `MEDEWID_LISTEN` | `:8443` |
  | `-tls-cert` / `-tls-key` / `-tls-client-ca` | `MEDEWID_TLS_CERT` / `_KEY` / `_CLIENT_CA` | — |
  | `-health-addr` | `MEDEWID_HEALTH_ADDR` | `127.0.0.1:9114` |
  | `-event-exec` | `MEDEWID_EVENT_EXEC` | — |
  | `-insecure-http` | — | false |

- `SpecName` is the XML file's base name without its extension.
- TLS: all three TLS settings set → `tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}`. Only some of them set → exit 2. None set and no `-insecure-http` → exit 2 with the message `refusing to serve without TLS (pass -insecure-http to override)`.
- The health listener serves only `/healthz`, in plaintext.
- Shutdown on `done`/SIGTERM/SIGINT: `http.Server.Shutdown` (5 s), the daemon's Run returns, then the store closes.

- [ ] **Step 1: Write failing tests:** `TestRefusesWithoutTLS`; `TestPartialTLSConfig` → 2; `TestMTLSRequiresClientCert` (CA, server and client certs generated in-test with `crypto/x509` + ecdsa; without a client cert the request fails, with one `/api/status` → 200); `TestHealthPlaintext`; `TestEnvFallback` (`MEDEWI_MACHINE_ADDR` is used when `MEDEWID_MACHINE_ADDR` is unset). `machine_test.go`: `TestMachineSmoke`, double-gated (`MEDEWI_MACHINE_ADDR` + `MEDEWI_MACHINE_CONFIRM=1`), insecure-http on localhost; it asserts Connected and SpecOK within 15 s and sends no power command.
- [ ] **Step 2: Run** `nix develop --command go test -race ./cmd/medewid/ -v` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(medewid): daemon binary with mTLS and health listener`.

### Task 12: cmd/medewictl

**Files:**
- Create: `cmd/medewictl/main.go`, `cmd/medewictl/main_test.go`

**Interfaces:**
- Produces: `run(args []string, stdout, stderr io.Writer) int`. Env: `MEDEWID_URL` (e.g. `https://brewhost:8443`), `MEDEWID_CTL_CERT`, `MEDEWID_CTL_KEY`, `MEDEWID_CTL_CA`; flags `-url -cert -key -ca` override them. Subcommands:
  - `status` → one `key: value` line per Status field.
  - `power active|eco|standby` → exit 0 when ok; 1 otherwise, printing the server's state or error.
  - `backup -out DIR` → GET mcu, then mcm. Each file is written atomically (temp file + rename) as `<ts>_<fw>.mcu.txt` / `<ts>_<fw>.mcm.hex`, where `<ts>` is the client's `time.Now().UTC().Format("2006-01-02T150405Z")` taken after each response finishes and `<fw>` comes from `X-Medewi-Firmware`. Each written path and its `X-Medewi-Lines` count go to stderr. With `-out -`, both bodies go to stdout, each preceded by a line `==> <filename> <==`. A non-200 response writes no file and exits 1.

- [ ] **Step 1: Write failing tests** against an `httptest.NewTLSServer` stub that serves canned responses: `TestBackupFileNames` (regexp `^\d{4}-\d{2}-\d{2}T\d{6}Z_3\.2\.mcu\.txt$` and the `.mcm.hex` twin; contents match); `TestBackupStdout`; `TestBackupMCmFailureWritesNothing` (502 → exit 1, the directory has no `.mcm.hex` and no temp leftovers — Review Focus 5); `TestPowerFailureExit1`; `TestStatusOutput`.
- [ ] **Step 2: Run** `nix develop --command go test -race ./cmd/medewictl/ -v` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS. Then run the full gate.
- [ ] **Step 5: Commit** — `feat(medewictl): API client for status, power and backups`.

### Task 13: Packaging, NixOS module, docs

**Files:**
- Modify: `flake.nix`, `.github/workflows/ci.yml`, `README.md`, `docs/architecture.md`
- Create: `nix/module.nix`

**Interfaces:**
- Produces:
  - `packages.x86_64-linux.medewid` and `.medewictl` (via the existing `build` helper).
  - `nixosModules.medewid` with options `services.medewid.{enable, package, environmentFile, listen, healthAddr}`. It defines a systemd service with `DynamicUser=true`, `StateDirectory=medewid`, `EnvironmentFile=` (machine address, XML path and TLS paths live there), `Restart=always`, `RestartSec=5`, `ExecStart=${package}/bin/medewid -listen … -health-addr …`.
  - The module defaults `package` to `self.packages.${system}.medewid` (`outputs` already binds `self`).
  - The new packages join `checks` automatically (it includes every package); CI already runs `nix flake check`.

- [ ] **Step 1: Implement** the flake outputs and the module. The module must evaluate in the check: add `checks.x86_64-linux.medewid-module` = `pkgs.writeText "medewid-module-eval" (builtins.toJSON eval.config.systemd.services.medewid.serviceConfig)`, where `eval` is `nixpkgs.lib.nixosSystem { system = "x86_64-linux"; modules = [ self.nixosModules.medewid { services.medewid.enable = true; services.medewid.environmentFile = "/dev/null"; fileSystems."/" = { device = "none"; fsType = "tmpfs"; }; boot.loader.grub.enable = false; system.stateVersion = "25.11"; } ]; }`. This forces evaluation of the unit without building a system closure; it must be a derivation, not a string.
- [ ] **Step 2: Run** `nix flake check -L` — success, with `medewid`, `medewictl` and `medewid-module` among the checks.
- [ ] **Step 3: Docs.** README gets a "Daemon" section covering the env vars, the TLS requirement, `medewictl backup -out DIR` and the file-name scheme. `docs/architecture.md` marks phase 2 as implemented and lists the new packages.
- [ ] **Step 4: Run** the full gate (it is Step 2 again after the docs change; the docs alone leave it cached).
- [ ] **Step 5: Commit** — `feat(nix): medewid packages and NixOS module`.
