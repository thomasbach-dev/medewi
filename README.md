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

The API requires mutual TLS: set the server certificate, key and client CA.
`-insecure-http` exists for local testing only and exits with an error when
combined with TLS settings.

NixOS module (`nixosModules.medewid`): options
`services.medewid.{enable,package,environmentFile,xmlFile,tlsCert,tlsKey,tlsClientCA,listen,healthAddr}`.
The service runs with `DynamicUser`, so it cannot open root-owned files
itself. `xmlFile`, `tlsCert`, `tlsKey` and `tlsClientCA` are therefore
delivered via systemd credentials (`LoadCredential`, read as root before
privileges drop): the files may be root-owned `0600` and live anywhere,
including `/home` or `/run/secrets`. The XML keeps its base name as
credential name, because the spec name must match the machine's `XML=`
reply. The `environmentFile` must contain `MEDEWID_MACHINE_ADDR` (and
optionally `MEDEWID_EVENT_EXEC`); a `MEDEWID_DB` override must stay under
`/var/lib/medewid`. The module never passes `-insecure-http`.

`medewictl` is the client: `MEDEWID_URL` plus `MEDEWID_CTL_CERT`,
`MEDEWID_CTL_KEY` and `MEDEWID_CTL_CA` for the client certificate and the
server CA.

```sh
medewictl status
medewictl power eco            # active | eco | standby
medewictl backup -out DIR
```

`backup` reads the controller settings and writes two files into `DIR`:
`<ts>_<fw>.mcu.txt` and `<ts>_<fw>.mcm.hex`, where `<ts>` looks like
`2026-11-03T041500Z` and `<fw>` is the firmware version. The files contain
the license key: they are written `0600` and must never be committed. If the
`mcm` dump fails, the `mcu` file already written stays in `DIR`.

## Build and test

```sh
nix develop                   # Go toolchain, linters
go test ./...                 # unit + conformance against the simulator
nix build .#levasim           # also .#levacap, .#medewid, .#medewictl
go run ./cmd/levasim -fixtures testdata/fixtures/synthetic
```
