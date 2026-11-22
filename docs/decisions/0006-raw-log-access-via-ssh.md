# 0006 — Raw-log access stays with ssh, not the daemon API

Date: 2026-11-22. Status: accepted.

## Context

Reading the raw logs from a laptop was tedious: `sudo cp` into a temp
directory, `chown`, `scp`, clean up (see
[runbooks/capture-session.md](../runbooks/capture-session.md)). The wish:
run any `levalog` subcommand (`events`, `summary`, `runs`, `extract`, …)
against the brewhost's raw logs from elsewhere, e.g. `medewictl`. Options
weighed:

- medewid serves the raw-log files over its mTLS API; `medewictl` fetches
  and caches them and runs `levalog`'s code locally.
- medewid runs the `levalog` subcommand itself and streams the result.
  This turns every `levalog` flag into API surface: a remote-command
  interface on the daemon, and decoding work in the process that watches
  the machine.
- rsync over ssh to a local mirror, then plain `levalog --dir`.

No other API client needs raw logs, and troubleshooting is done from a
full computer that can ssh to the brewhost.

## Decision

The daemon API gets no raw-log endpoint. Raw logs are read on the
brewhost (`sudo levalog …`) or mirrored with rsync over ssh and read
locally with `levalog --dir`. Who may read them is a deployment choice:
which ssh keys are deployed, for which account, and how they are
restricted (e.g. `command="rrsync -ro <raw dir>",restrict`). medewid
stays the one tool that owns the machine, `levalog` the one that reads
logs.

## Consequences

- No change to medewid, `medewictl` or the NixOS module for log access.
  A fixed service user instead of `DynamicUser` can be forced from the
  host config if a deployment wants one.
- A separate read-only account or group cannot read the raw logs as
  things stand: `rawlog` forces the directory to 0700 and its files to
  0600, and the module sets `UMask=0077`. Granting group read needs a
  code change (and a new record).
- A local mirror outlives the brewhost's retention unless it is pruned.
