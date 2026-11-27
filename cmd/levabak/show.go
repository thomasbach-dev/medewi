package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/thomasbach-dev/medewi/backup"
)

func showCmd(args []string, firmware string, stdout, stderr io.Writer) int {
	fs := subFlags("show", stderr)
	format := addFormat(fs)
	eeprom := fs.Bool("eeprom", false, "list EEPROM entries instead of settings")
	prefix := addPath(fs)
	if code, done := parse(fs, args, format, stderr); done {
		return code
	}
	bks, code := loadBackups(fs.Args(), stderr)
	if code != 0 {
		return code
	}
	t := table{
		cols: []string{"src", "note", "line", "path", "label", "value", "number", "unit", "flag", "raw"},
		num:  map[string]bool{"line": true, "number": true},
	}
	if *eeprom {
		t = table{
			cols: []string{"src", "note", "offset", "type", "raw", "value", "name", "confidence"},
			num:  map[string]bool{"raw": true, "value": true},
		}
	}
	ls := &layouts{stderr: stderr}
	flagged := false
	for i := range bks {
		b := &bks[i]
		if *eeprom {
			t.rows = append(t.rows, eepromRows(b, fwOf(b, firmware), *prefix, ls, stderr)...)
			continue
		}
		if b.MCu == nil {
			warn(stderr, "%s: no MCu file: no settings to show", shortSrc(b.Src))
			continue
		}
		if len(b.MCu.Regions) > 0 {
			warn(stderr, "%s: damaged (see levabak check): settings in lines %s may sit under wrong paths; flagged damaged",
				shortSrc(b.Src), spans(b.MCu.Regions))
		}
		for _, s := range b.MCu.Settings {
			if s.Menu || !strings.HasPrefix(s.Path, *prefix) {
				continue
			}
			var number, unit string
			if n, u, ok := backup.SplitNumber(s.Value); ok {
				number, unit = jsonNumber(n), u
			}
			flag := ""
			if s.Damaged {
				flag, flagged = "damaged", true
			}
			t.rows = append(t.rows, []string{
				b.Src, b.Note, strconv.Itoa(s.Line), s.Path, s.Label, s.Value, number, unit, flag, s.Raw,
			})
		}
	}
	if !*eeprom {
		t.textHide = map[string]bool{"number": true, "unit": true, "flag": !flagged}
	}
	if err := t.write(stdout, *format); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	if *prefix != "" && len(t.rows) == 0 {
		noMatch(stderr, *prefix)
	}
	return 0
}

// spans lists regions as "5-6, 9-10".
func spans(rs []backup.Region) string {
	var s []string
	for _, r := range rs {
		s = append(s, fmt.Sprintf("%d-%d", r.From, r.To))
	}
	return strings.Join(s, ", ")
}

// jsonNumber turns a SplitNumber number into valid JSON number syntax:
// no leading "+", no bare ".5" or "5.", no superfluous leading zeros.
// Anything still invalid prints as a string in jsonl.
func jsonNumber(n string) string {
	sign := ""
	if n != "" && (n[0] == '-' || n[0] == '+') {
		if n[0] == '-' {
			sign = "-"
		}
		n = n[1:]
	}
	n = strings.TrimSuffix(n, ".")
	n = strings.TrimLeft(n, "0")
	if n == "" || n[0] == '.' {
		n = "0" + n
	}
	return sign + n
}

func eepromRows(b *backup.Backup, fw, prefix string, ls *layouts, stderr io.Writer) [][]string {
	if b.MCm == nil {
		warn(stderr, "%s: no MCm file: no EEPROM entries to show", shortSrc(b.Src))
		return nil
	}
	l, ok := ls.get(fw)
	if !ok {
		return nil
	}
	var rows [][]string
	for _, e := range l.At(b.MCm.Image) {
		if !strings.HasPrefix(e.MCuPath, prefix) {
			continue
		}
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
