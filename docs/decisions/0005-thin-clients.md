# 0005 — Thin clients; phone deployment modes deferred

Date: 2026-11-01. Status: accepted.

## Context

Three ways to host the system were on the table: a home server, an
always-plugged "display phone" mounted at the machine (daemon + kiosk UI),
and a serverless single-phone mode where everything lives in one mobile
app. Multi-user recipes are a hard requirement for the reference setup, and
always-on shot capture (UC5) degrades in any phone-hosted mode that isn't
permanently awake.

## Decision

The reference deployment is: daemon on a home server, every phone a thin
client (browser). Two modes are deferred, in order:

1. **Display phone** (next evaluation): daemon + kiosk UI on an
   always-plugged rooted ARMv7 Android phone at the machine — resolves
   always-on capture without a server.
2. **Single-phone, no server** (for other users): core embedded in an app.
   Documented trades: no multi-user recipes; capture only while the app is
   open.

## Consequences

- The web UI must work well on phone browsers from day one.
- The core keeps a narrow, embeddable API and the pure-Go policy
  ([0004](0004-go.md)) so both deferred modes stay cheap to attempt.
- No native app work happens until the display-phone evaluation.
