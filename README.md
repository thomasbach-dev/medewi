# medewi

A per-coffee recipe system for espresso machines driven by the
[leva!](https://www.softwareandcircuits.com/) controller firmware: the
barista picks (or the regular's usual is remembered) a recipe for the coffee at
hand, and medewi drives the machine accordingly.

**Status: phase 2 (daemon).** Protocol library (`leva/`), machine
simulator (`cmd/levasim`), conformance suite (`conformance/`), capture
tooling (`cmd/levacap`), and the always-on daemon `medewid` with its client
`medewictl`. The only writes to the machine are the power toggles.

## Documentation

- [Use cases](docs/use-cases.md)
- [Architecture](docs/architecture.md)
- [Decision records](docs/decisions/)
- [Phase 1 design](docs/design/2026-11-01-phase1-foundations.md)
- [Capture-session runbook](docs/runbooks/capture-session.md) (first contact,
  conformance runs, firmware-flash gate)

## Configuration

Instance settings come from two environment variables; neither is checked in:

- `MEDEWI_MACHINE_ADDR` — `host:port` of the machine's leva! text interface
  (port 23; port 2323 is refused everywhere).
- `MEDEWI_XML_PATH` — path to your local copy of the firmware release's
  Status-Monitor XML (vendor content, never committed).

With [direnv](https://direnv.net/), create a local `.envrc` (gitignored):

```sh
use flake
export MEDEWI_MACHINE_ADDR=machine.example:23
export MEDEWI_XML_PATH=/path/to/leva-release.xml
```

Real captures and settings backups live in the gitignored `private/`
directory (see the runbook); nothing under it is ever committed.

## Daemon

`medewid` owns the machine connection, records every brew in SQLite, serves
the HTTP API, ops panel and Prometheus metrics. It is configured by flags or
environment variables (flags win):

| Variable | Flag | Default |
| --- | --- | --- |
| `MEDEWID_MACHINE_ADDR` (fallback `MEDEWI_MACHINE_ADDR`) | `-machine` | required |
| `MEDEWID_XML_PATH` (fallback `MEDEWI_XML_PATH`) | `-xml` | required |
| `MEDEWID_DB` | `-db` | `/var/lib/medewid/medewi.db` |
| `MEDEWID_LISTEN` | `-listen` | `:8443` |
| `MEDEWID_TLS_CERT`, `MEDEWID_TLS_KEY`, `MEDEWID_TLS_CLIENT_CA` | `-tls-cert`, `-tls-key`, `-tls-client-ca` | none |
| `MEDEWID_HEALTH_ADDR` | `-health-addr` | `127.0.0.1:9114` (plaintext `/healthz`) |
| `MEDEWID_EVENT_EXEC` | `-event-exec` | none |
| `MEDEWID_RAW_DIR` | `-raw-dir` | `/var/lib/medewid/raw` |
| `MEDEWID_RAW_RETENTION` | `-raw-retention` | `168h`; `0` disables the raw log |
| `MEDEWID_RAW_MAX_MB` | `-raw-max-mb` | `256` |
| `MEDEWID_DISPLAY_EVERY` | `-display-every` | `5s`; `0` disables display sampling |
| `MEDEWID_DISPLAY_FOR` | `-display-for` | `500ms`; at most `2s` |

The API requires mutual TLS: set the server certificate, key and client CA.
`-insecure-http` exists for local testing only and exits with an error when
combined with TLS settings.

**Raw traffic log.** medewid keeps what it exchanges with the machine in
`-raw-dir` for `-raw-retention` (7 days; capped at `-raw-max-mb`), so a
failure noticed days later can be troubleshot from the exact bytes. One
gzip file per session (rotated hourly) of JSON lines in the fixture event
format. Every chunk sent is kept; of the received lines only report lines
(`{`), display frames (`<`), banners (`$id:`), short machine notices such as
`[READY]`, and replies to commands medewid itself sent shortly before.
`MCu`/`MCm` replies (which contain the license key) are never logged. Marks
(below) land in the log as `note` events. If an explicitly configured
directory cannot be opened, medewid exits 1; with the default directory it
only warns and runs without the log.
Counters: `medewi_rawlog_dropped_total`, `medewi_rawlog_write_errors_total`,
`medewi_rawlog_redacted_lines_total`. Approximate replay through `levasim`
(it replays every received line, banners and replies included, as the
report stream):

```sh
gzip -dc FILE 2>/dev/null | grep '}$' > DIR/log.jsonl; levasim -fixtures DIR
```

The `grep` keeps only complete lines, so a file still being written or left
by a crash replays too.

**Display, machine READY and marks.** Every `-display-every` medewid
switches the machine to its LCD mirror for `-display-for`, then back to
reports, so the raw log also shows what the display shows (programs, menus,
keys) at the cost of about 10 % of the report lines; other clients see the
same switch. `/api/status` carries the latest LCD rows (`display`) and
`machine_ready`, the machine's own `[READY]` announcement (cleared by eco,
standby or a disconnect); `ready` is medewid's earlier "boiler at
temperature". `[READY]` runs the event hook with `MEDEWI_EVENT=MACHINE_READY`
and drives `medewi_machine_ready`. `POST /api/mark` (`{"text": "…"}`, the
panel's Mark field or `medewictl mark`) writes `mark: …` into the raw log,
to find a moment again later. Design:
[docs/design/2026-11-05-display-sampling.md](docs/design/2026-11-05-display-sampling.md).

NixOS module (`nixosModules.medewid`): options
`services.medewid.{enable,package,environmentFile,xmlFile,tlsCert,tlsKey,tlsClientCA,listen,healthAddr}`.
The service runs with `DynamicUser`, so it cannot open root-owned files
itself. `xmlFile`, `tlsCert`, `tlsKey` and `tlsClientCA` are therefore
delivered via systemd credentials (`LoadCredential`, read as root before
privileges drop): the files may be root-owned `0600` and live anywhere,
including `/home` or `/run/secrets`. The XML keeps its base name as
credential name, because the spec name must match the machine's `XML=`
reply. The `environmentFile` must contain `MEDEWID_MACHINE_ADDR` (and
optionally `MEDEWID_EVENT_EXEC`); a `MEDEWID_DB` or `MEDEWID_RAW_DIR` override must
stay under `/var/lib/medewid`. The unit orders after `time-sync.target`, which
makes an unsynced clock less likely to prune the raw log; a hard guarantee
that the clock is synced needs `systemd-time-wait-sync` (your choice; the
module does not enable it). The module never passes `-insecure-http`.

`medewictl` is the client: `MEDEWID_URL` plus `MEDEWID_CTL_CERT`,
`MEDEWID_CTL_KEY` and `MEDEWID_CTL_CA` for the client certificate and the
server CA. Against a local `medewid -insecure-http`, pass `-insecure` with an
`http://` URL instead; `-insecure` never skips certificate checks over
`https://`, and an `http://` URL without it is refused.

```sh
medewictl status
medewictl power eco            # active | eco | standby
medewictl mark pre-infusion over
medewictl backup -out DIR
medewictl -insecure -url http://127.0.0.1:8080 status
```

`backup` reads the controller settings and writes two files into `DIR`:
`<ts>_<fw>.mcu.txt` and `<ts>_<fw>.mcm.hex`, where `<ts>` looks like
`2026-11-03T041500Z` and `<fw>` is the firmware version. The files contain
the license key: they are written `0600` and must never be committed. If the
`mcm` dump fails, the `mcu` file already written stays in `DIR`.

### Local trial run without TLS

To try the daemon from a checkout against the real machine, without NixOS
or certificates. Stop every other client first (`levacap`, the vendor
Status-Monitor app): only one client may talk to the machine, and
starting or stopping the report stream affects every client.

Terminal 1, the daemon (the `.envrc` above provides `MEDEWI_MACHINE_ADDR` and
`MEDEWI_XML_PATH`):

```sh
MEDEWI_BIN=$HOME/tmp/medewi-bin
MEDEWI_DATA=$HOME/tmp/medewi-data
nix develop --command go build -o "$MEDEWI_BIN/" ./cmd/medewid ./cmd/medewictl
mkdir -p "$MEDEWI_DATA"
"$MEDEWI_BIN/medewid" -insecure-http -listen 127.0.0.1:8080 -db "$MEDEWI_DATA/medewi.db" -raw-dir "$MEDEWI_DATA/raw"
```

Expect `medewid serving … tls=false`, then `connected … firmware=…` and
`reports started`. If `spec_ok` stays false, the XML name does not match
the machine's `XML=` reply (logged as an error). Ctrl-C stops the reports
(`MC `) and exits.

The ops panel is at <http://127.0.0.1:8080/>. Its buttons send real power
commands (`MCa`, `MCe`, `MCE`), verified against the report stream for up
to 10 s. They are refused while a brew is open (including the few seconds
after the pump stops), before the first report arrives, and when the
machine is already in that state.

Terminal 2, the client:

```sh
MEDEWI_BIN=$HOME/tmp/medewi-bin
export MEDEWID_URL=http://127.0.0.1:8080
"$MEDEWI_BIN/medewictl" -insecure status
"$MEDEWI_BIN/medewictl" -insecure power eco        # active | eco | standby
mkdir -p private/backups
"$MEDEWI_BIN/medewictl" -insecure backup -out private/backups
```

Pull a flush and a shot: they appear in `medewictl status` (`last_brew`)
and under `$MEDEWID_URL/api/brews`; counters are at `$MEDEWID_URL/metrics`.

## Build and test

```sh
nix develop                   # Go toolchain, linters
go test ./...                 # unit + conformance against the simulator
nix build .#levasim           # also .#levacap, .#medewid, .#medewictl
go run ./cmd/levasim -fixtures testdata/fixtures/synthetic
```
