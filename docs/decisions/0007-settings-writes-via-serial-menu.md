# 0007 — Recipe execution writes through the serial menu; no restore, no menu driving

Date: 2026-11-22. Status: proposed.

## Context

Recipe execution (#47) has to make the machine ready for a coffee: brew
temperature, pressure profile, dose. Before designing it, #44 asked what
the firmware lets a network client set at all. This record answers that
from the vendor documents alone (firmware manual 3.2, ito module manual,
Virtual Display protocol; page numbers refer to the firmware manual
unless noted), from the machine's `MCh` reply and from the shape of our
own backups and report stream. No write command was sent for it.

The firmware offers three ways in.

### A. Serial-menu commands (port 23)

What they set (p. 222, 229–231):

| Target | Command | Kind |
|---|---|---|
| Setpoint of the selected PID | `MC1`/`MC2` select, `MC+`/`MC-` step, `MC#` step size (0.1, 1.0, 10) | relative |
| Setpoint, stored values | `MCc` + `SP USER 1`…`3` (both PIDs), `TEMP RESET`, `TEMP +`/`TEMP -` | absolute / relative |
| PID constants Kc, Ti, Td, anti-windup | `MCp`/`MCP`, `MCi`/`MCI`, `MCd`/`MCD`, `MCw`/`MCW` | relative |
| Active pressure profile | `MCc` + `PROFILE 1`…`4`, `P.PROF OFF`, `P.PROF +` | absolute / cycling |
| Power state | `MCa`, `MCe`, `MCE` (in use since phase 2), `MCc` + `ECO`, `STANDBY`, `COLD`, `PID1 ON`, `PID2 ON` | absolute / toggle |
| Run something | `MCc` + `DOSE`, the named doses, `PUMP PB`, `WARMUP`, `FLUSH`, `SKIP` | starts or stops the pump |

What they cannot set: the contents of a pressure profile (points, times,
pressures; p. 151–153), dose amounts (p. 171), which dose `DOSE` runs, and
every other Setup value. The machine holds four profile slots. A profile
can carry its own brew temperature (p. 135) and a dose its own profile
(p. 174).

Failure modes:

- Most commands are relative steps or toggles. A swallowed command (the
  machine swallows commands sent shortly after connect) leaves the value
  off by one step, and a toggle sent twice undoes itself. Serial commands
  do not wake the machine from eco or standby (p. 238).
- `MC+`/`MC-` act on whichever PID is selected and use whatever step size
  `MC#` was left at. Both are global state another client or the encoder
  can change.
- The spelling of multi-word function names after `MCc` is not documented;
  only `MCcECO` is shown (p. 229). Whether a write is acknowledged, and
  what a refused one looks like, is not documented either.
- Setpoint changes are always saved to the EEPROM (p. 19, 56). Power must
  stay on for about 2 s after a change (p. 53, 234).
- Gate: the `Serial menu` Wifi permission (p. 220). With it on, any host
  in the LAN can drive pump and heater.

Verify-after-write: the report stream carries the setpoint, Kc, Ti, Td,
the windup guard and the selected profile's name, about four times a
second. Every value A can set is read back within a second, without a
dump.

### B. EEPROM restore (bootloader, port 2323)

The bootloader's `e` command takes an EEPROM image over XMODEM (p. 58).
It can set everything at once. There is no restore command on port 23.

Failure modes:

- It needs the bootloader port. The first byte resets the controller:
  heating stops, a running program ends, cold mode is lost.
- We would write all 4 KiB while knowing ten entries of the layout
  (`levabak layout`). The image's checksum scheme is unknown, and a failed
  checksum resets every setting to factory defaults (p. 234).
- Images only fit the firmware version they came from (p. 58).
- The neighbouring bootloader commands erase the EEPROM or the firmware
  (ito manual p. 34).

Verify-after-write: exact, by reading `MCm` back.

### C. Menu driving (Virtual Display protocol)

`MC@` mirrors the LCD; `MC` plus a raw key byte presses a key (protocol
p. 3–4). It reaches everything the menu reaches, so also profile contents
and dose amounts.

Failure modes:

- The menu also holds factory reset, firmware update, the clear commands
  and the programs. A navigation error does not just set a wrong value.
- `MC@` and `MCr` exclude each other, so the daemon is blind to reports
  (shot recording, metrics, power verification) while it drives the menu.
- A person at the encoder moves the same cursor. A connection lost
  mid-edit leaves the menu open in that edit.
- Values are changed by key steps, a held key must be repeated at least
  once a second, and the release message contains a NUL byte.
- The menu layout belongs to one firmware version and partly to this
  machine's configuration. The simulator replays recorded frames and has
  no menu model, so nothing of it could be tested without the machine.
- Gate: the `Virtual LCD` and `Setup` Wifi permissions.

Verify-after-write: good. Every frame shows cursor and value before a key
is sent, and an `MCu` dump before and after (`levabak diff`) proves that
only the intended settings changed.

## Decision

1. Recipe execution writes through the serial menu (A) and nothing else.
   A recipe names a brew temperature and one of the machine's four profile
   slots (and with it, later, a dose). The contents of profiles and doses
   are edited at the machine, as today.
2. The daemon never restores an EEPROM image (B). Restore stays a manual
   disaster-recovery step with its own runbook. The ban on port 2323
   stands without exception.
3. Menu driving (C) is not built. If four profile slots become the limit,
   it needs a new record, a menu model in the simulator and its own
   guards. Key codes stay forbidden.
4. First slice of #47: the setpoint. The daemon reads the current setpoint
   from the stream, steps towards the clamped target, and checks the
   stream after each step; it never counts steps blindly. Profile
   selection follows as the second slice.
5. The pump-starting functions (`DOSE`, the named doses, `PUMP PB`,
   `WARMUP`, `FLUSH`) are not part of recipe execution. A person starts
   the shot.

## Consequences

- The daemon cannot tell from the stream what is inside a profile slot,
  only which one is selected. Recipes therefore refer to slot contents by
  the latest verified backup: the profile's settings in the `MCu` dump
  identify what a shot was pulled with, and `levabak diff` shows when the
  barista changed them.
- #38 shrinks. Neither the write path nor its read-back needs the EEPROM
  map, so mapping more of it is only worth it for values the menus do not
  show (counters, the test tables of #73).
- Open questions that only the machine can answer, each a single planned
  write with read-back, to be run when explicitly asked for:
  - the spelling of `MCc` + `PROFILE 1` and `SP USER 1`;
  - whether `MC#` takes a value or cycles, and what it is after a restart;
  - what a write replies, and what happens with the permission off;
  - how writes behave in eco, in standby, during a program and during a
    shot;
  - whether a selected profile's own temperature shows up as the stream's
    setpoint.
  #47's design starts with these probes; they need the simulator to
  answer the same way first.
- The read-only whitelist in `CLAUDE.md` stays as it is until #47 lands a
  guarded command. Each new command then joins it by name.
- The `Serial menu` permission is the only one the daemon needs. `Virtual
  LCD` and `Setup` can stay off or be turned off.
