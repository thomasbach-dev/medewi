# Medewi Phase 2: The Daemon (medewid) — Design

Date: 2026-11-02. Status: approved 2026-11-03 (barista answered the open
questions; see "Resolved questions").

## Intent

Phase 2 delivers the always-on daemon from decision 0002: one process on
the brewhost (the always-on host role — see docs/architecture.md) owns the
machine connection and the data, and the operator's
current Status-Monitor workflows move to it. "Done" looks like: the barista
wakes the machine from a phone or script and sees READY; metrics land in
Prometheus; every brew is silently captured into SQLite; the settings
backup is one command away for existing backup machinery; and all of it is
behind mTLS. Recipes, guided brewing and the steam assist stay out (issue
#6 keeps the write half for later).

## Grounding

The repo CLAUDE.md's "Empirical machine facts" bind this design. Additional
phase-2-relevant facts, all verified live on 2026-11-02:

- `MCa` wakes the machine from eco/standby; `MCe` enters eco; `MCE` enters
  standby. All three were exercised remotely and their stream signatures
  recorded (mode field `PID`/`ECO`, standby = `?`-masked fields).
- The mode field (offset 4, len 3) plus field-masking gives power state
  without any extra query.
- A full lifecycle capture (standby → wake → ramp → READY → flushes → shot
  → eco → standby) exists in `captures/coldstart-ready/` (unpromoted) and
  pins realistic thresholds: READY criterion (|temp−setpoint| ≤ 0.5 for 10
  reports) fired ~145 s after wake from 47 °C; the shot ran ~49 s above
  1 bar with a 8.9 bar peak; flushes stayed short.
- The `watch/` detector package (plan `2026-11-02-levawatch.md`) is the
  shared event component; the daemon consumes the same Ready/SteamWindow/
  Eco events. If levawatch's plan has not been executed yet when phase 2
  starts, its Tasks 1–2 become prerequisites of the daemon plan.

## Goals

1. **`cmd/medewid`** — single static binary; all config via `MEDEWID_*`
   env vars with flag overrides; embedded ops panel; NixOS module.
2. **Connection ownership**: Dial → `WaitReady` → `StartReports`; decode
   via the spec at `MEDEWI_XML_PATH`; reconnect forever with capped backoff
   (machine-off is a normal state, reported, never fatal). Startup XML
   handshake: on `MC?` name ≠ loaded spec name, the daemon stays up but
   enters a degraded mode — no decoding, no writes, connectivity-only
   metrics, a loud log line and `medewi_spec_mismatch 1`.
3. **Brew capture**: a recorder state machine turns decoded records into
   stored brews. Every pump episode is stored (metered flow > 0 opens an
   episode; 5 s of zero flow closes it — amended 2026-11-03, see
   "Planning amendments") and classified: `shot` if
   duration ≥ 20 s AND peak ≥ 5 bar, else `flush` (initial thresholds —
   `// ASSUMPTION:` markers, tunable against the fixtures). Curves are
   stored at full report resolution.
4. **SQLite** (`modernc.org/sqlite`, WAL): `brews` (id, started_at,
   ended_at, kind, duration_s, peak_bar, volume_ml, notes) and `brew_points`
   (brew_id, t_ms, temp, setpoint, pressure, flow, heat), plus a
   `schema_version` meta table. DB path `MEDEWID_DB`, default
   `/var/lib/medewid/medewi.db`. No retention logic in phase 2; the file
   is the operator's to back up.
5. **Metrics** (`prometheus/client_golang`) on the same TLS listener:
   `medewi_machine_connected`, `medewi_power_state` (labeled enum:
   active/eco/standby/unknown), `medewi_boiler_celsius`,
   `medewi_setpoint_celsius`, `medewi_heater_percent`, `medewi_ready`,
   `medewi_shots_total`, `medewi_flushes_total`,
   `medewi_pump_seconds_total`, `medewi_volume_liters_total` (sum of
   per-episode firmware volume), `medewi_spec_mismatch`,
   `medewi_report_lines_total`, `medewi_decode_errors_total`,
   `medewi_reconnects_total`. No pressure/flow time series — curves live
   in the DB.
6. **Write surface — exactly three commands**: `MCa`, `MCe`, `MCE`, via
   `POST /api/power {"state": "active"|"eco"|"standby"}`. Verify-after-
   write: the daemon watches the stream for the expected signature (mode
   field / masking / setpoint) within 10 s and reports success or failure
   in the response. Writes are refused while a brew episode is open.
   Nothing else is ever sent beyond the read-only whitelist.
7. **HTTP API** (JSON unless noted): `GET /api/status` (connected, spec
   ok, mode, temp, setpoint, ready, last brew summary), `POST /api/power`,
   `GET /api/brews?limit=N`, `GET /api/brews/{id}/points` (JSON array;
   `?format=csv` for text), `GET /api/backup/mcu` and `/api/backup/mcm`
   (text/plain dumps run over the daemon's connection, serialized,
   refused while a brew is open), `GET /healthz` (plain 200, no client
   cert required — see TLS note).
8. **Ops panel**: one embedded HTML page at `/` — mode, temperature vs
   setpoint, READY indicator, three buttons (wake / eco / standby) calling
   `/api/power`. Polls `/api/status` every 2 s. No framework, no build
   step, phone-friendly. This is the first thin client of decision 0005,
   not the guided UI.
9. **TLS**: one listener. `MEDEWID_TLS_CERT`, `MEDEWID_TLS_KEY`,
   `MEDEWID_TLS_CLIENT_CA` → HTTPS with `RequireAndVerifyClientCert`
   (mTLS) for everything including `/metrics`. Without TLS config the
   daemon REFUSES to start unless `--insecure-http` is passed explicitly.
   Exception: `/healthz` is also served on a separate localhost-only
   plaintext port (`MEDEWID_HEALTH_ADDR`, default `127.0.0.1:9114`) so
   systemd watchdog/scripts need no certs. Certificate provisioning is the
   operator's NixOS configuration's job, including Prometheus's client
   cert.
10. **`cmd/medewictl`** — thin mTLS API client for scripts:
    `medewictl status`, `medewictl power active|eco|standby`,
    `medewictl backup -out DIR` (writes
    `<timestamp>_<firmware>.mcu.txt` and `<timestamp>_<firmware>.mcm.hex`,
    e.g. `2026-11-03T041500Z_3.2.mcu.txt`; stdout with `-out -`).
    Timestamp is the dump's completion time in UTC, Go layout
    `2006-01-02T150405Z` (sortable, no colons);
    firmware is the version from the `$id: leva! (3.2)` banner
    (`Conn.ID()`), which the daemon returns in an `X-Medewi-Firmware`
    response header on both backup endpoints (`unknown` if no banner was
    parsed yet). Client cert config via `MEDEWID_CTL_CERT/KEY/CA` and
    `MEDEWID_URL`. This is the hook for the existing backup machinery.
11. **Events seam**: the daemon feeds decoded records through `watch/` and
    exposes the resulting events two ways in phase 2: structured log lines,
    and an optional `MEDEWID_EVENT_EXEC` hook (same contract as levawatch:
    `MEDEWI_EVENT`/`MEDEWI_TEMP`/`MEDEWI_SETPOINT` env). Desktop/push
    wiring stays external; levawatch remains the standalone interim and is
    deprecated once the daemon's hook covers its users.
12. **Deployment**: flake gains `packages.medewid`/`packages.medewictl`
    and `nixosModules.medewid` (systemd service, `DynamicUser=true`,
    `StateDirectory=medewid`, env-file based config, restart always).

## Non-goals (later phases)

Recipes and recipe execution; the steam assist and any setpoint writes
(issue #6); guided/barista UI beyond the ops panel; SSE/push endpoints;
Grafana dashboards (follow-up in the NixOS repo); promotion of the
lifecycle capture (separate review pass); Beanconqueror/Visualizer.

## Anti-goals

- A second process talking to the machine while medewid runs. Every tool
  becomes an API client (hence `medewictl`); the conformance suite's
  direct-dial mode is for daemon-stopped maintenance only (flash gate).
- Any byte to port 2323; any write command beyond `MCa`/`MCe`/`MCE`.
- A plaintext listener that wasn't explicitly demanded by flag.
- Losing a brew because of a slow consumer: capture must not block the
  reader (bounded buffering, drop-and-count applies to metrics/status, but
  brew points are written synchronously enough to keep full curves —
  SQLite inserts at 4 rows/s are trivial).

## Decided design details

- **Concurrency shape**: one connection-owner goroutine (dial/ready/
  stream/reconnect loop) publishes decoded records to: the recorder, the
  watch detector, and a latest-state cache (RWMutex struct) read by HTTP
  handlers and prometheus collectors. Power and backup requests go through
  a single serialized command channel handled by the connection owner
  between stream reads (leva's demux makes commands safe during streaming;
  serialization prevents interleaved dumps).
- **Brew attribution**: phase 2 stores brews unattributed (`notes` free
  text via `PATCH /api/brews/{id}` is OPTIONAL — builder may include if
  trivial, else it waits for phase 3).
- **Spec handling**: field lookup by export name (`Temperature PID1`,
  `Setpoint PID`, `Pressure`, `Flow rate`, `Heat PID`), resolved once at
  startup; missing names degrade that feature with a warning, never crash.
- **Testing**: everything against `sim` with the committed real fixtures
  (shot-genlever for recorder tests; steam-eco for power-state tests) plus
  crafted streams; mTLS tested with in-test generated CAs (crypto stdlib);
  machine tests double-gated as everywhere. The full gate and CI as in
  CLAUDE.md.
- **Dependencies added**: `modernc.org/sqlite`, `prometheus/client_golang`
  (both pure Go — ADR 0004 holds).
- **leva/ prerequisites (explicitly in scope, built first):**
  1. `factor` attribute support in the spec parser: the vendor XML scales
     some int fields (e.g. the flow meter: `factor="0.0166"`, unit ml/s).
     `ValueSpec` gains `Factor float64` (default 1); `Record.Float`
     returns the scaled value. Without this, flow lands in the DB ~60×
     off. Name lookup stays document-order-first (documented); the
     metered "Flow rate" (index 30) precedes the scale-based duplicate
     (index 45), so by-name lookup is correct as-is.
  2. `Conn.CommandOpts(ctx, cmd, firstLine, idleGap time.Duration)` — the
     dump endpoints must not rely on the 300 ms idle gap (the machine can
     pause mid-dump); dumps use a ~2 s inter-line gap. `Command` becomes a
     thin wrapper.
- **Volume accounting** (amended 2026-11-03): the firmware's "Volume"
  field (ml) is a per-pump-run counter, not a cumulative one — see
  "Planning amendments". Each brew stores its final Volume reading as
  `volume_ml`; `medewi_volume_liters_total` adds it on episode close.
  `brew_points.flow` stores the factor-scaled metered flow in ml/s.
- **Dump completeness**: `/api/backup/mcm` validates the Intel-HEX EOF
  record (`:00000001FF`) before returning 200. `MCu` has no known
  terminator: the endpoint uses the 2 s gap heuristic, returns the line
  count in a response header, and `medewictl backup` prints it so backup
  scripts can sanity-check; whether `MCu` has a reliable terminator is a
  verify-live item for the next machine session.
- **Ready as a level**: the shared `watch/` package gains
  `(d *Detector) Ready() bool` (current level, consistent with the event
  edges) — the levawatch plan is amended accordingly rather than
  duplicating the threshold logic in the daemon.
- **Hard-rule interpretation (ruling)**: for pure power toggles the
  CLAUDE.md write-rule triad reads as: clamp — vacuous (no values sent);
  watchdog — provided by the firmware's own eco/standby timers plus the
  Grafana left-on alert (decision 0002), NOT duplicated in the daemon;
  verify-after-write — implemented as specified. CLAUDE.md gets one
  clarifying clause so future executors don't trip on this.

## Planning amendments (2026-11-03, from the committed fixtures)

Re-reading the fixtures while planning disproved two assumptions:

- **Pressure is not a pump signal.** With the pump off and the brew valve
  closed, pressure climbs to the OPV by thermal expansion: right after the
  genlever shot it rises from 4 to 9.9 bar at zero flow, and the steam
  fixture opens with 54 s at ≈10 bar and zero flow. A 1-bar trigger would
  never close the shot episode and would log the steam phase as a shot.
  Conversely, flushes (≈220 ticks flow) and hot-water draws (≈490 ticks)
  run at ≈0.05 bar and would never open one. Metered flow (raw "Flow
  rate" > 0) is the pump signal; pressure only feeds `peak_bar` and the
  curve. Peak and duration are taken over the episode up to the last
  flowing report; the 5 s zero-flow tail is not stored.
- **"Volume" resets at every pump start** (reads 000, then counts the run's
  ml: 101 for the genlever shot, 71–73 for flushes, 216 for a hot-water
  draw) and holds afterwards. Cumulative deltas would be wrong; the
  per-episode maximum is the run's volume.

Classification thresholds are unchanged and fit all four fixture
episodes (shot 54 s/9.9 bar; flushes 16–21 s/<0.1 bar; hot water
20 s/0.45 bar → `flush`).

## Left to the builder

Schema DDL details and indexes; backoff constants; panel markup/styling;
collector implementation style; CSV formatting; exact degraded-mode
behavior matrix; log format (`log/slog`).

## Resolved questions (barista, 2026-11-03)

1. **DB home on the brewhost**: `/var/lib/medewid/medewi.db`
   (`StateDirectory=medewid`) — fits the existing backup machinery.
2. **Backup file names**: `<timestamp>_<firmware_version>.<ext>`, i.e.
   `….mcu.txt` and `….mcm.hex` (details in goal 10).
