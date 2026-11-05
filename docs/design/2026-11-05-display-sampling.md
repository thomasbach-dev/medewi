# Machine READY, display sampling and marks in medewid — Design

Date: 2026-11-05. Status: agreed with the barista in conversation.

## Intent

- **Machine READY.** The machine announces its own whole-machine readiness
  with an unsolicited `[READY]` line in the stream (seen twice on
  2026-11-05, with the beeps and READY on the LCD). medewid turns it into a
  state, an event and a metric.
- **Display sampling.** Some state exists only on the LCD: the running
  program (`WARM-UP`, `FLUSH`) and its countdown, menus, pressed keys.
  medewid samples the LCD mirror between reports, so the raw traffic log
  holds everything a `levacap capture -scenario display` run records.
- **Marks** let the barista annotate the log from the phone or
  `medewictl`, replacing levacap's `m` + Enter.

For now, data to understand the machine beats complete brew data: sampling
also runs during brews. It is configurable and can be turned off.

## Machine READY

- `leva.Conn` routes bracketed lines (`[READY]`) to a `Notices` channel in
  every mode; they never count as command replies. The raw traffic log
  keeps short bracketed lines.
- `/api/status` gains `machine_ready`: set by `[READY]`, cleared on
  eco/standby and on disconnect. It is distinct from the existing `ready`
  (boiler at temperature, `watch.Detector`), which comes minutes earlier.
- An event `MACHINE_READY` runs the event hook; a gauge
  `medewi_machine_ready` follows the state. Other notices are logged.

## Display sampling

- Every `DisplayEvery` (default 5 s) the daemon sends `MC@`, waits
  `DisplayFor` (default 500 ms) and sends `MCr`. `DisplayEvery` 0 disables
  sampling (flags `-display-every`, `-display-for`; env
  `MEDEWID_DISPLAY_EVERY`, `MEDEWID_DISPLAY_FOR`).
- Cost: about 10 % of the report lines, also during shots (the brew
  recorder tolerates a 0.5 s gap; its thresholds are 2 s and more). The
  stream setting is global, so other clients (the phone's Status Monitor)
  see frames instead of reports during a sample.
- The switch commands run on the command worker, so they never interleave
  with a power change or a settings dump.
- Frames reach the daemon through `leva.Conn.DisplayFrames`, fed only
  between `StartDisplay` and the next `StartReports`/`StopReports`;
  `leva.ParseFrame` decodes them. `/api/status` gains `display`: the four
  rows of the latest frame and its time.

## Marks

- `POST /api/mark` with `{"text": "…"}` (1–200 bytes, one line) writes a
  `note` event `mark: …` into the current raw log session. No raw log
  (disabled or not connected): 409. Same protections as `/api/power` (no
  cross-origin, JSON only).
- `medewictl mark TEXT`, and a mark field with a button on the phone panel.

## Not in scope

- Program detection (issues #12–#18).
- The simulator's `MC@` stays a canned reply; it does not stop the report
  replay as the real machine does.
