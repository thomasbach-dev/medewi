# Fixtures from the raw log — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `levalog extract` cuts raw-log windows into capture fixtures, levasim replays LCD frames after `MC@`, the committed-fixture guard accepts raw-log fixtures, and `testdata/fixtures/backflush/` is committed.

**Architecture:** The pure cut (window, line boundaries, sessions, firmware, `t` rebase) lives in `logview/extract.go` over opened `logview.Source`s; `cmd/levalog/extract.go` does flags, file selection, manifest and output. The capture writer moves from levacap into `fixture` so both tools share it. levasim gets a display flag next to `reporting` and filters at emit time on one timeline.

**Tech Stack:** Go (pure, no cgo), pflag, `systime`, existing `fixture`/`rawlog`/`logview`/`leva` packages.

**Spec:** [docs/superpowers/specs/2026-11-14-raw-log-fixtures-design.md](../specs/2026-11-14-raw-log-fixtures-design.md)

## Global Constraints

- Work in worktree `.claude/worktrees/backflush-fixture`, branch `backflush-fixture`; one rebased PR against main.
- TDD: failing test first. Conventional commits; commit with `git -c commit.gpgsign=false commit`; trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Gate before every commit: `nix develop --command gate`; `nix flake check -L` (`git add` new files first) before committing go.mod/go.sum/flake.nix/nix changes and before the final push. Tests needing the XML: `nix develop --command go test ./PKG/ -run NAME -v` (the local `.envrc` sets `MEDEWI_XML_PATH`).
- Never contact the machine. `extract` is offline.
- Extracted bytes differ from the recorded ones only by the cut and the rebased `t`.
- Existing fixtures (no `<` frames) replay byte-for-byte as before.
- `machine_addr` is always `machine.example:23`; manifest notes carry file basenames only, never directories.
- CLI per docs/cli-conventions.md: pflag long options, `-s/--since`, `-u/--until`, exit codes 0/1/2.
- Real raw logs for Task 6 are in a local scratch directory outside the repo (`$RAW` below) (never committed as such).

## Review Focus

1. Window starting inside a line (or inside a piece of one, rawlog splits long rx lines) → the fixture starts at the next line; a window ending mid-line → the incomplete tail is dropped. Tests in Task 3.
2. Window crossing an hourly rotation → one continuous `t`, no duplicate XML note in the middle. Test in Task 3.
3. Window crossing a session boundary → refused, message names the boundary time. Test in Task 3.
4. A window with no events, or `--until` not after `--since` → exit 1 "no events in …" / exit 2. Tests in Task 4.
5. levasim switching `MCr` ↔ `MC@` mid-replay → the timeline continues (no restart); `MC ` then `MC@` resumes frames. Test in Task 2. Existing tests that relied on the sim ignoring `MC@` (levacap display sampling, daemon display sampling) are updated to the real semantics, not the sim bent back.

---

### Task 1: Shared capture writer in `fixture`

**Files:**
- Modify: `fixture/fixture.go` (add `WriteCapture`)
- Modify: `cmd/levacap/main.go:567-600` (`writeOutput` calls `fixture.WriteCapture`)
- Test: `fixture/fixture_test.go`

**Interfaces:**
- Produces: `func WriteCapture(dir, logName, manName string, events []Event, m Manifest) error` — creates `dir` (0755) if needed, writes `.<name>.tmp` files, renames both only after both succeeded. `m.Recorded` is written as given (levacap passes `time.Now()`).

- [ ] **Step 1: Write the failing tests** `TestWriteCaptureWritesBoth` (round-trip: `ReadLog` of `dir/log.jsonl` equals the events; `LoadManifest` equals `m`; no `.tmp` left) and `TestWriteCaptureKeepsOldOnFailure` (existing `log.jsonl` in a dir where the manifest temp path is a directory → error, old `log.jsonl` unchanged).
- [ ] **Step 2: Run** `nix develop --command go test ./fixture/ -run WriteCapture -v` → FAIL (undefined).
- [ ] **Step 3: Implement** `WriteCapture` by moving the body of levacap's `writeOutput`; `writeOutput` builds the `Manifest` and calls it.
- [ ] **Step 4: Run** `nix develop --command go test ./fixture/ ./cmd/levacap/ -v` → PASS (levacap's `TestCaptureWritesLogAndManifest`, `TestWriteOutputFailureLeavesGoodFilesUntouched` unchanged).
- [ ] **Step 5: Gate and commit** `refactor(fixture): share the atomic capture writer with levacap`.

### Task 2: levasim display mirror

**Files:**
- Modify: `sim/sim.go` (state, `readLoop`, `startReplay`, `emit`, `heartbeat`, connect banner at :250, doc comments on `Config.Stream`)
- Test: `sim/replay_test.go` (new tests), existing sim/levacap/daemon tests as they fail

**Interfaces:**
- Produces: commands `MCr` (reports), `MC@` (display), `MC ` (off). No new `Config` field. `Server` keeps `reporting bool` meaning "a stream is on" and adds `display bool` (which class).

- [ ] **Step 1: Write the failing tests** in `sim/replay_test.go`, with a stream of alternating events `{` report lines and `<` 69-byte frames (`t` 0, 50, 100, …):
  - `TestMCAtReplaysFramesOnly`: after `MC@` every received line starts with `<`; at least 3 received.
  - `TestMCrSkipsFrames`: after `MCr` no received line starts with `<`.
  - `TestStreamSwitchKeepsTimeline`: `Speed` high, `Once`; `MCr`, then `MC@` after the first report arrives → the first frame received has a `t` later than the report's (frames carry an index in their text), i.e. no restart from event 0.
  - `TestMCSpaceThenMCAtResumesFrames`: `MC@`, `MC `, `MC@` → frames again.
  - `TestNoHeartbeatWhileDisplay`: `BannerInterval` set, `MC@` → no `$id` line for 3 intervals.
  - `TestMCRResendsLastFrame`: in display mode `MCR` resends the last frame.
- [ ] **Step 2: Run** `nix develop --command go test -race ./sim/ -v` → the new tests FAIL.
- [ ] **Step 3: Implement**: `MC@` sets `reporting = true, display = true` and starts the replayer if none runs; `MCr` sets `display = false` likewise; `MC ` clears both. `startReplayerLocked` keeps all RX events; `emit` skips (returns true, keeps timing) an event whose class (`Data[0] == '<'`) differs from `display`; `lastItem` is set only for emitted items. Heartbeat and connect banner stay silent while `reporting`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./... ` → sim PASS. Any levacap/daemon test that expected reports to keep flowing after `MC@` is updated to expect what the machine does (frames or nothing during the display sample); note each such change in the commit body.
- [ ] **Step 5: Gate and commit** `feat(sim): replay LCD frames after MC@, reports after MCr`.

### Task 3: The cut — `logview.Extract`

**Files:**
- Create: `logview/extract.go`
- Test: `logview/extract_test.go` (synthetic `Source`s built in the test; no XML)

**Interfaces:**
- Consumes: `logview.Source` (`Start`, `HasWall`, `Events`, `Path`, `Wall`), `rawlog.XMLNotePrefix`, `fixture.Event`.
- Produces:
  ```go
  // SessionStart reports whether src begins a raw-log session: its first
  // event is not the "xml: " note at t = 0.
  func SessionStart(src Source) bool

  type Extracted struct {
      Events      []fixture.Event // t rebased: first kept event at 0; XML note first
      First, Last time.Time       // wall times of the first and last kept event
      Files       []string        // basenames of the sources the events come from
      XMLName     string          // from the session's last xml note at or before the window; "" if none
      Firmware    string          // last "$id:" banner line of the session before or in the window, CRLF trimmed; "" if unknown
      Reports, Frames, NoteEvents int
      Notes       []string        // e.g. "window starts mid-line: dropped 120 bytes", "firmware unknown: …"
  }

  // SessionBoundaryError: the window crosses into a new session at At.
  type SessionBoundaryError struct{ At time.Time }

  // Extract cuts [since, until) out of srcs: raw logs of one directory in
  // start order, possibly beginning before the window (for the firmware).
  func Extract(srcs []Source, since, until time.Time) (Extracted, error)
  ```

- [ ] **Step 1: Write the failing tests** (helpers build `Source{Start, HasWall: true, Events}`):
  - `TestSessionStart`: note-first → false; banner-first → true; empty → true.
  - `TestExtractRebasesAndPrependsXMLNote`: one file, window inside it → `Events[0]` is `note "xml: X"` at `t=0`, the rest verbatim with `t` = wall − `First`, `XMLName == "X"`.
  - `TestExtractStartsAtLineBoundary`: window starts in a report split over two rx events (rawlog pieces) → the first rx event kept starts with `{` of the next line; `Notes` names the dropped byte count.
  - `TestExtractDropsTrailingPartialLine`: last kept rx bytes after the final CRLF are dropped.
  - `TestExtractJoinsRotation`: two files of one session (second starts with the note), window spans both → `t` continuous across the join, exactly one XML note (the first), `Files` has both basenames.
  - `TestExtractRefusesSessionBoundary`: second file starts with a banner → `errors.As(err, &SessionBoundaryError{})` with `At` = that file's `Start`.
  - `TestExtractFirmwareFromSessionStart`: banner only in an earlier file of the same session → `Firmware == "$id: leva! (3.2)"`; when the session's first file is missing (first source starts with a note) → `Firmware == ""` and a note containing "firmware unknown".
  - `TestExtractCounts`: `Reports`, `Frames`, `NoteEvents` over the kept lines (a report is a line starting `{`, a frame `<`).
- [ ] **Step 2: Run** `nix develop --command go test ./logview/ -run 'Extract|SessionStart' -v` → FAIL.
- [ ] **Step 3: Implement** `Extract`: walk sources in order; a source with `SessionStart` true after kept events → `SessionBoundaryError`; a session start before the window resets firmware/XML. Clean start: if the last rx event before the window did not end in `\r\n` (or a kept rx event is the first of the file and the previous file's last rx did not), trim the first kept rx data through the first `\r\n` (drop the event if none). Skip the XML note at `t=0` of every kept rotated file; prepend one note with `XMLName` at `t=0` when known.
- [ ] **Step 4: Run** the same command → PASS.
- [ ] **Step 5: Gate and commit** `feat(logview): cut a raw-log window into fixture events`.

### Task 4: `levalog extract`

**Files:**
- Create: `cmd/levalog/extract.go`
- Modify: `cmd/levalog/main.go` (`cmds["extract"]`, `usage` text and examples)
- Test: `cmd/levalog/main_test.go` (or `extract_test.go`, same package, using the existing `runCmd` helper and a temp raw-log dir written with gzip `fixture.WriteLog` under `rawlog` file names)

**Interfaces:**
- Consumes: `logview.Extract`, `logview.SessionStart`, `logview.Open`, `logview.Select`, `fixture.WriteCapture`, `fixture.WriteLog`, the `input` type and `scan` (for `--run`).
- Produces: `func extractCmd(args []string, xmlPath string, stdout, stderr io.Writer) int`.

Flags: `-s/--since`, `-u/--until`, `--dir`, `--run N` (one number), `--margin SPAN`, `--out DIR|-` (required), `--scenario NAME` (default `extract`), `--notes TEXT`. `--format` is not registered here.

- [ ] **Step 1: Write the failing tests**:
  - `TestExtractWritesCapture`: window over a temp raw dir → exit 0; `log.jsonl` events equal the expected cut; manifest: `Scenario`, `MachineAddr == "machine.example:23"`, `Recorded` == wall of the last event, `Notes` ends with `extracted from 20261114T070552.646Z.jsonl.gz 2026-11-14 08:58:30.000 – … by levalog extract` (basenames, local time), `FirmwareVersion` from an earlier session file in the dir.
  - `TestExtractStdout`: `--out -` → stdout is exactly the bytes of `fixture.WriteLog(events)`; stderr contains `no manifest with --out -`.
  - `TestExtractRefusesNonEmptyOut`: `--out` dir holding a file → exit 1, the file untouched.
  - `TestExtractUsageErrors`: no `--out` → 2; neither window nor `--run` → 2; `--until` ≤ `--since` → 2; a capture as input → 2; `--margin` without `--run` → 2; `--run` without XML → 2.
  - `TestExtractEmptyWindow`: window with no events → exit 1, `no events in` on stderr, no output dir created.
  - `TestExtractSessionBoundary`: exit 1, stderr names the boundary time.
  - `TestExtractXMLSHA`: `--xml FILE` whose basename matches the session XML → `XMLSHA256` = its sha256; non-matching or none → `""`.
- [ ] **Step 2: Run** `nix develop --command go test ./cmd/levalog/ -run Extract -v` → FAIL.
- [ ] **Step 3: Implement** `extractCmd`: input files = `logview.Select(dir, since, until)` (or FILE/DIR args resolved by `sourcePaths`), extended backwards in the same directory to the nearest file with `SessionStart` true (stop there; at most `maxExtend` files). With `--run N`: `scan` with detection, window = run `First.Wall − margin` … `Last.Wall + margin + 1ms`. `xml_sha256`: sha256 of `--xml` if it is a file named `<XMLName>.xml`, of `<dir>/<XMLName>.xml` if it is a directory, else `""`. Output: `--out -` → `fixture.WriteLog(stdout, …)`; else refuse an existing non-empty dir, then `fixture.WriteCapture(dir, "log.jsonl", "manifest.json", …)` and print `DIR/log.jsonl: E events (R reports, F frames, N notes)` and `DIR/manifest.json` on stdout. `Extracted.Notes` go to stderr via `warn`.
- [ ] **Step 4: Run** the same command → PASS; `go run ./cmd/levalog extract --help` shows the flags.
- [ ] **Step 5: Docs**: README's levalog section and the `usage` examples get an `extract` line.
- [ ] **Step 6: Gate and commit** `feat(levalog): extract a raw-log window as a capture`.

### Task 5: Committed-fixture guard accepts raw-log fixtures

**Files:**
- Modify: `conformance/real_fixtures_test.go`

**Interfaces:**
- Consumes: `leva.SplitLine(line []byte) []leva.Piece`.

- [ ] **Step 1: Write the failing test** `TestFixtureGuardClassifies` (table over a small helper `classify(line []byte) (kind string, ok bool)` extracted from the loop): report → `report`; 69-byte `<…>` → `frame`; `[READY]` → `notice`; `$id: …` → `banner`; a report glued onto a cut-off frame → split into `fragment` + `report`; a 40-byte `<…` without glue → `fragment`; `hello` → not ok.
- [ ] **Step 2: Run** `nix develop --command go test ./conformance/ -run 'FixtureGuard|CommittedFixtures' -v` → FAIL.
- [ ] **Step 3: Implement**: tx allowlist gains `MC@` only; rx lines go through `leva.SplitLine` and `classify`; note event data are checked against the leak strings; fragments are counted and `t.Logf`'d; reports keep checksum/decode/`~` checks.
- [ ] **Step 4: Run** the same command → PASS for all existing fixtures.
- [ ] **Step 5: Gate and commit** `test(conformance): accept LCD frames, notices and notes in committed fixtures`.

### Task 6: The backflush fixture

**Files:**
- Create: `testdata/fixtures/backflush/log.jsonl`, `testdata/fixtures/backflush/manifest.json`
- Modify: `sim/replay_test.go` (or `cmd/levasim/main_test.go`), `logview/runs_test.go` (`TestFindRunsFixture`), `brew/fixture_test.go` (`TestFixtureRuns`)

- [ ] **Step 1: Extract**:
  ```sh
  RAW=$HOME/tmp/medewi-raw   # the copied raw logs, outside the repo
  nix develop --command go run ./cmd/levalog extract --dir "$RAW" \
    -s "2026-11-14 08:58:30" -u "2026-11-14 09:07:00" \
    --out testdata/fixtures/backflush --scenario backflush \
    --notes "Backflush program from the run menu, blind basket inserted. Marks: 'S Backflush' at the start, 'Clicked' at the key press on EMPTY, 'F backflush' at the end."
  ```
  Expected: about 1824 reports, 507 frames, 4 notes (XML + 3 marks); `firmware_version` `$id: leva! (3.2)`.
- [ ] **Step 2: Review** per docs/runbooks/capture-session.md §3: read `manifest.json`; `jq -r .data < testdata/fixtures/backflush/log.jsonl | base64 -d | grep -av '^[{<]'` shows only notes/banners; grep for `fritz.box`, `rancilio`, `Nextcloud`, `/home/`, the brewhost's private name → nothing. **Stop and show the operator the manifest and the review output before `git add`.**
- [ ] **Step 3: Write the tests** (they pass at once — they pin today's behaviour on new data):
  - sim: replay `backflush` with `MC@` → among received frames one contains `EMPTY`; with `MCr` → no line starting `<`.
  - `TestFindRunsFixture/backflush` (XML): 9 runs, all `flush`, none truncated.
  - `TestFixtureRuns` backflush (XML): the recorder closes 9 flushes.
- [ ] **Step 4: Run** `nix develop --command go test -race ./sim/ ./logview/ ./brew/ ./conformance/ -v -run 'backflush|Fixture'` → PASS (with the XML, not skipped).
- [ ] **Step 5: Gate and commit** `test(fixtures): the backflush program from the raw log of 2026-11-14`.

### Task 7: Docs

**Files:**
- Modify: `docs/runbooks/capture-session.md` (new section "Fixture from the raw log"), `docs/machine-behaviour.md` (backflush section names `testdata/fixtures/backflush`; `MC@` replay note), `CLAUDE.md` (architecture paragraph: levasim `MC@`; fixture list if any)

- [ ] **Step 1: Rebase** onto main once PR #55 is merged (`git fetch && git rebase origin/main`); if #55 is not merged, stop and ask.
- [ ] **Step 2: Runbook section**: copy the whole day's raw logs off the brewhost (the variables-first sudo/scp block from this session, with `$DAY`), `levalog extract … --out testdata/fixtures/NAME`, §3 review, commit; `--out -` for quick looks (`| jq`), not through `ssh -t`.
- [ ] **Step 3: machine-behaviour.md and CLAUDE.md** lines as listed above.
- [ ] **Step 4: Gate and commit** `docs: fixtures from the raw log`.

## Addendum 2026-11-15 (spec addenda "the sim holds the LCD" and "LCD custom characters")

### Task 8: LCD custom characters in `leva.ParseFrame`

**Files:**
- Modify: `leva/frame.go` (`ParseFrame`, the `Frame.Rows` doc comment)
- Test: `leva/frame_test.go`; any test elsewhere that pinned the old spaces
- Modify: `docs/machine-behaviour.md` ("Display mirror (`MC@`)": the table of codes, the bar, "meaning of ✓ unknown")

**Interfaces:**
- Produces: `Frame.Rows` with `0x01`→`▍`, `0x02`→`▋`, `0x03`→`█`, `0x04`→`–` (U+2013), `0x05`→`✓` (U+2713); other bytes `< 0x20` stay a space; `0xDF` stays `°`. Rows are then 16 runes, no longer 16 bytes.

- [ ] **Step 1: Write the failing test** `TestParseFrameCustomGlyphs`: a 69-byte frame whose rows contain `1: 94.1\xdf \x04 94.0\xdf`, ` PUMP \x03\x03\x03\x01     `, `\x01             4%`, ` 1.8b\x05  0s 233ml` → rows `1: 94.1° – 94.0°`, ` PUMP ███▍     `, `▍             4%`, ` 1.8b✓  0s 233ml`; a row with `\x00`/`\x06`/`\x07` keeps spaces; each row has 16 runes (`utf8.RuneCountInString`).
- [ ] **Step 2: Run** `nix develop --command go test ./leva/ -run ParseFrame -v` → FAIL.
- [ ] **Step 3: Implement** a small lookup in `ParseFrame`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./...` → PASS; fix tests elsewhere that asserted spaces for these codes (daemon status/panel, levalog frame text); check that the ops panel (`daemon/panel.html`) shows rows with a monospace font so the runes line up.
- [ ] **Step 5: Docs** machine-behaviour.md as listed. **Gate and commit** `feat(leva): show the LCD's bar, dash and check-mark glyphs`.

### Task 9: levasim holds the LCD

**Files:**
- Modify: `sim/sim.go` (replayer: current-LCD state; display-mode emitter; `MCR`; Switch; `Config.Stream` doc)
- Test: `sim/replay_test.go`, `sim/backflush_test.go`; daemon/levacap/medewid tests that count frames

**Interfaces:**
- Consumes: the classification of Task 2 (`isFrameEvent`, last-record rule).
- Produces: in display mode one frame per period `max(100ms/Speed, 1ms)`, content = current LCD (latest recorded frame the timeline has passed; before any, the stream's first frame); first frame immediately on `MC@`. Recorded frames only update the current LCD. Report mode unchanged.

- [ ] **Step 1: Write the failing tests**:
  - `TestMCAtSendsCurrentLCDAtOnce`: stream with frames F1 (t=0) and F2 (t=1000) and reports in between; `MCr` first, wait until a report after t≥1000 has arrived, then `MC@` → the first frame arrives within 2 periods and equals F2.
  - `TestMCAtBeforeFirstFrameSendsFirstFrame`: stream whose first frame is at t=1000; `MC@` at once → F1 (the stream's first frame) arrives within 2 periods.
  - `TestDisplayRepeatsAtFramePeriod`: one frame in the stream, `Speed` 10 → several identical frames arrive, spaced ≥ the period (10 ms), none faster.
  - `TestDisplayAfterOnceKeepsLastFrame`: `Once`; after the stream ends, display mode still emits the last frame.
  - `TestMCRResendsCurrentLCD`; `TestSwitchResetsCurrentLCD`; `TestNoFramesNoDisplay` (stream without frames → nothing in display mode).
  - Backflush: replace the exact-507 count with: every line a frame, ≥1 `EMPTY`, frame count within ±10 % of replay duration / period.
- [ ] **Step 2: Run** `nix develop --command go test -race ./sim/ -v` → the new tests FAIL.
- [ ] **Step 3: Implement**: the replayer updates the current LCD under `s.mu` as it passes frame events (in every mode); a display emitter goroutine (or the replayer's timer) emits the current LCD each period while `display` is on; `MC@` triggers an immediate emit. Keep race-clean; stop the emitter with the replay.
- [ ] **Step 4: Run** `nix develop --command go test -race ./...` → PASS; update daemon/levacap/medewid tests that relied on frames only at recorded times (list each in the commit body).
- [ ] **Step 5: Docs**: CLAUDE.md architecture line and the levasim bullet in machine-behaviour.md ("holds the current LCD"). **Gate and commit** `feat(sim): hold the current LCD and mirror it every 100 ms`.
