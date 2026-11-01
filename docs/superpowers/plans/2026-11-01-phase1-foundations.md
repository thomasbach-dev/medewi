# Medewi Phase 1 (Foundations) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bootstrap the medewi mono-repo and build the leva! protocol library, the machine simulator (levasim), the conformance suite, and the capture tool (levacap) — everything needed to develop against a simulated espresso machine and to run the first real-machine capture session.

**Architecture:** One Go module. Package `leva` owns the wire protocol (spec parsing, record decoding, connection with command/report demultiplexing); `fixture` owns the recorded-traffic format; `sim` is an importable fake machine replaying fixtures; `conformance` asserts the protocol model against any target (sim in CI, real machine on demand); two thin CLIs wrap these.

**Tech Stack:** Go (from nixpkgs via flake devShell), stdlib + `golang.org/x/text` (Latin-1 charset for the vendor XML) only. Nix flake, direnv.

**Spec:** `docs/design/2026-11-01-phase1-foundations.md` (read it first; it carries the machine facts and safety rules).

## Global Constraints

- Module path: `github.com/thomasbach-dev/medewi`. Pure Go, **no cgo**; dependencies: stdlib + `golang.org/x/text` only.
- **Never dial port 2323** (bootloader: first byte resets the machine). `leva.Dial` refuses it in code; levasim carries a tripwire listener.
- Never commit: vendor-authored content (Status-Monitor XML, help text), real `MCu`/`MCm` dumps, real personal names. Personas: "barista", "regular".
- Wire data is `[]byte` end to end; lines are CR+LF; payloads may contain NUL and Latin-1 bytes — no lossy string round-trips.
- Instance config via env vars from a local, gitignored `.envrc` (`use flake`): `MEDEWI_MACHINE_ADDR=<machine host:port>`, `MEDEWI_XML_PATH=<local vendor XML>`.
- Tests must not require the real machine: anything targeting `MEDEWI_MACHINE_ADDR` skips when unset and additionally requires `MEDEWI_MACHINE_CONFIRM=1`.
- Formatting law: gofumpt + goimports with local prefix `github.com/thomasbach-dev/medewi`, enforced via golangci-lint v2 formatters (`golangci-lint fmt --diff` must be clean). Lint law: `golangci-lint run` with the repo's `.golangci.yml` (curated set, never enable-all). Tests run with `-race` where a task says so and always in the Task 11 gate and CI.
- Every commit message ends with these two trailer lines:
  `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_01BwcWPWTFuDgwMFMBdmYqMe`

## Review Focus

1. A report line containing NUL or Latin-1 bytes mid-payload (another client using the Virtual-LCD protocol) must flow through reader, demux, and decoder uncorrupted — test pinned in Tasks 4 and 5.
2. Command output interleaved with report lines (single shared UART fans everything to every client) must demultiplex: reports never appear in `Command` results, commands never stall the report channel — test pinned in Task 5.
3. The machine dropping the connection mid-stream (5-minute idle timeout, power-off) must close the reports channel and surface the cause; no goroutine leak — test pinned in Task 5.
4. The `~HH` checksum is optional: lines without it decode fine; lines with a wrong one fail loudly — test pinned in Task 4.
5. `MEDEWI_MACHINE_ADDR` accidentally pointing at `:2323` must be refused before any byte is sent — tests pinned in Task 5 (`Dial`) and exercised again via levacap (Task 9).

---

### Task 1: Repo bootstrap (flake, direnv, module)

**Files:**
- Create: `go.mod`, `flake.nix`, `.envrc`, `.gitignore`, `.golangci.yml`
- Already present (commit as part of this task): `docs/design/2026-11-01-phase1-foundations.md`, this plan

**Interfaces:**
- Produces: a working `nix develop` shell with `go`, `gopls`, `golangci-lint`; the Go module `github.com/thomasbach-dev/medewi`.

- [ ] **Step 1: Write `go.mod`** — module `github.com/thomasbach-dev/medewi`, `go` directive matching the nixpkgs Go major.minor.

- [ ] **Step 2: Write `flake.nix`** — inputs `nixpkgs` (nixos-unstable); outputs for `x86_64-linux`: `devShells.default` with `go`, `gopls`, `golangci-lint`, `gofumpt`, `govulncheck`; leave a `packages` attrset stub (filled in Task 11).

- [ ] **Step 2a: Write `.golangci.yml`** (exact content; then verify with `nix develop --command golangci-lint config verify`, adjusting key shapes only if the schema demands):

```yaml
version: "2"

linters:
  default: standard
  enable:
    - revive
    - misspell
    - gocritic
    - unparam

formatters:
  enable:
    - gofumpt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - github.com/thomasbach-dev/medewi
```

- [ ] **Step 3: Write `.envrc` and `.gitignore`**

`.envrc` (exact content):
```bash
use flake
export MEDEWI_MACHINE_ADDR=machine.example:23
export MEDEWI_XML_PATH=/path/to/vendor/leva-release.xml
```
`.gitignore`: `.direnv/`, `result*`, `captures/`.

- [ ] **Step 4: Verify the shell**

Run: `git add -A && nix develop --command go version`
Expected: prints the Go version (flakes only see tracked files — add first).

- [ ] **Step 5: Commit** — `chore: bootstrap flake, direnv, go module` (include the docs).

### Task 2: fixture package (recorded-traffic format)

**Files:**
- Create: `fixture/fixture.go`, `fixture/fixture_test.go`

**Interfaces:**
- Produces:
  - `type Dir string` with `const TX Dir = "tx"`, `RX Dir = "rx"`
  - `type Event struct { T int64 \`json:"t"\`; Dir Dir \`json:"dir"\`; Data []byte \`json:"data"\` }` — `T` = milliseconds since session start; `Data` marshals as base64 (encoding/json default for []byte).
  - `func WriteLog(w io.Writer, events []Event) error` / `func ReadLog(r io.Reader) ([]Event, error)` — JSON Lines, one event per line.
  - `type Manifest struct { Scenario, FirmwareVersion, XMLName, XMLSHA256, MachineAddr, Notes string; Recorded time.Time }` with `func (m Manifest) Save(path string) error` / `func LoadManifest(path string) (Manifest, error)` (pretty-printed JSON).

- [ ] **Step 1: Write failing tests** — `TestLogRoundTrip` (events containing `\x00`, `\r\n`, byte 0xDF survive Write→Read byte-identical; T and Dir preserved), `TestManifestRoundTrip`.

- [ ] **Step 2: Run** `go test ./fixture/ -v` — expected: FAIL (undefined symbols).

- [ ] **Step 3: Implement** the interfaces above.

- [ ] **Step 4: Run** `go test ./fixture/ -v` — expected: PASS.

- [ ] **Step 5: Commit** — `feat(fixture): JSONL traffic log and manifest`.

### Task 3: Spec parser (Status-Monitor XML → RecordSpec)

**Files:**
- Create: `leva/spec.go`, `leva/spec_test.go`, `testdata/spec/synthetic.xml`

**Interfaces:**
- Produces:
  - `type ValueType int` with `const (String ValueType = iota; Float; Int)`
  - `type ValueSpec struct { Index int; Name string; At, Len int; Type ValueType; Unit string }` — `Name` is the XML `export` attribute (may be empty).
  - `type RecordSpec struct { Values []ValueSpec }`
  - `func ParseSpec(r io.Reader) (*RecordSpec, error)`; `func LoadSpecFile(path string) (*RecordSpec, error)`

**Vendor schema facts (from the real file, which is NOT committed):** root `<cfg version="4">`; one `<values>` element holding ~52 `<value at="7" len="6" export="Temperature PID1" type="float" unit="&#xb0;C">…</value>` entries; `export`, `unit`, and element text are optional; `type` ∈ {string,float,int}. The file has **no XML encoding declaration but contains Latin-1 bytes in comments** — `ParseSpec` must set `Decoder.CharsetReader` and fall back to `golang.org/x/text/encoding/charmap.ISO8859_1` so parsing survives. Other elements (`views`, `palettes`, `render`, …) are ignored.

- [ ] **Step 1: Write `testdata/spec/synthetic.xml`** — mirrors the schema above: `<cfg version="4">` with a `<values>` block of 5 entries (a string at 4/3 without export, floats "Temperature PID1" 7/6 and "Pressure" 36/5 with units, an int "Heat PID" 18/3, one entry with element text `Out=%%`), a Latin-1 0xB0 byte inside an XML comment, CRLF line endings, no encoding declaration.

- [ ] **Step 2: Write failing tests** — `TestParseSpecSynthetic` (5 values; spot-check Index/Name/At/Len/Type/Unit of entries 0, 1, 3), `TestParseSpecRejectsGarbage` (non-XML input → error), `TestLoadSpecFileRealXML` — skips unless `MEDEWI_XML_PATH` is set, then asserts ≥ 40 values and that one of them is named `Temperature PID1`.

- [ ] **Step 3: Run** `go test ./leva/ -run TestParseSpec -v` — expected: FAIL.

- [ ] **Step 4: Implement**; `go get golang.org/x/text`.

- [ ] **Step 5: Run** `go test ./leva/ -v` — expected: PASS (real-XML test passes locally thanks to `.envrc`, skips in bare CI).

- [ ] **Step 6: Commit** — `feat(leva): status-monitor XML spec parser`.

### Task 4: Record decoding and checksum

**Files:**
- Create: `leva/record.go`, `leva/record_test.go`

**Interfaces:**
- Consumes: `RecordSpec`, `ValueSpec` from Task 3.
- Produces:
  - `type Field struct { Spec ValueSpec; Raw string }` (Raw is the space-trimmed slice)
  - `type Record struct { Fields []Field }` with `func (r Record) Float(name string) (float64, bool)` and `func (r Record) Field(name string) (Field, bool)` (lookup by `ValueSpec.Name`)
  - `func (s *RecordSpec) Decode(line []byte) (Record, error)` — slices `line[v.At : v.At+v.Len]` per value; offsets are relative to the raw report line as received (leading `{` included — `// ASSUMPTION:` verified in the capture session); line shorter than a value's window → error naming the value.
  - `func SplitChecksum(line []byte) (payload []byte, sum byte, present bool)` and `func VerifyChecksum(line []byte) (payload []byte, err error)` — trailing `~HH` (two uppercase hex digits) holds the one's complement of the 8-bit sum of the payload bytes (`// ASSUMPTION:` on the complement variant, verified in the capture session); absent checksum is valid.

- [ ] **Step 1: Write failing tests** — `TestDecode` (hand-built 48-byte line against the synthetic spec: Float/Field lookups return the planted values), `TestDecodeShortLine` (error names the truncated value), `TestDecodeBinaryBytes` (planted `\x00` and `\xDF` inside a string field survive into `Raw` — Review Focus 1), `TestChecksumAbsent` (payload passes through), `TestChecksumValid`, `TestChecksumInvalid` (error).

- [ ] **Step 2: Run** `go test ./leva/ -run 'TestDecode|TestChecksum' -v` — expected: FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run same** — expected: PASS.

- [ ] **Step 5: Commit** — `feat(leva): report decoding and optional checksum`.

### Task 5: Connection (dial guard, demux, reports, tap)

**Files:**
- Create: `leva/conn.go`, `leva/conn_test.go`

**Interfaces:**
- Produces:
  - `var ErrBootloaderPort = errors.New("refusing bootloader port 2323")`
  - `func Dial(ctx context.Context, addr string, opts ...Option) (*Conn, error)` — resolves the port *before* connecting; port `"2323"` → `ErrBootloaderPort`.
  - `func NewConn(c net.Conn, opts ...Option) *Conn` (used by tests and sim-backed tools)
  - `type Option func(*config)`; `func WithIdleGap(d time.Duration) Option` (command-response collection gap, default 300 ms)
  - `func (c *Conn) ID() string` — last `$id:` banner line seen.
  - `func (c *Conn) Command(ctx context.Context, cmd string) ([]string, error)` — writes `cmd + "\r\n"`, returns the non-report, non-banner lines that arrive until the idle gap elapses.
  - `func (c *Conn) StartReports(ctx context.Context) (<-chan []byte, error)` — sends `MCr`, returns a buffered channel of raw report lines (lines starting `{`); `func (c *Conn) StopReports(ctx context.Context) error` — sends `MC ` (MC + space).
  - `func (c *Conn) Tap(fn func(dir string, p []byte)) (remove func())` — observes all raw traffic; `dir` is `"tx"` or `"rx"` (callers map this to `fixture.Dir` — leva must not import fixture).
  - `func (c *Conn) Close() error`; `func (c *Conn) Err() error` — non-nil after the reader terminates abnormally; the reports channel is closed then.
- Line classification (one reader goroutine): starts with `{` → report; starts with `$id:` → banner; otherwise → command output. Reader must tolerate CR+LF split across TCP reads and NUL bytes inside lines.

- [ ] **Step 1: Write failing tests** against an in-test fake server over `net.Pipe` — `TestDialRefuses2323` (both `"host:2323"` and an `MEDEWI_MACHINE_ADDR`-style value; no connection attempt — Review Focus 5), `TestCommandCollectsResponse`, `TestDemuxInterleaved` (server interleaves report lines inside a command response; `Command` returns only command lines, channel gets only reports — Review Focus 2), `TestReportsBinarySafe` (report line with `\x00`/`\xDF` arrives byte-identical — Review Focus 1), `TestSplitCRLFAcrossReads`, `TestPeerDisconnectClosesReports` (channel closes, `Err()` non-nil, reader goroutine exits — use `goleak`? no: assert via `runtime.NumGoroutine` delta or a done-channel on the reader — Review Focus 3), `TestTapSeesBothDirections` (callback receives tx bytes of a command and rx bytes of its response with correct dir values).

- [ ] **Step 2: Run** `go test ./leva/ -run 'TestDial|TestCommand|TestDemux|TestReports|TestSplit|TestPeer|TestTap' -v` — FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** `go test -race ./leva/ -v` — PASS; also `go vet ./...`.

- [ ] **Step 5: Commit** — `feat(leva): connection with demux, report stream, dial guard`.

### Task 6: Simulator core (sim package)

**Files:**
- Create: `sim/sim.go`, `sim/sim_test.go`

**Interfaces:**
- Consumes: `fixture.Event` (Task 2).
- Produces:
  - `type Config struct { Addr, TripwireAddr string; MaxClients int; IdleTimeout time.Duration; Speed float64; Banner string; Responses map[string][]string; Stream []fixture.Event }` — zero values mean: `127.0.0.1:0`, no tripwire, 4, 5 min, 1.0, `"$id: leva!"`, empty, nil.
  - `func New(cfg Config) *Server`; `func (s *Server) Start() error`; `func (s *Server) Addr() net.Addr`; `func (s *Server) TripwireAddr() net.Addr`; `func (s *Server) TripwireHits() int`; `func (s *Server) Stop() error`
  - Behavior: on accept send banner line; per received line look up `Responses[line]` and send those lines (CRLF) to **all** clients (UART fan-out); unknown command → send nothing; client over `MaxClients` → close immediately; per-connection idle timer → close on expiry; any connection on the tripwire listener increments `TripwireHits` and logs to stderr.

- [ ] **Step 1: Write failing tests** — `TestBannerOnConnect`, `TestCannedResponseFansOutToAllClients` (two clients, one sends `MCh`, both receive), `TestFifthClientRejected`, `TestIdleTimeoutCloses` (IdleTimeout 50 ms), `TestTripwireCounts`.

- [ ] **Step 2: Run** `go test ./sim/ -v` — FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** `go test ./sim/ -v` — PASS.

- [ ] **Step 5: Commit** — `feat(sim): fake leva! server with fan-out, limits, tripwire`.

### Task 7: Simulator report replay

**Files:**
- Modify: `sim/sim.go`
- Create: `sim/replay_test.go`

**Interfaces:**
- Produces (behavior on the same `Server`): `MCr` sets the **global** reporting flag and starts replaying `Config.Stream` rx events as lines to all clients, paced by the events' `T` deltas divided by `Speed`; replay loops when exhausted; `MC ` (space) clears the flag and stops replay for everyone; `MCR` while reporting resends the last line. Reporting state survives individual client disconnects.

- [ ] **Step 1: Write failing tests** (Speed ≥ 1000 for fast tests) — `TestMCrStartsReplayForAllClients`, `TestMCSpaceStopsGlobally` (client A stops what client B started — the global-toggle trap from the spec), `TestMCRResendsLast`, `TestReplayLoops`.

- [ ] **Step 2: Run** `go test ./sim/ -v` — FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** `go test -race ./sim/ -v` — PASS.

- [ ] **Step 5: Commit** — `feat(sim): MCr stream replay with global reporting state`.

### Task 8: Conformance suite

**Files:**
- Create: `conformance/conformance.go`, `conformance/conformance_test.go`

**Interfaces:**
- Consumes: `leva.Dial`/`Conn`/`RecordSpec` (Tasks 3–5), `sim` (Tasks 6–7), `fixture` (Task 2).
- Produces:
  - `type Class int` with `const (ReadOnly Class = iota; StreamState; Interactive)`
  - `type Env struct { Spec *leva.RecordSpec; Confirm func(prompt string) bool; Record func(fixture.Event) }` — `Record` nil means not recording; levacap wires it to a log collected via `fixture.WriteLog`.
  - `type Check struct { Name string; Class Class; Run func(ctx context.Context, c *leva.Conn, env Env) error }`
  - `func All() []Check` — v0 checks: `banner` (ID() non-empty, contains `leva!`), `help` (`MCh` output mentions `MCu` and `MCr`), `xml-name` (`MC?` output has a line with prefix `XML=`), `reports-decode` (StreamState: StartReports, first 3 report lines decode via `env.Spec` when non-nil, StopReports).
  - `func Run(ctx context.Context, addr string, max Class, env Env) ([]Result, error)`; `type Result struct { Name string; Class Class; Err error; Skipped bool }` — checks above `max` are Skipped; any connection error aborts.

- [ ] **Step 1: Write failing tests** — `TestAllChecksPassAgainstSim` (sim configured with banner `$id: leva! 3.2`, canned `MCh`/`MC?` responses, a 4-event synthetic stream whose lines decode against the synthetic spec; `Run(..., StreamState, ...)` → all pass, none skipped), `TestMaxClassSkips` (max `ReadOnly` → `reports-decode` skipped), `TestMachineConformance` — skips unless `MEDEWI_MACHINE_ADDR` set **and** `MEDEWI_MACHINE_CONFIRM=1`; runs `ReadOnly` only.

- [ ] **Step 2: Run** `go test ./conformance/ -v` — FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** `go test ./conformance/ -v` — PASS (machine test skips).

- [ ] **Step 5: Commit** — `feat(conformance): protocol checks runnable against sim or machine`.

### Task 9: levacap CLI

**Files:**
- Create: `cmd/levacap/main.go`, `cmd/levacap/main_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: binary `levacap` with two subcommands:
  - `levacap conform [-max readonly|stream] [-yes]` — target from `MEDEWI_MACHINE_ADDR` (flag `-addr` overrides); loads spec from `MEDEWI_XML_PATH` if set; unless `-yes`, prompts `machine idle, nobody brewing? [y/N]` via `Env.Confirm`; prints one line per Result, exits 1 on any failure.
  - `levacap capture -scenario <name> [-out captures/]` — connects, replays the scenario's scripted commands (scenario `commands` = the read-only whitelist; scenario `stream`/`shot` = StartReports until Ctrl-C/Enter with interactive prompts), tees every tx/rx byte through `Conn.Tap` into `<out>/<scenario>/log.jsonl` + `manifest.json`.
  - Testable core: `func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int` in package main.

- [ ] **Step 1: Write failing tests** — `TestConformAgainstSim` (start sim in-process, `run(["conform","-addr",simAddr,"-yes","-max","stream"], …)` returns 0 and prints `reports-decode`), `TestConformRefuses2323` (`-addr "localhost:2323"` → exit 1, message contains "2323", sim tripwire on that port records **zero** hits — Review Focus 5), `TestCaptureWritesLogAndManifest` (scenario `commands` against sim → both files exist, log round-trips via `fixture.ReadLog`).

- [ ] **Step 2: Run** `go test ./cmd/levacap/ -v` — FAIL.

- [ ] **Step 3: Implement** (std `flag` with subcommand dispatch).

- [ ] **Step 4: Run** `go test ./cmd/levacap/ -v` — PASS.

- [ ] **Step 5: Commit** — `feat(levacap): conform and capture subcommands`.

### Task 10: levasim CLI

**Files:**
- Create: `cmd/levasim/main.go`, `cmd/levasim/main_test.go`

**Interfaces:**
- Produces: binary `levasim`, flags: `-addr 127.0.0.1:2300`, `-tripwire 127.0.0.1:2323`, `-speed 1.0`, `-fixtures <dir>` (loads `log.jsonl` as the replay stream and optional `responses.json` as `map[string][]string`; without the flag, serves built-in synthetic defaults identical to Task 8's test config). Same testable `run(...)` shape as levacap.

- [ ] **Step 1: Write failing test** — `TestServesOnFlagAddr` (run on `:0`, parse the printed listen address, dial it, read banner, send `MCh`, get the synthetic response).

- [ ] **Step 2: Run** `go test ./cmd/levasim/ -v` — FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** `go test ./cmd/levasim/ -v` — PASS.

- [ ] **Step 5: Commit** — `feat(levasim): simulator binary`.

### Task 11: Runbook, synthetic dump fixtures, flake packages, polish

**Files:**
- Create: `docs/runbooks/capture-session.md`, `testdata/fixtures/synthetic/` (committed synthetic scenario: `responses.json` incl. shaped-but-fake `MCu`/`MCm` dumps, a small `log.jsonl` stream), `.github/workflows/ci.yml`
- Modify: `flake.nix` (packages), `README.md`

**Interfaces:**
- Consumes: the design doc's "Conformance suite & capture session" section — the runbook is its executable prose.

- [ ] **Step 1: Write `docs/runbooks/capture-session.md`** — prerequisites (machine on and idle, Status Monitor closed, daemonless, `direnv allow` done); the command whitelist with the one-line purpose of each; the scenario list in capture order (commands → idle stream 60 s → warmup → two shots → optional second-client fan-out check); the assumptions to verify (report-line offset origin, checksum variant, keepalive candidate, 5th-client behavior, idle-timeout duration); the sanitization gate (real dumps → private storage (gitignored `private/`), review every fixture before `git add`); the firmware-flash gate procedure (stop daemon → flash → `levacap conform` → update `MEDEWI_XML_PATH` → restart).

- [ ] **Step 2: Write the synthetic fixtures** — `MCu` response shaped like an indented settings listing with fake values; `MCm` shaped like Intel-HEX records with valid per-record checksums over fake bytes; stream log whose lines decode against `testdata/spec/synthetic.xml`.

- [ ] **Step 3: Verify fixtures end-to-end** — Run: `go run ./cmd/levasim -fixtures testdata/fixtures/synthetic -addr 127.0.0.1:0` + `levacap conform` against it (or the Task 9 test updated to load this directory). Expected: all checks pass.

- [ ] **Step 4: Add `packages.levacap` / `packages.levasim`** to `flake.nix` via `buildGoModule` (fill `vendorHash` from the build error), update `README.md` (what medewi is, status: phase 1, links to docs/).

- [ ] **Step 5: Write `.github/workflows/ci.yml`** (exact content; verify the two actions' current major versions and adjust the `@vN` pins if newer majors exist):

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
  schedule:
    - cron: "17 4 * * 1"

jobs:
  checks:
    if: github.event_name != 'schedule'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: DeterminateSystems/nix-installer-action@v16
      - run: nix build .#levacap .#levasim
      - run: nix develop --command bash -c 'golangci-lint run && golangci-lint fmt --diff && go vet ./... && go test -race ./... && go mod tidy -diff'
  vulncheck:
    if: github.event_name == 'schedule'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: DeterminateSystems/nix-installer-action@v16
      - run: nix develop --command govulncheck ./...
```

- [ ] **Step 6: Full gate locally** — Run: `nix build .#levacap .#levasim && nix develop --command bash -c 'golangci-lint run && golangci-lint fmt --diff && go vet ./... && go test -race ./... && go mod tidy -diff'` — all green (identical to the CI line).

- [ ] **Step 7: Commit and push** — `feat: synthetic fixtures, capture runbook, flake packages, CI` — then verify the Actions run is green on GitHub.
