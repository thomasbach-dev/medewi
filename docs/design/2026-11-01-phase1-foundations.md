# Medewi Phase 1: Repo, Capture, Protocol Library, Simulator — Design

Date: 2026-11-01. Status: approved.


## Intent

Medewi (per-coffee recipe system with guided brewing and automatic shot
logging) builds on the leva! 3.2 firmware in a Rancilio Silvia, reachable as
host `rancilio`, TCP port 23 (text command interface). Everything later — the
daemon on a home server, the Prometheus exporter, the shot database, the web UI —
depends on trustworthy machine communication. Phase 1 creates the ground
truth and the tooling to develop without a hot espresso machine:

1. a bootstrapped Go mono-repo,
2. a capture tool plus a one-time **capture session** against the real
   machine, producing protocol fixtures (and, as a by-product, the
   long-missing settings backup),
3. a **protocol library** (v0) that speaks leva! over TCP,
4. a **simulator** that replays the fixtures so development and tests never
   need the real machine.

## Machine facts the builder must know

Vendor references live in `private/doc-summaries/` (firmware
manual, ito module manual, Virtual LCD protocol — all with page refs). Key
facts:

- Port 23: plain text, CR+LF lines. On connect the firmware greets with
  `$id: leva!`. Commands are the `MC*` family (`MCh` lists them). Up to 4
  simultaneous TCP clients share one ATMega UART: every client's output is
  fanned out to all clients, commands interleave. 5-minute inactivity
  timeout per connection.
- `MCr` starts a continuous telemetry stream (lines like `{ - … }`,
  ~fixed-width columns); `MCR` resends; `MC<space>` resets reporting state —
  note this state is **global**, shared by all clients. The column layout is
  described by the firmware release's Status-Monitor XML
  (`leva-3.2/leva! (for ito)/xml file for status monitor/leva!-65FCB201.xml`,
  `<value at/len/type>` entries) — parse the XML, do not hardcode offsets.
  Lines may carry an optional `~HH` checksum (complement of 8-bit char sum).
- Binary safety is mandatory: the Virtual-LCD input protocol sends raw bytes
  0x00–0x07 as key events. The library must never corrupt or accidentally
  emit them; readers must tolerate NUL bytes.
- **Port 2323 is hazardous**: the first byte sent to it resets the ATMega
  into the bootloader. Nothing in Medewi may ever connect to 2323 (one
  deliberate exception in the simulator, below).

## Goals

- `git init`-ed local mono-repo (publish to Codeberg later), Go, Nix flake
  (devShell + package), `go test ./...` green.
- Conformance suite + capture tooling (`cmd/levacap`) and a written
  runbook; the first real-machine run is executed together with Thomas
  (machine on; two real shots are part of it) and doubles as the
  fixture-recording session.
- Fixture set committed to the repo (sanitized), raw unsanitized originals
  stored privately in `private/` (not in the repo).
- Protocol library package `leva/`: dial/maintain a connection, send
  commands, demultiplex command responses from stream lines, parse the
  Status-Monitor XML into a column spec, decode `MCr` records into typed Go
  values, expose a raw-frame tap for logging/capture.
- Simulator (`cmd/levasim`): a fake leva! good enough that the daemon (phase
  2) can be developed against it exclusively.

## Non-goals (later phases)

Daemon, Prometheus metrics, shot database, recipe engine, HTTP API, web UI,
Virtual-LCD menu driving, any command that changes machine state (`MCc`,
`MC+`/`MC-`, PID constant writes). The library v0 is read/observe only; the
write surface is phase 2+.

## Anti-goals

- Touching port 2323 from any tool, test, or future health check.
- Sending anything to the real machine beyond the read-only whitelist below.
- Fixtures in the repo containing the license key or anything private.

## Conformance suite & capture session

The protocol model is executable. A conformance suite asserts every
assumption the library and simulator encode: banner, command set (`MCh`),
XML name (`MC?`), response framing, stream record layout against the XML
spec, checksum behavior, timeout handling. The same assertions run against
three targets:

1. **levasim** — always, in CI. This keeps the simulator honest: simulator
   and machine must pass identical assertions.
2. **Real machine, read-only subset** — on demand, notably **after every
   firmware flash**: the suite reports what changed (commands, XML
   name/layout, timings) before the daemon trips over it.
3. **Real machine, interactive** — the capture session below is simply the
   first run of this mode: the suite guides the human steps ("pull a shot
   now, Enter when done") and records fixtures as it verifies. Re-runnable
   per firmware version; fixtures tagged with firmware version + XML hash.

Safety classes are explicit in code: read-only checks (free), stream-state
checks (restore prior state), write checks (do not exist in phase 1; later
behind an explicit flag that requires an idle machine). Target selection is
builder's choice (e.g. env var; default spins up levasim in-process).

Access discipline (decided): **at most one Medewi process talks to the
machine at a time.** Even read-only traffic can interfere — stream toggles
are global state, and large dumps compete for the single shared UART during
a shot — so direct conformance runs require an idle machine (the suite
checks observable state where possible and prompts for human confirmation
otherwise) and no running daemon. From phase 2 on, the daemon owns the
connection: routine re-checks go through a daemon endpoint that serializes
them around brews, and the firmware-flash gate uses the natural sequence
stop daemon → flash → run suite directly → update the spec/config → start
daemon (whose startup handshake re-verifies).

Phase-2 note: the daemon will run the permanent form of the flash gate at
startup — `MC?` must match the XML spec it loaded, else refuse/warn.

### Capture session (first conformance run; runbook is a deliverable)

Command whitelist, all read-only: `MCh` (help), `MC?` (XML name), `MCu`
(setup dump — **this is the settings backup**; store privately, too), `MCm`
(EEPROM hex dump — second backup form), `MCb`, `MCf` (pressure/flow test
data), `MCr`/`MCR`/`MC<space>` (stream control; restore prior state at the
end). Optional: `MC@` display dump (then send `MC` + byte 0x00? — NO: input
injection is out of scope; capture display frames passively only if `MC@`
alone triggers them; otherwise skip).

Scenarios to record (each a separate fixture file):
1. connect → banner, idle, clean disconnect;
2. each whitelist command once, quiescent machine;
3. `MCr` stream: idle machine, ~60 s;
4. `MCr` stream: warmup after power-on, a few minutes;
5. `MCr` stream: 1–2 real shots (coordinated — Thomas brews actual coffee);
6. optional: eco/standby entry if convenient; a second concurrent client
   observing fan-out (laptop + second terminal).

Dump policy (decided): the real `MCu` and `MCm` dumps are **never committed**
to the repo — text-level redaction provably fails on Intel-HEX (`MCm`
encodes the license key as hex bytes split across records, and
length-preserving redaction breaks record checksums), and git history is
forever once published. The real dumps are stored privately in
`private/` as the settings backups. The repo's fixtures for
dump commands are synthetic (hand-written, realistic shape, fake values),
and the simulator serves those. All other captured fixtures get a
Thomas-review gate before the commit that adds them.

## Decided design (changeable on pushback, but these are the defaults)

- **Repo**: `github.com/thomasbach-dev/medewi`, checked out at
  `the local checkout` (docs for use-cases, decisions, and
  architecture already live in `docs/`). Layout: `cmd/levacap/`, `cmd/levasim/`, `leva/`
  (the protocol package), `testdata/fixtures/`, `docs/design/`,
  `docs/runbooks/`, `flake.nix`. Commit style: conventional, English.
- **Module path**: `github.com/thomasbach-dev/medewi`.
- **Configuration**: instance settings come from environment variables, set
  up by a committed direnv `.envrc` that also contains `use flake`:
  `MEDEWI_MACHINE_ADDR=machine.example:23` (the machine), and
  `MEDEWI_XML_PATH` pointing at the local copy of the firmware's
  Status-Monitor XML (vendor content, outside the repo). All tooling comes
  from `flake.nix` (devShell).
- **Fixture format**: JSON Lines, one event per line:
  `{"t": <ms since session start, monotonic>, "dir": "tx"|"rx", "data": "<base64>"}`
  plus a `manifest.json` per scenario (date, firmware version, XML file
  name/hash, host, notes). Base64 preserves exact bytes (CRLF, NUL, 0xDF
  glyphs); timestamps let the simulator replay with real pacing.
- **levacap**: connects to a host/port (port 23 default; **2323 refused in
  code, not just docs**), runs a scripted command list with configurable
  waits, tees everything to a fixture file, supports a "record until Ctrl-C"
  stream mode for shots. Only sends printable ASCII + CRLF.
- **levasim**: listens on a configurable port (default 2300, unprivileged —
  nothing in phase 1 binds :23; that becomes a deployment concern in later
  phases); serves banner + canned responses from the
  fixture library; `MCr` replays a selected stream scenario at recorded
  timing (speed factor flag); output fans out to all connected clients;
  global reporting toggle shared across clients; max 4 clients; 5-minute
  idle timeout. Additionally listens on a "trap" port (default 2323 analog)
  that logs loudly and fails tests if anything connects — a tripwire for the
  anti-goal. Unknown real-world edge behaviors (5th client, half-open lines)
  are builder's choice, marked with `// ASSUMPTION:` comments and listed in
  the runbook as "verify during capture session".
- **Library API sketch** (builder refines): `leva.Dial(host) (*Conn, error)`;
  `Conn.Command(ctx, cmd string) (Response, error)` with response/stream
  demultiplexing; `Conn.Stream(ctx) (<-chan Record, error)` starting/joining
  `MCr`; `spec.ParseXML(r io.Reader) (*RecordSpec, error)`;
  `RecordSpec.Decode(line []byte) (Record, error)`; `Conn.Tap(w io.Writer)`.
  Keepalive strategy: to be determined empirically during the capture
  session (candidate: periodic CRLF or `MC?`); until verified, reconnect-on-
  timeout is the required behavior.
- **Testing**: the conformance suite is the backbone (identical assertions
  against levasim in CI and the real machine on demand); plus golden-fixture
  tests for the XML spec parser and record decoder. levasim in-process or
  subprocess — builder's choice.
- **Vendor content (decided)**: the vendor's Status-Monitor XML and other
  vendor-authored artifacts (help text, manuals) are never committed — the
  firmware is closed-source and the material copyrighted. This is also the
  right architecture regardless of licensing: every leva! installation has
  its own release XML, so the library loads the XML from a configured path
  at runtime, and tests run on synthetic XML fixtures. Manifests record
  only the XML's file name and hash. Captured `MCh` output may be quoted in
  private notes but not committed; the levasim help response is a synthetic
  equivalent listing only command names.
- **Nix**: flake with `devShell` (go, gopls, golangci-lint, gofumpt,
  govulncheck) and `buildGoModule` package for the two commands; NixOS
  module deferred to the daemon phase. All tooling is version-pinned
  through the flake.
- **Coding standards**: gofumpt formatting; goimports grouping with local
  prefix `github.com/thomasbach-dev/medewi`; golangci-lint v2 as the single
  lint harness with a curated set (standard defaults — errcheck, govet,
  ineffassign, staticcheck, unused — plus revive, misspell, gocritic,
  unparam); tests run with the race detector; `go mod tidy -diff` must be
  clean; govulncheck runs weekly, not per commit.
- **CI**: one GitHub Actions workflow runs the full gate (build via nix,
  lint, format check, race tests, tidy check) on push/PR, plus a scheduled
  weekly govulncheck job.
- **Vocabulary**: real names never appear in the repo; personas are
  "barista" and "regular" (confirmed; used nowhere in phase 1 anyway).

## Left to the builder

Internal package structure, parser implementation details, error taxonomy,
logging choice (std `log/slog`), flag handling, exact test layout, fixture
directory naming.

## Open questions (require Thomas)

1. **First interactive run scheduling**: it needs the machine on, two real
   shots, and the Status Monitor closed — when? (Follow-up read-only runs
   are covered by the access discipline above: idle machine, no daemon,
   confirmation prompt.)
