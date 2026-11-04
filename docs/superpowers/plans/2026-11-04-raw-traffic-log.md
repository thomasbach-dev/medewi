# Raw Traffic Log Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** medewid keeps every byte it exchanges with the machine for 7 days as gzip'd fixture logs, without ever storing `MCu`/`MCm` replies.

**Architecture:** A new package `rawlog` owns files, rotation, retention and the received-line allowlist; a per-connection `Session` plugs into `leva.Conn.Tap`, watches its own `tx` to open reply windows, and only enqueues. The daemon opens a Session per connection; medewid wires flags and metrics.

**Tech Stack:** Go stdlib (`compress/gzip`, `encoding/json`), `fixture`, Prometheus client.

**Spec:** [docs/design/2026-11-04-raw-traffic-log.md](../../design/2026-11-04-raw-traffic-log.md)

## Global Constraints

- Pure Go, no cgo, no new module dependencies.
- `Session.Tap` never blocks and never does I/O: enqueue or drop+count. Tap after Close is a no-op; `Log.Close` never closes the channel taps send on (use a closed flag under a mutex or atomic).
- No `MCu`/`MCm` reply line may reach a log file (spec requirement 5).
- Files `0600`, directory `0700`; names `20061102T150405.000Z.jsonl.gz` (UTC, Go layout `20060102T150405.000Z`), created with `O_EXCL`.
- All durations are `cfg.Now().Sub(x)` with `x` also from `cfg.Now()` (never `time.Since`, which would mix a fake clock with the real one); with the default `time.Now` both carry the monotonic reading. Retention uses the name time.
- All `*Log`/`*Session` methods are no-ops on a nil receiver.
- Run `nix flake check -L` before every commit (`git add` new files first); conventional commits; branch `phase2-daemon-design`.
- No real-machine contact. No vendor XML or real dumps in tests; use fake dump lines.

## Review Focus

1. Another client's `MCu` reply arriving while medewid's own `MC?` window is open: its first `|` line shuts the window, so the letter lines among the `|` lines are dropped too. Test in Task 3.
2. An `MCu` reply pausing mid-stream past the dump's idle gap, followed by a power command: `DumpQuiet` keeps the window shut, so the tail is dropped. Test in Task 3.
3. Report lines interleaving with a dump reply must still be logged. Test in Task 3.
4. The disk is unwritable or vanishes mid-run: the daemon keeps running, the error counter rises. Test in Task 2.
5. The default `/var/lib/medewid/raw` is not writable in a local trial run: medewid warns and runs without the log; an explicit `-raw-dir` that fails exits 1. Test in Task 5.

---

### Task 1: `fixture.Note`, the writer core and `ReadFile`

**Files:**
- Modify: `fixture/fixture.go` (add `Note Dir = "note"`, doc: plain-text annotation, ignored on replay)
- Create: `rawlog/rawlog.go`, `rawlog/rawlog_test.go`

**Interfaces:**
- Produces:
  ```go
  type Config struct {
      Dir         string
      Retention   time.Duration    // files older than this are deleted
      MaxBytes    int64            // directory total cap
      RotateEvery time.Duration    // default 1h
      FlushEvery  time.Duration    // default 5s
      ReplyWindow time.Duration    // default 5s
      DumpQuiet   time.Duration    // default 60s
      Buffer      int              // queued events, default 4096
      Now         func() time.Time // default time.Now; must be goroutine-safe
      writeHook   func()           // tests only: called before each write
  }
  func Open(cfg Config) (*Log, error)       // MkdirAll 0700, prune, start writer
  func (l *Log) Close() error               // stop taps, drain queue, close file; idempotent; releases a blocked writeHook
  func (l *Log) Session() *Session
  func (s *Session) Tap(dir string, p []byte) // signature of leva.Conn.Tap's fn
  func (s *Session) Close()                 // emits the partial rx line, ends the file; idempotent
  func ReadFile(path string) ([]fixture.Event, error) // tolerates a missing gzip trailer (io.ErrUnexpectedEOF)
  ```

In this task the allowlist is not yet active: every rx line is kept (Task 3 adds it).

- [ ] **Step 1: Failing tests** in `rawlog/rawlog_test.go` (helper `readDir(t, dir) [][]fixture.Event`: `ReadFile` per `.jsonl.gz`, sorted by name; fake clock helper with a mutex):
  - `TestSessionWritesLinesAndTX`: Tap rx `"$id: leva"`, `"! (3.2)\r\n{a}\r\n{b"`, tx `"MCr\r\n"`, Close session, Close log → one file; events in order: rx `"$id: leva! (3.2)\r\n"`, rx `"{a}\r\n"`, tx `"MCr\r\n"`, rx `"{b"` (partial at Close); `t` non-decreasing; the first line's `t` is from its first chunk; file mode `0600`, dir mode `0700`; name matches `^\d{8}T\d{6}\.\d{3}Z\.jsonl\.gz$`.
  - `TestLongPartialLineEmitted`: `FlushEvery` 20ms; `"{"` + 4999 bytes rx without newline (report-shaped, so Task 3 keeps it) → within 1 s `ReadFile` shows one 4096-byte rx event while the session is open.
  - `TestNewFilePerSession`: two sessions, same fake wall time → two files (the second name's millisecond bumped by `O_EXCL` collision).
  - `TestRotateEvery`: `RotateEvery` 1 min, fake clock; tap, advance 61 s, tap → two files; the second file's first event has `t` 0.
  - `TestTapAfterClose`: Tap after `Session.Close` and after `Log.Close` → no panic, nothing written.
  - `TestReadFileTruncated`: copy an open file's flushed bytes (no trailer) → `ReadFile` returns the events, no error.
  - `TestNilSafe`: `var l *Log; s := l.Session(); s.Tap("rx", nil); s.Close(); l.Close()` does not panic.

- [ ] **Step 2: Run** `nix develop --command go test -race ./rawlog/` → FAIL (package missing).

- [ ] **Step 3: Implement.** `Session.Tap` copies `p`, splits rx on `\n` under a per-session mutex (time stamped at a line's first byte), and enqueues `item{sess, at, dir, data}` with a non-blocking `select`. One writer goroutine owns the file: opens on a session's first item (`O_CREATE|O_EXCL`, bump ms on `EEXIST`), rotates when a NEW session's first item arrives (an item of a session that is no longer current is dropped) or `at.Sub(fileStart) >= RotateEvery`, encodes `fixture.Event{T: at.Sub(fileStart).Milliseconds()}` into a `gzip.Writer`, `Flush`es on a `FlushEvery` ticker.

- [ ] **Step 4: Run** `go test -race ./rawlog/ ./fixture/` → PASS.

- [ ] **Step 5: Commit** `feat(rawlog): gzip'd per-session fixture logs of raw traffic`.

### Task 2: Retention, drops and write errors

**Files:** Modify `rawlog/rawlog.go`, `rawlog/rawlog_test.go`

**Interfaces:**
- Produces: `func (l *Log) Dropped() uint64`, `func (l *Log) WriteErrors() uint64` (atomic; 0 on nil).

- [ ] **Step 1: Failing tests:**
  - `TestPruneByAge`: pre-create `20261020T000000.000Z.jsonl.gz` (old), `20261103T000000.000Z.jsonl.gz` (young), `notes.txt`; fake Now 2026-11-04, Retention 7×24h → after `Open`, the old file is gone, the young one and `notes.txt` remain.
  - `TestPruneBySize`: three 1000-byte `.jsonl.gz` files of increasing name time, `MaxBytes` 2500 → after Open the oldest is gone. Also: `MaxBytes` 1, two sessions → after the second file's first flush it still exists (prune runs after opening the new file and skips it).
  - `TestDropsWhenFull`: `Buffer` 1, `writeHook` blocking on a channel → 100 Taps return within 100 ms and `Dropped() > 0`; `Close` returns (releases the hook).
  - `TestWriteErrorKeepsRunning`: `os.RemoveAll(dir)` after Open and replace it with a regular file; start a session and tap → `WriteErrors() > 0` within 1 s, `Close` returns without hanging.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** `prune(open string)` (parse names with the layout; skip non-matching; delete by age, then oldest-first by size, never `open`); call it from `Open` and after each new file is opened. On any open/write/flush error: count, `slog.Warn` once per file, drop the file and discard items until the next rotation trigger.

- [ ] **Step 4: Run** `go test -race ./rawlog/` → PASS.

- [ ] **Step 5: Commit** `feat(rawlog): retention by age and size; drops and write errors counted`.

### Task 3: Received-line allowlist

**Files:** Modify `rawlog/rawlog.go`, `rawlog/rawlog_test.go`

**Interfaces:**
- Produces: `func (l *Log) Redacted() uint64`.

- [ ] **Step 1: Failing tests** (fake clock for windows):
  - `TestKeepsReportsBannersAndReplies`: tx `"MC?\r\n"`, rx `"XML=x\r\n{r}\r\n$id: leva! (3.2)\r\n"` → all three rx lines kept.
  - `TestDropsUnsolicitedLines`: no tx; rx `"XML=x\r\nhello\r\n{r}\r\n"` → only `{r}` kept, preceded by note `"dropped 2 lines"`; `Redacted() == 2`.
  - `TestDumpReplyDropped`: tx `"MCu\r\n"`, rx `"|secret 1\r\n{r1}\r\nKEY=x\r\n"`, tx `"MCm\r\n"`, rx `":10000000AA\r\n:00000001FF\r\n{r2}\r\n"` → kept rx exactly `{r1}`, `{r2}`; no event contains `secret`, `KEY=`, `:1000`; `Redacted() == 4`.
  - `TestDumpLineClosesWindow`: tx `"MC?\r\n"`, rx `"XML=x\r\n|secret\r\nKey line\r\n"` → only `XML=x` kept: the `|` line closes the window, so the letter line after it is dropped (Review Focus 1, another client's MCu).
  - `TestDumpQuietBlocksWindows`: tx `"MCu\r\n"`, fake clock +10 s, tx `"MCa\r\n"`, rx `"Key tail\r\n"` → dropped (an aborted dump's tail after a power command); +61 s after the `MCu`, tx `"MC?\r\n"`, rx `"XML=x\r\n"` → kept.
  - `TestDumpLineStartsQuiet`: no tx; rx `":10000000AA\r\n"`; +10 s tx `"MC?\r\n"`, rx `"Key\r\n"` → dropped.
  - `TestWindowExpires`: tx `"MCa\r\n"`, fake clock +6 s, rx `"late\r\n"` → dropped (Review Focus 2: also the shape of a late dump tail).
  - `TestStreamTogglesOpenNoWindow`: tx `"MCr\r\n"` then rx `"x\r\n"` → dropped.
  - `TestLongLineJudgedOnce`: rx `"|"` + 5000 × `"a"` + `"\r\n"` (no window) → nothing but a note written.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** in `Session` under its mutex: `lastDump` time (zero at start). On tx, parse the command (bytes before `\r`): `MCu`/`MCm` set `lastDump = Now()` and close the window; `MCr`/`MC ` do nothing; any other opens `windowUntil = Now()+ReplyWindow` unless `Now().Sub(lastDump) < DumpQuiet`. On rx, record `inWindow` at a line's first byte; judge when the line completes (or at its first 4096-byte piece): a dump line (`|`-prefixed or Intel HEX) is dropped, sets `lastDump = Now()` and closes the window; otherwise keep iff report shape (`{…}` after trimming CR/LF; a piece: starts with `{`), starts `$id:`, or `inWindow`. Count dropped lines; enqueue a `fixture.Note` `"dropped N lines"` before the next kept rx event or at Close.

- [ ] **Step 4: Run** `go test -race ./rawlog/` → PASS.

- [ ] **Step 5: Commit** `feat(rawlog): allowlist received lines; MCu/MCm replies never logged`.

### Task 4: Daemon integration

**Files:** Modify `leva/conn.go`, `leva/conn_test.go`, `daemon/daemon.go`, new `daemon/rawlog_test.go`

**Interfaces:**
- Consumes: Tasks 1–3 (`rawlog.ReadFile` for assertions).
- Produces: `Config.Raw *rawlog.Log` (nil: off); `leva.Conn.send` fires the tx tap before the write.

- [ ] **Step 0: leva tx tap order.** Failing test in `leva/conn_test.go`, `TestTapTXBeforeReply`: a sim that answers `MC?` immediately; a tap appending `dir` to a mutex-guarded slice; 50 × `Command("MC?")` → in every round the `tx` entry precedes that round's first `rx`. Move `fireTaps("tx", …)` before the write in `send` (update its comment: a failed write is still tapped). Run `go test -race ./leva/` → PASS. Commit `fix(leva): fire the tx tap before writing`.

- [ ] **Step 1: Failing tests** (helpers `startSim`, `daemonConfig`, `runDaemon` from `helpers_test.go`; `dumpSim`, `fakeMCm`, `waitActive`, `shrinkDumpTimings` from `commands_test.go`). Each test opens the log with `FlushEvery` 20ms on `t.TempDir()`, and calls `stop()` then `Log.Close()` before reading:
  - `TestRawLogRecordsSession`: run to `waitActive`, stop → tx `"MC?\r\n"`, `"MCr\r\n"`, `"MC \r\n"` (the final stop) and at least one rx report line are logged.
  - `TestRawLogDumpsNeverLogged`: `shrinkDumpTimings`, `dumpSim(idleLines(20), fakeMCm)`; `DumpMCm`, `DumpMCu`, stop → no event contains any `fakeMCm` line or `"user setting 1 = 42"`; report lines from during the dumps are present.

- [ ] **Step 2: Run** `go test -race ./daemon/ -run RawLog` → FAIL.

- [ ] **Step 3: Implement** in `session`, in this order right after a successful `Dial`:
  ```go
  rs := d.cfg.Raw.Session()
  defer rs.Close()                   // runs last: after conn.Close in disconnected
  defer conn.Tap(rs.Tap)()
  defer d.disconnected(ctx, conn)    // existing
  ```
  The final `MC ` is sent in the loop body before any defer runs, so it is logged. Update the package doc comment.

- [ ] **Step 4: Run** `go test -race ./daemon/` → PASS.

- [ ] **Step 5: Commit** `feat(daemon): raw traffic log per session`.

### Task 5: medewid config, metrics, module and docs

**Files:** Modify `cmd/medewid/main.go`, `cmd/medewid/main_test.go`, `nix/module.nix`, `README.md`, `CLAUDE.md`, `docs/architecture.md` (one line where the daemon's data is listed)

**Interfaces:**
- Consumes: `rawlog.Open`, `Dropped`, `WriteErrors`, `Redacted`, `daemon.Config.Raw`.

- [ ] **Step 1: Failing tests:**
  - `baseArgs` gains `-raw-dir` on a temp dir; `clearEnv` clears `MEDEWID_RAW_DIR`, `MEDEWID_RAW_RETENTION`, `MEDEWID_RAW_MAX_MB`.
  - `TestRawLogFlags`: after a run against the sim, the raw dir holds a `.jsonl.gz`; `/metrics` lists `medewi_rawlog_dropped_total`, `medewi_rawlog_write_errors_total`, `medewi_rawlog_redacted_lines_total`.
  - `TestRawLogDisabled`: `-raw-retention 0` → raw dir stays empty, the metrics are absent.
  - `TestRawLogExplicitDirUnusable`: `-raw-dir` below a regular file → exit 1.
  - `TestRawLogDefaultDirUnusableWarns`: make `rawDefault` (package var, default `/var/lib/medewid/raw`) point below a regular file, no `-raw-dir` → runs, stderr contains `raw traffic log disabled`.
  - `TestRawLogBadFlags`: `-raw-retention -1h` or `-raw-max-mb 0` → exit 2.

- [ ] **Step 2: Run** `go test -race ./cmd/medewid/` → FAIL.

- [ ] **Step 3: Implement** flags `-raw-dir` (env `MEDEWID_RAW_DIR`, default `rawDefault`), `-raw-retention` (env `MEDEWID_RAW_RETENTION`, default `168h`, `0` disables), `-raw-max-mb` (env `MEDEWID_RAW_MAX_MB`, default `256`). "Explicit" = flag set (`fset.Visit`) or env non-empty. Open after the store; register the three counters as `prometheus.NewCounterFunc` on `reg`; `Close` the log after `d.Run` returned. `nix/module.nix`: `after` and `wants` gain `time-sync.target`.

- [ ] **Step 4: Docs.** README: three rows in the daemon table; a short "Raw traffic log" paragraph (what, where, 7 days, which received lines are kept and that `MCu`/`MCm` replies never are, replay recipe `gzip -dc FILE 2>/dev/null > DIR/log.jsonl; levasim -fixtures DIR` and that replay is approximate); the trial run passes `-raw-dir "$MEDEWI_DATA/raw"`; the NixOS paragraph says a `MEDEWID_RAW_DIR` override must stay under `/var/lib/medewid`. CLAUDE.md "What this is": mention `rawlog/`; Hard rules: "The raw log must never contain `MCu`/`MCm` reply lines; keep the `rawlog` allowlist and its tests."

- [ ] **Step 5: Run** `nix flake check -L` → `all checks passed!`

- [ ] **Step 6: Commit** `feat(medewid): raw traffic log flags, metrics and module ordering; docs`.
