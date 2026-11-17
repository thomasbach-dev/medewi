package main

import (
	"io"
	"strconv"

	"github.com/thomasbach-dev/medewi/backup"
)

func layoutCmd(args []string, firmware string, stdout, stderr io.Writer) int {
	fs := subFlags("layout", stderr)
	format := addFormat(fs)
	if code, done := parse(fs, args, format, stderr); done {
		return code
	}
	var fws []string
	if len(fs.Args()) == 0 {
		fw := firmware
		if fw == "" {
			fw = "3.2"
		}
		fws = []string{fw}
	} else {
		bks, code := loadBackups(fs.Args(), stderr)
		if code != 0 {
			return code
		}
		seen := map[string]bool{}
		for i := range bks {
			if fw := fwOf(&bks[i], firmware); !seen[fw] {
				seen[fw] = true
				fws = append(fws, fw)
			}
		}
	}
	t := table{
		cols: []string{"firmware", "offset", "type", "scale", "name", "confidence", "evidence"},
		num:  map[string]bool{"scale": true},
	}
	for _, fw := range fws {
		l, ok := backup.LayoutFor(fw)
		if !ok {
			if fw == "" {
				return fail(stderr, 1, "no firmware known (not in the file name; use --firmware)")
			}
			return fail(stderr, 1, "no layout for firmware %s", fw)
		}
		for _, e := range l.Entries {
			t.rows = append(t.rows, []string{
				l.Firmware, hex4(e.Offset), e.Type,
				strconv.FormatFloat(e.Scale, 'f', -1, 64), e.Name, e.Confidence, e.Evidence,
			})
		}
	}
	if err := t.write(stdout, *format); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	return 0
}
