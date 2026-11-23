# Program detection in levalog — design

Issues #12–#15, #17, #18, #52 (and #42, #64, #65 as consumers). Status:
approved 2026-11-23.

## Intent

Maintenance history and wear tracking (UC14): which program ran when, did
it finish, and what did it show about the machine. This is step 1 of 3: a
shared detector, and `levalog programs` on top of it. It must give the
same answer for old logs as for new ones.

Later steps, not designed here: (2) `medewid` and `levacap` write settings
notes into the log; (3) `medewid` runs the detector live and stores the
results (#12, #42).

## Goals

1. Package `program/`: pure (no I/O, no clock), fed decoded report samples,
   LCD frames, gaps and optional settings. It returns program runs.
2. `levalog programs`: one row per program run; text, jsonl and csv.
3. `levalog events --program N`: the window of one run, like `--run N`.
4. `--settings BACKUP` (an `MCu` dump) refines results. Never required.
5. Correct `docs/machine-behaviour.md` where this work showed it wrong
   (below).

## Non-goals

- No daemon, database, brew-recorder or machine-contact change. Flush rows
  stay as they are, and so does `levalog runs`.
- No start or end marks in the raw log. Settings are observed; a program
  boundary is inferred, so a wrong one would stay in the log forever.
- No flow meter calibration (#16: no data) and no valve test (no trace).
- No wear verdicts. Rows carry figures; trends come later.
- No machine-specific settings in the repo: rules use firmware constants
  and test data is synthetic or an already committed fixture.

## Approaches considered

1. **Report stream only.** Simple; rules tied to settings break when they
   change.
2. **Stream plus backup settings.** Sees the planned program, but couples
   logs to backup files and needs a backup current at each run.
3. **LCD names, stream measures.** Independent of settings; depends on
   display sampling (about 10 % of the reports) and firmware wording.

**Chosen: stream rules that work alone, with settings as optional
enrichment** (the operator's model), and the LCD as a cross-check. Settings
enter the log as an observation in step 2, so logs become self-contained.

## Detection rules

A *pulse* is a stretch of pump marker `*`; a report gap inside it does not
split it while the marker is `*` on both sides and the gap is below 20 s.
A *continued* pulse is one where the flow-meter ticks (`field_31`) did not
fall below their last value before it. With that value 0 or masked,
continuation is unknown. Rules apply in this order:

| Kind | Core rule (no settings, no LCD) |
|---|---|
| autotune | mode `ATN`; start and end are the mode's |
| warm-up | setpoint ramp: at least 4 distinct setpoints within 20 s, in mode `PID`; starts at the jump that precedes the ramp |
| pressure test | one pulse, `Phase` falling, more than 8 distinct values |
| flow test | one pulse, `Phase` rising, more than 8 distinct values |
| backflush | at least 2 neighbouring pulses, `Phase` 0, `Pressure SP` masked, each reaching 1.0 bar in its second half |
| descale | continued pulses, `Phase` 0, `Pressure SP` masked, at least one not `CLD` pulse with setpoint 0 |
| purge | continued pulses, `Phase` 0, `Pressure SP` masked, and not descale |

- Shots (a `Pressure SP` in the pulse) and pulses matching nothing are not
  programs. They are not listed.
- 1.0 bar is the firmware's own backflush threshold (manual p. 37:
  `ERR:OPEN`). In the logs no flush exceeds 0.15 bar in its second half;
  every backflush pulse exceeds 10. The second half excludes pressure left
  from a shot.
- `Phase` 0: no pump power setting exists in the backflush and descale
  menus (manual pp. 194-199). A flush can be set to full power, so `Phase`
  0 alone identifies nothing.
- **Grouping.** Descale/purge pulses join by continuation, with no time
  limit (the `Wait` of a descale profile is configurable). A counter reset
  ends the group. It does not end a descale when the next group is a purge
  with setpoint 0: then the descale goes on with phase `purge`.
  Backflush pulses join up to 10 min apart (builder's call: covers `EMPTY`,
  which waits for a key press). Disconnect and source boundaries end a
  group as in `RunFinder`. Standby and a mode change also end it.
- The first pulse of a purge or descale is recognised in hindsight, when
  the second arrives. A single-pulse purge is not detected without the LCD.

### Known weak spots

Reported as `inferred`, not hidden: a hand-run series of blind-basket
flushes at full power looks like a backflush; a descale profile with a PID
temperature looks like a purge without the LCD; a purge in mode `PID`, the
descale `Wait`, `Path` WAND and tray `-` have never been captured.
Every rule was fitted to one or a few runs.

## Evidence and status

Each run has a `status`:

- `confirmed`: core rule matches, and the LCD or the settings agree.
- `inferred`: core rule only.
- `conflict`: the LCD or the settings contradict the rule. The row names
  both, and the rule's kind is kept.
- `lcd`: the LCD names a program the rules missed.

`evidence` lists what was used: `report`, `lcd`, `settings`. LCD words
(whole-row matching, menu selections excluded): `WARM-UP`, `DESCALER`,
`PURGE`, backflush `PUMP`/`SOAK`/`EMPTY`, and `WAIT` next to
`Test flow`/`Test pressure`. `FLUSH` on the LCD against a program rule is
a conflict. Missing frames never lower a status below `inferred`.

## Settings

`--settings` takes a backup as `levabak` does and runs `backup.CheckDump`
first; a damaged dump is refused (exit 2). It reads only `Warm-Up`,
`Backflush`, `Descale`, `Flush` and `Autotuning`.

It applies to every run in the input: the operator vouches that it was
current. Used for: expected pulse times (warm-up), cycles/soak/rinse
(backflush), tray, purge volume and PID of the profile in use (descale).
A profile temperature lets descale be told from purge without the LCD.

- Agreement raises the status to `confirmed`. A mismatch gives `conflict`
  plus a note `settings may be outdated`.
- Settings never create or suppress a run.
- Pump power is not compared: after a flow test the dump shows it as a
  flow rate (#73).

## Output

Columns: `program` (number), `kind`, `start`, `t`, `duration`, `outcome`,
`status`, `evidence`, `pulses`, `pump_s`, `volume_ml`, `peak_bar`,
`settings_match`, `notes`, `xml`, `src`.

- `outcome`: `finished` (the LCD or the stream shows the program's own
  end: the sweep's last step, the ramp's return, `ATN` left with new
  constants), `aborted` (known cut-off), `cut` (gap or end of input),
  else `unknown`. `aborted` is only given on evidence, since most aborts
  look like ends.
- jsonl adds `pulses_detail` (start, duration, volume, peak, phase per
  pulse) and kind-specific `figures`, starting with: autotune old and new
  Kc/Ti/Td and periods; pressure test pressure per step; flow test flow
  per step; backflush holding pressure per pulse and `EMPTY` leak-down;
  warm-up pulse schedule.
- `volume_ml` uses the per-pulse difference where the counter continues.
- Numbering, `-s`/`-u`, sources, exit codes and formats follow `runs` and
  cli-conventions. `rule_version` is in jsonl and the csv.

## Documentation fixes

- Backflush rinse: the blind filter **was in** (operator), as the manual
  says; the pressure sawtooth note is corrected accordingly.
- Warm-up's 21 s pulse: a scheduled pulse running into the Vent step
  (likely: it began as the boiler fell to the Vent reading).
- `Phase` distinguishes programs; the claims that only the LCD shows
  backflush and purge are corrected.
- Hypothesis: EEPROM 0x002F skips descale and purge water, 0x0027 counts
  it (two intervals fit, a third does not).
- Very short `Phase` 0 flushes just before a shot are probably aborted
  shots (two seen). Noted only; no classification change.

## Testing

TDD. Synthetic sample sequences per rule and per weak spot (full-power
flush, hot shot leftovers, gap inside a pulse, reset between descale and
purge, disconnect mid-program, masked ticks). Committed fixtures:
`backflush` gives one backflush and no other program; `descale-tail`,
cut mid-program, gives one program of the descale family;
`shot-genlever` and `steam-eco` give none.
No new fixtures from real runs: pressure test, flow test and autotune
recordings carry this machine's readings. Those kinds, purge and warm-up
are tested on synthetic sequences in CI and on the private logs locally.

Local acceptance (private logs, not in CI): at least the counts of
2026-11-06..22: 3 autotune, 1 pressure test, 1 flow test, 1 backflush,
1 descale (purge included) and 2 purge starts, and no program among the
136 LCD-labelled flushes and 64 shots. A fresh agent then answers
"which programs ran, and can I trust each?" using `--help` only.

## Left to the builder

File layout inside `program/`, exact LCD matchers, the sweep statistic,
and text column widths. Thresholds above are starting points: a change
needs a logged reason and a test.
