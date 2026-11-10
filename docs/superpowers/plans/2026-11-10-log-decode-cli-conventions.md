# Log decoding CLI and CLI conventions — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `levalog decode` for troubleshooting from raw logs and captures, a shared `systemd.time(7)`-subset parser, and GNU-style `--long` flags across every medewi command, written down as a guideline.

**Architecture:** A new `systime/` package parses times and spans. `pflag` replaces `flag` in all commands. The decoding lives in a new `logview/` package (open → select → decode → format), so #30 can wrap it later. `cmd/levalog` is a thin CLI over it. `leva` exports its glued-line splitter, and `rawlog` exports its file-name parsing and its dump rules, so neither is written twice.

**Tech Stack:** Go 1.26, `github.com/spf13/pflag` (new, pure Go), Nix flake, NixOS module.

**Spec:** [docs/superpowers/specs/2026-11-10-log-decode-cli-conventions-design.md](../specs/2026-11-10-log-decode-cli-conventions-design.md)

## Global Constraints

- Hard rules of CLAUDE.md apply. Never touch port 2323. No machine contact. Pure Go, no cgo. No real logs, dumps or private strings in commits; tests use `testdata/spec/synthetic.xml`, `testdata/fixtures/*` and logs built in the test.
- TDD: failing test first. Conventional commits. Work in a separate git worktree on branch `log-decode` with one rebased PR against main and no merge commits. Commit with `git -c commit.gpgsign=false commit`.
- Before every commit: `git add` new files, then `nix flake check -L` passes. After `go.mod` changes, update `vendorHash` in `flake.nix` from the build error.
- Every time or span form we accept is valid in `systemd.time(7)` with the same meaning; reject the rest. No guessing.
- Long options are `--name`. A single-dash long option (`-ca`) is an error. Short aliases only where the spec names them (`-s`, `-u`).
- `levalog` hides dump lines by default and prints one `dump redacted (N lines)` entry in their place; `--show-dumps` prints them.
- The raw-log allowlist and its tests in `rawlog` keep their behaviour. The refactors in Task 4 must leave every existing `rawlog` test passing unchanged.
- Historical documents (`docs/design/*`, `docs/superpowers/plans/*` other than this one, the 2026-11-10 spec) keep their old flag spelling: they record what was true then. Living docs get updated.

## Review Focus

1. **A file being written right now** (no gzip trailer, last line cut mid-JSON). Expected: the events up to the cut are decoded, with no error exit. → test in Task 5.
2. **A window that starts inside a file** (`-s 10:42` with files starting at 10:00 and 11:00). Expected: the 10:00 file is read and filtered, not skipped. → test in Task 5.
3. **A capture whose rx chunks split lines at arbitrary bytes** (levacap writes raw reads, not lines). Expected: lines are reassembled before classification, and a report split across chunks decodes as one. → test in Task 6.
4. **A report that doesn't fit the spec** (the wrong XML, or too short). Expected: printed raw with the decode error, and decoding continues. → test in Task 6.
5. **Local time around a DST change**, and `HH:MM` meaning today. Expected: parsed in the given `*time.Location`, like journalctl. → test in Task 1 using `Europe/Berlin` on 2026-10-25.

---

### Task 1: Worktree, spec commit, `systime` package

**Files:**
- Create: `systime/span.go`, `systime/timestamp.go`, `systime/flag.go`, `systime/systime_test.go`
- Commit as well: `docs/superpowers/specs/2026-11-10-log-decode-cli-conventions-design.md`, this plan

**Interfaces:**
- Produces:
  - `func ParseSpan(s string) (time.Duration, error)`
  - `func FormatSpan(d time.Duration) string`
  - `func ParseTimestamp(s string, now time.Time, loc *time.Location) (time.Time, error)`
  - `type Span time.Duration` implementing `pflag.Value` (`Set`, `String` via `FormatSpan`, `Type() == "span"`); `func NewSpan(p *time.Duration) *Span`
  - `type Timestamp struct{ T time.Time; Now func() time.Time; Loc *time.Location }` implementing `pflag.Value` (`Type() == "timestamp"`, `String()` is `""` when unset)

- [ ] **Step 1: Set up.** Use `superpowers:using-git-worktrees` to create a worktree on branch `log-decode` from main. Move the untracked spec and plan into it and commit them as `docs: design and plan for log decoding and CLI conventions`. Run `go get github.com/spf13/pflag@latest` and update `vendorHash`; this is folded into this task's commit.

- [ ] **Step 2: Write the failing tests** (table-driven; `TestParseSpan`, `TestFormatSpan`, `TestParseTimestamp`, `TestFlagValues`):
  - ParseSpan ok: `"0"`→0; `"90"`→90s (a bare number means seconds); `"1h30min"`→90m; `"1h 30min"`→90m; `"500ms"`→500ms; `"10us"`, `"10µs"`→10µs; `"2d"`→48h; `"1w"`→168h; `"1.5h"`→90m; `"720h"`→720h; `"1h0m0s"`→1h (Go's `String()` form must round-trip); long names `"5 minutes"`, `"2 hours"`, `"3sec"`.
  - ParseSpan errors: `""`, `"M"`, `"1M"` (a month in systemd: not supported, reject), `"1y"`, `"-5s"`, `"1x"`, `"h"`, an overflow like `"9999999999999h"`.
  - FormatSpan: `90*time.Minute`→`"1h 30min"`; `0`→`"0"`; `500*time.Millisecond`→`"500ms"`; `48*time.Hour`→`"2d"`. Every FormatSpan output round-trips through ParseSpan.
  - ParseTimestamp with `now = 2026-11-10 14:00:00 Europe/Berlin` and `loc = Europe/Berlin`: `"now"`→now; `"today"`→2026-11-10 00:00; `"yesterday"`→2026-11-09 00:00; `"tomorrow"`→2026-11-11 00:00; `"2026-11-10 10:42"`, `"2026-11-10 10:42:05"`, `"2026-11-10"`; `"10:42"`→today 10:42; `"+1h"`→15:00; `"-10min"`→13:50; `"10min ago"`→13:50; `"2026-11-10 10:42 UTC"`→11:42 Berlin.
  - DST (Review Focus 5): `"2026-10-25 02:30"` in Berlin is valid (the second 02:30 is ambiguous; accept Go's choice and pin it in the test). `"2026-10-25 12:00"` minus `"2026-10-25 00:00"` is 13h.
  - ParseTimestamp errors: `""`, `"10:61"`, `"2026-13-01"`, `"next week"`, `"1h"` (a span without a sign is not a timestamp), `"@"`.
  - TestFlagValues: a `pflag.FlagSet` with `NewSpan(&d)` parses `--every 1h30min`; `Timestamp` with fixed `Now`/`Loc` parses `-s 10:42` via `VarP(..., "since", "s", ...)`.

- [ ] **Step 3: Run them to see them fail:** `nix develop --command go test ./systime/` → FAIL (undefined).

- [ ] **Step 4: Implement.** Spans: whitespace-separated or adjacent groups of `number[.fraction] unit`, with the units from systemd.time(7): `usec us µs`, `msec ms`, `seconds second sec s`, `minutes minute min m`, `hours hour hr h`, `days day d`, `weeks week w`. Reject `M`/`months`/`y`/`years` (systemd has them, but at 30.44d and 365.25d; we don't need them). Check for overflow. Timestamps: the forms tested above and nothing else; `+SPAN`, `-SPAN` and `SPAN ago` go through `ParseSpan`; a trailing ` UTC` switches the location.

- [ ] **Step 5: Run:** `nix develop --command go test -race ./systime/` → PASS.

- [ ] **Step 6: Commit:** `feat(systime): parse a systemd.time(7) subset`.

### Task 2: Move every command to pflag

**Files:**
- Modify: `cmd/medewictl/main.go`, `cmd/medewictl/until.go` (delete `parseUntil`; the file goes if it is empty), `cmd/medewictl/until_test.go` (rewrite against the new flag) and `main_test.go`; `cmd/medewid/main.go`, `main_test.go`, `log.go`, `log_test.go`, `machine_test.go`; `cmd/levacap/main.go`, `reset.go` and their tests; `cmd/levasim/main.go`, `main_test.go`
- Modify: `nix/module.nix:146-156` (`ExecStart` words → `--log-flavour`, `--listen`, `--health-addr`, `--xml`, `--tls-cert`, `--tls-key`, `--tls-client-ca`, `--tls-metrics-ca`), `nix/vm-test.nix:75` (`levasim --addr … --tripwire … --fixtures …`)

**Interfaces:**
- Consumes: `systime.NewSpan`, `systime.ParseSpan`, `systime.ParseTimestamp` (Task 1)
- Produces: nothing new. Flag names stay the same; only the dash style changes.

- [ ] **Step 1: Write the failing tests**, one per command, in the existing test files:
  - `TestSingleDashLongFlagRejected`: `run([]string{"-ca", "x", "status"}, …)` (medewictl), `run([]string{"-machine", "h:1"}, …)` (medewid), `levasim -addr …`, `levacap … -addr …` all exit 2, and stderr contains `unknown shorthand flag`.
  - `TestDoubleDashFlags`: the existing happy-path tests rewritten to `--flag` keep passing (convert every existing test argument list from `-x` to `--x`).
  - medewictl `TestReleaseUntil`: `--until +1h` → now+1h; `--until 1h30min` is rejected (a span is not a timestamp; the error names `--until` and shows `+1h30min` as the example); `--until "2026-11-10 15:00"` with now 14:00 → 15:00 (absolute times become allowed, the builder's call); `--until "-1h"` and any time not after now → error `--until must be in the future`. The error messages say `--until`, not `-until`.
  - medewid `TestDurationEnvSpan`: `MEDEWID_RAW_RETENTION=720h` and `=30d` both work; `=1M` exits 2 naming the variable. `--display-every 0` still disables sampling.
  - levacap `--display-every 10s` and `--display-for 500ms` parse as before.

- [ ] **Step 2: Run:** `nix develop --command go test ./cmd/...` → FAIL.

- [ ] **Step 3: Implement.** Use `pflag.NewFlagSet(name, pflag.ContinueOnError)`. Every `fs.Duration`/`DurationVar` becomes `fs.Var(systime.NewSpan(&d), …)`, and medewid's env loop calls `systime.ParseSpan`. medewictl keeps two flag sets: the global one with `SetInterspersed(false)` so that `medewictl --url U status` stops at the subcommand, and the per-subcommand set. `release --until` is parsed by `systime.ParseTimestamp(s, now, time.Local)`. Update usage texts and error strings (`-machine (or …) is required` → `--machine …`, `-insecure-http` → `--insecure-http`, and so on). `pflag.ErrHelp` replaces `flag.ErrHelp`. `fset.Visit` keeps working with `*pflag.Flag`.

- [ ] **Step 4: Run:** `nix develop --command go test -race ./cmd/...` → PASS. Then `nix flake check -L` → PASS, including `medewid-vm` and `medewid-module`, which prove the new `ExecStart` and the levasim unit.

- [ ] **Step 5: Commit:** `feat!: GNU-style --long flags for all commands`, with a `BREAKING CHANGE:` footer saying single-dash long options (`-ca`, `-until`) are rejected.

### Task 3: Export leva's glued-line splitter

**Files:**
- Modify: `leva/conn.go:540-570` (`split` uses the new function), Create: `leva/split.go`, Test: `leva/split_test.go`

**Interfaces:**
- Produces: `type Piece struct{ Data []byte; Fragment bool }` and `func SplitLine(line []byte) []Piece` (CRLF already stripped). The result is the same split `Conn.split` makes today: intact frames and reports are whole pieces, cut-off records are `Fragment: true`, and a non-record remainder is a whole piece.

- [ ] **Step 1: Failing test `TestSplitLine`:** an intact 69-byte frame containing `<100` gives 1 piece; a frame cut at 40 bytes plus a whole report gives `[fragment, report]`; a whole report with a frame glued on gives `[report, frame]`; a cut-off frame alone gives `[fragment]`; a reply `"P <100"` gives 1 piece; `"$id: leva! (3.2)"` glued after a fragment gives `[fragment, banner]`.
- [ ] **Step 2: Run:** `nix develop --command go test ./leva/ -run TestSplitLine` → FAIL.
- [ ] **Step 3: Implement** `SplitLine` by moving the loop out of `split`. `split` then routes every non-fragment piece and counts the fragments.
- [ ] **Step 4: Run:** `nix develop --command go test -race ./leva/ ./conformance/` → PASS. The existing fragment-counter tests are unchanged.
- [ ] **Step 5: Commit:** `refactor(leva): export SplitLine for offline decoding`.

### Task 4: Export rawlog's file-name parsing and dump rules

**Files:**
- Create: `rawlog/rules.go`, Test: `rawlog/rules_test.go`; Modify: `rawlog/rawlog.go` (`tx`, `judge`, `open`, `prune` and `defaults` use the exports)

**Interfaces:**
- Produces:
  - `func FileStart(name string) (time.Time, bool)`: the UTC start from a base name `20060102T150405.000Z.jsonl.gz`, false for anything else. `prune` uses it too.
  - `func IsDumpCommand(cmd []byte) bool` (`MCu`/`MCm` with any suffix), `func IsStreamToggle(cmd []byte) bool` (`MCr`, `MC `, `MC@`)
  - `func IsDumpLine(line []byte) bool` (starts with `|` or `:`), `func IsStreamLine(line []byte) bool` (`{`, `<`, `$id:`, or `[…]` of at most 32 bytes; CRLF stripped)
  - `const DefaultReplyWindow = 5 * time.Second`, `const DefaultDumpQuiet = 60 * time.Second` (used by `defaults`)

- [ ] **Step 1: Failing test `TestRules`:** table rows for each function, including `FileStart("20261110T094200.123Z.jsonl.gz")` → 2026-11-10 09:42:00.123 UTC, `FileStart("foo.jsonl.gz")` → false, `IsStreamLine([]byte("[READY]"))` → true, and a 33-byte `[…]` line → false.
- [ ] **Step 2: Run:** `nix develop --command go test ./rawlog/ -run TestRules` → FAIL.
- [ ] **Step 3: Implement** by moving the existing conditions out. Behaviour does not change.
- [ ] **Step 4: Run:** `nix develop --command go test -race ./rawlog/ ./daemon/` → PASS with no existing test edited.
- [ ] **Step 5: Commit:** `refactor(rawlog): export file-name and dump rules`.

### Task 5: `logview` — open sources and select a window

**Files:**
- Create: `logview/source.go`, Test: `logview/source_test.go`

**Interfaces:**
- Consumes: `rawlog.FileStart`, `fixture.Event`, `fixture.Manifest`
- Produces:
  - `type Source struct{ Path string; Start time.Time; HasWall bool; Notes string; Events []fixture.Event }`. `HasWall` is true only for raw logs. `Notes` is the capture manifest's `notes`, or empty.
  - `func Open(path string) (Source, error)`. `path` is a raw-log file (name parsed by `FileStart`), a capture `log.jsonl` (plain or gzip, detected by the magic bytes `1f 8b`), or a capture directory holding `log.jsonl`. A sibling `manifest.json` fills `Notes`. A missing gzip trailer or a cut last line returns the events before it, with no error (same rule as `rawlog.ReadFile`, which is reused for gzip).
  - `func Select(dir string, since, until time.Time) ([]string, error)`: raw-log files in `dir`, sorted by start. File *i* is kept when `start_i < until` (a zero `until` means no bound) and (`start_{i+1} > since`, or *i* is the last file). Names that `FileStart` rejects are ignored.
  - `func (s Source) Wall(ev fixture.Event) time.Time` = `Start + T ms`

- [ ] **Step 1: Failing tests:**
  - `TestOpenRawLog`: write two events with `rawlog.Open`/`Session` into `t.TempDir()`, then `Open` that file. `HasWall` is true and `Start` equals the name time.
  - `TestOpenTruncated` (Review Focus 1): gzip three events, cut the file to 70%, `Open` it. No error, and at least one event.
  - `TestOpenCapture`: `Open("../testdata/fixtures/shot-genlever")` and `Open(".../log.jsonl")` give the same events, `HasWall` false, and `Notes` equal to the manifest's.
  - `TestSelect` (Review Focus 2): files named 10:00, 11:00 and 12:00 UTC plus `junk.txt`. Window 10:42–10:50 → [10:00]; 10:42–11:05 → [10:00, 11:00]; 12:30–(zero) → [12:00]; (zero)–(zero) → all three; 09:00–09:30 → none.
- [ ] **Step 2: Run:** `nix develop --command go test ./logview/` → FAIL.
- [ ] **Step 3: Implement** in `logview/source.go`.
- [ ] **Step 4: Run:** `nix develop --command go test -race ./logview/` → PASS.
- [ ] **Step 5: Commit:** `feat(logview): open raw logs and captures, select a time window`.

### Task 6: `logview` — decode events into entries

**Files:**
- Create: `logview/decode.go`, Test: `logview/decode_test.go`

**Interfaces:**
- Consumes: `Source` (Task 5), `leva.SplitLine` (Task 3), `leva.VerifyChecksum`, `(*leva.RecordSpec).Decode`, `leva.ParseFrame`, the `rawlog` rules (Task 4)
- Produces:
  - `type Kind string` with the constants `KindCmd="cmd"`, `KindReport="report"`, `KindFrame="frame"`, `KindBanner="banner"`, `KindNotice="notice"`, `KindReply="reply"`, `KindNote="note"`, `KindFragment="fragment"`, `KindPartial="partial"` (an unterminated rx tail at the end of the file), `KindRedacted="redacted"`
  - `type Entry struct{ Offset time.Duration; Wall time.Time; HasWall bool; Dir fixture.Dir; Kind Kind; Raw []byte; Record *leva.Record; Frame *leva.Frame; Glued bool; Err error; Count int }`. `Count` is the number of hidden lines for `KindRedacted`. `Glued` marks pieces that came from one split line.
  - `type Options struct{ Spec *leva.RecordSpec; ShowDumps bool; Since, Until time.Time }`. A nil `Spec` gives raw reports with no error per line.
  - `func Decode(src Source, opt Options) []Entry`

- [ ] **Step 1: Failing tests**, using events built in the test and `leva.LoadSpecFile("../testdata/spec/synthetic.xml")`:
  - `TestDecodeKinds`: tx `"MCr\r\n"` → cmd; rx report → report with `Record` set; rx 69-byte frame → frame with `Frame.Rows`; `$id: leva! (3.2)` → banner; `[READY]` → notice; `"P 12\r\n"` → reply; a note event → note with `Raw` holding the mark text.
  - `TestDecodeReassembles` (Review Focus 3): one report split over three rx events at byte 7 and byte 150 gives exactly one report entry, at the first chunk's offset.
  - `TestDecodeGlued`: a cut frame with a report glued on gives `[fragment (Glued), report (Glued)]`.
  - `TestDecodeBadReport` (Review Focus 4): a report shorter than the spec needs gives `KindReport` with `Err != nil` and `Record == nil`, and the following entries still decode.
  - `TestDecodeRedactsDumps`: tx `MCu`, then rx `|0001…` ×3, then rx `"some reply"`, then a report 1 s later. The result is `[cmd, redacted(Count 4), report]`. With `ShowDumps` it is `[cmd, reply×4, report]`. Rule: hide a line if `IsDumpLine`, or if it is not `IsStreamLine` and arrives within `rawlog.DefaultDumpQuiet` of the last dump command or dump line. Consecutive hidden lines fold into one entry at the first one's time.
  - `TestDecodeWindow`: with `Since`/`Until` set on a raw source, only entries with `Since <= Wall < Until` come out.
  - `TestDecodeTail`: an rx tail without `\n` at the end of the file gives `KindPartial`.
- [ ] **Step 2: Run:** `nix develop --command go test ./logview/ -run TestDecode` → FAIL.
- [ ] **Step 3: Implement** `Decode`. rx bytes go into a line buffer that keeps the time of its first byte. Each complete line is CRLF-stripped, run through `SplitLine`, classified, and redacted where the rule says so. Reports go through `VerifyChecksum`, then `Decode`. Set `Glued` when `SplitLine` returned more than one piece.
- [ ] **Step 4: Run:** `nix develop --command go test -race ./logview/` → PASS.
- [ ] **Step 5: Commit:** `feat(logview): decode events into typed entries with dump redaction`.

### Task 7: `logview` — text format

**Files:**
- Create: `logview/format.go`, Test: `logview/format_test.go`

**Interfaces:**
- Consumes: `Entry` (Task 6)
- Produces: `func Format(w io.Writer, e Entry, loc *time.Location) error`, one line per entry

Layout (fixed here so tests can pin it): `OFFSET WALL DIR KIND TEXT`, single-space separated.
- `OFFSET`: seconds from the file start with 3 decimals, right-aligned to 10 characters (`   592.123`).
- `WALL`: `2006-01-02 15:04:05.000` in `loc`, or `-` without wall time.
- `DIR`: `tx`, `rx` or `note`, padded to 4.
- `KIND`: padded to 8, with a `+` suffix when `Glued`.
- `TEXT`:
  - report: `name=value` pairs joined by `; `, values trimmed raw with `?` masks kept; an unnamed field shows as `#index=value`;
  - frame: `bkl=1 key=0 layout=2 |row1|row2|row3|row4|`;
  - redacted: `dump redacted (N lines)`;
  - a decode error: the raw line quoted with `%q`, then `  # error: …`;
  - everything else: the text quoted with `%q` when it contains bytes outside printable ASCII or Latin-1, else as it is.

- [ ] **Step 1: Failing test `TestFormat`**, golden strings for one entry of each kind, including a glued fragment (`fragment+`) and a source with no wall time.
- [ ] **Step 2: Run:** `nix develop --command go test ./logview/ -run TestFormat` → FAIL.
- [ ] **Step 3: Implement** `Format`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit:** `feat(logview): one-line text format per entry`.

### Task 8: `cmd/levalog`, packaging, deployment, docs and guideline

**Files:**
- Create: `cmd/levalog/main.go`, `cmd/levalog/main_test.go`, `docs/cli-conventions.md`
- Modify: `flake.nix:71-76` (add `levalog = build "levalog";`), `nix/module.nix` (`environment.systemPackages` gets a wrapper), `nix/vm-test.nix` (assert), `CLAUDE.md` (pointer to the conventions and `cmd/levalog` in the phase list), `README.md`, `docs/deployment.md`, `docs/runbooks/capture-session.md`, `docs/architecture.md` (the `logview/` and `systime/` packages), and any other living doc `git grep` finds with single-dash flags
- Create: `docs/runbooks/troubleshoot-connection.md`, a short "machine disconnected" procedure: `sudo levalog decode -s … -u …` plus `journalctl -u medewid -S … -U …` over the same window

**Interfaces:**
- Consumes: `logview.Open`, `logview.Select`, `logview.Decode`, `logview.Format`, `systime.Timestamp`, `leva.LoadSpecFile`
- Produces: the CLI `levalog [--xml F] decode [--dir D] [-s|--since T] [-u|--until T] [--show-dumps] [FILE|DIR ...]`
  - `--xml` is global and placed before the subcommand (set `SetInterspersed(false)` on the global set), so the wrapper can prepend it. Its env default is `MEDEWID_XML_PATH`, then `MEDEWI_XML_PATH`.
  - `--dir` defaults to `MEDEWID_RAW_DIR`, then `/var/lib/medewid/raw`. It is used when no FILE is given.
  - Without a spec: one stderr warning `levalog: no vendor XML (--xml / MEDEWI_XML_PATH); reports shown raw`, and the exit code stays 0.
  - `-s`/`-u` together with a capture argument: exit 2, `levalog: --since/--until need raw logs with wall time; <path> is a capture`.
  - Several sources are printed in the order of their start time. Before each one comes a header line `# <path>`, plus `# notes: <notes>` when `Notes` is not empty.
  - Exit codes: 0 ok, 1 read error, 2 usage error.

- [ ] **Step 1: Failing tests `TestDecodeCmd`:** build a raw dir in `t.TempDir()` via `rawlog`, then `run([]string{"--xml", synth, "decode", "--dir", dir, "-s", "…", "-u", "…"}, stdout, stderr)` and check that the stdout lines match the window. Also: no XML → warning and exit 0; a capture plus `-s` → exit 2; `decode ../../testdata/fixtures/shot-genlever` → a `# notes:` header and report lines; `-ca` → exit 2.
- [ ] **Step 2: Run:** `nix develop --command go test ./cmd/levalog/` → FAIL.
- [ ] **Step 3: Implement** `cmd/levalog/main.go` with `func run(args []string, stdout, stderr io.Writer) int` in the shape of medewictl.
- [ ] **Step 4: Packaging.** In the module add `environment.systemPackages = [ (pkgs.writeShellScriptBin "levalog" ''exec ${self.packages.${system}.levalog}/bin/levalog --xml ${lib.escapeShellArg cfg.xmlFile} "$@"'') ];`, following the module's existing package plumbing. In the VM test, after `reports started`: `brewhost.succeed("levalog decode --since=-5min | grep -q ' report '")` (the `=` form, so the leading `-` can't be read as a flag). The raw log needs a flush (5 s); use `wait_until_succeeds`.
- [ ] **Step 5: Write `docs/cli-conventions.md`.** It covers GNU-style `--long` options with short aliases only by intent; `--since/-s` and `--until/-u` for time ranges as in journalctl; times and durations only in `systemd.time(7)` forms, with the supported subset copied from `systime`; env vars as defaults that flags override, named in the help text; exit codes 0/1/2. Link it from CLAUDE.md (one line in "Commands"). Update the living docs listed above to `--flag` spelling and document `levalog` in the README.
- [ ] **Step 6: Run** `nix develop --command go test -race ./...` and then `nix flake check -L` → PASS, including `medewid-vm`.
- [ ] **Step 7: Commit:** `feat(levalog): decode raw logs and captures (#19)`, then `docs: CLI conventions and the connection troubleshooting runbook` as a second commit if the diff reads better split.

### Finish

- [ ] Run `superpowers:finishing-a-development-branch`: rebase onto main and open one PR that closes #19. The PR body lists the breaking change: single-dash flags no longer work, and the brewhost config needs no change because the module writes `ExecStart` itself.
