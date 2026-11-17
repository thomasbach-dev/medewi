package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/pflag"
)

// helpHead opens levabak --help: the synopsis and the subcommands.
const helpHead = `usage: levabak [--firmware V] SUBCOMMAND [OPTIONS] BACKUP ...

levabak reads leva! settings backups offline: the MCu dump (the settings
menu tree, *.mcu.txt) and the MCm dump (the firmware image with the EEPROM,
*.mcm.hex) that "medewictl backup --out DIR" writes. It never talks to the
machine.

  check   is the backup whole? problems with line numbers; exit 1 if any
  show    the settings as PATH and VALUE; --eeprom: the known EEPROM entries
  diff    A B: changed settings, then changed EEPROM bytes
  layout  the known EEPROM entries: offset, type, scale, name, confidence

levabak SUBCOMMAND --help prints that subcommand's part of this text and its flags.`

// helpShared is what every subcommand shares.
const helpShared = `BACKUP, for all subcommands, is a *.mcu.txt or *.mcm.hex file, or a
directory. The kind of a file comes from its content (an MCm line starts
with ":"), not only its name. A directory's files are paired into one backup
per pair: files with the same name stem TIMESTAMP_FIRMWARE[_NOTE] first;
for older backups without a shared stem, an MCu file and the MCm file
closest after it in time (by the file-name timestamp, same firmware, within
5 minutes). An unpaired file is a backup of one kind (a warning on stderr).
A directory with no backup files of its own is read through its
subdirectories, in name order. File names are
TIMESTAMP_FIRMWARE[_NOTE].mcu.txt or .mcm.hex, TIMESTAMP like
2026-11-10T094200Z (UTC); the optional _NOTE is its own output column.
Every row carries src, the MCu file's path (else the MCm file's), so show
over many backups is the long-term view of a setting.

--format (after the subcommand) is text (default: aligned columns with a
header), jsonl (one object per row; numbers are JSON numbers, an empty
cell is null) or csv (header and rows). The jsonl keys equal the csv
headers. Offsets print as 0x0173. All output is UTF-8 (the dumps are
Latin-1; the degree sign arrives as °).

Firmware comes from the file name. --firmware V, given BEFORE the
subcommand, overrides it for every backup. The EEPROM layout is known per
firmware (see layout); for an unknown firmware EEPROM entries have no names
(one warning on stderr), and diff still reports changed bytes raw.

Sensitive data: output is local, like the dumps it reads, and is NOT
redacted. A dump may hold the license key. Keep dumps and output out of the
repo and out of pasted text.

Warnings and problems go to stderr as "levabak: ..." lines.
Exit codes: 0 ok, 1 read error (and check finding a problem), 2 usage error.
diff exits 0 whether or not the backups differ.`

// subHelp is one subcommand's part of the help.
type subHelp struct {
	name     string
	text     string
	examples []string
}

// pointer is the line ending each SUBCOMMAND --help: the shared rules.
const pointer = "BACKUP forms and pairing, --format, --firmware (before the subcommand), the sensitive-data note and exit codes: levabak --help"

var subHelps = []subHelp{
	{
		name: "check",
		text: `usage: levabak [--firmware V] check [--format F] BACKUP ...

check asks of each backup: is it whole? It prints one row per problem:
src, note, line, kind, problem. kind is mcu (a line breaking the menu
rules: wrong width, missing Cancel, ...), mcm (a bad checksum, a gap, a
missing or misplaced end record) or pair (an MCu value that disagrees
with the same value in the EEPROM, checked when the firmware's layout is
known). line is the line in the file, empty for a whole-file problem. In
text format each backup also gets a verdict line at the end, with src as
the file path: "SRC: ok" or "SRC: damaged, N problems". Exit 1 if any
backup has a problem, else 0. A backup with only one kind of file is
checked as far as it goes, with a warning.`,
		examples: []string{
			"  levabak check private/backups/morning/                   # every backup in that directory",
			"  levabak check --format jsonl x.mcu.txt | jq -r .problem  # just the problem texts",
		},
	},
	{
		name: "show",
		text: `usage: levabak [--firmware V] show [--format F] [--eeprom] BACKUP ...

show lists the settings of each backup's MCu: one row per setting with
src, note, line, path, label, value, raw. path is the menu labels joined
with "/" (a label that repeats among siblings gets #2, #3); label and
value are a best-effort split of the 15-character cell, raw is the cell
itself, so a bad split never loses data. Menus (cells ending in "...")
are not listed.

With --eeprom it lists the known EEPROM entries of each backup's MCm
instead: src, note, offset, type (u8, u16, u24), raw (the integer), value
(raw times the entry's scale), name, confidence (confirmed, likely, guess).
Over many backups, show is the long-term view of one setting: filter the
rows by path.`,
		examples: []string{
			"  levabak show x.mcu.txt                                  # the settings of one backup",
			"  levabak show --eeprom --format csv private/backups/ | grep 0x0173  # Kc over time",
		},
	},
	{
		name: "diff",
		text: `usage: levabak [--firmware V] diff [--format F] A B

diff compares exactly two backups (after resolution: two files, two pairs,
or a directory holding two; anything else is a usage error that lists what
it found). Rows: section, key, a, b, note. Section setting: the changed
settings first, key is the path, a and b the values (empty: absent on that
side). Section eeprom: the changed bytes, key is the offset; known entries
first, with the layout's name in note and the decoded values, then the
unknown changed bytes as runs of hex. A setting in a damaged region of
a damaged backup (see check) is marked damaged in note instead of
being reported as added or removed. Damaged input still diffs, with a
warning. With no differences nothing is printed but a "no differences"
line on stderr. Exit 0 either way.`,
		examples: []string{
			"  levabak diff private/backups/2026-11-10 private/backups/2026-11-12  # two backup directories",
			"  levabak diff --format jsonl a.mcu.txt b.mcu.txt | jq -r '.key'     # changed paths",
		},
	},
	{
		name: "layout",
		text: `usage: levabak [--firmware V] layout [--format F] [BACKUP ...]

layout lists the known EEPROM entries: firmware, offset, type, scale (the
factor from raw to value), name, confidence and evidence (why we believe
it). Without BACKUP it lists the layout for --firmware, default 3.2. With
BACKUPs it lists the layout of each distinct firmware among them. A
firmware without a layout is an error (exit 1). These entries are what
show --eeprom reads and diff names.`,
		examples: []string{
			"  levabak layout                                          # the entries for firmware 3.2",
			"  levabak --firmware 3.2 layout --format csv > layout.csv",
		},
	},
}

// usage prints the whole help.
func usage(w io.Writer) {
	var b strings.Builder
	b.WriteString(helpHead + "\n\n" + helpShared + "\n")
	var ex []string
	for _, h := range subHelps {
		b.WriteString("\n" + h.text + "\n")
		ex = append(ex, h.examples...)
	}
	b.WriteString("\nExamples:\n" + strings.Join(ex, "\n") + "\n")
	_, _ = io.WriteString(w, b.String())
}

// subUsage prints the help of subcommand name: its part, its examples, its
// flags and a pointer to levabak --help.
func subUsage(w io.Writer, name string, fs *pflag.FlagSet) {
	for _, h := range subHelps {
		if h.name != name {
			continue
		}
		_, _ = fmt.Fprintf(w, "%s\n\nExamples:\n%s\n\n%s flags:\n", h.text, strings.Join(h.examples, "\n"), name)
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(w, "\n%s\n", pointer)
		return
	}
}
