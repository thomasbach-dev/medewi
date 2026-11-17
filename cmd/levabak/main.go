// Command levabak checks, lists and compares leva! settings backups: the
// MCu menu dump and the MCm firmware (EEPROM) dump that medewictl backup
// writes. It only reads files; it never talks to the machine.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/pflag"

	"github.com/thomasbach-dev/medewi/backup"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var firmware string
	fs := pflag.NewFlagSet("levabak", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.SetInterspersed(false) // "levabak --firmware V show ..." stops at the subcommand
	fs.Usage = func() { usage(stderr) }
	fs.StringVar(&firmware, "firmware", "", "firmware version, overriding the one in each file name (layout without BACKUP: 3.2)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return fail(stderr, 2, "%v", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		usage(stderr)
		return 2
	}
	cmds := map[string]func([]string, string, io.Writer, io.Writer) int{
		"check":  checkCmd,
		"show":   showCmd,
		"diff":   diffCmd,
		"layout": layoutCmd,
	}
	cmd, ok := cmds[rest[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "levabak: unknown subcommand %q\n", rest[0])
		usage(stderr)
		return 2
	}
	return cmd(rest[1:], firmware, stdout, stderr)
}

// fail prints "levabak: MESSAGE" to stderr and returns code.
func fail(stderr io.Writer, code int, format string, a ...any) int {
	_, _ = fmt.Fprintf(stderr, "levabak: "+format+"\n", a...)
	return code
}

// warn prints one "levabak: " line.
func warn(stderr io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(stderr, "levabak: "+format+"\n", a...)
}

// subFlags returns the flag set of subcommand name; its --help prints the
// subcommand's own part of the help.
func subFlags(name string, stderr io.Writer) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { subUsage(stderr, name, fs) }
	return fs
}

// parse parses args into fs and checks --format; done is true when the
// caller returns code.
func parse(fs *pflag.FlagSet, args []string, format *string, stderr io.Writer) (code int, done bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0, true
		}
		return fail(stderr, 2, "%v", err), true
	}
	switch *format {
	case "text", "jsonl", "csv":
		return 0, false
	}
	return fail(stderr, 2, "unknown --format %q (text, jsonl or csv)", *format), true
}

// addFormat registers --format on fs.
func addFormat(fs *pflag.FlagSet) *string {
	return fs.String("format", "text", "output format: text, jsonl or csv")
}

// fwOf is the firmware of b: the --firmware override, else the file name's.
func fwOf(b *backup.Backup, override string) string {
	if override != "" {
		return override
	}
	return b.Firmware
}

// loadBackups resolves and loads the BACKUP arguments. Resolve notes go to
// stderr as warnings. A non-zero code means the caller returns it.
func loadBackups(args []string, stderr io.Writer) (bks []backup.Backup, code int) {
	if len(args) == 0 {
		return nil, fail(stderr, 2, "no BACKUP given (a *.mcu.txt or *.mcm.hex file, or a directory)")
	}
	bks, notes, err := backup.Resolve(args)
	if err != nil {
		return nil, fail(stderr, 1, "%v", err)
	}
	for _, n := range notes {
		warn(stderr, "%s", shortNote(n))
	}
	if len(bks) == 0 {
		return nil, fail(stderr, 1, "no backups found in %v", args)
	}
	for i := range bks {
		if err := bks[i].Load(); err != nil {
			return nil, fail(stderr, 1, "%v", err)
		}
	}
	return bks, 0
}

// layouts looks up layouts per firmware and warns once per unknown one.
type layouts struct {
	stderr io.Writer
	warned map[string]bool
}

func (ls *layouts) get(fw string) (backup.Layout, bool) {
	l, ok := backup.LayoutFor(fw)
	if !ok && !ls.warned[fw] {
		if ls.warned == nil {
			ls.warned = map[string]bool{}
		}
		ls.warned[fw] = true
		if fw == "" {
			warn(ls.stderr, "no firmware known (not in the file name; use --firmware): EEPROM entries have no names")
		} else {
			warn(ls.stderr, "no layout for firmware %s: EEPROM entries have no names", fw)
		}
	}
	return l, ok
}

func hex4(off int) string { return fmt.Sprintf("0x%04X", off) }

// shortSrc is how a backup's src prints for humans (text tables, verdict
// lines, stderr warnings): relative to the current directory unless that
// path leaves it ("..") or cannot be made relative, else the base name.
// jsonl and csv keep the full src.
func shortSrc(src string) string {
	if wd, err := os.Getwd(); err == nil {
		abs := src
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(wd, abs)
		}
		if rel, err := filepath.Rel(wd, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return filepath.Base(src)
}

// shortNote shortens the path in a Resolve note ("unpaired mcu file PATH",
// "skipped PATH: not a backup file name") like shortSrc.
func shortNote(n string) string {
	if rest, ok := strings.CutPrefix(n, "skipped "); ok {
		if p, tail, ok := strings.Cut(rest, ": not a backup file name"); ok {
			return "skipped " + shortSrc(p) + ": not a backup file name" + tail
		}
	}
	if i := strings.Index(n, " file "); i >= 0 && strings.HasPrefix(n, "unpaired ") {
		return n[:i+len(" file ")] + shortSrc(n[i+len(" file "):])
	}
	return n
}

// noMatch warns that --path PREFIX selected nothing.
func noMatch(stderr io.Writer, prefix string) {
	warn(stderr, "no rows match --path %q", prefix)
}

// addPath registers --path on fs.
func addPath(fs *pflag.FlagSet) *string {
	return fs.String("path", "", "keep only rows whose path (diff: setting key) starts with PREFIX; a trailing / selects a whole menu")
}
