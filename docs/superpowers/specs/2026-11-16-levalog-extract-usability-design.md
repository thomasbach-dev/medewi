# levalog extract: usability fixes — design

Follow-up to [2026-11-14-raw-log-fixtures-design.md](2026-11-14-raw-log-fixtures-design.md),
same branch (`backflush-fixture`), at the operator's request.

## Intent

Three agents (Haiku, Sonnet, Opus) with no prior knowledge did four
`levalog extract` tasks on a real day of raw logs (2026-11-15). All
finished; all lost time on the same points. The goal is that an agent
(or the operator) gets each task done without guessing, without hand
arithmetic, and without reading the Go source.

The findings, by time lost:

1. **Sessions are invisible.** The session-boundary refusal names only
   the first boundary (one failed run per boundary); `summary` lists
   files, not sessions; its `firmware:`/`xml:` lines describe the
   window, not the session.
2. **Marks are hard to find.** The help never says "mark"; marks are
   `note` events with text `mark: …`; there is no text filter.
3. **`--out -` is base64 and cannot be read back.** Counting frames
   needed `jq @base64d`; `levalog events -` fails.
4. **`--margin` only works with `--run`**: ±30 s around two marks is
   hand arithmetic.
5. **`SUBCOMMAND --help` reprints the whole ~110-line main help** before
   its own flags.
6. Small: an `--xml` file named differently from the session's XML warns
   once per raw file (31×); the "XML named only at …" note is UTC; the
   extract counts leave out the other events; the help example is the
   real backflush window.

## Goals

1. **Sessions.** One shared notion of a raw-log session (the inference
   rule of the raw-log spec: a file starting with the `xml:` note at
   t=0 continues the previous session; any other file starts one).
   - `extract`'s refusal lists **every** session boundary inside the
     window, in local time, and prints one ready-to-run `-s … -u …` pair
     per part.
   - `extract --split-sessions` with `--out DIR` writes one capture per
     session part into `DIR/01`, `DIR/02`, … (a parent of captures, which
     every subcommand already reads); refused with `--out -`.
   - `summary` on raw logs prints a **sessions** block: per session
     overlapping the input window its start (local), its first file and
     whether that file is in the input, firmware (the session's banner),
     XML name, and the part of the window it covers. The per-source
     `firmware:` and `xml:` lines say they are per file/window.
2. **Marks.** `events --match TEXT`: keep only events whose text
   contains TEXT, case-insensitive (note text, notice text, banner and
   reply lines, a frame's four rows, a report's raw line). The help names
   marks: "`medewictl mark TEXT` writes a note `mark: TEXT`; find marks
   with `levalog events --event note --match TEXT`".
3. **`--out -` and stdin.** The help says `data` is base64 (the fixture
   format) and shows the decode (`jq -r '.data | @base64d'`). `-` as a
   FILE argument reads a capture `log.jsonl` from stdin for `events`,
   `runs` and `summary` (no manifest, no wall time), so
   `levalog extract … --out - | levalog events --event frame -` works.
4. **`--margin` with `-s/-u`.** In `extract` and `events`, `--margin`
   also widens an explicit `-s/-u` window on both sides (and a run window
   as now).
5. **Help per subcommand.** `levalog SUBCOMMAND --help` prints that
   subcommand's synopsis, its paragraph(s), its flags and its examples,
   plus one line pointing to `levalog --help` for the shared input rules.
   `levalog --help` stays the full text.
6. Small:
   - an XML name mismatch warns once per distinct session XML name, not
     per file;
   - every time levalog prints is local time with zone offset (the "XML
     named only at" note included);
   - the extract counts line adds "N other";
   - help examples use neutral times (`-s 09:00 -u 09:05`), not the real
     backflush window.

## Non-goals

- Changing the fixture format, the raw-log format or the daemon.
- Program detection (#13).
- Changes to `--out -`'s content (it stays exactly `log.jsonl`'s lines).

## Anti-goals

- A refusal that hides how to proceed: every exit-1 message of `extract`
  says what to run next.
- `--match` that searches decoded report *values* (field names, units):
  it matches the raw text only, so its result is predictable.

## Decisions (my calls; correct me)

- `--split-sessions` numbering `01`, `02`, … in time order, two digits
  (more than 99 parts is refused).
- `--match` is a substring match, not a regex (agents reached for grep
  anyway; a substring is enough for marks and LCD texts).
- Stdin `-` is capture-only: a raw log needs its file name for wall time.

## Verification

After the fixes, run the same usability brief again with a fresh Haiku
agent and compare its friction list and command count with the first
run.
