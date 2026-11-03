# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

medewi: a per-coffee recipe system for espresso machines running the leva!
controller firmware. Phase 1 (complete): protocol library (`leva/`),
machine simulator (`sim/`, `cmd/levasim`), conformance suite
(`conformance/`), capture tooling (`cmd/levacap`). Phase 2 (next): an
always-on daemon (exporter, shot DB, recipe execution). Start with
[docs/architecture.md](docs/architecture.md); decisions live in
[docs/decisions/](docs/decisions/), plans in `docs/superpowers/plans/`,
operator procedures in [docs/runbooks/](docs/runbooks/) and
[docs/procedure.md](docs/procedure.md).

## Commands

```sh
nix develop                       # toolchain (go, gopls, golangci-lint, gofumpt)
nix develop --command go test -race ./leva/ -run TestName -v   # one test
go run ./cmd/levasim -fixtures testdata/fixtures/shot-genlever # replay a real shot
```

The full gate — run it before EVERY commit (CI runs the same):

```sh
nix flake check -L     # builds + lint, fmt, vet, test -race, tidy; each a cached check
nix build .#checks.x86_64-linux.test -L   # one check
```

The flake only sees files git tracks: `git add` new files first, or the
check runs without them. The checks' source is only Go files, `go.mod`,
`go.sum`, `.golangci.yml` and `testdata/`, so docs-only commits hit the
cache. Tests needing `MEDEWI_XML_PATH` skip inside the sandbox; run them
with `nix develop --command go test …` when you touch them. After
`go.mod`/`go.sum` changes, update `vendorHash` in `flake.nix` (the build
error prints the new one).

Config comes from env vars (`MEDEWI_MACHINE_ADDR`, `MEDEWI_XML_PATH`) via a
local, gitignored `.envrc` — see the README's Configuration section.

## Hard rules

- **Never send a single byte to port 2323.** It is the machine's bootloader
  port; the first byte resets the controller. `leva.Dial` refuses it; the
  sim ships a tripwire; keep both.
- **Real-machine contact only when the user explicitly asks.** Machine
  tests are double-gated (`MEDEWI_MACHINE_ADDR` set AND
  `MEDEWI_MACHINE_CONFIRM=1`). At most one process talks to the machine;
  stream toggles (`MCr`/`MC `) are GLOBAL machine state.
- **Read-only command whitelist:** `MCh MC? MCu MCm MCb MCf` plus
  `MCr`/`MC ` stream control. Anything that writes (setpoints, `MCc`
  functions, `MCa`, PID constants) only lands via an explicitly planned
  write feature with a setpoint clamp, watchdog and verify-after-write.
  For the pure power toggles `MCa`/`MCe`/`MCE` (phase-2 daemon design) the
  clamp is vacuous and the watchdog is the firmware's own eco/standby
  timers; verify-after-write is still mandatory.
- **Never commit:** vendor-authored content (the Status-Monitor XML, help
  text), real `MCu`/`MCm` dumps (license key inside), private hostnames or
  paths. `private/` and `captures/` are gitignored;
  `conformance/real_fixtures_test.go` trips on known-private strings.
- Personas are "barista" and "regular" — no real personal names.
- Pure Go, no cgo (protects cross-compile and gomobile paths).
- TDD (failing test first), conventional commits, one feature branch + PR
  per unit of work; CI runs the same gate as above.

## Architecture in one breath

`leva/` owns the wire: spec parsing from the vendor XML (loaded at runtime,
never committed), fixed-width record decode with optional `~HH` checksum
(`VerifyChecksum` BEFORE `Decode`), and a race-clean `Conn` (two-phase
`Command` with first-line timeout; `WaitReady` readiness gate; CRLF-stripped
lines; `Tap` for capture). `sim/` replays `fixture/` logs with the real
machine's semantics (global reporting toggle, idle-banner heartbeat that
pauses during reporting, whole-message client queues). `conformance/`
asserts the protocol model against sim (CI) or machine (gated) — it is also
the firmware-flash gate. `cmd/levacap` records fixtures atomically;
`cmd/levasim` serves them.

## Empirical machine facts (from real captures; keep code consistent)

- Report lines: 289 bytes, ~4/s, offsets count from the leading `{`.
- Banner `$id: leva! (3.2)` ~1/s when idle, SILENT while reporting.
- A command sent shortly after connect is silently swallowed — even just
  after the first banner. Always go through `leva.WaitReady`.
- A reply's first byte can take >300 ms (hence the two-phase `Command`).
- Eco entry: mode field (offset 4) reads `ECO` and the setpoint drops
  (e.g. 91 → 79), temperatures stay live. STANDBY is the deeper state:
  nearly all report fields are `?`-masked while the stream keeps flowing.
  Report lines can include user-defined profile/dose label strings.
- No `~HH` checksums in the default machine config.
- Report byte 2 (not in the XML) is the pump marker: `*` while leva!
  drives the pump, `-` otherwise, `?` in standby. Wand draws (hot-water
  switch, wired past leva!) show flow with marker `-`. Pressure rises to
  the OPV with the pump off (thermal expansion) — not a pump signal.
  The Volume field resets to 0 at each leva! pump start and keeps
  counting during wand draws; the Flow rate field is a decaying estimate
  that lingers for seconds after the pump stops.
