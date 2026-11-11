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
- `MCr` and `MC@` replace each other; the switch can cut the current line
  off without its CRLF and glue the next record onto it (report after
  report, frame after frame, report after frame). `leva` splits such lines
  and counts the cut-off part as a fragment.

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
4. After the program the manual asks to vent the boiler through the wand
   (air collects above boiling point).

Detection is issue #13; the data model for programs is issue #12.

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
