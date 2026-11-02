# Robustness Round (Issues #2–#4) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the three protocol-robustness findings from the first capture session: the post-banner readiness window (#2), first-reply latency exceeding the command idle gap (#3), and the simulator's missing idle-banner heartbeat (#4).

**Architecture:** All changes stay inside existing packages: `leva/conn.go` gains a first-line timeout and a banner counter plus a shared `WaitReady` helper; `conformance` and `cmd/levacap` switch to that helper; `sim` gains a periodic idle banner that pauses during replay.

**Tech Stack:** unchanged (Go, stdlib only; existing lint/test gate).

**Spec:** GitHub issues #2, #3, #4 (empirical findings recorded 2026-11-02; restated per task below). Binding empirical facts: real machine's idle banner ≈ 1/s and stops while `MCr` reporting is active; a command ~0.7 s after the first banner was swallowed; a first reply later than 300 ms was missed by `Command`.

## Global Constraints

- Same as the phase-1 plan (docs/superpowers/plans/2026-11-01-phase1-foundations.md): pure Go, no cgo; never dial port 2323; binary-safe `[]byte`; gofumpt/golangci-lint clean; tests `-race`; commit trailers:
  `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_01BwcWPWTFuDgwMFMBdmYqMe`
- Backward compatibility within the repo: all existing tests keep passing unless a task explicitly amends them.
- No real-machine contact in tests (unchanged gating).

## Review Focus

1. A command that legitimately produces no output must not hang: the first-line wait is bounded and ctx-cancellable — test pinned in Task 1.
2. `WaitReady` with a dead connection (no banner ever) must fail at its deadline, not block — test pinned in Task 2.
3. The sim heartbeat must never interleave bytes mid-line with replay output (same writer queue discipline) — test pinned in Task 3.
4. Existing conformance-against-sim tests must stay green with the heartbeat ON and the new readiness rule (second banner arrives quickly) — integration step in Task 3.
5. `TestConformAgainstSim`-class timings must not balloon: readiness via second banner must beat the settle fallback when a heartbeat exists — asserted in Task 2's fast-path test.

---

### Task 1: First-line timeout in Conn.Command (#3)

**Files:**
- Modify: `leva/conn.go`
- Test: `leva/conn_test.go`

**Interfaces:**
- Produces: `WithFirstLineTimeout(d time.Duration) Option` (default 2 s, stored in config next to the idle gap). `Command` semantics change: wait up to the first-line timeout for the FIRST command-output line (ctx still wins); once at least one line arrived, collect further lines under the existing idle-gap rule. Zero lines after the timeout → return empty slice, nil error (unchanged result shape). Doc comment updated: the two-phase rule, and the note that a no-output command now costs the first-line timeout.

- [ ] **Step 1: Write failing tests** — `TestCommandWaitsForSlowFirstLine` (fake server delays the first reply 3× the idle gap but within the first-line timeout; reply must be collected — fails under current code), `TestCommandNoOutputBounded` (server sends nothing; `Command` returns empty within ~first-line timeout, well before the 2 s default by configuring a short one; also assert ctx cancel interrupts the wait — Review Focus 1).
- [ ] **Step 2: Run** `nix develop --command go test -race ./leva/ -run TestCommand -v` — expected: FAIL (slow-first-line case returns empty today).
- [ ] **Step 3: Implement** in `leva/conn.go`.
- [ ] **Step 4: Run** `nix develop --command go test -race ./leva/ -v` — all PASS (existing Command tests included).
- [ ] **Step 5: Commit** — `fix(leva): wait for a command's first reply line beyond the idle gap`.

### Task 2: Readiness helper — second banner or settle (#2)

**Files:**
- Modify: `leva/conn.go`, `conformance/conformance.go`, `cmd/levacap/main.go`
- Test: `leva/conn_test.go`, plus amended call sites' tests

**Interfaces:**
- Produces: `(c *Conn) Banners() uint64` (count of `$id:` lines seen, atomic); `leva.WaitReady(ctx context.Context, c *Conn, settle time.Duration) error` — returns nil once `Banners() >= 2`, or once `Banners() >= 1` and `settle` has elapsed since the FIRST banner was observed by WaitReady; errors on ctx expiry. Default settle used by callers: 1500 ms (package-level var in each caller, shrinkable in tests).
- Consumes/updates: `conformance` banner check and levacap's `waitBanner` both delegate to `leva.WaitReady` (levacap keeps its 5 s overall deadline + settle). The runbook's "5 s banner gate" sentence gets a one-line touch naming the second-banner-or-settle rule.

- [ ] **Step 1: Write failing tests** — `TestWaitReadyTwoBannersFast` (server sends two banners 100 ms apart; WaitReady returns before settle elapses — Review Focus 5), `TestWaitReadySingleBannerSettles` (one banner only; returns after settle), `TestWaitReadyNoBannerDeadline` (no banner; ctx deadline error, no hang — Review Focus 2).
- [ ] **Step 2: Run** `nix develop --command go test -race ./leva/ -run TestWaitReady -v` — FAIL (undefined symbols).
- [ ] **Step 3: Implement** `Banners`/`WaitReady`; switch both call sites; touch the runbook sentence.
- [ ] **Step 4: Run** `nix develop --command go test -race ./... ` — all PASS.
- [ ] **Step 5: Commit** — `fix(leva): readiness gate waits for second banner or settle`.

### Task 3: Simulator idle-banner heartbeat (#4)

**Files:**
- Modify: `sim/sim.go`
- Test: `sim/replay_test.go` or new `sim/heartbeat_test.go`

**Interfaces:**
- Produces: `Config.BannerInterval time.Duration` — zero keeps today's behavior (banner once on connect, no heartbeat) so existing tests stay valid; when set, the banner is re-sent to all clients every interval WHILE reporting is off, pauses during active reporting, resumes after `MC `. Heartbeat lines go through the same per-client queue as everything else (whole-message items — Review Focus 3).
- Consumes: the reporting flag/stop-channel machinery from Task 7 of phase 1 (read `sim/sim.go` first).

- [ ] **Step 1: Write failing tests** — `TestHeartbeatWhileIdle` (interval 20 ms → a client sees ≥3 banner lines, each a complete `$id:`-prefixed line), `TestHeartbeatPausesDuringReporting` (start `MCr` with a long-gap stream: no banner between report lines while reporting; after `MC `, banners resume), plus an update run of the conformance suite: with `BannerInterval` set in the conformance test sim config, `TestAllChecksPassAgainstSim` passes and the banner check takes the fast path (Review Focus 4 — amend that test's config in this task).
- [ ] **Step 2: Run** `nix develop --command go test -race ./sim/ -v` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `nix develop --command bash -c 'golangci-lint run && golangci-lint fmt --diff && go vet ./... && go test -race ./... && go mod tidy -diff'` — full gate green.
- [ ] **Step 5: Commit** — `feat(sim): periodic idle banner, silent during reporting`.
