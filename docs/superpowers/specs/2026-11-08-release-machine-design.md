# Release the machine for a while — design

Issue: #11. Status: ready for planning.

## Why

At most one process may talk to the machine, and the stream toggles
(`MCr`/`MC@`/`MC `) are global machine state. While `medewid` runs it holds
a session with reporting on, so `levacap` captures, machine-gated
conformance runs and firmware flashes cannot get a clean machine: banners
stay silent while reporting, and the daemon's reconnect loop grabs the
machine back (and sends `MC ` when it sees no banner, stopping the other
client's stream). Today the only way out is logging into the brewhost and
stopping the service.

The operator wants to hand the machine over from their laptop with one
command and be confident nothing interferes until the time is up — also
across a medewid restart (deploy, crash), so it is safe for a firmware
flash.

## Words

Naming was decided deliberately; keep these words in code, API, docs and UI.

- **release**: medewid gives up the machine until a given time.
  **reclaim**: it takes the machine back early. Not "pause"/"drop":
  "drop" reads like an accident (a dropped connection).
- A brew whose recording stopped before the brew did is **truncated**
  (not "interrupted", which reads as "someone stopped the shot"). The cause
  is part of the kind string:
  - `truncated-lost` — the connection broke (stall, power cycle, network,
    another client's `MC `)
  - `truncated-released` — the operator released the machine mid-brew
  - `truncated-shutdown` — medewid was stopped cleanly (ctx cancelled)
  - `truncated-crash` — a brew left open at startup (`CloseDangling`)
- A future watchdog stopping a shot on purpose is not a truncation (the
  brew really ended); it would get its own kind later. Out of scope here.

## Goals

1. `medewictl release [--until=+30min]` makes medewid stop reports, close
   its connection and not dial the machine until the time. Default when
   `--until` is omitted: one hour from now. **No upper cap.**
2. The command returns only once the connection is closed, so
   `medewictl release && levacap …` works.
3. `medewictl reclaim` ends a release early; medewid reconnects right away.
4. When the time runs out medewid reconnects on its own (a forgotten
   release ends at the time the operator chose).
5. A release survives a medewid restart: on startup a release still in
   force means no dial until it ends.
6. Visible everywhere: `GET /api/status`, `medewictl status`, the ops
   panel, and a metric.
7. Brew kinds renamed to the `truncated-*` family, with the cause recorded.

## Non-goals

- Absolute times for `--until` (`2026-11-08 11:12:13`, `11:12:13`, …).
  Only relative times now; the API is shaped so absolute ones can be added
  to the client later without an API change.
- Negative relative times (`-5min`): rejected.
- Coordination with `levacap`/conformance (they don't ask medewid; the
  operator runs `release` first). Docs/runbooks mention it.
- A separate "reason" column: the cause lives in the kind string.

## Anti-goals

- Medewid touching the machine while released: no dial, no `MC `, nothing,
  not even on startup or shutdown.
- A release that silently never ends (it always has an end time).
- Losing the release across a restart.

## Behaviour

### Time syntax (client side)

`--until` takes `+` followed by one or more `<number><unit>` groups, units
`h`, `min`, `s` (systemd style), e.g. `+30min`, `+1h30min`, `+90s`,
`+2h`. Whitespace is not allowed. A missing `+`, a leading `-`, an unknown
unit or a zero total is a usage error (exit 2). The client turns it into an
absolute UTC time and sends that.

### API

Same protections as `POST /api/power`: cross-origin protection, JSON body
with `Content-Type: application/json`, body size limit.

- `POST /api/release` body `{"until": "<RFC 3339 time>"}`.
  - `until` missing → 400. `until` not in the future (daemon clock) → 400.
  - A release while already released replaces the end time (extend or
    shorten).
  - Releasing works in any connection state: connected, dialling/backing
    off, machine off, spec mismatch hold. If a session is up, the daemon
    sends `MC ` (bounded, like the shutdown stop), checks that the idle
    banner returns within a short bound (a few seconds; banners come ~1/s),
    closes the connection, and only then responds.
  - If the banner does not come back the daemon still closes and stays
    released (the point is freeing the port); the response says so
    (`"banner": false`) and it is logged as a warning. The client prints a
    warning but exits 0.
  - An open brew is ended right away and stored as `truncated-released`
    (a coasting one keeps its normal kind, as today on disconnect).
  - The release is persisted *before* the connection is touched, so a crash
    mid-release still comes back released. If persisting fails the release
    is refused (500), nothing is freed and the client exits non-zero, so a
    `release && levacap …` chain stops. A release that is not on disk would
    break the restart guarantee.
  - Response 200: `{"ok": true, "until": "<RFC 3339>", "banner": true|false}`.
- `POST /api/reclaim`, empty JSON body `{}`.
  - Clears the persisted release and wakes the reconnect loop (no waiting
    out a backoff). 200 `{"ok": true}`, also when nothing was released.
  - Returns immediately; it does not wait for the reconnect.
  - If clearing the persisted release fails, the reclaim is refused (500)
    and the daemon stays released; otherwise a restart would release the
    machine again.
- A release running out needs no write: a persisted end time in the past
  has no effect (at startup or later). Clearing it then is optional.
- `GET /api/status` gains `"released_until": "<RFC 3339>"` or `null`.
- While released, `POST /api/power` and `GET /api/backup/*` are refused
  with 409 (display sampling is internal and only runs during a session;
  `status.display` is `null` while released, as after any disconnect) and a "machine released until …" error
  (not the generic 503 disconnected), so the operator sees why.
  `POST /api/mark` keeps its current behaviour (it needs a raw log session,
  so it is refused too, with the same 409 wording).

### Daemon

- The release end time is daemon state, persisted in the brew DB (schema
  v2, see Store). It is read at startup before the first dial.
- The reconnect loop checks the release before every dial: while released
  it waits until the end time or a reclaim, whichever comes first, then
  dials with the minimum backoff.
- Shutdown while released sends nothing to the machine.
- Truncation causes: the session's end path must know why it ended
  (released / shutdown / lost) so `Recorder.Abort` results are stored with
  the right kind. `CloseDangling` writes `truncated-crash`.
- `brew.Kind` gets a `Truncated()` method; metrics use it (truncated brews
  are no shot or flush but add pump seconds and volume, as today).

### Metric

`medewi_machine_released_until_seconds`: gauge, Unix time of the release
end, 0 when not released. (One series that gives both "is it released" —
`> time()` — and when it ends; a separate boolean is my call to skip.)
`medewi_machine_connected` is 0 while released. The example flap alert in
`docs/deployment.md` (`changes(medewi_machine_connected[1h]) > 6`) counts a
release as two changes, which is acceptable; the docs say so, and that
panels gated on `medewi_machine_connected == 1` go blank while released.

### Store (schema v2)

- New table holding the release end time (single row, or empty). Exact
  shape is the builder's call.
- Migration v1 → v2, run once inside the existing open transaction:
  **delete all brews with kind `interrupted` and their `brew_points`**
  (points first, foreign keys are on), create the new table, bump the
  version. Not reversible — the deployment notes tell the operator to copy
  the rows first if wanted (query below).
- v1 databases open and migrate; v2 opens; anything else fails as today.

Query to inspect the rows before deploying (brewhost):

```sh
DB=/var/lib/medewid/medewi.db
sudo nix shell nixpkgs#sqlite --command sqlite3 -readonly -header -column "$DB" "
  SELECT b.id, b.started_at, b.ended_at, b.duration_s, b.peak_bar, b.volume_ml,
         (SELECT COUNT(*) FROM brew_points p WHERE p.brew_id = b.id) AS points,
         b.ended_at = b.started_at AS crash
  FROM brews b WHERE b.kind = 'interrupted' ORDER BY b.id;"
```

### Clients

- `medewictl release [--until=+DURATION]`: prints
  `released until 2026-11-08 11:42:00 (local time)`; with `"banner": false`
  adds a warning line on stderr.
- `medewictl reclaim`: prints `reclaimed`.
- `medewictl status` shows `released until …` when set.
- Ops panel: shows "Machine released until HH:MM" and a **Reclaim** button
  while released. No release button on the panel (release is a laptop/CLI
  action before a capture). My call; easy to add later.

### Docs

Update `docs/architecture.md`/daemon package doc (the list of strings
sent is unchanged), `docs/deployment.md` (API, metric, alert, migration
note), `docs/runbooks/capture-session.md` and the conformance/firmware
flash procedure (`medewictl release` first, `reclaim` after), README CLI
section, and the CLAUDE.md "phase 2" summary if it lists endpoints.

## Constraints

- Hard rules from CLAUDE.md apply: no new machine strings (only `MC `,
  which is already whitelisted), never port 2323, pure Go, TDD,
  conventional commits, one branch + PR.
- Tests run against the sim; the banner-returns check must work with the
  sim's idle-banner heartbeat.
- The client converts relative times with its own clock; the daemon judges
  "in the future" with its clock. Both hosts run NTP; acceptable.

## Left to the builder

- Exact Go API (`Daemon.Release(ctx, until)`, `Reclaim()`, error values),
  the store table shape, how the cause is threaded to the session's end.
- The banner-check bound (a few seconds).
- Whether the duration parser lives in `cmd/medewictl` or a small shared
  package (only the client needs it now).
- Splitting the work into commits (the kind rename + migration is a natural
  first step).
