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

## Build and test

```sh
nix develop                   # Go toolchain, linters
go test ./...                 # unit + conformance against the simulator
nix build .#levasim           # also .#levacap
go run ./cmd/levasim -fixtures testdata/fixtures/synthetic
```
