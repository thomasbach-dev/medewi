# Fixtures from the raw log: `levalog extract`, display replay in levasim

## Intent

Since 2026-11-06 the machine's traffic comes from medewid's raw log on
the brewhost, not from levacap captures. Operator marks (`medewictl
mark`) label what happened. The first program recorded this way is the
backflush of 2026-11-14 (marks "S Backflush", "Clicked", "F backflush";
see the "Backflush program" section of
[machine-behaviour.md](../../machine-behaviour.md), added by PR #55;
this branch is rebased onto main once #55 is merged). It should become a
committed fixture, made by proper tooling rather than a one-off script,
because each machine program of issues #12–#18 (warm-up, descale,
purge, autotune, …) will need one.

The fixture serves three uses at once, and each one shapes it:

- **Reference record:** the exact bytes as recorded, the daemon's
  `MC@`/`MCr` toggles, the marks, provenance in the manifest.
- **Test input for program detection (#13):** the whole program uncut,
  idle margins on both sides (the detector must say "no program" there),
  the LCD frames (only they name the phases), the marks as the right
  answers. Its nine pump pulses look like nine flushes to the current
  run detection, which is the trap a detector has to get past.
- **Replay in levasim:** a client (medewid, levacap) connected to the
  sim sees what the real machine sends: reports after `MCr`, LCD frames
  after `MC@`, never both at once.

## Goals

1. `levalog extract` cuts a time window out of the raw logs and writes a
   capture directory (`log.jsonl` + `manifest.json`), the same shape
   levacap writes.
2. levasim models the display mirror: `MCr` and `MC@` select what the
   replay emits, the last one wins, `MC ` stops both.
3. The committed-fixture guard (`conformance/real_fixtures_test.go`)
   accepts what a raw-log fixture contains: `MC@` sent, LCD frames,
   `[READY]` notices, note events.
4. `testdata/fixtures/backflush/`, made with goal 1 and reviewed per the
   capture-session runbook, with tests that use it.

## Non-goals

- Program detection itself (#13) and the program data model (#12).
- Copying raw logs off the brewhost: the operator does that (sudo); the
  runbook documents it.
- Editing, redacting or re-timing traffic. The raw log's allowlist
  already keeps `MCu`/`MCm` replies out; nothing else is removed.
- Extracting from captures (no wall time; a capture is already a
  fixture).

## Anti-goals

- An extracted fixture that differs from the recorded bytes in any way
  but the cut and the rebased `t`.
- levasim emitting reports and frames to the same client at the same
  time: no real machine does that.
- Changes to the replay of existing fixtures (no frames in them; their
  behaviour must stay byte-for-byte the same).
- Anything that sends to the machine: `extract` is offline.

## Approaches

**Where the extraction lives.**

- *A `levalog` subcommand (chosen).* levalog already reads raw logs,
  resolves `--since/--until` to files, selects runs (`--run`,
  `--margin`), knows the session's XML note and firmware. One more
  subcommand reuses all of that and follows docs/cli-conventions.md.
- *A levacap subcommand.* levacap is the tool that talks to the
  machine; mixing offline file work into it blurs that line.
- *A script (jq/Python).* Not reproducible, no tests, and would be
  rewritten for every program.

**How replay treats a log with frames** (Thomas picked option 1).

1. *Commit verbatim, levasim learns the display mirror (chosen).*
   Replaying the fixture gives a client the same alternation of reports,
   frames and gaps the daemon saw.
2. *Commit verbatim, levasim unchanged.* Replay would interleave frames
   into the report stream, which the machine never does.
3. *Strip frames from the fixture.* Loses the only signal that names the
   program phases; rejected by the detection use.

## Design

### `levalog extract`

```
levalog extract (-s T -u T | --run N [--margin SPAN] -s T -u T) \
    --out DIR|- [--scenario NAME] [--notes TEXT] [--dir RAWDIR] [FILE|DIR ...]
```

- Input: raw logs only (`*.jsonl.gz`), found like the other subcommands
  (`--dir`, or FILE/DIR arguments). A capture as input is a usage error
  (exit 2).
- Window: `--since`/`--until` as everywhere. With `--run N` (and
  optional `--margin`) the window is that run's window as `events --run`
  defines it; `-s`/`-u` then pick the input, as for `runs`.
- The cut: every event whose wall time lies in the window, verbatim. The
  first kept rx event must start a line: rx bytes before the first CRLF
  in the window (a line cut in half, or a piece of one) are dropped, and
  so is a trailing incomplete line at the end. tx and note events are
  kept as they are.
- One session only: a window that crosses a raw-log session boundary
  (daemon reconnect or restart; hourly rotation within one session is
  fine and is joined) is refused with exit 1 and a message naming the
  boundary's time.
- Session boundaries are inferred from the files (my call; no rawlog
  format change, which would need a redeploy and would not help the logs
  already recorded): rawlog writes nothing on disk at a session's end,
  but once `Session.SetXML` ran every file of the session starts with
  the `xml:` note at `t = 0`. A file that starts with the note continues
  the previous file's session; any other file starts a new session.
  Checked on the 2026-11-14 log: the session's first file (06:05:52 UTC)
  starts with the `$id:` banner, its rotated files (07:05, 08:05, …) with
  the note. A session whose `MC?` never yielded an XML has no notes, so
  each of its files counts as a session of its own: extract then refuses
  more windows than necessary, never too few.
- extract reads back from the window to the session's first file, among
  the input files, for the `$id:` banner (it comes only at connect,
  possibly hours before the window). If that file is not in the input,
  `firmware_version` stays empty and stderr says which file is missing.
  The runbook's copy step therefore collects the whole day, not just the
  window's hours.
- `t`: rebased so the first kept event is `t = 0`, offsets between
  events unchanged, across rotated files too.
- First event: the session's XML note (`xml: <name>`) at `t = 0`, as
  every raw-log file starts with one, so the fixture names its XML.
- `manifest.json`:
  - `scenario`: `--scenario`, default `extract`;
  - `firmware_version`: the session's last `$id:` banner before or in the
    window (empty if none);
  - `xml_name`: from the XML note; `xml_sha256`: of the XML in effect
    (`--xml` / `MEDEWI_XML_PATH`), empty if none is given;
  - `machine_addr`: `machine.example:23`, always;
  - `notes`: `--notes`, followed by a provenance line `extracted from
    <file basename> <first wall time> – <last wall time> by levalog
    extract` (file basenames only, never directories);
  - `recorded`: the wall time of the last kept event, which is what
    levalog's START computation for captures assumes.
- Output: `--out DIR` must not exist or be empty; files are written to
  temp names and renamed, like levacap. No `--force`.
- `--out -` (for pipelines and quick looks, e.g. `| jq`, `| gzip`):
  writes exactly the lines `log.jsonl` would hold to stdout, and no
  manifest; stderr says so once ("no manifest with --out -"), plus the
  usual notes. Same `-` convention as `medewictl backup --out -`. Not
  meant for `ssh -t` (a pty turns LF into CRLF); fixtures are made with
  `--out DIR`.
- The marks and all other note events stay in; `levalog events` on the
  result shows them as it does for raw logs.
- With `--out DIR`, stdout: one line per file written plus the event,
  report and frame counts; stderr for notes (e.g. "window starts mid-line, dropped N
  bytes"). Exit 0/1/2 as in docs/cli-conventions.md.
- Help text and its examples grow an `extract` entry; the README and the
  capture-session runbook name it.

### levasim: display mirror

- The replay keeps one timeline over the whole stream, as now (`t`
  deltas / `Speed`, loop, `Once`).
- New mode state next to `reporting`: off, reports, display.
  `MCr` → reports, `MC@` → display, `MC ` → off. Switching between
  reports and display does not restart the timeline; it changes which
  rx events are emitted from then on.
- Classification by an rx event's first byte: `<` is a display frame,
  anything else is report-stream traffic (reports, `[READY]`, glued
  pieces). In display mode only frames are emitted; in reports mode only
  the rest. Events of the other class are skipped silently, keeping
  their time slot, so a replay to a client that alternates differently
  from the recording sees gaps, as the real machine would show
  different data.
- The banner heartbeat pauses in both modes, as now for reports.
- `MCR` resends the last item emitted.
- A stream with no `<` events behaves exactly as today: in display mode
  it emits nothing.
- No new flag: the mode comes from the commands, the frames from the
  fixture.

### Fixture guard

`TestCommittedFixturesShape` additionally accepts:

- tx `MC@`, and nothing else new (my call: the tx list stays a reviewed
  allowlist; a window with other commands, e.g. a power toggle, fails
  the guard and is re-cut or the list is widened in review). The
  backflush window sends only `MC@` and `MCr` (102 each).
- rx display frames: lines starting with `<`, 69 bytes long (the frame
  length in machine-behaviour.md), and notices matching `[…]`;
- note events, which are also checked against the leak strings.

Before classifying, the guard splits each CRLF-delimited rx line with
`leva.SplitLine`, as `leva.Conn` does, so a report or frame glued
onto a cut-off one at a stream switch is checked as its own record. A
cut-off piece is accepted as a fragment and counted; the test logs the
count. (The backflush window has none: 1824 whole reports, 507 whole
frames. Other windows will.)

Reports keep the checks they have (checksum and decode with the XML, `~`
at len−3).

### The backflush fixture

- `testdata/fixtures/backflush/`, cut from the raw log of 2026-11-14,
  window 08:58:30 – 09:07:00 local (about 1 MB): the menu scroll to the
  program at 08:59:15, the program, the menu exit at 09:06:36, about
  30 s of idle on either side.
- `--scenario backflush`, `--notes` describing the procedure: blind
  basket inserted, marks "S Backflush" at the start, "Clicked" at the key
  press on `EMPTY`, "F backflush" at the end.
- Reviewed per docs/runbooks/capture-session.md §3 before `git add`.
- Tests:
  - levasim: replaying it with `MC@` yields frames that include `EMPTY`;
    with `MCr` it yields reports and no frames.
  - With the XML (skipped in the sandbox): `levalog runs` on it finds 9
    flushes; the brew recorder sees 9 flushes, none truncated. These pin
    today's behaviour; #13 will change them on purpose.

### Docs

- docs/runbooks/capture-session.md: a section "Fixture from the raw
  log": copy the files off the brewhost (the sudo command), `levalog
  extract`, review, commit.
- machine-behaviour.md: the backflush section names the fixture.
- CLAUDE.md: the fixture list and the levasim `MC@` behaviour in the
  architecture paragraph.

## Left to the builder

- Package boundaries: where the cut, session and manifest logic lives
  (logview vs. a new small package), how much of `loadAll` it reuses.
- How the levasim mode interacts with `MC ` and the loop in detail, and
  `xml_sha256` when `--xml` names a directory.
- The exact stdout format of `extract`.
- Test names and the split into commits; TDD as always.
