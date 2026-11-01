# medewi

A per-coffee recipe system for espresso machines driven by the
[leva!](https://www.softwareandcircuits.com/) controller firmware: the
barista picks (or the regular's usual is remembered) a recipe for the coffee at
hand, and medewi drives the machine accordingly.

**Status: phase 1 (foundations).** Protocol library (`leva/`), machine
simulator (`cmd/levasim`), conformance suite (`conformance/`) and capture
tooling (`cmd/levacap`). No daemon and no write path to the machine yet.

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

## Build and test

```sh
nix develop                   # Go toolchain, linters
go test ./...                 # unit + conformance against the simulator
nix build .#levasim           # also .#levacap
go run ./cmd/levasim -fixtures testdata/fixtures/synthetic
```
