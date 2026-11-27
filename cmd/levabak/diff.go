package main

import (
	"io"
	"strings"

	"github.com/thomasbach-dev/medewi/backup"
)

func diffCmd(args []string, firmware string, stdout, stderr io.Writer) int {
	fs := subFlags("diff", stderr)
	format := addFormat(fs)
	prefix := addPath(fs)
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
			found = append(found, "  "+shortSrc(b.Src)+" ("+kinds(b)+")")
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
			warn(stderr, "%s: damaged, %s (see levabak check); diffing anyway", shortSrc(x.Src), count(n, "problem"))
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
	// EEPROM rows carry B's offsets, also for a state block that moved.
	var entries []backup.Entry
	if haveLayout && b.MCm != nil {
		entries = l.At(b.MCm.Image)
	}
	t := table{cols: []string{"section", "key", "a", "b", "note"}}
	for _, c := range backup.Diff(a, b, l, haveLayout) {
		if !keepChange(c, *prefix, entries) {
			continue
		}
		t.rows = append(t.rows, []string{c.Section, c.Key, c.A, c.B, c.Note})
	}
	if err := t.write(stdout, *format); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	switch {
	case len(t.rows) > 0:
	case *prefix != "":
		noMatch(stderr, *prefix)
	default:
		warn(stderr, "no differences (a: %s, b: %s)", shortSrc(a.Src), shortSrc(b.Src))
	}
	return 0
}

// keepChange applies --path: a setting row by its key, a section row
// ("<label>/") when either path is a prefix of the other, an EEPROM row by
// its layout entry's MCuPath (unknown byte runs and the row of a moved
// state block have none and are dropped). entries are those of B's image.
func keepChange(c backup.Change, prefix string, entries []backup.Entry) bool {
	if prefix == "" {
		return true
	}
	switch {
	case c.Section == "eeprom":
		if c.Note == "state block moved" || strings.HasPrefix(c.Note, "in the state block") {
			return false
		}
		for _, e := range entries {
			if hex4(e.Offset) == c.Key {
				return strings.HasPrefix(e.MCuPath, prefix)
			}
		}
		return false
	case strings.HasPrefix(c.Note, "section only in "):
		return strings.HasPrefix(prefix, c.Key) || strings.HasPrefix(c.Key, prefix)
	}
	return strings.HasPrefix(c.Key, prefix)
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
