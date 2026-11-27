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
*.mcm.hex) that "medewictl backup --out DIR" writes. write makes a new
MCm file from one, with edited entries. It never talks to the machine.

  check   is the backup whole? problems with line numbers; exit 1 if any
  show    the settings as PATH and VALUE; --eeprom: the known EEPROM entries
  diff    A B: changed settings, then changed EEPROM bytes
  layout  the known EEPROM entries: offset, type, scale, name, confidence
  write   a new MCm file from a backup's EEPROM image; --set edits entries

levabak SUBCOMMAND --help prints that subcommand's part of this text and
its flags.`

// helpShared is what every subcommand shares.
const helpShared = `BACKUP, for all subcommands, is a *.mcu.txt or *.mcm.hex file, or a
directory. The kind of a file comes from its content (an MCm line starts
with ":"), not only its name. A directory's files are paired into one backup
per pair: files with the same name stem TIMESTAMP_FIRMWARE[_NOTE] first;
for older backups without a shared stem, an MCu file and the MCm file
closest after it in time (by the file-name timestamp, same firmware, within
5 minutes). An unpaired file is a backup of one kind (a warning on
stderr). Files named as arguments are paired the same way, among
themselves and within one directory: "x.mcu.txt x.mcm.hex" is one backup,
not two.
A directory is also read through its subdirectories, always. The
backups from one directory argument come ordered by the time in their file
names (stable; undated ones last), so "diff DIR" takes the older as A.
File names are
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
Exit codes: 0 ok, 1 read error (and check finding a problem, write
refusing an edit or a file), 2 usage error.
diff exits 0 whether or not the backups differ.`

// subHelp is one subcommand's part of the help.
type subHelp struct {
	name     string
	text     string
	examples []string
}

// pointer is the line ending each SUBCOMMAND --help: the shared rules.
const pointer = `BACKUP forms and pairing, --format, --firmware (before the
subcommand), the sensitive-data note and exit codes: levabak --help`

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
			"  levabak check DIR   # a damaged one ends: x.mcu.txt: damaged, 2 problems, 1 damaged region (lines 5-6)",
			"  levabak check --format jsonl x.mcu.txt | jq -r .problem  # just the problem texts",
		},
	},
	{
		name: "show",
		text: `usage: levabak [--firmware V] show [--format F] [--path PREFIX]
             [--eeprom] BACKUP ...

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
guess). Entries that lie past the end of the image are skipped. The
counters and the current setpoints sit in a state block that the
firmware moves: its entries are read where the block is in that image
(so their offsets differ between backups) and are left out when the
image holds no such block. There
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
"N settings", note "section only in A" or "section only in B", plus
" (damaged)" when either backup is damaged; its paths are not listed;
top-level leaves like Cancel are no sections), then the changed
settings, key is the path, a and b the values (empty: absent on that
side). --path PREFIX keeps only rows whose
key starts with PREFIX ("PID 1/" for one menu), a section row when
either path starts with the other, and eeprom rows of entries whose MCu
path starts with it. Section eeprom: the changed bytes, key is the
offset; known entries first, with the layout's name in note and the
decoded values, then the unknown changed bytes as runs of hex. The state
block (counters, current setpoints) is compared by meaning: if it sits at
different places in A and B, the first eeprom row says so (note "state
block moved", a and b its offsets), and the block's rows have B's offset
as key and "at 0x.... in A" in note. A setting
in a damaged region of a damaged backup (see check) is marked damaged in
note instead of being reported as added or removed; with any damage a
path on one side only is "damaged; only in A" or "damaged; only in B".
Damaged input still diffs, with a warning. With no differences nothing
is printed but a "no differences" line on stderr. If the two backups'
firmware differs (file names), diff warns and uses A's for the layout;
--firmware (before the subcommand) chooses the layout and also silences
that warning. Exit 0 either way.`,
		examples: []string{
			"  levabak diff \"$D\"/*_pre-x.* \"$D\"/*_post-x.*   # two pairs, A = pre",
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
show --eeprom reads and diff names. The entries of the state block come
first; the block moves, so their offset counts from its start
("state+0x06").`,
		examples: []string{
			"  levabak layout                                          # the entries for firmware 3.2",
			"  levabak --firmware 3.2 layout --format csv > layout.csv",
		},
	},
	{
		name: "write",
		text: `usage: levabak [--firmware V] write --out FILE [--set PATH=VALUE ...]
             [--standard-eof] [--force] [--format F] BACKUP

write takes the EEPROM image of exactly one backup (its MCm file) and
writes it as a new MCm file to --out FILE. It writes that file and
nothing else: no byte goes to the machine, and putting an image on the
machine is not levabak's job. Without --set the new file equals the
source byte for byte (16-byte records, LF line ends, the firmware's end
record ":00000001F"); --standard-eof ends it with the Intel HEX end
record ":00000001FF" instead.

--set PATH=VALUE (repeatable) sets the EEPROM entry that belongs to an
MCu path, as show --eeprom --path PATH lists it, and fixes the settings
checksum (it XORs the change into the checksum byte). VALUE is a number
in the entry's unit; a unit after it is ignored ("3.3b"), and "-" means
not set where the entry knows that (profile points). The result differs
from the source only in the entries' bytes and the checksum byte; write
prints these changes like diff does (section, key, a, b, note).

write refuses (exit 1, no file written): a path without an EEPROM
entry, an entry of confidence guess, a value that is no multiple of the
entry's scale, is negative or does not fit, a change to a byte outside
the region the checksum is known to cover (the state block with the
current setpoints lies outside), a damaged MCm dump, and an existing
--out file unless --force. It does not check that a value makes sense
to the firmware (a range, a profile point with a time but no pressure):
compare with show --eeprom of a backup where the menu set such a value.

The image holds everything the source holds, the license key included:
keep FILE out of the repo. It is created with mode 0600. Name it like a
backup (TIMESTAMP_FIRMWARE_NOTE.mcm.hex) and check, show --eeprom and
diff read it.`,
		examples: []string{
			"  levabak write --out copy.mcm.hex x.mcm.hex               # the same image again",
			"  levabak write --out \"$D\"/2026-11-10T094200Z_3.2_edit.mcm.hex --set 'Eco/Eco=50' \"$D\"/2026-11-10T094200Z_3.2.mcm.hex",
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
