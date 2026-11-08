# Release the machine — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `medewictl release [--until=+30min]` / `medewictl reclaim` make medewid give up and take back the machine connection, persisted across restarts; brews cut off by any disconnect become `truncated-<cause>`.

**Architecture:** The release end time lives in the brew DB (schema v2) and in `Status.ReleasedUntil`. Each `session` runs under its own cancel-cause context registered on the Daemon; `Release` persists, then cancels the session with `errReleased` and waits for it to finish (`MC `, banner check, close). `Run` waits out a release before every dial; `Reclaim` clears it and wakes the loop. The cause of a session's end picks the truncated kind passed to `Recorder.Abort`.

**Tech Stack:** Go (pure, no cgo), `modernc.org/sqlite`, `net/http`, Prometheus client, sim-based tests.

**Spec:** [docs/superpowers/specs/2026-11-08-release-machine-design.md](../specs/2026-11-08-release-machine-design.md) — read it first; this plan argues from it.

## Global Constraints

- Words: **release**/**reclaim** everywhere (code, API, CLI, metric, docs, UI). Never "pause"/"drop". Brew kinds `truncated-lost`, `truncated-released`, `truncated-shutdown`, `truncated-crash`; `interrupted` disappears from code and DB.
- No new machine strings: the daemon still sends only `MC?`, `MCr`, `MC `, `MC@`, `MCu`, `MCm`, `MCa`, `MCe`, `MCE`. While released it sends nothing, not even at startup or shutdown.
- No upper cap on a release; CLI default is one hour.
- Release/reclaim are refused (500) when the store write fails; nothing is freed / it stays released.
- Same protections as `POST /api/power`: `http.CrossOriginProtection`, `decodeJSON` (JSON content type, `maxBodyBytes`).
- TDD, conventional commits, one branch (`release-machine`) + one PR, linear history. Gate before every commit: `nix flake check -L` (`git add` new files first). Commit with `git -c commit.gpgsign=false commit`, trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Tests run against the sim only. Never the real machine, never port 2323.

## Review Focus

1. **Release racing a dial or `WaitReady`:** a release that lands while `session` is between `Dial` and `StartReports` must end that session without sending `MC?`/`MCr`, and no new dial may start until the release ends. Test: `TestReleaseDuringConnect` (Task 4).
2. **Restart while released:** a daemon started on a DB holding a future release must not dial at all (the sim's connection count stays 0) and must dial once the time passes. Test: `TestReleasePersistsAcrossRestart` (Task 4).
3. **Release with an expired persisted time at startup:** it has no effect; the daemon connects at once. Test: covered in `TestReleasePersistsAcrossRestart` (second subtest, Task 4).
4. **Display sampler in flight at release:** the stop must wait for the sampler, or an `MCr` after `MC ` leaves the machine streaming for the next client. Test: `TestReleaseWithDisplaySampling` asserts the last command the sim received is `MC ` (Task 4).
5. **Clock edge in the CLI:** `--until=+0s`, `--until=-5min`, `--until=30min` (no `+`), `--until=+5m`, `--until=+1h 30min` are usage errors (exit 2), never a request. Test: `TestParseUntil` table (Task 6).

---

### Task 1: Truncated brew kinds

**Files:**
- Modify: `brew/recorder.go` (Kind constants ~l.23-31, `Abort` ~l.226-241)
- Modify: `daemon/daemon.go` (`disconnected` call to `Abort`), `daemon/metrics.go` (`brewClosed` doc), `store/store.go` (`CloseDangling`)
- Test: `brew/recorder_test.go` (`TestAbort`), new `TestKindTruncated`; `store/store_test.go` (`TestCloseDangling`); daemon tests naming `brew.Interrupted`

**Interfaces:**
- Produces: `brew.TruncatedLost`, `brew.TruncatedReleased`, `brew.TruncatedShutdown`, `brew.TruncatedCrash` (`Kind` values `"truncated-lost"` … `"truncated-crash"`); `func (k Kind) Truncated() bool`; `func (r *Recorder) Abort(cut Kind) *Summary` — a pumping brew closes as `cut`, a coasting one with its normal kind. `Interrupted` is removed. `Store.CloseDangling` writes `brew.TruncatedCrash`.

- [ ] **Step 1: Write the failing tests.** `TestAbort/pumping` calls `h.r.Abort(TruncatedReleased)` and wants `s.Kind == TruncatedReleased`; other subtests pass `TruncatedLost`. `TestKindTruncated`: all four truncated kinds → `true`; `Shot`, `Flush`, `Kind("")`, `Kind("truncated")` → `false` (the bare legacy word is not produced anywhere, so it need not match; pin the exact set).
- [ ] **Step 2: Run** `nix develop --command go test ./brew/ -run 'TestAbort|TestKindTruncated'` — expect a compile failure (undefined kinds).
- [ ] **Step 3: Implement.** Constants with one-line docs naming the cause (spec "Words"). `Truncated()` is a switch over the four. `Abort(cut Kind)`; update its doc. In `daemon.disconnected` pass `brew.TruncatedLost` for now (Task 3 threads the real cause). `metrics.brewClosed` already ignores non-shot/flush kinds; reword its doc and the `pumpSeconds` help to "truncated ones included". Update `daemon/*_test.go` references to `brew.Interrupted` → `brew.TruncatedLost` (`TestDisconnectMidShotInterrupted` renamed `TestDisconnectMidShotTruncatedLost`, `TestStalledStreamReconnects`), `metrics_test.go` (`TestMetricsTruncatedAddsNoShot`). `CloseDangling` writes `brew.TruncatedCrash` (doc: "marks brews left open by a crash as truncated-crash"); `TestCloseDangling` and `TestDanglingClosedAtStartup` expect `truncated-crash`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./brew/ ./store/ ./daemon/` — PASS.
- [ ] **Step 5: Gate and commit** `feat(brew)!: truncated-<cause> kinds replace interrupted` (body: existing `interrupted` rows are deleted by the schema v2 migration in the next commit).

### Task 2: Store schema v2 — migration and release row

**Files:**
- Modify: `store/store.go` (`schemaVersion`, `migrate`, `CloseDangling`; add release methods)
- Test: `store/store_test.go`

**Interfaces:**
- Produces:
  - `schemaVersion = 2`; fresh DBs are created at v2, v1 DBs migrate in `Open`.
  - `func (s *Store) Release(ctx context.Context) (until time.Time, ok bool, err error)` — the persisted end time, `ok=false` if none (an expired one is returned as stored; the daemon judges it).
  - `func (s *Store) SetRelease(ctx context.Context, until time.Time) error` — upsert of the single row.
  - `func (s *Store) ClearRelease(ctx context.Context) error` — deletes it; no error if absent.

- [ ] **Step 1: Write the failing tests.**
  - `TestMigrateV1`: build a v1 file by hand (`sql.Open("sqlite", "file:"+path)`, exec `schemaV1`, `INSERT INTO schema_version VALUES (1)`, insert brew 1 kind `interrupted` with 2 `brew_points`, brew 2 kind `shot` with 1 point). `Open` succeeds; `Brews` returns only brew 2; `Points(ctx, 1)` → `ErrNotFound`; `Points(ctx, 2)` has 1 point; `SELECT MAX(version)` is 2; `Release` → `ok=false`. Re-`Open` the same file succeeds (idempotent).
  - `TestReleaseRoundTrip`: `Release` on a fresh DB → `ok=false`; `SetRelease(t1)` then `Release` → `t1` (compare with `Equal`, UTC); `SetRelease(t2)` replaces it; survives `Close`/`Open`; `ClearRelease` → `ok=false`; `ClearRelease` again → nil error.
  - `TestUnknownSchemaVersion` stays as is (version 99 is still refused).
- [ ] **Step 2: Run** `nix develop --command go test ./store/` — FAIL (undefined methods, version 1).
- [ ] **Step 3: Implement.** Keep `schemaV1` unchanged; add `schemaV2` = `CREATE TABLE IF NOT EXISTS machine_release (id INTEGER PRIMARY KEY CHECK (id = 1), until TEXT NOT NULL);`. In `migrate`, inside the one transaction: exec v1 DDL; read the version; no version → exec v2 DDL, insert 2; version 1 → `DELETE FROM brew_points WHERE brew_id IN (SELECT id FROM brews WHERE kind = 'interrupted')`, `DELETE FROM brews WHERE kind = 'interrupted'`, exec v2 DDL, `UPDATE schema_version SET version = 2`; version 2 → nothing; other → the existing error. Times stored with `ts()`, read with `time.RFC3339Nano`. `SetRelease`: `INSERT … ON CONFLICT(id) DO UPDATE SET until = excluded.until`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./store/ ./daemon/` — PASS.
- [ ] **Step 5: Gate and commit** `feat(store)!: schema v2 drops interrupted brews, stores the machine release` (body: the migration is irreversible).

### Task 3: Thread the truncation cause through the session

**Files:**
- Modify: `daemon/daemon.go` (`session`, `disconnected`)
- Test: `daemon/daemon_test.go`

**Interfaces:**
- Consumes: `Recorder.Abort(cut brew.Kind)`.
- Produces: `func (d *Daemon) disconnected(ctx context.Context, conn *leva.Conn, cut brew.Kind)`; inside `session` a local `cut := brew.TruncatedLost` read by a deferred closure. Task 4 adds the released case.

- [ ] **Step 1: Write the failing test** `TestShutdownMidShotTruncatedShutdown`: pumping sim stream (as in `TestDisconnectMidShotTruncatedLost`), wait `BrewOpen`, call `stop()`; the stored brew has kind `truncated-shutdown` and `EndedAt != nil`.
- [ ] **Step 2: Run** `nix develop --command go test ./daemon/ -run TestShutdownMidShot` — FAIL (`truncated-lost`).
- [ ] **Step 3: Implement.** `defer func() { d.disconnected(ctx, conn, cut) }()` replaces the plain defer. In the `<-ctx.Done()` branch set `cut = brew.TruncatedShutdown` before returning; all other ends keep `TruncatedLost`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./daemon/` — PASS.
- [ ] **Step 5: Gate and commit** `feat(medewid): record shutdown truncations as truncated-shutdown`.

### Task 4: Daemon release and reclaim

**Files:**
- Create: `daemon/release.go` (Release, Reclaim, the wait helper, errors)
- Modify: `daemon/daemon.go` (`Daemon` fields, `New`, `Run`, `session`), `daemon/state.go` (`Status.ReleasedUntil`, its copy in `Status()`), `daemon/commands.go` (`refuse`), `daemon/display.go` (`Mark`), `daemon/metrics.go` (gauge), package doc
- Test: create `daemon/release_test.go`; `daemon/metrics_test.go`

**Interfaces:**
- Consumes: `Store.Release/SetRelease/ClearRelease` (Task 2); `cut` in `session` (Task 3).
- Produces:
  - `var ErrReleased = errors.New("daemon: machine released")` — refusals wrap it as `fmt.Errorf("%w until %s", ErrReleased, until.Local().Format(time.DateTime))`.
  - `var ErrInvalidRelease = errors.New("daemon: release end must be in the future")`.
  - `var ErrReleaseStore = errors.New("daemon: release state could not be saved")` — wraps the store error.
  - `type ReleaseResult struct { Until time.Time; Banner *bool }` — `Banner` nil when no session was up (nothing to check).
  - `func (d *Daemon) Release(ctx context.Context, until time.Time) (ReleaseResult, error)`.
  - `func (d *Daemon) Reclaim(ctx context.Context) error`.
  - `Status.ReleasedUntil *time.Time` (nil when not released or expired).
  - Metric `medewi_machine_released_until_seconds` (gauge, Unix seconds, 0 when not released), set in `metrics.status`.
  - Variable `releaseBannerTimeout = 3 * time.Second` (tests shrink it).

Design decisions the implementer cannot make alone:
- **Session registration.** `session` creates `sctx, cancel := context.WithCancelCause(ctx)` and, under `d.mu`, refuses to start if `st.ReleasedUntil` is in the future, else stores `d.sess = &sessionHandle{cancel, done: make(chan struct{}), banner: nil}`. All of `session` (Dial, WaitReady, xmlName, StartReports, holdUntilEnd, the select loop) uses `sctx`. On exit (deferred, after `disconnected`) it clears `d.sess` under `d.mu` and closes `done`.
- **Release** (`release.go`): validate `until.After(time.Now())` else `ErrInvalidRelease`; `Store.SetRelease` (bounded by `storeTimeout`) — on error return `ErrReleaseStore` and change nothing; then under `d.mu` set `st.ReleasedUntil`, grab `d.sess`, and if present `cancel(errReleased)`; signal the wake channel; release the lock; if a session was present wait for its `done` or `ctx.Done()`; return `ReleaseResult{until, sess.banner}`. Setting the state and reading `d.sess` under one lock is what closes the dial race (Review Focus 1).
- **Session end on release.** Where `session` sees `sctx` done: if `context.Cause(sctx) == errReleased` → `cut = brew.TruncatedReleased`; if reports had started, wait `samplerDone`, note `conn.Banners()`, `d.stopReports`, then poll `conn.Banners()` (25 ms tick) for an increase within `releaseBannerTimeout`, storing the result in `d.sess.banner` (false → `slog.Warn("idle banner did not return after MC ; releasing anyway")`); if reports never started, `banner = conn.Banners() > 0` and nothing is sent. Otherwise (shutdown) keep Task 3's path. The cause check must also cover the early returns (`WaitReady`, `xmlName`, `StartReports` failing because `sctx` ended) so they send nothing and set the released cut — in particular the `DeadlineExceeded` branch of `WaitReady` must not send `MC ` when the cause is `errReleased`.
- **Reclaim:** `Store.ClearRelease` — on error return `ErrReleaseStore` and stay released; then clear `st.ReleasedUntil` and signal the wake channel.
- **Wake channel:** `wake chan struct{}` with buffer 1, sent non-blocking.
- **Run.** Before the loop (after `CloseDangling`): `Store.Release` — an error makes `Run` return it (fail closed: never dial when the release state is unknown); a future time sets `st.ReleasedUntil`. Each iteration: `d.waitReleased(ctx)` (loop: read `st.ReleasedUntil`; nil or not after now → clear it in status, return true; else wait for a timer at the end, `wake`, or ctx — false on ctx), then `session`, then the backoff sleep, which also returns early on `wake` (reclaim must not wait out a 3 min backoff). After a release ends or is reclaimed, reset the backoff to `BackoffMin`. An expired release is not written back (spec: no write needed).
- **Refusals:** `refuse` checks `st.ReleasedUntil != nil` first and returns the wrapped `ErrReleased`; `Mark` does the same before `ErrNoRawLog`.
- **Shutdown while released** sends nothing (no session exists).

- [ ] **Step 1: Write the failing tests** in `daemon/release_test.go` (idle sim stream `idleLines`, `daemonConfig`, `releaseBannerTimeout` shrunk via `t.Cleanup` like `reportStallTimeout`):
  - `TestReleaseClosesSession`: wait active; `Release(ctx, now+1h)` → nil error, `res.Banner != nil && *res.Banner`; right after return `Status().Connected == false`, `ReleasedUntil` equal to the time; the last command the sim received is `"MC "`; over 300 ms `s.Received()` stays unchanged (a new session would send `MC?`); `assertOnlyAllowed`.
  - `TestReleaseMidShotTruncatedReleased`: pumping stream; wait `BrewOpen`; `Release` → stored brew kind `truncated-released`.
  - `TestReleaseExpiresAndReconnects`: `Release(now+300ms)`; wait disconnected; then wait reconnected (`Connected && SpecOK`) and `ReleasedUntil == nil`.
  - `TestReclaim`: `Release(now+1h)`, then `Reclaim` → reconnected within `waitLong`; with `BackoffMin/Max = 1 min` set before start (so only `wake` can explain a fast reconnect). `Reclaim` with nothing released → nil.
  - `TestReleaseReplacesUntil`: release +1h, then +2h → `ReleasedUntil` is +2h and `Store.Release` returns +2h; release +300ms → reconnects soon.
  - `TestReleaseRefusals`: `Release(now-1s)` → `ErrInvalidRelease`, still connected; while released `SetPower` → `errors.Is(err, ErrReleased)` and the message contains "until"; `DumpMCu` → `ErrReleased`; `Mark("x")` (with `Raw` configured as in `rawlog_test.go`) → `ErrReleased`.
  - `TestReleaseStoreFailure`: close the store (`st.Close()`) under a running daemon, `Release` → `errors.Is(err, ErrReleaseStore)`, still `Connected`, `ReleasedUntil == nil`. Then a variant: release successfully, close the store, `Reclaim` → `ErrReleaseStore`, still released.
  - `TestReleasePersistsAcrossRestart`: subtest "future": `st.SetRelease(now+1h)` before `runDaemon`, daemon `Addr` pointing at a test helper `acceptCounter(t) (addr string, n func() int)` (a `net.Listener` on `127.0.0.1:0` that counts and closes accepted conns; the sim has no connection counter); over 500 ms `n() == 0` and `Status().ReleasedUntil` is set; subtest "expired": `SetRelease(now-1m)` → daemon connects normally (Review Focus 2, 3).
  - `TestReleaseDuringConnect`: sim with a long `Settle` on the daemon (e.g. `cfg.Settle = 2s`) so the session sits in `WaitReady`; `Release(now+1h)` while `Connected` is still false → returns promptly, `Banner` reports the banners seen; the sim never received `MC?` or `MCr` (Review Focus 1).
  - `TestReleaseWithDisplaySampling`: `DisplayEvery = 50ms`, `DisplayFor = 30ms`; release; the last received command is `"MC "` (Review Focus 4).
  - `TestReleaseWhileDisconnected`: no sim listening (as in `TestDialFailureIsInfo`); `Release` → nil error, `Banner == nil`.
  - In `metrics_test.go`: `TestMetricsReleasedUntil` — `m.status(Status{ReleasedUntil: &t})` sets the gauge to `t.Unix()`; nil → 0.
- [ ] **Step 2: Run** `nix develop --command go test ./daemon/ -run 'Release|Reclaim|MetricsReleased'` — compile failure.
- [ ] **Step 3: Implement** per the decisions above. Update the package doc in `daemon.go` (one sentence on release: no traffic while released, persisted in the store). The sent-strings list is unchanged.
- [ ] **Step 4: Run** `nix develop --command go test -race -count=3 ./daemon/` — PASS, no flakes.
- [ ] **Step 5: Gate and commit** `feat(medewid): release and reclaim the machine connection`.

### Task 5: HTTP API

**Files:**
- Modify: `daemon/api.go` (routes, handlers, `status`, `statusFor`)
- Test: `daemon/api_test.go`

**Interfaces:**
- Consumes: `Release`, `Reclaim`, `ReleaseResult`, `ErrReleased`, `ErrInvalidRelease`, `ErrReleaseStore`, `Status.ReleasedUntil`.
- Produces: `POST /api/release` body `{"until": RFC 3339}` → 200 `{"ok":true,"until":…,"banner":true|false|null}`; `POST /api/reclaim` body `{}` → 200 `{"ok":true}`; `GET /api/status` adds `"released_until"` (RFC 3339 or `null`).

- [ ] **Step 1: Write the failing tests** (`apiSetup` pattern):
  - `TestAPIRelease`: POST `{"until":"<now+1h RFC3339>"}` → 200, `ok`, `until` echoes, `banner` true; `GET /api/status` → `released_until` set, `connected` false; `POST /api/power` → 409 with error containing "released until"; `GET /api/backup/mcu` → 409; `POST /api/reclaim` `{}` → 200; status `released_until` null.
  - `TestAPIReleaseBadInput`: missing `until` (`{}`) → 400; past time → 400; `"until":"tomorrow"` → 400 (invalid JSON body); no content type → 415; cross-origin headers (as in the power test) → 403; same 415/403 for `/api/reclaim`.
  - `TestAPIReleaseStoreError`: store closed → 500, error "store error"-style message from `ErrReleaseStore`.
  - Extend the method-not-allowed table with `GET /api/release`, `GET /api/reclaim`.
- [ ] **Step 2: Run** `nix develop --command go test ./daemon/ -run TestAPIRelease` — FAIL (404).
- [ ] **Step 3: Implement.** Handlers decode with `decodeJSON`; a zero `until` → 400 `"until is required"`. `statusFor`: `ErrInvalidRelease` → 400, `ErrReleased` → 409, `ErrReleaseStore` → 500. `status` gains `ReleasedUntil *time.Time \`json:"released_until"\``. The `-metrics-ca` allowlist in `cmd/medewid` needs no change (it only admits `/metrics`); confirm by reading it.
- [ ] **Step 4: Run** `nix develop --command go test -race ./daemon/ ./cmd/medewid/` — PASS.
- [ ] **Step 5: Gate and commit** `feat(medewid): POST /api/release and /api/reclaim`.

### Task 6: medewictl release, reclaim, status

**Files:**
- Create: `cmd/medewictl/until.go`, `cmd/medewictl/until_test.go`
- Modify: `cmd/medewictl/main.go` (usage, dispatch, `cmdStatus`, new `cmdRelease`, `cmdReclaim`)
- Test: `cmd/medewictl/main_test.go`

**Interfaces:**
- Produces: `func parseUntil(s string, now time.Time) (time.Time, error)`; subcommands `release [-until +DURATION]` (default `+1h`) and `reclaim`.

- [ ] **Step 1: Write the failing tests.**
  - `TestParseUntil` table, `now` fixed: `+30min` → now+30m; `+1h30min` → +90m; `+90s`; `+2h`; `+1min30s`; `+1h1h` → +2h (groups add, any order). Errors: `""`, `"+"`, `"30min"`, `"-5min"`, `"+0s"`, `"+5m"`, `"+1d"`, `"+1h 30min"`, `"+1.5h"`, `"+h"`.
  - `TestRelease` (httptest server as in the existing tests): `release -until +30min` posts JSON with `until` within a second of now+30m to `/api/release`; prints `released until <local DateTime> (local time)` on stdout, exit 0. Without `-until` → now+1h. Response `"banner": false` → exit 0 plus a stderr line containing `warning`. Server 500 → exit 1 with the error text. `release -until 30min` → exit 2, no request sent.
  - `TestReclaim`: posts `{}` to `/api/reclaim`, prints `reclaimed`, exit 0; 500 → exit 1.
  - `TestStatus` extension: `released_until` set → line `released_until: <local DateTime>`; null → `released_until: none`.
- [ ] **Step 2: Run** `nix develop --command go test ./cmd/medewictl/` — FAIL.
- [ ] **Step 3: Implement.** `parseUntil`: require a leading `+`, then consume the rest as one or more `(\d+)(h|min|s)` groups (anything left over, incl. a bare `m`, is an error); sum; zero → error. Errors are usage errors (exit 2). The flag is registered only for `release` (like `-out` for `backup`); Go's `flag` accepts both `-until` and `--until=`. Times printed with `t.Local().Format(time.DateTime)`. Usage text: `release [-until +DURATION]   free the machine (default +1h; units h, min, s)` and `reclaim                    take it back now`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./cmd/medewictl/` — PASS.
- [ ] **Step 5: Gate and commit** `feat(medewictl): release and reclaim subcommands`.

### Task 7: Ops panel and docs

**Files:**
- Modify: `daemon/panel.html`, `daemon/api_test.go` (panel test, if one asserts content), `docs/architecture.md`, `docs/deployment.md`, `docs/runbooks/capture-session.md`, `docs/machine-behaviour.md` only if it mentions `interrupted`, `README.md` (CLI section), `CLAUDE.md` (phase-2 summary, if it lists features)

- [ ] **Step 1: Write the failing test** `TestPanelHasReclaim`: `GET /` body contains `id="reclaim"` and `/api/reclaim`.
- [ ] **Step 2: Run** it — FAIL.
- [ ] **Step 3: Implement the panel.** In `refresh()`: when `s.released_until` is set, `#conn` reads `released until HH:MM` (local, `toLocaleTimeString([], {hour:"2-digit", minute:"2-digit"})`) and a `<button id="reclaim" hidden>Reclaim</button>` is shown; click → `post("/api/reclaim", {})` then `refresh()`. No release button.
- [ ] **Step 4: Docs.**
  - `docs/deployment.md`: API table rows for both endpoints; metric row `medewi_machine_released_until_seconds`; `pump_seconds` row "truncated ones included"; a note that the reconnect-flap alert (`changes(medewi_machine_connected[1h]) > 6`) counts a release as two changes and that panels gated on `medewi_machine_connected == 1` go blank while released; an "Upgrading to schema v2" note: the first start deletes `interrupted` brews and their points, irreversible, with the read-only query from the spec to inspect them first.
  - `docs/runbooks/capture-session.md` l.39-42: replace "stop medewid" with `medewictl release --until=+1h` before, `medewictl reclaim` after; keep `systemctl stop medewid` as the fallback.
  - Firmware-flash / conformance gate docs (wherever `conformance` against the machine is described): same instruction.
  - `docs/architecture.md`: one paragraph on release (persisted, nothing sent while released, truncated kinds).
  - README CLI section: the two subcommands and the `--until` syntax.
- [ ] **Step 5: Run** `nix develop --command go test ./daemon/ -run TestPanel` — PASS; `grep -rn -i interrupted --include='*.go' --include='*.md' --include='*.html' . | grep -v docs/superpowers` returns nothing except the migration's SQL and its test.
- [ ] **Step 6: Gate and commit** `feat(medewid): show releases in the ops panel` (docs in the same commit, or a separate `docs:` commit).

### Finish

- [ ] Full gate `nix flake check -L`; then superpowers:finishing-a-development-branch — one PR against `main` titled `medewid: release and reclaim the machine (#11)`, body ending with the Claude Code line, `Closes #11`. Pushing needs the ssh-agent; ask Thomas to `ssh-add` if it fails.
