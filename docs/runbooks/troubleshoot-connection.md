# Runbook: medewid lost the machine

Use it when medewid reports the machine as disconnected
(`medewi_machine_connected 0`, `"connected": false` in `medewictl status`)
or stopped recording brews, and you want to know why. You look at two
things over the same time window: what medewid itself logged (journal) and
what was on the wire (raw traffic log). Both run on brewhost and only read
files; nothing here talks to the machine.

## 1. Pick the window

Find roughly when it happened: the metric
`medewi_machine_connected`, the last brew in `medewictl status`, or the
time someone noticed. Take a few minutes on either side. Times are local
time, as in `journalctl`; add ` UTC` for UTC.

```sh
SINCE='2026-11-10 10:40'
UNTIL='2026-11-10 10:50'
```

## 2. Read the daemon journal

```sh
journalctl -u medewid -S "$SINCE" -U "$UNTIL"
```

The connection messages, in the order of a normal session:

| Message | Meaning |
| --- | --- |
| `dial failed` | TCP connect to `MEDEWID_MACHINE_ADDR` failed (machine off, network, DNS, `ipAddressAllow`) |
| `machine not ready; stopping a possibly running stream and reconnecting` | connected, but no banner within 10 s: a stream left on by another client, or the controller is hung |
| `connection lost before ready` | the socket closed while waiting for the banner |
| `connected` | banner seen; carries the firmware version |
| `no XML= reply to MC?; reconnecting` | the machine never answered `MC?` |
| `machine XML does not match the loaded spec; not starting reports` | wrong vendor XML for this firmware; medewid holds the connection and sends nothing |
| `starting reports failed` | sending `MCr` failed |
| `reports started` | the healthy state |
| `report stream silent; reconnecting` | no report for 10 s on an open connection |
| `connection lost` | the socket closed (the error says how) |
| `machine released` / `machine reclaimed` / `machine release ended` | a deliberate `medewictl release`, not a fault |

Reconnects back off between attempts, so a long gap between `dial failed`
lines is normal. A loop of `connected` followed by
`no XML= reply to MC?; reconnecting` or `report stream silent; reconnecting`
means the TCP side works and the controller does not answer.

## 3. Read the wire

```sh
sudo levalog decode -s "$SINCE" -u "$UNTIL"
```

`sudo` is needed because the raw-log directory (`/var/lib/medewid/raw`) is
`0700` under `DynamicUser`. The wrapper the module installs already passes
the vendor XML, so reports are decoded. Each connection session starts a
new file, and files also rotate hourly, so one session can span several
files: a file boundary is not by itself a disconnect. levalog prints a
`# <path>` header before each file, and the file name is the file's start
time in UTC. Columns: seconds since the file start, wall time, direction (`tx`, `rx`, `note`), kind, text. The full syntax and
time forms are in [docs/cli-conventions.md](../cli-conventions.md).

What to look for at the end of the last good session and in the next ones:

- **Reports stop, nothing else arrives:** the machine went silent while
  the TCP connection stayed up. Matches `report stream silent; reconnecting`.
- **Banners only (`banner $id: leva! (3.2)`), no reports after `tx cmd MCr`:**
  the machine is idle and alive but ignored the stream request; a command
  sent too early after connect is swallowed (see
  [docs/machine-behaviour.md](../machine-behaviour.md)).
- **No banners and no reports in a new session:** another client left the
  stream on, or the controller hangs. Matches
  `machine not ready; …`.
- **`notice [READY]`, `ECO` in the mode field or `?`-masked reports:** the
  machine changed state (heat-up done, eco, standby); not a connection fault
  by itself.
- **`note` lines:** `dropped N lines` (traffic the allowlist did not keep)
  and `mark: …` (a mark someone set with `medewictl mark`).
- **`partial` or `fragment` at the end of a file:** the connection ended in
  the middle of a line.
- **Many short session files:** a reconnect loop; their start times line up
  with the journal's `connected` lines.

## 4. Afterwards

Note findings in the machine logbook. If the machine needs a power cycle,
do it at the machine; never touch port 2323. To keep a raw-log file for
later analysis, copy it to `private/` on a workstation, never into the
repository.
