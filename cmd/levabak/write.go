package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/thomasbach-dev/medewi/backup"
)

// writeCmd writes the EEPROM image of one backup as a new MCm file,
// after the --set edits. It reads and writes files only.
func writeCmd(args []string, firmware string, stdout, stderr io.Writer) int {
	fs := subFlags("write", stderr)
	format := addFormat(fs)
	out := fs.String("out", "", "the MCm file to write (required)")
	sets := fs.StringArray("set", nil, "set the EEPROM entry of an MCu path: PATH=VALUE (repeatable)")
	standardEOF := fs.Bool("standard-eof", false, "end with the Intel HEX end record instead of the firmware's")
	force := fs.Bool("force", false, "overwrite an existing --out file")
	if code, done := parse(fs, args, format, stderr); done {
		return code
	}
	if *out == "" {
		return fail(stderr, 2, "write needs --out FILE")
	}
	type edit struct{ path, value string }
	var edits []edit
	seen := map[string]bool{}
	for _, s := range *sets {
		i := strings.LastIndex(s, "=")
		if i <= 0 {
			return fail(stderr, 2, "--set %q: want PATH=VALUE", s)
		}
		e := edit{s[:i], s[i+1:]}
		if seen[e.path] {
			return fail(stderr, 2, "--set names %q twice", e.path)
		}
		seen[e.path] = true
		edits = append(edits, e)
	}
	bks, code := loadBackups(fs.Args(), stderr)
	if code != 0 {
		return code
	}
	if len(bks) != 1 {
		return fail(stderr, 2, "write takes exactly one backup, found %d", len(bks))
	}
	b := &bks[0]
	if b.MCm == nil {
		return fail(stderr, 1, "%s: no MCm file: no EEPROM image to write", shortSrc(b.Src))
	}
	if len(b.MCm.Problems) > 0 {
		return fail(stderr, 1, "%s: the MCm dump is damaged (see levabak check): not written", shortSrc(b.MCmPath))
	}
	if same, err := sameFile(*out, b.MCmPath); err != nil {
		return fail(stderr, 1, "%v", err)
	} else if same {
		return fail(stderr, 2, "--out is the source file %s", shortSrc(b.MCmPath))
	}

	img := append([]byte(nil), b.MCm.Image...)
	var l backup.Layout
	haveLayout := false
	if len(edits) > 0 {
		fw := fwOf(b, firmware)
		if l, haveLayout = backup.LayoutFor(fw); !haveLayout {
			if fw == "" {
				return fail(stderr, 1, "no firmware known (not in the file name; use --firmware): --set needs a layout")
			}
			return fail(stderr, 1, "no layout for firmware %s: --set needs one", fw)
		}
	}
	for _, e := range edits {
		if _, err := l.Set(img, e.path, e.value); err != nil {
			return fail(stderr, 1, "%v", err)
		}
	}

	data := backup.EncodeMCm(img, *standardEOF)
	// Read our own output back before it reaches the disk.
	if back := backup.ParseMCm(backup.SplitLines(data)); len(back.Problems) > 0 || !bytes.Equal(back.Image, img) {
		return fail(stderr, 1, "internal error: the encoded image does not read back")
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if *force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(*out, flags, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fail(stderr, 1, "%s exists (use --force to overwrite)", *out)
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(stderr, 1, "%v", err)
	}

	if len(edits) == 0 {
		warn(stderr, "no --set: the image is unchanged; wrote %s", *out)
		return 0
	}
	t := table{cols: []string{"section", "key", "a", "b", "note"}}
	edited := &backup.Backup{MCm: &backup.MCm{Image: img}}
	for _, c := range backup.Diff(&backup.Backup{MCm: b.MCm}, edited, l, true) {
		t.rows = append(t.rows, []string{c.Section, c.Key, c.A, c.B, c.Note})
	}
	if err := t.write(stdout, *format); err != nil {
		return fail(stderr, 1, "%v", err)
	}
	if len(t.rows) == 0 {
		warn(stderr, "every --set value is already in the image: it is unchanged")
	}
	warn(stderr, "wrote %s", *out)
	return 0
}

// sameFile reports whether a and b name the same file; a file that does
// not exist is no file's equal.
func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if errors.Is(err, fs.ErrNotExist) {
		pa, _ := filepath.Abs(a)
		pb, _ := filepath.Abs(b)
		return pa == pb, nil
	}
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}
