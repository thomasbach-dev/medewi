# Machine behaviour (observed)

What the machine does on the wire, from real captures. CLAUDE.md lists the
short facts; this file has the detail and the evidence. Extend it with each
capture that teaches something new, and mark hypotheses as such.

## Display mirror (`MC@`)

- One frame every 100 ms, always 69 bytes: `<`, three header characters,
  four 16-character LCD rows, `>`. Header: backlight (`0`/`1`), pressed key
  (`0` none; `9` while the encoder was held, 2026-11-05), layout (`2` main
  screen, `1` menu).
- Main screen rows: profile name; `1:` boiler temperature and setpoint;
  heat output (with a bar); pressure, shot time, volume. During a shot the
  first row becomes the timer and volume, during programs the program name
  (`WARM-UP`, `FLUSH`) and a countdown.
- Custom characters (HD44780 CGRAM codes in the rows) and how `leva.ParseFrame`
  shows them (2026-11-15): `0x01` `▍`, `0x02` `▋`, `0x03` `█` are the bar's
  cell fills (partial, partial, full; the heat bar and the `PUMP` bar);
  `0x04` is a dash `–` (operator-confirmed for an empty menu shortcut slot;
  the same code also appears between temperature and setpoint on the main
  screen, assumed to be the same dash); `0x05` is a check mark `✓` after the
  pressure on the main screen (look operator-confirmed; meaning unknown). `0xDF` is `°`. Other bytes below `0x20`
  (`0x00`, `0x06`, `0x07`) are not seen and stay spaces. Rows are 16 runes,
  no longer 16 bytes.
- `MCr` and `MC@` replace each other; the switch can cut the current line
  off without its CRLF and glue the next record onto it (report after
  report, frame after frame, report after frame). `leva` splits such lines
  and counts the cut-off part as a fragment.
- `levasim` replays recorded reports after `MCr` and holds the current
  LCD: the latest whole recorded frame its timeline has passed (in every
  mode; before the first, the stream's first frame). After `MC@` it
  mirrors that LCD at once and then every 100 ms (divided by `--speed`,
  at least 1 ms), so a client sampling out of phase with the recording
  still sees the display. As on the machine, the last of `MCr`/`MC@`
  sent wins.

## READY

- The machine sends an unsolicited line **`[READY]`** in the stream, in
  report mode too (not a report, not a reply). Seen 2026-11-05 at 582.7 s
  into a warm-up from 80 °C and about 7 minutes after the warm-up program
  ended.
- At the same moment READY replaces the setpoint on the main screen
  (`1: 91.0ß   READY`) for about 10 s, with the beeps; the backlight comes
  on.
- The manual: READY means the whole machine (not just the boiler) is warm,
  decided by steady-state detection over the last three minutes.
- `Heat PID (average)` in the reports updates once a minute. Both READY
  events appeared within about 1 s after such an update.
- Hypothesis (two positive examples): READY when the last three one-minute
  averages agree and the temperature stayed in a narrow band.

  | Capture | Last three averages | Temperature range (3 min) | READY |
  | --- | --- | --- | --- |
  | warm-up, 522 s | 8.7, 7.1, 7.1 | 0.25 °C | no |
  | warm-up, 582 s | 7.1, 7.1, 6.9 | 0.25 °C | yes |
  | cold start, 363 s | 8.0, 8.0, 8.1 | 1.0 °C | no |
  | after warm-up program, 967 s | 4.6, 3.5, 3.9 | 0.70 °C | no |
  | after warm-up program, 1027 s | 3.5, 3.9, 3.5 | 0.15 °C | yes |

  The daemon does not need the rule: it takes READY from `[READY]`. The
  hypothesis matters for predicting READY (e.g. "ready in about 3 min").

## Shots and flushes

- `Pressure SP` is set during a profiled shot (1.0 bar pre-infusion, 9.0,
  then the profile's ramp) and `?????` during a flush and during the pump
  pulses of the warm-up program. `brew` classifies by it.
- `Selected profile` names the profile (e.g. `SOAK 1.0b`); the dose buttons
  (`[1] DBL ESPR`, `[2] ESPRESSO` in the menu) do not show in the reports.

## Pressure band

- Hypothesis: `pressure_band_min` and `pressure_band_max` are the lowest
  and highest raw sensor reading within one report interval (~250 ms),
  and `pressure` is their average. The manual does not define the fields.
- Evidence: in all 644 reports of the `shot-genlever` fixture,
  `pressure_band_min` ≤ `pressure` ≤ `pressure_band_max`. At rest the
  band is sensor noise (e.g. 0.00 … 0.03 … 0.11 bar); near 10 bar it is
  e.g. 9.80 … 9.90 … 9.94, with a median width of about 0.3 bar above
  3 bar.
- Fits the manual: it says the sensor is sampled several times a second
  and the samples averaged, and its sensor-noise value is the spread of
  the readings (max − min). A firmware changelog entry fixed the band
  ignoring the user-set pressure offset. The Status Monitor XML draws it
  as a band around the pressure curve, hidden by default.
- Use: the width shows pressure jitter within a report (pump pulsation,
  sensor noise); a narrowing band means the pressure regulation settled.
  It is not the profile target (that is `pressure_sp`).
- To confirm: a profiled shot should widen the band during phase control
  (marker `*`, `pressure_sp` set) and shrink it to noise with the pump off.

## Warm-up program (run menu)

Observed 2026-11-05 from a cold start (boiler 34 °C):

1. Setpoint jumps to 110 °C, heater at 100 %.
2. At 110 °C a 7:00 countdown starts (LCD `WARM-UP 06m:59s`); the setpoint
   ramps down linearly to the brew setpoint.
3. Pump pulses (marker `*`, no `Pressure SP`): about one per minute, then
   every 15–30 s in the last two minutes, mostly 2–4 s and 10–20 ml; one
   21 s pulse reached 12.2 bar. 285 ml in total.
   (`levalog runs` on the same capture gives 12.75 bar for that pulse.)
   The `Volume` field does **not** reset between the pulses: it counts on
   through the whole program (15, 31, 50 … 285 ml), unlike the reset at
   every pump start outside programs. The first shot after the program
   starts from 0 again. One run only; whether the program or the pulse
   length decides this is open. Per-pulse volume is the difference from
   the previous pulse.
4. After the program the manual asks to vent the boiler through the wand
   (air collects above boiling point).

Detection is issue #13; the data model for programs is issue #12.

## Backflush program (run menu)

Observed 2026-11-14 from the raw log, warm machine (boiler 94 °C), blind
basket in the portafilter, operator marks at start, at the key press and
at the end. Fixture: `testdata/fixtures/backflush` (cut with `levalog
extract`; the LCD frames show the phases and the progress bar). About
4:40 min from selecting the program to the return to the menu. Mode stays `PID`, setpoint 94, `Pressure SP` `?????`
throughout; only the LCD (third menu row, in place of `Backflush`) and
the pump marker show the program.

1. Against the blind basket: 6 pump pulses (marker `*`), 5.5–5.8 s each,
   starting every 16 s. The LCD alternates `PUMP` and `SOAK 00m:NNs`
   (counting up during the pause). Pressure rises to the OPV, 10.9–11.7
   bar, and holds there; 22–35 ml per pulse.
2. `EMPTY`: the LCD asks to empty the blind basket and waits for a key
   press, pump off, here for about 80 s. The pressure trapped above the
   blind basket leaks down only slowly, 9.6 → 7.1 bar. The press itself
   was not seen: the frame sampling (500 ms every 5 s) has a gap from the
   last `EMPTY` frame to 3 s later, when the pump started.
3. Rinse without the blind basket: 3 pump pulses of 19.6–19.9 s, starting
   every 40 s; the LCD alternates `PUMP` and `WAIT`. Within each pulse the
   pressure saws between about 0.3 and 12–13 bar with a period of about
   3.3 s (6–7 cycles per pulse): about 1.7 s rising slowly from 0.3 to
   2 bar, then about 1.3 s at the OPV. Hypothesis: the program opens and
   closes the grouphead valve while the pump runs. 100–112 ml per pulse.
4. The LCD returns to the run menu, `Backflush` selected.

- The `Volume` field resets at every pump pulse of this program, unlike
  the warm-up program, where it counts on through all pulses.
- `levalog runs` lists each pulse as a flush of its own (9 runs): nothing
  in the report stream marks the program (detection: issue #14).

## Pressure test program (run menu)

Observed 2026-11-17 from the raw log with LCD sampling (500 ms every
5 s): cold machine (PID1 control off, mode `CLD`, 25 °C), no blind
filter (the sensor sits before the firmware-controlled grouphead valve),
operator mark on starting it. One run. Times are seconds after the mark.
About 4 min from selecting the program to `Test pressure ✓`.

1. Before: pressure left over from earlier use stays trapped with the
   pump off (here 1.94 bar).
2. +5.2 s: the valve opens; pressure drops to 0 within one report. The
   `Volume` field resets to 0 here, with the pump marker still `-`,
   2.8 s before the pump starts (outside programs it resets at the pump
   start). LCD: `WAIT 1.9✓`, then `WAIT 0.0✓`.
3. +8.0 s: pump marker `*`. `Phase` reads 100° for two reports, then
   120°, and sweeps down: **120° → 50° in 2° steps**, each held about
   4.5 s (the odd degree in between passes within about 0.5 s), then
   **50° → 0° in 5° steps** of about 6.5 s, also passing through the
   degrees in between. About 230 s of pump in total; the manual says
   "approximately 2 minutes".
4. Pressure follows the pump's static curve (pressure against phase) up
   to the OPV. `Volume` and flow stay 0 until the OPV opens; then the
   water it lets through shows on the flow meter with marker `*` (17 ml
   in this run). How the curve looks is machine-specific, which is the
   point of the test (pump health, pump-menu values).
5. +238 s: `Phase` 180°, pressure → 0 within one report (the valve
   opens to relieve the pressure); one more report with `*`, then `-`.
   LCD: `Test pressure ✓`.

- Mode `CLD`, setpoint and `Pressure SP` `?`-masked, heat 0 % throughout;
  only `Phase` is live (unlike the rest of [cold mode](#cold-mode)). Its
  descending sweep from 120° is the program's mark in the report stream.
- LCD, third row of the run menu: `WAIT`, a progress bar growing about
  every 10–20 s (it ended at `████▋`, not full) and the live pressure
  (`…b`).
- Afterwards `MCb` returns 46 points (120°–50° every 2°, 45°–0° every
  5°); each value equals the pressure at the end of that step in the
  report stream. The data stays in EEPROM until the next test. The test
  also rewrites Pump › Press max, Press OPV, Phase OPV and Phase off:
  `levabak diff` of backups before and after showed no other change. The
  run menu shows `Test pressure ✓` / `Test flow -`: the suffix says
  whether a test has stored data (`MCf` returns nothing while the flow
  test has never run).
- `levalog runs` lists the whole program as one flush (230.6 s, peak at
  the OPV). Detection is issue #18; open: whether the test can run with
  PID1 on and what it then shows.

## Descale program (run menu)

Observed 2026-11-20 from the raw log with LCD sampling (500 ms every
5 s): cold machine in cold mode (mode `CLD`, 22 °C), no portafilter,
operator marks at the start, at each tray emptied and at the end. One
run, 27.6 min from the start to the return to the menu. The descale
profile used had no PID setpoint, cycle volume or wait time; Path GROUP,
Tray 250 ml, Purge 2000 ml.

1. Start: mode `CLD` → `PID`, **setpoint 0**, heat 0 % for the whole run
   (the profile's PID value; profiles with one would show it instead,
   not captured). `Pressure SP` `?????`, pressure 0.1–0.2 bar (open
   group) throughout.
2. Descaler, one tray at a time: pump about 20 s (marker `*`), off about
   20 s (LCD `PAUSE 00m:NNs` counting down), pump about 10 s until the
   tray volume is reached, then `EMPTY TRAY!`; the program waits for a
   key press, pump off. The LCD counts `DESCALER Nml` up. 9 trays, 19
   pulses here.
3. Tank empty (no flow during a pulse, here after about 2330 ml): the
   pulse ends early and the LCD asks `FILL TANK!`, waiting. `Volume` and
   the tick counter (`field_31`, below) reset to 0.
4. Purge: the same rhythm (16 pulses here), the LCD counting `PURGE Nml`
   **down** from the Purge volume. When the tank ran dry again it asked
   `FILL TANK!` and went on after the refill.
5. End: setpoint 0 → the brew setpoint, heater on. **Cold mode is not
   restored.** The LCD returns to Run › Descale.

- `Volume` counts on through all pulses of a phase (as in the warm-up
  program, unlike backflush) and reads `?` above 999 ml: the field is 3
  characters wide. It stays `?` until the next pump-start reset, so run
  volumes and wand draws in that stretch are lost (issue #67).
- Report field `field_31` (offset 155, width 5, XML unit `ticks`, no
  name in the XML) is the raw flow-meter tick counter: it equals
  `Volume` × `Impulses/l` / 1000 throughout and resets with it, but keeps
  counting above 999 ml.
- `levalog runs` lists the program as 35 flushes. In the report stream
  only setpoint 0 with mode `PID` marks it; the LCD names every phase.
  Detection is issue #15.
- A `levabak diff` of backups before and after showed no change in the
  `MCu` settings.

## Steam

- With the steam switch on, the boiler rises to about 120 °C while leva!'s
  heat output stays at 0 %: the steam thermostat drives the heater.
- Opening the wand to purge drops the pressure; steaming draws it down from
  about 1.9 bar while the temperature peaks and then falls.
- A cool-down (wand open, pump on) shows as a wand draw: volume grows with
  pump marker `-`.

## Eco

- A long press on the encoder (configured by the barista) switches to eco:
  mode field `ECO`, setpoint 91 → 79, LCD shows `ECO` in the setpoint
  column.

## Cold mode

- With PID1 control switched off (Setup › PID1 › Control, as the pressure
  test and flow meter calibration ask for), the mode field reads `CLD`.
  Raw log of 2026-11-13, cold machine at 21 °C, controller in the run
  menu: `CLD` for the whole 12-minute session. Temperatures stay live;
  the setpoint, all PID1/PID2 terms, `Pressure SP` and the phase fields
  are `?`-masked. The daemon reports power `cold`; it never switches power
  from there (what `MCa` does in cold mode is unverified).
- Exception: while the pressure test runs, `Phase` is live in `CLD` (the
  program's sweep, see below); the other masked fields stay masked.

## Valve test

- Run menu › Test valve (switches the grouphead valve three times, pump
  off) leaves no trace in the report stream. Raw log of 2026-11-13, cold
  mode, 25 s between operator marks: across 93 reports no byte changed
  except noise in `pressure_band_max` (0.01–0.05 bar, as in the rest of
  the session); pressure, volume and flow stayed 0, pump marker `-`.
- The five sampled LCD frames (500 ms every 5 s) all showed the Run menu,
  `key=0`. Whether the LCD shows the test at all is unconfirmed; the
  sampling may have missed it. The daemon cannot detect this program and
  does not misrecord it either (no pump run).

## Connection

- The banner is the idle heartbeat: while any client has a stream on, a new
  connection sees no banner at all (`levacap session-reset` stops a
  leftover stream).
- The first command can be swallowed even after the banner wait; `MC?` is
  retried.
- The machine's mains switch powers the whole unit, the leva! controller
  included: switched off, the machine is gone from the network (operator,
  2026-11-05). A disconnect is therefore the normal "off" state, not a
  fault.

## Settings dumps

- `MCm` replies in Intel HEX: 256 data records of 16 bytes, addresses
  0x0000–0x0FFF in order (4 KiB), every checksum valid, then the end
  record, all within about 1.1 s. leva! 3.2 sends the end record as
  `:00000001F`, one hex digit short of the standard `:00000001FF`. Seen
  in the 2026-11-02 command capture and in both daemon backups on
  2026-11-15 (257 lines each time). The idle banner follows right after.
  The daemon's completeness check accepts both forms of the end record.
- `MCu` replies with the settings menu tree as text, Latin-1 (`°` is byte
  0xB0), 1711 lines in the 2026-11-16 dump of leva! 3.2, ending in
  `Cancel`. Each line is `"| "` x depth followed by a 15-character cell,
  so its length is 15 + 2 x depth (15..25 seen). A cell whose trimmed text
  ends in `...` is a menu that opens the next depth; a menu may be empty
  (8 of them in that dump, e.g. a section with no stored presets). A cell
  with a label too long for the cell ends in `..` at full width and is a
  menu too (found by `levabak check` on a real backup). Other cells are
  settings: label left, value right-aligned, sometimes with no space in
  between (`Duration00m:03s`). 26 top-level sections, from `PID 1...` to
  `Cancel`. Evidence: two real backups of 2026-11-16 (before and after an
  autotune).
- **An `MCu` dump can lose lines mid-dump and still look whole.** The
  pre-autotune dump of 2026-11-16 lost about 153 lines: line 1094 glues
  `|    ` to `| | Time    04m:00s`, and the dump still ended in `Cancel`.
  The daemon used to accept any non-empty `MCu` reply; it now refuses a
  dump that fails `levabak check` (`ErrIncompleteDump`). `MCm` is checked
  as before.
- The daemon serves backups as `text/plain; charset=iso-8859-1`, the
  bodies byte-identical to the machine's reply.
- **EEPROM layout** (`MCm` image, little-endian; all from the pre/post
  autotune pair of 2026-11-16, the table lives in `backup/`; see
  `levabak layout`):

  | offset | type | scale | meaning | confidence and evidence |
  |---|---|---|---|---|
  | 0x0173 | u16 | 0.1 | PID 1 Kc (%/deg) | confirmed: matches MCu 8.9 -> 8.8 |
  | 0x0175 | u16 | 0.1 | PID 1 Ti (s) | confirmed: 67.2 -> 72.3 |
  | 0x0177 | u16 | 0.1 | PID 1 Td (s) | confirmed: 16.8 -> 18.1 |
  | 0x0179 | u16 | 0.1 | PID 1 windup limit (%) | likely: 200 = MCu 20.0 %, unchanged |
  | 0x0027 | u24 | 1 | flow-meter ticks, lifetime? | likely: +1006 = sum of `field_31` over runs 5-9 |
  | 0x002F | u24 | 1 | flow-meter ticks, copy or trip | likely: same values as 0x0027 |
  | 0x0033 | u16 | 1 | flow-meter ticks, resettable? | likely: +1006 |
  | 0x0022 | u8 | 1 | run counter? | guess: +5 = runs 5-9 |
  | 0x0037 | u8 | 1 | shot counter? | guess: +1 = one shot |
  | 0x04F9 | u8 | 1 | Profiles: Use (slot) | likely: 1 -> 3, MCu slot 1 -> 3 |

  0x0087 changed (cd -> a0) and is unknown. Counter widths (u8 vs u16,
  u24 vs u32) are not settled by one pair. `levabak check` cross-checks
  the MCu values `PID 1/Kc`, `PID 1/Ti`, `PID 1/Td` and `PID 1/WU limit`
  against these offsets. `Profiles/Use` is named in the layout but not
  compared (the MCu shows the profile name).

## Autotune

From the raw logs of three runs (2026-11-16, two on 2026-11-19), each
12.5-13.5 min, autotune config Safety 1.5, Power 30 %, Wait 2 min, Cycles 3:

- Report mode `ATN`. The setpoint shows 88 - Safety 1.5 = 86.5; PID terms,
  Kc/Ti/Td, windup and dT/dt are masked; temperature and heat stay live.
- Relay test with hysteresis: the heater switches on when the temperature
  falls to 86.3 (setpoint - 0.2) and off when it rises to 86.65
  (setpoint + 0.15), in every run.
- The first phase (heat 0 %) is not a fixed 2 min Wait: it lasted 126, 112
  and 108 s and always ended at the switch-on threshold.
- Then one pulse at Power 30 % (24.2 s in all three runs), then three
  cycles at 18 %: heater on 32-33 s, periods (on to on) 149-165 s,
  temperature 86.15-87.70 deg C. The swing after the 30 % pulse reaches
  88.4-88.5.
- The new constants follow from the cycles:
  - Kc = 0.6 Ku (Ziegler-Nichols), with Ku = 4 d / (pi a), d half the 18 %
    step and a half the temperature swing: 8.87 computed against 8.8-9.0
    stored.
  - Ti = 0.445 x the mean of the three periods in all three runs (within
    0.3 s; the textbook rule would be 0.5).
  - Td = Ti / 4.
- Run-to-run scatter: Ti 67.2-72.3 s (+-4 %), Kc 8.8-9.0.
- LCD: `PID 1 WAIT` blinking; a check mark after the 30 % pulse; a bar
  grows by `██` per cycle; after Cycles = 3 back to `PID`, new constants
  live (also in EEPROM: 0x0173..0x0177 above).
