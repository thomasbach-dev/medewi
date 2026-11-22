# 0007 — Serial menu for recipe execution, EEPROM restore for editing profiles and doses

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
  stay on for about 2 s after a change (p. 53, 234). If the firmware
  saves every step, stepping in 0.1° increments wears the setpoint's cell
  far faster than any restore would.
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
  heating stops, a running program ends, cold mode is lost. Fine for an
  editing session, too heavy for every shot.
- The neighbouring bootloader commands erase the EEPROM or the firmware
  (ito manual p. 34). One wrong byte there costs a reflash.
- A failed checksum resets every setting to factory defaults (p. 234).
  Our backups show the scheme: byte 0x0087 is an XOR over the settings.
  In all nine diffs between ten backups its change equals the XOR of the
  changed settings bytes. The counters (0x0022–0x0037) and the test
  tables (from 0x0F54) are outside it. An edit inside the covered region
  can therefore fix the checksum by XORing the same change into 0x0087.
  The exact bounds of the region are not known yet.
- We know ten entries of the layout (`levabak layout`), none of them a
  profile point or a dose amount. Those have to be found by diffing
  backups first (#38).
- Images only fit the firmware version they came from (p. 58).

Wear is not a limit. The ATmega1284's EEPROM is specified for 100,000
write cycles per cell (flash: 10,000), and a restore writes each cell
once.

Verify-after-write: exact, twice over. `MCm` must read back the image
byte for byte, and `MCu` shows how the firmware itself reads the edited
bytes, in plain text.

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

The two jobs get different mechanisms.

1. **Making the machine ready for a shot** uses the serial menu (A):
   brew temperature and the selection among what the machine already
   holds. It works whenever profile and dose are already as the recipe
   expects, which is the everyday case.
2. **Editing what the machine holds** (profile contents, dose amounts;
   UC15) goes through an EEPROM restore (B), in stages:
   1. Offline, no machine contact: find the profile and dose entries by
      diffing backups (#38), pin the checksum's region, and let `levabak`
      write an edited image whose `MCu`-visible meaning is predicted.
   2. The barista restores that image by the vendor's manual procedure
      and takes a backup; `levabak` compares it with the prediction.
   3. Only then, with a new record: the daemon restores by itself. That
      record lifts the port-2323 rule for exactly this procedure and
      names its guards (backup first, only the `e` command, read-back,
      the restore runbook for when it fails).
3. Menu driving (C) is the fallback if stage 1 or 2 fails, for example if
   the bootloader rejects an edited image. Until a record says otherwise,
   key codes stay forbidden.
4. First slice of #47: the setpoint. The daemon reads the current
   setpoint from the stream, moves to the clamped target in as few steps
   as the step size allows, and checks the stream after each step; it
   never counts steps blindly. Selection follows as the second slice.
5. Whether medewi also starts the shot (`MCc` plus a named dose, after a
   tap in guided mode) stays open. A recipe may name a dose either way:
   the guided mode then tells the person which shortcut to pick. If the
   daemon ever starts the pump, it does so only on a person's tap and
   needs a stop command proven first.

## Consequences

- A recipe refers to machine-side things by name (profile slot, dose) and
  to their contents by the latest verified backup: the settings in the
  `MCu` dump identify what a shot was pulled with, and `levabak diff`
  shows when they changed.
- #38 gets a precise scope instead of shrinking: the profile entries
  (four slots with the same structure, so one mapped slot gives all
  four), the dose entries and the checksum's bounds. The write path for
  shots (A) needs none of it.
- The hard rules in `CLAUDE.md` do not change with this record. Stage 2
  is a person following the vendor's procedure; stage 3 needs its own
  record.
- Open questions that only the machine can answer, each a single planned
  write with read-back, to be run when explicitly asked for:
  - the spelling of `MCc` + `PROFILE 1`, `SP USER 1` and the dose names;
  - whether `MC#` takes a value or cycles, and what it is after a restart;
  - whether the firmware saves the setpoint on every step or once after
    the last one;
  - what a write replies, and what happens with the permission off;
  - how writes behave in eco, in standby, during a program and during a
    shot;
  - whether a selected profile's own temperature shows up as the stream's
    setpoint;
  - whether the bootloader accepts an edited image at all (stage 2).
  #47's design starts with the serial-menu probes; they need the
  simulator to answer the same way first.
- The `Serial menu` permission is the only one the daemon needs. `Virtual
  LCD` and `Setup` can stay off or be turned off.
