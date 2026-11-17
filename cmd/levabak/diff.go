package main

import (
	"io"
	"strings"

	"github.com/thomasbach-dev/medewi/backup"
)

func diffCmd(args []string, firmware string, stdout, stderr io.Writer) int {
	fs := subFlags("diff", stderr)
	format := addFormat(fs)
	if code, done := parse(fs, args, format, stderr); done {
		return code
	}
	bks, code := loadBackups(fs.Args(), stderr)
	if code != 0 {
		return code
	}
	if len(bks) != 2 {
		var found []string
		for _, b := range bks {
			found = append(found, "  "+b.Src+" ("+kinds(b)+")")
		}
		return fail(stderr, 2, "diff needs exactly two backups, found %d:\n%s", len(bks), strings.Join(found, "\n"))
	}
	a, b := &bks[0], &bks[1]
	for _, x := range []*backup.Backup{a, b} {
		n := 0
		if x.MCu != nil {
			n += len(x.MCu.Problems)
		}
		if x.MCm != nil {
			n += len(x.MCm.Problems)
		}
		if n > 0 {
			warn(stderr, "%s: damaged, %d problems (see levabak check); diffing anyway", x.Src, n)
		}
	}
	if (a.MCu == nil) != (b.MCu == nil) {
		warn(stderr, "only one side has an MCu: settings not compared")
	}
	if (a.MCm == nil) != (b.MCm == nil) {
		warn(stderr, "only one side has an MCm: EEPROM not compared")
	}
	fw := fwOf(a, firmware)
	if firmware == "" && a.Firmware != b.Firmware {
		warn(stderr, "firmware differs (%s, %s): using %s for the layout", a.Firmware, b.Firmware, a.Firmware)
	}
	var l backup.Layout
	var haveLayout bool
	if a.MCm != nil && b.MCm != nil {
		l, haveLayout = (&layouts{stderr: stderr}).get(fw)
	} else {
		l, haveLayout = backup.LayoutFor(fw)
	}
	t := table{cols: []string{"section", "key", "a", "b", "note"}}
	for _, c := range backup.Diff(a, b, l, haveLayout) {
		t.rows = append(t.rows, []string{c.Section, c.Key, c.A, c.B, c.Note})
	}
	if err := t.write(stdout, *format); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	if len(t.rows) == 0 {
		warn(stderr, "no differences (a: %s, b: %s)", a.Src, b.Src)
	}
	return 0
}

// kinds says which dumps b holds.
func kinds(b backup.Backup) string {
	switch {
	case b.MCu != nil && b.MCm != nil:
		return "MCu+MCm"
	case b.MCu != nil:
		return "MCu only"
	}
	return "MCm only"
}
