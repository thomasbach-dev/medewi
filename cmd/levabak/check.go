package main

import (
	"fmt"
	"io"
	"strconv"

	"github.com/thomasbach-dev/medewi/backup"
)

func checkCmd(args []string, firmware string, stdout, stderr io.Writer) int {
	fs := subFlags("check", stderr)
	format := addFormat(fs)
	if code, done := parse(fs, args, format, stderr); done {
		return code
	}
	bks, code := loadBackups(fs.Args(), stderr)
	if code != 0 {
		return code
	}
	t := table{cols: []string{"src", "note", "line", "kind", "problem"}, num: map[string]bool{"line": true}}
	var verdicts []string
	ls := &layouts{stderr: stderr}
	bad := false
	for i := range bks {
		b := &bks[i]
		var probs []backup.Problem
		switch {
		case b.MCu == nil:
			warn(stderr, "%s: no MCu file: the settings were not checked", b.Src)
		case b.MCm == nil:
			warn(stderr, "%s: no MCm file: the EEPROM was not checked", b.Src)
		}
		if b.MCu != nil {
			probs = append(probs, b.MCu.Problems...)
		}
		if b.MCm != nil {
			probs = append(probs, b.MCm.Problems...)
		}
		if b.MCu != nil && b.MCm != nil {
			if l, ok := ls.get(fwOf(b, firmware)); ok {
				probs = append(probs, backup.CrossCheck(b.MCu, b.MCm, l)...)
			}
		}
		for _, p := range probs {
			line := ""
			if p.Line > 0 {
				line = strconv.Itoa(p.Line)
			}
			t.rows = append(t.rows, []string{b.Src, b.Note, line, string(p.Kind), p.Text})
		}
		if len(probs) == 0 {
			verdicts = append(verdicts, b.Src+": ok")
		} else {
			bad = true
			verdicts = append(verdicts, fmt.Sprintf("%s: damaged, %d problems", b.Src, len(probs)))
		}
	}
	if err := t.write(stdout, *format); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	if *format == "text" {
		for _, v := range verdicts {
			_, _ = fmt.Fprintln(stdout, v)
		}
	}
	if bad {
		return 1
	}
	return 0
}
