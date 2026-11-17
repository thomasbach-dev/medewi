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
5 minutes). An unpaired file is a backup of one kind (a warning on stderr). Files named
as arguments are paired the same way, among themselves and within one
directory: "x.mcu.txt x.mcm.hex" is one backup, not two.
A directory with no backup files of its own is read through its
subdirectories, in name order. File names are
TIMESTAMP_FIRMWARE[_NOTE].mcu.txt or .mcm.hex, TIMESTAMP like
2026-11-10T094200Z (UTC); the optional _NOTE is its own output column.
Every row carries src, the MCu file's path (else the MCm file's), so show
over many backups is the long-term view of a setting. In text format (and
in every stderr warning) src is shortened: relative to the current
directory, or just the file name when that path would start with "..".
jsonl and csv keep the full path.

--format (after the subcommand) is text (default: aligned columns with a
header), jsonl (one object per row; numbers are JSON numbers, an empty
cell is null) or csv (header and rows). The jsonl keys equal the csv
headers. Text prints an empty cell as "·" (a middle dot; a real value can
be "-") and, with no rows, no header at all: use jsonl or csv for exact
values. Offsets print as 0x0173. All output is UTF-8 (the dumps are
Latin-1; the degree sign arrives as °).

show and diff take --path PREFIX: keep only rows whose path (diff: setting
key) starts with PREFIX, a plain case-sensitive string prefix. A trailing
"/" selects a whole menu: --path 'PID 1/'. With --eeprom (and in diff's
eeprom rows) the path is the one of the entry's layout MCuPath; unknown
byte runs have none and are dropped. If nothing matches, stdout stays
empty and stderr says: levabak: no rows match --path "PREFIX".

Firmware comes from the file name. --firmware V, given BEFORE the
subcommand, overrides it for every backup. The EEPROM layout is known per
firmware (see layout); for an unknown firmware EEPROM entries have no names
(one warning on stderr), and diff still reports changed bytes raw.

Sensitive data: output is local, like the dumps it reads, and is NOT
redacted. A dump may hold the license key. Keep dumps and output out of the
repo and out of pasted text.

Warnings and errors go to stderr as "levabak: ..." lines; check's
problems are rows on stdout.
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
known). line is the line in the file, empty for a whole-file problem.

After a backup's problem rows come its damaged regions, one row each
(kind mcu, line = the region's first line): "damaged region: lines F-T,
starting in section S; lines may be lost here and the settings in it may
sit under wrong paths". A lost stretch of lines can leave later settings
under the wrong parent, so show flags such settings damaged and diff
does not trust their paths. Region rows are not counted as problems.

In text format each backup also gets a verdict line at the end: "SRC: ok"
or "SRC: damaged, N problems", plus ", M damaged regions (lines F-T, ...)"
when there are regions. Exit 1 if any backup has a problem, else 0. A
backup with only one kind of file is checked as far as it goes, with a
warning.`,
		examples: []string{
			"  levabak check private/backups/morning/                   # every backup in that directory",
			"  levabak check DIR   # a damaged one ends: x.mcu.txt: damaged, 2 problems, 1 damaged regions (lines 5-6)",
			"  levabak check --format jsonl x.mcu.txt | jq -r .problem  # just the problem texts",
		},
	},
	{
		name: "show",
		text: `usage: levabak [--firmware V] show [--format F] [--path PREFIX] [--eeprom] BACKUP ...

show lists the settings of each backup's MCu: one row per setting with
src, note, line, path, label, value, number, unit, flag, raw. path is the
menu labels joined with "/" (a label that repeats among siblings gets #2,
#3); label and value are a best-effort split of the 15-character cell,
raw is the cell itself, so a bad split never loses data. number and unit
split the value ("88.0°" is 88.0 and "°"; a JSON number in jsonl); both
are empty when the value is not a plain number ("1:30"). flag is
"damaged" for a setting in a damaged region (see check), else empty.
Text omits number and unit, and flag when no printed row has one. For a
backup with damaged regions show warns on stderr once. Menus (cells
ending in "...") are not listed.

--path PREFIX keeps only the settings whose path starts with PREFIX; end
it with "/" for a whole menu (--path 'PID 1/'). Over many backups, show
is the long-term view of one setting.

With --eeprom it lists the known EEPROM entries of each backup's MCm
instead: src, note, offset, type (u8, u16, u24), raw (the integer), value
(raw times the entry's scale), name, confidence (confirmed, likely,
guess). Entries that lie past the end of the image are skipped. There
--path selects by the entry's MCu path (entries without one are dropped).`,
		examples: []string{
			"  levabak show x.mcu.txt                                  # the settings of one backup",
			"  levabak show --path 'PID 1/' --format csv BACKUP > pid1.csv  # one menu, as a spreadsheet",
			"  levabak show --eeprom --format csv private/backups/ | grep 0x0173  # Kc over time",
		},
	},
	{
		name: "diff",
		text: `usage: levabak [--firmware V] diff [--format F] [--path PREFIX] A B

diff compares exactly two backups (after resolution: two files, two pairs,
or a directory holding two; anything else is a usage error that lists what
it found). Rows: section, key, a, b, note. Section setting: first one row
per menu section that has settings on one side only (key "Label/", a or b
"N settings", note "section only in A" or "section only in B"; its paths
are not listed), then the changed settings, key is the path, a and b the
values (empty: absent on that side). --path PREFIX keeps only rows whose
key starts with PREFIX ("PID 1/" for one menu), a section row when
either path starts with the other, and eeprom rows of entries whose MCu
path starts with it. Section eeprom: the changed bytes, key is the offset; known entries
first, with the layout's name in note and the decoded values, then the
unknown changed bytes as runs of hex. A setting in a damaged region of
a damaged backup (see check) is marked damaged in note instead of
being reported as added or removed. Damaged input still diffs, with a
warning. With no differences nothing is printed but a "no differences"
line on stderr. If the two backups' firmware differs (file names), diff
warns and uses A's for the layout; --firmware (before the subcommand)
chooses the layout and also silences that warning. Exit 0 either way.`,
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
