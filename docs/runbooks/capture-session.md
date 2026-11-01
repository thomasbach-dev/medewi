# Runbook: capture session and conformance runs

Executable prose of the design's "Conformance suite & capture session"
section. Use it for the first contact with the machine, for every later
re-capture, and as the firmware-flash gate.

Tools: `levacap` (conformance and capture), `levasim` (simulator for
dry runs). Build with `nix build .#levacap` or `go run ./cmd/levacap`.

## Safety rules (read first)

1. **Whitelist.** The only commands ever sent are `MCh MC? MCu MCm MCb MCf`
   plus stream control `MCr` (start reports) and `MC ` (MC + space, stop
   reports and reset state). Nothing else. Do not type extra commands by
   hand on port 23 during a capture.
2. **Never touch port 2323.** It is the bootloader; the first byte sent there
   resets the controller into flashing mode. `levacap` and the library refuse
   it in code; do not work around that, and do not probe it with `nc` either.
3. **Real `MCu` and `MCm` dumps are settings backups.** The license key is
   embedded (in `MCm` as hex bytes split across records; Intel-HEX redaction
   is infeasible because it breaks record checksums). Store them
   **privately** in `private/`. **Never commit them.** The
   repo only contains synthetic dump fixtures.
4. **Human review before `git add`.** Every captured fixture (`log.jsonl`,
   `manifest.json`, `responses.json`) is read by a human first. Vendor help
   text (`MCh` output) and the vendor XML are not committed either.
5. **Machine idle, one client.** Nobody brewing, Status Monitor closed, no
   medewi daemon running. Dumps compete with a shot for the single UART and
   stream toggles are global state.

## Prerequisites

- Machine on, warm or cold as the scenario needs, idle.
- Status Monitor closed on every computer.
- `direnv allow` done in the repo, so `MEDEWI_MACHINE_ADDR`
  (`machine.example:23`) and `MEDEWI_XML_PATH` (local copy of the
  firmware's release XML, here `leva!-65FCB201.xml`) are set. Check with
  `echo $MEDEWI_MACHINE_ADDR $MEDEWI_XML_PATH`.
- Output goes to `captures/` (git-ignored). Review there, then copy what
  passes review into `testdata/fixtures/<name>/`.

## What the machine does (observed on first contact)

- Banner `$id: leva! (3.2)`, repeated about once per second.
- **Wait for the banner before sending.** A command sent immediately after
  connecting is silently swallowed. `levacap` waits up to 5 s for the first
  banner and refuses to send otherwise.
- `MC?` answers `XML=leva!-65FCB201`. The name must match the file behind
  `MEDEWI_XML_PATH`; Status Monitor and medewi cannot decode reports with a
  mismatched XML.
- No `~HH` checksums in the default configuration.

## 1. Conformance run (quick, read-only)

```sh
levacap conform -max readonly      # banner, MCh, MC?
levacap conform -max stream        # additionally MCr, 3 reports, MC<space>
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
levacap capture -scenario commands        # 1. each whitelist command once
levacap capture -scenario stream          # 2. idle report stream
levacap capture -scenario stream -out captures/warmup   # 3. warmup
levacap capture -scenario shot            # 4. a real shot
levacap capture -scenario shot -out captures/shot2      # 5. second shot
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
5. **optional: second-client fan-out** — while a `stream` capture runs, open
   `nc rancilio 23` in another terminal, only read, then disconnect. Note in
   `manifest.json` whether both clients saw the same lines. This also covers
   the 4/5-client question below if you are willing to try.

Behavior of the tool you need to know:

- **Refuses to overwrite.** If `log.jsonl` or `manifest.json` already exists
  in the target directory, `levacap` exits 1 with "refusing to overwrite";
  use a different `-out` or `-force` (only when you meant to replace a
  *complete* capture).
- **Interrupted or failed capture** (Ctrl-C, disconnect, timeout) writes
  `log.partial.jsonl` / `manifest.partial.json` instead, with the reason in
  the manifest notes, and exits 1. Partial files never overwrite a complete
  capture. Delete them or keep them for diagnosis; do not feed them to the
  simulator.
- Files are written under temp names and renamed only after both succeeded.
- Port 2323 as `-addr` is refused.

## 3. Sanitization gate

1. Real `MCu` / `MCm` / full `commands` capture: copy to
   `private/` (dated names) and keep it out of the repo.
2. For stream and shot captures, read `manifest.json` and skim `log.jsonl`
   (`jq -r .data < log.jsonl | base64 -d | less`) for anything private
   (license key, addresses, names). The manifest records only the XML's file
   name and hash, never its content.
3. Only then `git add testdata/fixtures/<name>/` and commit. Dump commands
   in committed fixtures stay synthetic (see
   `testdata/fixtures/synthetic/responses.json`).
4. Dry-run any fixture first: `levasim -fixtures <dir>` then
   `MEDEWI_XML_PATH=... levacap conform -addr 127.0.0.1:2300 -max stream`.

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
| 7 | `MC@` alone emits display frames passively. | Not sent by default. Input injection is out of scope; skip unless `MC@` alone is shown to be safe. |

## 5. Firmware-flash gate

After every firmware flash, before anything else talks to the machine:

1. Stop the medewi daemon (from phase 2 on; today: nothing running).
2. Back up the settings first (`MCu` on port 23 into private storage), then
   flash following the vendor procedure (`rancilio.org`, "Update").
   `levacap` never goes near port 2323.
3. `levacap conform -max readonly`. It reports what changed in the banner,
   help and XML name.
4. Compare the `MC?` name with the file behind `MEDEWI_XML_PATH`. On
   mismatch: put the release's new XML next to the old one, point
   `MEDEWI_XML_PATH` at it (update `.envrc`), and
   **re-run `levacap conform -max stream`** so reports are decoded against
   the new layout.
5. When both runs pass, restart the daemon (its startup handshake re-checks
   the XML name).
