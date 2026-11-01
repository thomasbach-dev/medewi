# 0002 — One always-on daemon owns machine and data

Date: 2026-11-01. Status: accepted.

## Context

Shots need identity: "show me yesterday's cappuccino" means timestamp,
coffee, drink, the recipe as it stood that morning, the curves, and an
optional verdict. The firmware forgets every curve the moment the shot
ends. Prometheus was considered as the telemetry store but cannot attribute
curves to a cup. Multi-user recipes (several people, several devices) are a
hard requirement for the reference household.

## Decision

Exactly one always-on daemon owns the machine connection and the data:

- **Database (SQLite)** for shots and recipes. A shot is an event with
  identity; recipes carry their edit history. The DB exports to
  LLM-friendly text. Backups are pushed off-device.
- **Prometheus** gets machine health and maintenance metrics only:
  reachability, power state, boiler temperature and setpoint, shot counter,
  water throughput since descale. Never high-frequency curves.
- Everything else — phones, dashboards, possible Beanconqueror — is a
  client of the daemon's HTTP API.

## Consequences

- Silent, automatic shot capture (UC5) holds in every deployment where the
  daemon runs on an always-on host.
- Grafana alerting covers warmup, left-on, descale-due, and
  recalibration-due signals from a handful of cheap gauges/counters.
- The daemon is a single point of ownership: no second process may hold
  machine state, which avoids fighting over leva!'s global stream toggles.
