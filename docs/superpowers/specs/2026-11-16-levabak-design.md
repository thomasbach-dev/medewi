# levabak: settings backup investigation — design

Builds on UC11 in [use-cases.md](../../use-cases.md) (machine settings
visibility) and follows [cli-conventions.md](../../cli-conventions.md).
Sibling of `levalog`: levalog is for traffic over time, levabak is for
settings snapshots.

## Intent

We are still getting to know the machine. `levalog`/`levasim`/`levacap`
turned traffic into knowledge; `levabak` does the same for the firmware's
settings backups (`medewictl backup` writes one `*.mcu.txt` menu dump and
one `*.mcm.hex` EEPROM image). Its users are **agents** first, the
operator second, exactly as with levalog.

The use cases it serves now or prepares:

1. Verify-after-write for future medewi settings writes.
2. Wear over months: compare counters across old backups.
3. After a calibration program (or autotune): what changed?
4. How a profile was set up back then (later: the database).
5. medewi telling pre-infusion from shot from the profile's setup (later).
6. Is a backup usable at all — before it is ever needed for a restore.
7. Firmware flashes: settings kept, reset or migrated?
8. Learning the EEPROM layout from diffs with a known cause.

**First and most important: is a backup usable (6).** On 2026-11-16 the
pre-autotune MCu dump lost ~153 lines mid-dump (line 1094 glues
`|    ` to `| | Time    04m:00s`); it still ended in `Cancel` and looked
whole. The daemon only checks MCm (`daemon/commands.go`, `complete`);
for MCu it accepts any non-empty reply.

## Goals

1. **`backup/` package** (pure Go, no machine I/O): parse an MCu dump into
   a menu tree, parse an MCm dump into a 4 KiB image, check both for
   damage, diff two backups, and hold the known EEPROM layout.
2. **`cmd/levabak`** with subcommands:
   - `check` — is this backup whole? Every problem with its line number.
   - `show` — the settings as `PATH = VALUE` rows; with `--eeprom` the
     known EEPROM entries decoded.
   - `diff A B` — changed settings, then changed EEPROM bytes, known ones
     decoded and named, unknown ones raw.
   - `layout` — the known EEPROM entries (offset, type, scale, name,
     confidence, evidence), like `levalog fields`.
3. `--format text|jsonl|csv` on every subcommand, stable keys and
   columns, as in levalog.
4. **The daemon refuses a damaged MCu dump** with `ErrIncompleteDump`,
   using the same check, so `medewictl backup` fails loudly instead of
   writing a bad file. MCm's existing check moves into `backup/`.
5. The 2026-11-16 findings go into
   [machine-behaviour.md](../../machine-behaviour.md): MCu format,
   the observed damage, the EEPROM offsets found, and what autotune
   looks like in the traffic (below).
6. **`medewictl backup --note TEXT`** (operator's request): the note goes
   into both file names, `2026-11-16T112555Z_3.2_post-autotune.mcu.txt`
   and `….mcm.hex`, so one flat backup directory replaces a subdirectory
   per backup. Both files of one run get **one** timestamp (taken once,
   not per fetch), so a pair is identified by name. The note is
   sanitized like the firmware part (letters, digits, `.`, `-`, `_`);
   the firmware part maps unsafe characters to `-` instead of `_`, so
   the name splits unambiguously: `TIMESTAMP_FIRMWARE[_NOTE].KIND`.
7. **The backups' charset**: `GET /api/backup/mcu` (and `mcm`) say
   `text/plain; charset=iso-8859-1`, the truth (`daemon/api.go` says
   utf-8 today). The body stays byte-identical to the machine's reply: a
   backup is raw.
8. **Usability run** as the final test (below).

## Non-goals

- No database, no data model. Backups stay files.
- No writing to the machine, no restore (restore is the bootloader on
  port 2323: never).
- No fixtures from real dumps in the repo: the license key's location in
  the dumps is unknown, so real dumps cannot be "cleaned" with
  confidence. Tests use synthetic dumps.
- No full EEPROM map. The layout table holds what evidence supports.

## Anti-goals

- A `check` that says "ok" to a damaged dump (the case that started
  this). When in doubt, report a problem.
- Committing vendor-authored content: the menu labels in tests are
  invented (`Alpha...`, `| Gain     1.0%`); real labels only appear at
  runtime from the user's own files. The layout table's names are our
  own words for our own findings. Exception, approved by the operator:
  the layout table may carry the short MCu path of the setting an offset
  mirrors (`PID 1/Kc`, `PID 1/Ti`, `PID 1/Td`, `PID 1/WU limit`,
  `Profiles/Use`) so the MCu–MCm cross-check works; full paths, since
  labels like `Kc` repeat deeper in the tree. The cross-check's tests
  use a test layout with invented paths.
- Columns or keys that shift between rows or runs.
- Hiding where a value came from: every row can carry its file and line
  (MCu) or address (MCm).

## Data facts the design rests on (firmware 3.2, two real backups)

- **MCu** is Latin-1 (`°` is byte 0xB0; the daemon labels it utf-8 in
  its HTTP header, wrongly). 1711 lines in the whole dump. Each line is
  `"| "` × depth followed by a 15-character cell, so lengths are
  15 + 2·depth (15…25 seen). A cell ending in `...` (after trimming) is a
  menu that opens the next depth; other cells are settings: label left,
  value right-aligned, sometimes without a space between
  (`Duration00m:03s`). Top level: 26 sections from `PID 1...` to the
  last line `Cancel`.
- **MCm**: 256 data records of 16 bytes, 0x0000–0x0FFF, then
  `:00000001F` (one digit short). Multi-byte values are little-endian.
- **Known offsets** (all from the pre/post-autotune pair):

  | offset | type | scale | meaning | confidence |
  |---|---|---|---|---|
  | 0x0173 | u16 | 0.1 | PID 1 Kc (%/°) | confirmed (matches MCu 8.9 → 8.8) |
  | 0x0175 | u16 | 0.1 | PID 1 Ti (s) | confirmed (67.2 → 72.3) |
  | 0x0177 | u16 | 0.1 | PID 1 Td (s) | confirmed (16.8 → 18.1) |
  | 0x0179 | u16 | 0.1 | PID 1 windup limit (%) | likely (200 = MCu 20.0 %, unchanged) |
  | 0x0027 | u24 | 1 | flow-meter ticks, lifetime? | likely (+1006 = sum of field_31 over runs 5–9) |
  | 0x002F | u24 | 1 | flow-meter ticks, copy or trip | likely (same values as 0x0027) |
  | 0x0033 | u16 | 1 | flow-meter ticks, resettable? | likely (+1006) |
  | 0x0022 | u8 | 1 | run counter? | guess (+5 = runs 5–9) |
  | 0x0037 | u8 | 1 | shot counter? | guess (+1 = one shot) |
  | 0x04F9 | u8 | 1 | Profiles: Use (slot) | likely (1 → 3, MCu SOAK 1.0b → DARK) |

  0x0087 changed (cd → a0) and is unknown. Widths of the counters
  (u8 vs u16, u24 vs u32) are not settled by one pair; the table records
  that as the confidence.

## CLI

```
levabak [--firmware V] SUBCOMMAND [OPTIONS] BACKUP ...

  check   is the backup whole? problems with line numbers; exit 1 if any
  show    the settings as PATH = VALUE; --eeprom: known EEPROM entries
  diff    A B: changed settings, then changed EEPROM bytes
  layout  the known EEPROM entries: offset, type, scale, name, confidence
```

- **BACKUP** is a `*.mcu.txt` or `*.mcm.hex` file, or a directory.
  `medewictl backup --out DIR` adds a pair per run, each file named at
  its own fetch (seconds apart), so a directory may hold many pairs. A
  directory's files are paired as **one backup per pair**: files with
  the same name stem (`TIMESTAMP_FIRMWARE[_NOTE]`) first; for older
  backups without a shared stem, an MCu file and the MCm file closest
  after it in time (by the file-name timestamp, same firmware, within
  5 min); an unpaired file is a backup of one kind (one warning). The
  note, if any, is its own output column (`note`) next to `src`. Directories whose subdirectories hold backups (the
  operator's `private/backups/<label>/` layout) are read in name order,
  as levalog reads a parent of captures. Every row carries `src`, so
  `show` over many backups is the long-term view (use case 2). `diff`
  needs exactly two backups after resolution, else usage error listing
  what it found. The kind of a file comes from its content (an MCm line
  starts with `:`), not only its name.
- **Sensitive data**: levabak's output is local, like the dumps it reads
  (the operator's rule: data with the instance or in `private/` may be
  sensitive; the repo, the raw log and committed fixtures may not). So
  levabak prints values unredacted; the usability run's logs stay
  outside the repo.
- **Firmware** comes from the file name (`…_3.2.mcu.txt`); `--firmware`
  overrides. The layout table is per firmware; an unknown firmware gets
  no EEPROM names (one warning), raw bytes still diff.
- **Paths**: menu labels joined with `/`, trailing `...` and padding
  removed: `PID 1/Ramp&Soak/Warm-Up/PID/Kc`. A label that repeats among
  siblings gets `#2`, `#3`. The label/value split is best effort; the raw
  cell is always available (`raw` column) so a bad split never loses data.
- **diff** pairs settings by path. Damaged input (failing `check`) still
  diffs, with a warning, and marks the paths in the damaged region as
  `damaged` instead of reporting them as added or removed.
- **Output columns** (jsonl keys equal csv headers):
  - `check`: `src note line kind problem` (kind `mcu`/`mcm`); text ends
    with one verdict line per file.
  - `show`: `src note line path label value raw`; `show --eeprom`:
    `src note offset type raw value name confidence`.
  - `diff`: `section key a b note`, section `setting` or `eeprom`; for
    eeprom, key is the offset and note the layout name (or empty).
  - `layout`: `firmware offset type scale name confidence evidence`.
- **Exit codes**: 0 ok; 1 read error, and `check` finding a problem;
  2 usage. `diff` exits 0 whether or not things differ.
- `--help` per subcommand, as levalog does since the usability fixes.

## The check

MCu (each a problem with line number):

- a line whose length is not 15 + 2·depth, or whose `| ` prefix is not
  well formed;
- a depth jump of more than one, or deeper than the previous line
  without that line being a menu (`...`);
- a menu with no children;
- first line not at depth 0, last line not `Cancel` at depth 0;
- non-Latin-1-printable bytes;
- with the layout known for the firmware: MCu values that contradict the
  same backup's MCm entries (e.g. Kc) — a mismatched pair.

The two real dumps are the acceptance case: post passes, pre fails at
line 1094. A gated test reads `MEDEWI_BACKUP_DIR` (skips when unset, like
`MEDEWI_XML_PATH`); the operator's copies are in the main checkout's
`private/backups/` (gitignored, so not in a worktree), the raw logs of
the day in a temp directory the operator names. Because the daemon will
refuse every MCu dump that fails these rules, the daemon change lands
only after the gated test has passed on both real dumps.

MCm: the existing rules (contiguous from 0, checksums, end record last),
reported per line instead of a bare false.

## Autotune in the traffic (for machine-behaviour.md)

From the 2026-11-16 raw logs, 08:08:57–08:22:30:

- Report mode `ATN`; setpoint shows 88 − Safety 1.5 = 86.5; PID terms,
  Kc/Ti/Td, windup and dT/dt masked; temperature and heat live.
- Relay test: heat 0 % for the Wait (2 min), one pulse at Power 30 %,
  then three cycles at 18 %, ~160 s apart, temperature ~86.2–88.4 °C.
- LCD: `PID 1 WAIT` blinking; `✓` after the 30 % pulse; a bar grows by
  `██` per cycle; after Cycles = 3 back to `PID`, new constants live.

## Usability run (final test)

Three fresh agents — Haiku, Sonnet, Opus — each with its own OUTDIR, the
real backups copied outside the repo, the tool only via `--help` and the
repo's docs (reading the Go source counts as friction). Each logs every
command, its exit code, what it expected and what it wished for. Tasks:

1. Which of these backups can be trusted for a restore? For the damaged
   one: where is it damaged and which settings are affected?
2. What did the autotune run change? Settings and EEPROM.
3. How much water has gone through the machine in its lifetime, in
   litres? (Needs the layout table's confidence and a calibration from
   the raw logs: a test of whether `layout` explains itself.)
4. Which pressure profile was in use in each backup, and how are its
   pre-infusion and shot points set up?
5. Produce a CSV of all PID 1 settings of the newer backup for a
   spreadsheet.

Then fix the friction, re-run Haiku, compare commands and stumbles.

## Decisions (my calls; correct me)

- `jsonl` rather than one JSON document, matching levalog; one object per
  row is what `jq` and agents handle best.
- `check` exits 1 on a problem (scripts and the daemon test on it).
- Paths use `/` and `#N` for repeated labels.
- The daemon change (goal 4) is in this branch: it is small and it is the
  point of "a backup must be usable". So are goals 6 and 7 (operator).

## Left to the builder

Package internals and type names; the exact label/value split heuristic;
text-format layout; how the layout table is stored in code (one table per
firmware, `3.2` only for now).
