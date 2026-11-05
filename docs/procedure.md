# Reference procedure: cappuccino on the single-boiler reference machine

The barista's actual routine, step by step, with the telemetry signature each
step leaves in the report stream. This is the ground truth the guided mode
(UC1) and the daemon's notifications are designed against. The first capture
session (2026-11-02) recorded a full run of this procedure with deliberate
pauses between steps.

| # | Step | Observable signature |
|---|------|----------------------|
| 1 | Power on, let the machine heat up. The firmware signals READY on the display and with a beep-beep-beep. | Warmup ramp; temperature reaches and holds the setpoint. Minutes later the machine sends an unsolicited `[READY]` line (captured 2026-11-05); see [machine-behaviour.md](machine-behaviour.md#ready). |
| 2 | Flush into the cups. | Short pump burst: flow > 0 at low pressure (open group). |
| 3 | Coffee from the freezer, a few water drops on the beans, grind, tamp. | Silence (machine idle at setpoint). |
| 4 | Pull the shot, with manual adjustments (encoder) as needed. | The full profile curve: preinfusion soak ~1 bar, ramp to ~8–9 bar, programmed decline to the 4-bar hold; intra-shot temperature droop ~4 °C; ends with flow→0 and the stop transient. |
| 5 | Press the steam button. | Temperature climbs with PID1 output at 0 % — the hardware steam-thermostat branch is heating, not the PID. Unambiguous steam-mode marker. |
| 6 | Lock out the portafilter, flush, wipe it clean. | Pump burst during the climbing steam ramp. |
| 7 | Prepare the milk while the machine heats. | Ramp continues. |
| 8 | Once ≥ ~100 °C, crack the steam wand briefly to purge condensed water. | Small temperature dip/no pump activity. |
| 9 | In the ~113–119 °C window, steam the milk; toggle the steam switch off toward the end. | Plateau then slow decline while steam is drawn (measured: peak ~119.5 °C at the sensor). **Left unattended, the thermostat regime overshoots to ~130 °C at the sensor — too hot to steam well.** This is the guided mode's key notification: "steam window now". |
| 10 | Clean the wand, open it to vent remaining steam. | Temperature drops faster. |
| 11 | Hold the manual pump switch until the boiler is refilled (wand still open). | Pump run with open outlet: flow without brew-like pressure; temperature falls as cold water enters. |
| 12 | Close the wand, switch to eco. | Setpoint drops (measured: 91 → 79 °C) — clean in-band power-state marker. |

Notes for the guided/regular flow:

- The regular's known failure mode is step 9's timing: pressing steam and
  walking away means returning to an overshot (~130 °C) boiler and a
  frustrating steaming experience. A "steam window now" notification (or,
  later, actual steam-temperature capping — see the PID2 evaluation issue)
  addresses exactly this.
- The ready signal (step 1) doubles as the barista's desktop notification:
  "machine is warm".
- Steps 2, 6, 8, 10, 11 are water-handling steps a guided UI can show as
  checklist items; steps 4 and 9 are where telemetry feedback matters.
