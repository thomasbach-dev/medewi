package main

import (
	"io"
	"strconv"

	"github.com/thomasbach-dev/medewi/backup"
)

func showCmd(args []string, firmware string, stdout, stderr io.Writer) int {
	fs := subFlags("show", stderr)
	format := addFormat(fs)
	eeprom := fs.Bool("eeprom", false, "list the known EEPROM entries of the MCm dump instead of the settings")
	if code, done := parse(fs, args, format, stderr); done {
		return code
	}
	bks, code := loadBackups(fs.Args(), stderr)
	if code != 0 {
		return code
	}
	t := table{cols: []string{"src", "note", "line", "path", "label", "value", "raw"}, num: map[string]bool{"line": true}}
	if *eeprom {
		t = table{
			cols: []string{"src", "note", "offset", "type", "raw", "value", "name", "confidence"},
			num:  map[string]bool{"raw": true, "value": true},
		}
	}
	ls := &layouts{stderr: stderr}
	for i := range bks {
		b := &bks[i]
		if *eeprom {
			t.rows = append(t.rows, eepromRows(b, fwOf(b, firmware), ls, stderr)...)
			continue
		}
		if b.MCu == nil {
			warn(stderr, "%s: no MCu file: no settings to show", b.Src)
			continue
		}
		for _, s := range b.MCu.Settings {
			if s.Menu {
				continue
			}
			t.rows = append(t.rows, []string{b.Src, b.Note, strconv.Itoa(s.Line), s.Path, s.Label, s.Value, s.Raw})
		}
	}
	if err := t.write(stdout, *format); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}

func eepromRows(b *backup.Backup, fw string, ls *layouts, stderr io.Writer) [][]string {
	if b.MCm == nil {
		warn(stderr, "%s: no MCm file: no EEPROM entries to show", b.Src)
		return nil
	}
	l, ok := ls.get(fw)
	if !ok {
		return nil
	}
	var rows [][]string
	for _, e := range l.Entries {
		raw, v, ok := e.Decode(b.MCm.Image)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			b.Src, b.Note, hex4(e.Offset), e.Type,
			strconv.FormatUint(uint64(raw), 10), e.Format(v), e.Name, e.Confidence,
		})
	}
	return rows
}
