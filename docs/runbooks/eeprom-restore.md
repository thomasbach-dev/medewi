# Runbook: restore an unedited backup by the vendor procedure

The one allowed contact with the bootloader port
([decision 0008](../decisions/0008-manual-restore-probe.md), issue #83).
The barista sends the machine the EEPROM image it already holds, with a
terminal program, and keeps a log of every byte. If all goes well no
setting changes; what we gain is the bootloader's real dialogue.

Plan 30 minutes in which nobody needs coffee. Read the whole runbook
once before the session, the section "When something goes wrong"
included.

## Safety rules (read first)

1. **A person does this, by hand.** No medewi tool and no agent connects
   to port 2323 or types into the session.
2. **Three keys only: RETURN, lower-case `e`, lower-case `q`.** Check
   that Caps Lock is off. The neighbours are destructive: `E` clears the
   EEPROM, `c` clears flash and EEPROM, `f` erases the firmware (ito
   module manual p. 34). Type nothing else, and paste nothing, into the
   bootloader session.
3. **Only the backup taken in this session.** Not an older one, and not
   a file from `levabak write --set`.
4. **The first byte resets the controller.** Heating stops, a running
   program ends, cold mode is lost. While the bootloader runs, nothing
   regulates the boiler as far as we know: if the machine heats or
   builds pressure in a way you do not like, switch it off at its power
   switch. That is always allowed and loses nothing: the image is on
   the laptop.
5. **Everything this session writes is private.** The backup and the
   wire log hold the license key. They stay in the gitignored
   `private/` directory.

## What we know, and from where

Two programs are involved, on the two chips of the ito module.

**The Wifi chip (ESP8266)** runs a firmware based on the open-source
[esp-link](https://github.com/jeelabs/esp-link). Port 2323 is esp-link's
"programming port". In the stock source (`serial/serbridge.c`), which
the vendor may have changed:

- Connecting does nothing. The first data received resets the
  controller, and the chip holds a "stay in the bootloader" signal for
  as long as the connection lives. That first data is not passed on.
- When the connection closes, the chip releases the signal and resets
  the controller once more. So closing the connection should bring the
  firmware back, whatever state the bootloader is in.
- Port 23 has a trap of the same kind: a connection whose first bytes
  are `0 ` (zero, space) or `?` and a line end also resets the
  controller. medewi's commands all start with `MC`.

**The controller (ATmega1284)** holds the vendor's own "ito bootloader".
It is closed: no source, and the chip is read-protected. All we have is
the ito manual (p. 31, 34):

- After the reset it prints

  ```
  Connected to AVR
  (q)uit or (c)lear (f)lash (e)eprom (r)eset
  (p)ass-through (b)ootloader (s)ignal
  ```

- A command is its letter; the manual says "enter f (and press
  RETURN)". Whether RETURN is needed is not known.
- `e` answers `Waiting for XMODEM-CRC transfer...` and a growing row of
  `C`. It takes "data in hex format (up to 4KB)" within 60 seconds.
- `q` leaves the bootloader; pressing the encoder does the same.
- "If a firmware upgrade fails, just repeat the process."
- The vendor's terminal settings: raw TCP (not Telnet), RETURN sends
  CR+LF, local echo on (so the bootloader does not echo).
- Transfers fail in the Wifi mode STA+AP; use AP or STA.

From the firmware manual: an image only fits the firmware version it
came from (p. 58), and a bad settings checksum resets all settings to
factory defaults (p. 234).

Not known from any source: what the bootloader does with the heater
outputs, whether it checks the image, and what it makes of our line
ends and the short end record.

Tried on a laptop against a local stand-in, not on the machine:

- `socat` and `picocom` send nothing when they connect; with the
  options below the Enter key sends CR+LF, like the vendor's terminal.
- `sx` sends 128-byte blocks with CRC and pads the last one with 0x1A,
  like the vendor's terminal program. A backup file is about 11.5 kB of
  text: 91 blocks.

## Prerequisites

- Machine on, idle, warmed up or not; nobody brewing, no program
  running. Do not start from cold mode if you want to keep it.
- The machine's Wifi mode is AP or STA, not STA+AP.
- Status Monitor and phone apps closed; no other client on port 23.
- The license key at hand on paper or in the vendor's mail, in case the
  settings have to be entered again.
- The laptop on mains power and on the same network as the machine,
  with a stable connection (not at the edge of the Wifi).
- In the repo, in `nix develop`, with `MEDEWI_MACHINE_ADDR` set
  (`echo $MEDEWI_MACHINE_ADDR`). The dev shell brings the terminal
  tools: `socat`, `picocom` and `sx` (from `lrzsz`).

  Open two terminals this way, both in the repo's root. Terminal 1 will
  hold the connection and the log, terminal 2 is where you type.

## 1. Fresh backup, checked

medewid still owns the machine for this step.

```sh
D=private/backups
medewictl backup --out "$D" --note pre-restore
levabak check "$D"/*_pre-restore.*
```

Go on only if `check` ends with `ok` and exit code 0. A damaged backup
is never restored: take a new one.

## 2. The file to send

The stored backup has LF line ends. A dump saved from a terminal, which
is what the vendor's procedure sends, has CRLF. Send a CRLF copy:

```sh
D=private/backups
W=private/restore-probe/$(date -u +%Y-%m-%dT%H%MZ)
SRC=$(ls "$D"/*_pre-restore.mcm.hex | tail -1)
mkdir -p "$W"
sed 's/$/\r/' "$SRC" > "$W/restore.hex"
levabak check "$W/restore.hex"
levabak diff "$SRC" "$W/restore.hex"
echo "$W"
```

`check` must say `ok`, and `diff` must say `no differences`. The last
line prints the session directory; the next steps find it by
themselves (the newest one).

## 3. Release the machine

```sh
medewictl release --until=+1h
```

From here on medewid is off the machine. The ops panel shows the
release.

## 4. Open the session

Terminal 1 holds the TCP connection and logs both directions with
timestamps. `W` is the session directory from step 2; check the path it
prints:

```sh
W=$(ls -d private/restore-probe/*/ | tail -1); W=${W%/}; echo "$W"
HOST=${MEDEWI_MACHINE_ADDR%:*}
socat -x -v PTY,link="$PWD/$W/tty",rawer TCP:"$HOST":2323 2> "$W/wire.log"
```

It prints nothing and stays in the foreground. It has sent nothing: the
controller still runs, the display is as before.

Terminal 2 is the keyboard:

```sh
W=$(ls -d private/restore-probe/*/ | tail -1); W=${W%/}; echo "$W"
cd "$W"
picocom --noinit --noreset --echo --omap crcrlf --send-cmd "sx -vv" --logfile screen.log tty
```

picocom says `Terminal ready`. Still nothing was sent. Its own keys:
`C-a C-s` sends a file, `C-a C-q` quits. Everything else you type goes
to the bootloader, and you see it because of `--echo`.

Note the wall-clock time of each step below on paper, and what the
machine does: beeps, display, pump, heating lamp.

## 5. Enter the bootloader

Press RETURN once.

Expected: the machine beeps, the display changes, and terminal 2 shows
`Connected to AVR` and the menu line `(q)uit or (c)lear (f)lash
(e)eprom (r)eset (p)ass-through (b)ootloader (s)ignal`.

If nothing appears within 10 s: press nothing more. Go to "No answer
after RETURN" below.

## 6. Send the image

1. Check Caps Lock. Type a lower-case `e`.
2. Expected: `Waiting for XMODEM-CRC transfer...`, then a growing row
   of `C`. If nothing comes within 3 s, press RETURN once (the manual
   says a command wants it) and note that it was needed.
3. Within 60 s, press `C-a C-s`. picocom asks `*** file:`. Type
   `restore.hex` and press Enter.
4. `sx` prints the blocks it sends and ends with `Transfer complete`.
   It takes a few seconds.

Do not type while the transfer runs.

If `sx` ends with `Transfer incomplete`, or no `C` ever appears: go to
"The transfer fails" below.

## 7. Leave the bootloader

Type a lower-case `q`. If nothing happens within 3 s, press RETURN
once.

Expected: the controller starts again; the display shows the normal
start-up. Note how long that takes.

Then quit picocom with `C-a C-q` and stop socat in terminal 1 with
`Ctrl-C`. Closing the connection resets the controller once more, if
the Wifi chip behaves like stock esp-link: note whether the machine
restarts a second time.

## 8. Read back

Watch the machine first: does it heat, which mode is it in, is it back
on the network? Then hand it back to medewid and compare:

```sh
D=private/backups
medewictl reclaim
medewictl backup --out "$D" --note post-restore
levabak check "$D"/*_post-restore.*
levabak diff "$D"/*_pre-restore.* "$D"/*_post-restore.*
```

`reclaim` can fail at first: the controller needs a moment, and medewid
retries every 30 s. Try the backup again after a minute.

Expected: `check` says `ok`; `diff` shows no `setting` row, and `eeprom`
rows only for the counters of the state block, or none at all. That is
the proof that the restore changed nothing.

If settings differ, or the machine shows factory defaults: go to "The
settings are gone" below.

## When something goes wrong

Stay calm in every case: the image and the `MCu` text are on the laptop,
and the power switch is always a safe move.

### No answer after RETURN

The byte may not have arrived, or the menu may be slow. Do not press
RETURN again and again. Wait a full minute, look at the machine. If the
display is unchanged, quit picocom (`C-a C-q`), stop socat, and check
the network (`ping`, then the banner on port 23 via `medewictl
reclaim`). Start again at step 3 once the machine answers on port 23.
If the display shows the bootloader but the terminal shows nothing, type
`q` once, quit, and note it: that is a finding too.

### The transfer fails

The bootloader should be back at its menu. Type `e` and send the file
once more. If it fails a second time, type `q` and go to step 8: the
read-back shows what the machine holds now. Do not try other files or
other tools in the same session.

### The settings are gone

A damaged image makes the firmware reset every setting to factory
defaults (firmware manual p. 234). The machine may then ask for its
license key.

1. Run the whole procedure again from step 3, with the same
   `restore.hex`. The bootloader does not live in the EEPROM, so it is
   still there.
2. If the second restore also ends in defaults: stop. Enter the license
   key at the machine, then the settings by hand, reading them from the
   backup: `levabak show "$D"/*_pre-restore.mcu.txt`. Keep both wire
   logs; they say what the bootloader did with the file.

### The controller does not come back after `q`

In this order, waiting a minute after each:

1. Press the encoder once: the manual says it leaves the bootloader
   like `q`.
2. Close the connection: quit picocom (`C-a C-q`) and stop socat. Stock
   esp-link then resets the controller into the firmware.
3. Switch the machine off at its power switch, wait ten seconds, switch
   it on.

If it starts normally, go to step 8. If the display stays dark or shows
the bootloader after all three, connect again (step 4), press RETURN,
and note what the terminal shows.

### A wrong key was typed

- `E`: the EEPROM is empty. Send the image again: `e`, then step 6.
- `r` or `R`: a reset. Wait, then start again at step 4.
- `c` or `f`: the firmware is gone. Do not improvise. The way back is
  the vendor's firmware update (ito manual p. 31–34) with the 3.2
  firmware file, then this restore. Stop the session and plan that
  calmly.
- Anything else: type nothing more, note the key and what the terminal
  shows, then `q`.

### The connection drops mid-session

socat ends, picocom reports the lost tty. Start again at step 4. If the
bootloader was already running, RETURN may only print the menu again.

## Afterwards

- Files in `private/restore-probe/<time>/`: `restore.hex`, `wire.log`
  (both directions, hex, timestamps), `screen.log` (what the terminal
  showed). Add your paper notes as `notes.txt`. Nothing of it is
  committed.
- The questions the session should answer (#83):
  - the bootloader's greeting, menu, prompts and line ends;
  - the timing: RETURN to menu, `e` to the first `C`, the pause between
    `C`s, the transfer, `q` to the display coming back, and to the first
    banner on port 23;
  - whether the counters and the test tables survive, and whether the
    bootloader checks the image at all;
  - what the machine does afterwards: heating, mode, Wifi.
- Review `wire.log` like a capture
  ([capture-session.md](capture-session.md), "Sanitization gate"). The
  answers go into `docs/machine-behaviour.md`; a fixture for the
  simulator gets a synthetic image in place of the real one.
