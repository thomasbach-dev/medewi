# Runbook: capture session and conformance runs

Executable prose of the design's "Conformance suite & capture session"
section. Use it for the first contact with the machine, for every later
re-capture, and as the firmware-flash gate.

Tools: `go run ./cmd/levacap` (conformance and capture) and
`go run ./cmd/levasim` (simulator for dry runs), from `nix develop`/direnv.
Below, `levacap ...` means `go run ./cmd/levacap ...`.

## Safety rules (read first)

1. **Whitelist.** The only commands ever sent are `MCh MC? MCu MCm MCb MCf`
   plus stream control `MCr` (start reports) and `MC ` (MC + space, stop
   reports and reset state), and `MC@` (display mirror, scenario `display`).
   Nothing else; in particular never the display protocol's button codes
   (`MC` + a raw byte), which press keys on the machine. Do not type extra commands by
   hand on port 23 during a capture.
2. **Never touch port 2323.** It is the bootloader; the first byte sent there
   resets the controller into flashing mode. `levacap` and the library refuse
   it in code; do not work around that, and do not probe it with `nc` either.
3. **Real `MCu` and `MCm` dumps are settings backups.** The license key is
   embedded (in `MCm` as hex bytes split across records; Intel-HEX redaction
   is infeasible because it breaks record checksums). Store them in the
   gitignored `private/` directory. **Never commit them.** The
   repo only contains synthetic dump fixtures.
4. **Human review before `git add`.** Every captured fixture (`log.jsonl`,
   `manifest.json`, `responses.json`) is read by a human first. Vendor help
   text (`MCh` output) and the vendor XML are not committed either.
5. **Machine idle, no other clients.** Nobody brewing, Status Monitor closed, no
   medewi daemon running, no other clients except the deliberate fan-out checks.
   Dumps compete with a shot for the single UART and stream toggles are global
   state.

## Prerequisites

- Machine on, warm or cold as the scenario needs, idle.
- Status Monitor closed on every computer.
- medewid stopped on the host that runs it: `systemctl stop medewid`
  (start it again afterwards with `systemctl start medewid`). Only one
  client may talk to the machine, and stream toggles are global: a capture's
  `MC ` stops medewid's stream too (it then reconnects after its stall
  timeout and restarts the stream with `MCr` mid-capture).
- `direnv allow` done in the repo, so `MEDEWI_MACHINE_ADDR`
  (the machine's `host:port`) and `MEDEWI_XML_PATH` (local copy of the
  firmware's release XML, e.g. `leva!-65FCB201.xml`) are set — see the
  README's Configuration section. Check with
  `echo $MEDEWI_MACHINE_ADDR $MEDEWI_XML_PATH`.
- Output goes to `captures/` (git-ignored). Review there, then copy what
  passes review into `testdata/fixtures/<name>/`.

## What the machine does (observed on first contact)

- Banner `$id: leva! (3.2)`, repeated about once per second.
- **Wait for the banner before sending.** A command sent immediately after
  connecting is silently swallowed. `levacap` waits up to 5 s for a banner,
  then until a second banner arrives or 1.5 s have passed since the first, and
  refuses to send otherwise.
- `MC?` answers `XML=leva!-65FCB201`. The name must match the file behind
  `MEDEWI_XML_PATH`; Status Monitor and medewi cannot decode reports with a
  mismatched XML.
- No `~HH` checksums in the default configuration.

## 1. Conformance run (quick, read-only)

```sh
go run ./cmd/levacap conform -max readonly      # banner, MCh, MC?
go run ./cmd/levacap conform -max stream        # additionally MCr, 3 reports, MC<space>
```

Both prompt "machine idle, nobody brewing? [y/N]" (skip with `-yes` only if
you checked). With `MEDEWI_XML_PATH` set, reports are also decoded against
the spec; without it a notice says decode assertions are skipped. Output is
one line per check: `ok`, `failed: <reason>`, `skipped` or `not-run` (the
banner gate failed). Exit code 0 only if everything passed.

`-max stream` leaves the machine as found: it always sends `MC ` at the
end. (`MC ` also resets current machine state; that is accepted.)

## 2. Capture scenarios, in order

Each scenario is written to `<out>/<scenario>/` (default `captures/<scenario>/`) with
`log.jsonl` (raw JSON Lines, base64 bytes, timestamps) and `manifest.json`
(firmware banner, XML name, XML sha256, address, notes).

```sh
go run ./cmd/levacap capture -scenario commands        # 1. each whitelist command once
go run ./cmd/levacap capture -scenario stream          # 2. idle report stream
go run ./cmd/levacap capture -scenario stream -out captures/warmup   # 3. warmup
go run ./cmd/levacap capture -scenario shot            # 4. a real shot
go run ./cmd/levacap capture -scenario shot -out captures/shot2      # 5. second shot
go run ./cmd/levacap capture -scenario display -out captures/warmup-display  # 6. warm-up to READY
```

1. **commands** — sends `MCh MC? MCu MCm MCb MCf` with a 1 s pause between
   them. Quiescent machine. The `MCu`/`MCm` output goes to private storage
   (rule 3), nothing from this directory is committed without review.
2. **stream, idle ~60 s** — machine idle and warm. `levacap` starts `MCr`,
   prints "press Enter to stop"; wait about 60 s, press Enter. It then sends
   `MC `.
3. **warmup, a few minutes** — switch the machine on from cold and record
   while it heats; Enter when settled. (Use a different `-out` per run,
   because the scenario name decides the subdirectory.)
4. **shots, 1 or 2** — scenario `shot` prints "pull the shot now, press Enter
   when done". Start recording, pull a real shot, press Enter afterwards.
6. **warm-up to READY, with the display (20–30 min)** — scenario `display`
   records the report stream (`MCr`) and samples the LCD mirror (`MC@`, 10
   frames/s): the machine sends only one of the two at a time, so `levacap`
   switches to `MC@` for 500 ms every 10 s and back to `MCr` (tune with
   `-display-every`, `-display-for`). About 1.2 KB/s. Start it while the
   machine is in standby or eco (cold is best), then wake the machine. Type `m` + Enter when you hear the
   three READY beeps (and at anything else worth finding again); the marks
   land in the manifest notes as milliseconds since start. An empty line
   stops: `MC ` ends both streams. Purpose: the exact READY moment from the
   display next to the full report data, to build the daemon's readiness
   heuristic.
7. **optional: second-client fan-out** — while a `stream` capture runs, open
   `nc rancilio 23` in another terminal, only read, then disconnect. Note in
   `manifest.json` whether both clients saw the same lines. This also covers
   the 4/5-client question below if you are willing to try.

Behavior of the tool you need to know:

- **Refuses to overwrite.** If `log.jsonl` or `manifest.json` already exists
  in the target directory, `levacap` exits 1 with "refusing to overwrite";
  use a different `-out` or `-force` (only when you meant to replace a
  *complete* capture).
- **Ctrl-C is a clean stop for `stream`, `shot` and `display`.** Once recording has
  started, Ctrl-C does exactly what Enter does: `levacap` sends `MC `, writes a
  complete `log.jsonl` and `manifest.json`, and exits 0. Ctrl-C during the
  banner wait aborts without writing any files (exit 1). Ctrl-C during the
  `MC?` query, or at any point in `commands`, aborts the run and writes
  partial files (next bullet).
- **Any other failure** (a disconnect, a timeout, a failed `MC `, or an abort
  as above) writes `log.partial.jsonl` / `manifest.partial.json` with the
  reason in the manifest notes, and exits 1. Partial files never overwrite a
  complete capture and are not fixtures; do not feed them to the simulator.
- **Partials versus retries:** a retry that fails overwrites existing
  `.partial` files, so move them aside first (into a dated subdirectory such as
  `captures/commands/failed-<date>/`) if you want to keep them for diagnosis.
- Files are written under temp names and renamed only after both succeeded.
- Port 2323 as `-addr` is refused.

## 3. Sanitization gate

1. Real `MCu` / `MCm` / full `commands` capture: move to the gitignored
   `private/` directory (dated subdirectories; never committed), and
   **delete the real dumps from `captures/`** afterwards.
2. For stream and shot captures, read `manifest.json` and skim `log.jsonl`
   (`jq -r .data < log.jsonl | base64 -d | less`) for anything private
   (license key, addresses, names). The manifest records only the XML's file
   name and hash, never its content.
3. Only then `git add testdata/fixtures/<name>/` and commit. Dump commands
   in committed fixtures stay synthetic (see
   `testdata/fixtures/synthetic/responses.json`).
4. Dry-run any fixture first, in two terminals:
   ```sh
   go run ./cmd/levasim -fixtures testdata/fixtures/synthetic -addr 127.0.0.1:2300
   MEDEWI_XML_PATH=testdata/spec/synthetic.xml go run ./cmd/levacap conform -addr 127.0.0.1:2300 -max stream -yes
   ```
   (For a new fixture directory, substitute it and its matching XML.)

## 4. Assumptions to verify during the first session

Each one is an `// ASSUMPTION:` in the code or a ledgered open question.
Record the answer in the manifest notes and fix code/simulator if wrong.

| # | Assumption | How to check |
|---|------------|--------------|
| 1 | Report-line XML offsets (`at=`) count from the line start **including the leading `{`** (`leva/record.go`). | Decode a captured line with the real XML; fields such as temperature and pressure must look plausible, not shifted by one. |
| 2 | Checksum, if enabled, is `~HH` = one's complement of the 8-bit sum of the payload, uppercase hex. Default config has none. | Only testable with a checksum-enabled configuration; otherwise leave open. |
| 3 | A literal `~` never appears in payload data at position len-3 (it would be misread as a checksum). | Scan all captured report lines for `~` at len-3. |
| 4 | Safe keepalive: periodic CRLF versus periodic `MC?`; and the real idle-timeout length (simulator assumes 5 min, the design's guess). | Hold an idle connection with no traffic and time the disconnect; then try each keepalive candidate over a longer period. Until verified: reconnect on timeout. |
| 5 | 5th client behavior (simulator caps at 4 clients; the real behavior is unknown). | Only if cheap: open five `nc` sessions, note what the fifth sees. Close them afterwards. |
| 6 | Real `MCr` after `MC ` resumes versus restarts the stream. | Start, stop, start again; compare sequence/timestamps in the log. |
| 7 | `MC@` alone emits display frames passively. | Verified 2026-11-04: frames every 100 ms, no key presses; `MC@` replaces the report stream and `MCr` replaces it back. Button codes stay out of scope. |

## 5. Firmware-flash gate

Some firmware upgrades reset ALL settings, hence the backup first. After
every firmware flash, before anything else talks to the machine:

1. Stop the medewi daemon (from phase 2 on; today: nothing running).
2. **Before** flashing, back up the settings and record the pre-flash state
   (nothing from here is committed):
   ```sh
   PREFLASH_DIR="private/preflash-$(date +%F)"
   go run ./cmd/levacap capture -scenario commands -out "$PREFLASH_DIR"
   ```
   Then flash per the vendor procedure (`rancilio.org`, "Update").
   `levacap` never goes near port 2323.
3. Copy the new release's XML into Status Monitor's `config/` directory
   AND update the local copy behind `MEDEWI_XML_PATH` (new file, then
   update `.envrc`).
4. Run `go run ./cmd/levacap conform -max readonly`; compare its output and
   the `MC?` name against the pre-flash capture by hand. A name that does
   not match the XML file means: fix the XML copy first.
5. Re-run `go run ./cmd/levacap conform -max stream` so reports are decoded
   against the new XML layout. If settings were reset, restore them from the
   `MCu` backup (vendor procedure).
6. When both runs pass, restart the daemon (its startup handshake re-checks
   the XML name).

## 6. If something fails

- **`not-run` / "no $id banner within 5s"**: the banner never arrived.
  Likely causes: Status Monitor (or another client) still connected, client
  limit reached, or the machine is in eco/standby. Close other clients, wake
  the machine at the machine itself (do NOT send `MCa` or anything else),
  wait, retry.
- **levacap died mid-stream** (crash or network loss; Ctrl-C stops cleanly, see above): `MCr` is
  GLOBAL machine state, so the report stream keeps running. The one
  sanctioned recovery, and the only hand-typed command exception, on port 23
  only:

  ```sh
  MACHINE_HOST="${MEDEWI_MACHINE_ADDR%%:*}"
  nc "$MACHINE_HOST" 23    # type "MC " (MC, space), press Enter, disconnect
  ```

  Never use port 2323.
- **`failed:` lines from conform**: re-run once. If it fails again, save the
  full output (and any `*.partial*` files) for the issue tracker; do not
  retry in a loop against the machine.
