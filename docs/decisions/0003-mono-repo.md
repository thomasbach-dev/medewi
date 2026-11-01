# 0003 — Mono-repo

Date: 2026-11-01. Status: accepted.

## Context

The system has several parts (protocol library, simulator, daemon, web UI,
packaging) that will evolve together, built by a very small number of
people at hobby pace.

## Decision

One repository holds everything: `github.com/thomasbach-dev/medewi`.
The protocol library lives as a package inside it; the web UI is embedded
in the daemon binary; Nix packaging sits alongside.

## Consequences

- Atomic changes across protocol/daemon/UI; one issue tracker; one flake.
- Splitting the protocol library out remains possible later if third-party
  users appear; until then, no multi-repo ceremony.
