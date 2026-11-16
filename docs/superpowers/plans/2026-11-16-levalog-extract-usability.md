# levalog extract usability — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** remove the friction three agents hit with `levalog extract` (sessions, marks, stdout/stdin, margin, help).

**Architecture:** one session model in `logview` (pure, over opened `Source`s) used by `extract` (refusal text, `--split-sessions`) and `summary`; a text filter in the events selection; stdin as a capture source; per-subcommand help texts split out of `usage`.

**Tech Stack:** Go, pflag, `systime`, existing `logview`/`fixture`/`rawlog`.

**Spec:** [docs/superpowers/specs/2026-11-16-levalog-extract-usability-design.md](../specs/2026-11-16-levalog-extract-usability-design.md)

## Global Constraints

- Branch `backflush-fixture`, worktree `.claude/worktrees/backflush-fixture`; TDD; conventional commits; `git -c commit.gpgsign=false commit`; trailer `Co-Authored-By:` naming the model that wrote it.
- Gate before every commit: `nix develop --command gate` (UTC); `nix flake check -L` before the push.
- CLI per docs/cli-conventions.md: pflag long options, `-s/--since`, `-u/--until`, exit codes 0/1/2; times printed in local time with zone offset.
- Tests with local-time arguments pin the zone (`inCET`), as in cmd/levalog/extract_test.go.
- No private hostnames or paths in committed text.

## Review Focus

1. A window whose start lies inside a session that began before the input → the sessions block and the refusal say the session's start is not in the input.
2. `--split-sessions` over a window with a session that has no events in the window → that part is skipped with a note, numbering stays contiguous.
3. `--match` with an empty string → usage error (exit 2), not "match everything".
4. `-` given twice, or `-` mixed with raw logs and `-s/-u` → usage error.
5. `--margin` with an open end (`-s` only) → only the start widens.

---

### Task U1: Sessions in `logview`

**Files:** Create `logview/session.go`, `logview/session_test.go`.

**Interfaces — Produces:**
```go
// Session is one raw-log session as inferred from the files.
type Session struct {
    Start     time.Time // wall time of its first file's start
    Files     []string  // basenames, in order
    StartKnown bool     // its first file is among the inputs
    Firmware  string    // its last "$id:" banner seen in the inputs ("" if none)
    XML       string    // its last xml note name seen in the inputs ("" if none)
}
// Sessions groups srcs (raw logs sorted by Start) into sessions by the
// SessionStart rule.
func Sessions(srcs []Source) []Session
// SplitWindow returns the parts of [since, until) per session (local
// boundaries at each session's Start), dropping parts outside every
// source.
func SplitWindow(ss []Session, since, until time.Time) []Window
type Window struct{ Since, Until time.Time; Session int }
```
- [ ] Tests first: rotation joins (note-first file), banner-first file starts a session, first source note-first → StartKnown false, Firmware/XML per session, SplitWindow over 3 sessions incl. one empty part dropped.
- [ ] Implement; reuse `SessionStart`; `Extract` keeps its own behaviour.
- [ ] Gate, commit `feat(logview): raw-log sessions and per-session windows`.

### Task U2: `extract` refusal lists all boundaries; `--split-sessions`

**Files:** Modify `cmd/levalog/extract.go`, tests in `cmd/levalog/extract_test.go`.
- [ ] Tests first: a window over 3 sessions → exit 1, stderr names both boundaries in local time and prints three `-s '…' -u '…'` lines; `--split-sessions --out DIR` → `DIR/01`, `DIR/02`, `DIR/03` each a capture with its own manifest (firmware per session), stdout lists them; `--split-sessions --out -` → exit 2; a window inside one session with `--split-sessions` → `DIR/01` only.
- [ ] Implement with `logview.Sessions`/`SplitWindow`; refusal text: `levalog: the window crosses N raw-log session boundaries (…); extract each part:` + one line per part.
- [ ] Gate, commit `feat(levalog): name every session boundary; extract --split-sessions`.

### Task U3: `summary` sessions block

**Files:** Modify `cmd/levalog/summary.go` (and `logview/summary.go` if the text writer lives there), tests.
- [ ] Tests first: summary over a dir with 2 sessions in the window prints a `sessions:` block, one line per session: start (local, zone), first file (+ "not in input" when StartKnown false), firmware, xml, covered part; jsonl output carries the same as a `sessions` array; the per-source `firmware:`/`xml:` labels become `firmware (in this file):` / `xml (in this file):` or equivalent wording.
- [ ] Implement; gate; commit `feat(levalog): summary lists raw-log sessions`.

### Task U4: `events --match`, marks in the help

**Files:** Modify `cmd/levalog/events.go`, the selection in `logview` (`Selector`/`Selection`), `cmd/levalog/main.go` help; tests.
- [ ] Tests first: `--match backflush` finds `mark: S Backflush` and `mark: F backflush` (case-insensitive) and nothing else on a synthetic raw dir; matches a frame row text and a notice; `--match ''` → exit 2; combines with `--event note`.
- [ ] Implement as a selection predicate on the event's raw text (frame: the parsed rows joined with `|` too, so `EMPTY` matches).
- [ ] Help: the marks sentence from the spec; gate; commit `feat(levalog): events --match; name marks in the help`.

### Task U5: `--margin` with `-s/-u`

**Files:** `cmd/levalog/extract.go`, `cmd/levalog/events.go`, tests.
- [ ] Tests first: `extract -s 08:59:21 -u 09:04:20 --margin 30s` equals `-s 08:58:51 -u 09:04:50`; `-s` only + margin widens only the start; `events` likewise; `--margin` with neither window nor run → exit 2.
- [ ] Implement; update the flag help; gate; commit `feat(levalog): --margin widens -s/-u windows too`.

### Task U6: stdin `-` as a capture; `--out -` help

**Files:** `cmd/levalog/input.go` (`sourcePaths`), `logview/source.go` (an `OpenReader` or a `-` case), tests.
- [ ] Tests first: `levalog events --event frame -` with a capture on stdin prints its frames; `runs -` and `summary -` work; `-` twice → exit 2; `-` with `-s/-u` → exit 2 (captures have no wall time); the round trip `extract … --out -` piped into `events -` gives the same events as `events` on the written capture dir.
- [ ] Help: `data` is base64; decode with `jq -r '.data | @base64d'`; `-` reads a capture from stdin. Gate; commit `feat(levalog): read a capture from stdin as -`.

### Task U7: help per subcommand and the small fixes

**Files:** `cmd/levalog/main.go` (split `usage` into a shared part and per-subcommand texts), `cmd/levalog/*.go`, tests.
- [ ] Tests first: `levalog extract --help` prints `usage: levalog extract`, the extract paragraph, its flags, its examples, and a pointer line to `levalog --help`, and does NOT contain the `fields` / `runs --stats` paragraphs; same for each subcommand; `levalog --help` unchanged in content.
- [ ] Small fixes, each with a test: XML name mismatch warns once per distinct name; the "XML named only at" note in local time with zone; extract counts line adds "N other"; help examples with neutral times.
- [ ] Gate; commit `feat(levalog): help per subcommand; quieter, local-time notes`.

### Task U8: Verify with a fresh agent

- [ ] Re-run the usability brief (`.superpowers/sdd/…/usability-brief.md`) with a fresh Haiku agent into a new OUTDIR; compare commands, failures and friction with the first Haiku run; report to the operator. No commit.
