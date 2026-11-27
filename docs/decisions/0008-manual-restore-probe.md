# 0008 — A person may restore an unedited backup by the vendor procedure

Date: 2026-11-27. Status: accepted.

## Context

[Decision 0007](0007-settings-write-mechanisms.md) explores the EEPROM
restore as a write path and says that no byte goes to the bootloader
before a record lifts the rule. Until now the rule had no exception:
nothing connects to port 2323 (`CLAUDE.md`, `docs/use-cases.md`,
`docs/deployment.md`, the capture runbook).

The offline half of the path exists: `levabak write` makes an image with
a fixed checksum, and `xmodem/` holds a sender.

Two programs sit behind port 2323. The Wifi chip runs a firmware based
on the open-source esp-link, whose source shows how its programming port
resets the controller on the first data and again when the connection
closes. The controller holds the vendor's own bootloader, which is
closed: the chip is read-protected and there is no source. For it we
have the manuals only (firmware manual p. 58, ito module manual
p. 31–34): the greeting, the menu, the prompt of the `e` command and
XMODEM CRC. Nobody here has seen its timing, its line ends, how it
treats the heater outputs, whether it checks an image, how long the
controller is away, or what the machine does afterwards. The runbook
lists what each source says. The simulator's bootloader (#85) and the
restore client (#86, #87) would be built on guesses.

The vendor documents a restore that a person does with a terminal
program. Doing exactly that once, with an image the machine already
holds, tests the procedure and nothing else: if it works, no setting
changes.

## Decision

1. The barista may restore an **unedited** backup with a terminal
   program, following
   [runbooks/eeprom-restore.md](../runbooks/eeprom-restore.md). This is
   the only allowed contact with port 2323.
2. The image is a backup of the same machine on the same firmware, taken
   in the same session and passed by `levabak check`. No image from
   `levabak write --set`, and no older backup.
3. The only keys sent to the bootloader are RETURN, `e` and `q`, plus
   the XMODEM transfer. The other bootloader commands erase the EEPROM
   or the firmware and are never typed.
4. The exception covers the runbook as written, including a repeat of
   the transfer when it fails, and a later repeat of the whole session
   if the first one leaves questions of #83 open.
5. No medewi code connects to port 2323, and no agent connects there or
   types into the terminal session. `leva.Dial` keeps refusing the port
   and the simulator keeps its tripwire.

Not decided here: a restore of an edited image, and a restore done by
medewid. Each needs its own record, written with what this probe shows
(#87).

## Consequences

- The wording of the rule changes where it said "nothing, ever":
  `CLAUDE.md`, the constraints in `docs/use-cases.md` and the capture
  runbook now name this exception. For code and agents the rule is
  unchanged.
- Risks we accept for this probe:
  - The first byte resets the controller: heating stops, a running
    program ends, cold mode is lost.
  - If the bootloader stores a damaged image, the firmware finds a bad
    checksum and resets every setting to factory defaults (firmware
    manual p. 234). The way back is the same restore again; the last
    resort is entering the settings by hand from the `MCu` dump. The
    license key is one of those settings.
  - A wrong key in the bootloader menu can erase the EEPROM or the
    firmware. The runbook has a section for each case.
  - The stored backups have LF line ends; a dump saved from a terminal,
    as the vendor describes, has CRLF. The runbook sends a CRLF copy.
    Whether the bootloader cares is not known.
- The session's log holds the image, and with it the license key. It
  stays in `private/`. A fixture made from it carries a synthetic image.
- After the session the answers go into `docs/machine-behaviour.md`, and
  #85 is unblocked.
