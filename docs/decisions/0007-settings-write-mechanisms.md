# 0007 — Write paths to explore: serial menu and EEPROM restore

Date: 2026-11-22. Status: accepted.

## Context

Medewi will have to change settings on the machine: at least brew
temperature, pressure profile and dose (#47), and the contents of
profiles and doses (UC15). #44 asked what the firmware lets a network
client set at all. This record answers that from the vendor documents
(firmware manual 3.2, ito module manual, Virtual Display protocol; page
numbers refer to the firmware manual unless noted), from the machine's
`MCh` reply and from the shape of our own backups and report stream. No
write command was sent for it.

It is about the wire only. How recipes, profiles, doses, coffees and
grind settings are represented in medewi is not decided here.

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

1. Two write paths are explored: the serial menu (A) for what it can set
   (setpoint, PID constants, selection), and EEPROM restore (B) for
   everything else. Neither is committed to before its probes are done.
2. Menu driving (C) is not pursued while B is open. Key codes stay
   forbidden.
3. For B the goal is that medewid does the restore, not a person with a
   terminal program. The order:
   1. Offline, no machine contact: map the profile and dose entries by
      diffing backups (#38), pin the checksum's region, and let `levabak`
      write an edited image.
   2. A record that lifts the port-2323 rule for exactly this procedure
      and names its guards (backup first, only the `e` command,
      read-back, a runbook for when it fails). No byte goes to the
      bootloader before that record is accepted.
   3. First machine probe: restore an unedited backup, so that only the
      procedure is tested. Then an edited image.
4. For A the first probe is the setpoint: read it from the stream, move
   to a clamped target in as few steps as the step size allows, check the
   stream after each step.

Not decided here: the data model (recipes, profiles, doses, coffees,
grind settings), which path a given feature uses, and whether medewi
ever starts a shot.

## Consequences

- #38 gets a precise scope: the profile entries (four slots with the
  same structure, so one mapped slot gives all four), the dose entries
  and the checksum's bounds. Exploring A needs none of it.
- The hard rules in `CLAUDE.md` do not change with this record.
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
  - whether the bootloader accepts an edited image at all.
  The serial-menu probes need the simulator to answer the same way
  first; the restore needs a bootloader in the simulator.
- A needs the `Serial menu` Wifi permission only. `Virtual LCD` and
  `Setup` are not needed while C is not pursued.
