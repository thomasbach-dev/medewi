// Command levalog reads medewid's raw traffic logs and levacap captures: it
// lists the report fields (fields), the pump runs and wand draws (runs) and
// the events (events), as text, JSON Lines or CSV, and cuts raw-log windows
// out as captures (extract). It only reads files (and writes the capture); it
// never talks to the machine.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/pflag"
)

const defaultRawDir = "/var/lib/medewid/raw"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// envOr returns the first non-empty environment variable of keys, else def.
func envOr(def string, keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return def
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	xmlPath := envOr("", "MEDEWID_XML_PATH", "MEDEWI_XML_PATH")
	fs := pflag.NewFlagSet("levalog", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.SetInterspersed(false) // "levalog --xml F events ..." stops at the subcommand
	fs.Usage = func() { usage(stderr) }
	fs.StringVar(&xmlPath, "xml", xmlPath, "vendor Status-Monitor XML file or directory (env MEDEWID_XML_PATH, MEDEWI_XML_PATH)")
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
	cmds := map[string]func([]string, string, io.Reader, io.Writer, io.Writer) int{
		"fields":  fields,
		"runs":    runsCmd,
		"events":  events,
		"summary": summaryCmd,
		"extract": extractCmd,
	}
	cmd, ok := cmds[rest[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "levalog: unknown subcommand %q\n", rest[0])
		usage(stderr)
		return 2
	}
	return cmd(rest[1:], xmlPath, stdin, stdout, stderr)
}

// fail prints "levalog: MESSAGE" to stderr and returns code.
func fail(stderr io.Writer, code int, format string, a ...any) int {
	_, _ = fmt.Fprintf(stderr, "levalog: "+format+"\n", a...)
	return code
}

// warn prints each message as a "levalog: " line.
func warn(stderr io.Writer, msgs []string) {
	for _, m := range msgs {
		_, _ = fmt.Fprintf(stderr, "levalog: %s\n", m)
	}
}

// subFlags returns the flag set of subcommand name, printing its own help
// and flags on --help.
func subFlags(name string, stderr io.Writer) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { subUsage(stderr, name, fs) }
	return fs
}

// parse parses args into fs; done is true when the caller returns code.
func parse(fs *pflag.FlagSet, args []string, stderr io.Writer) (code int, done bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0, true
		}
		return fail(stderr, 2, "%v", err), true
	}
	return 0, false
}

// splitList splits a comma-separated flag value, dropping empty items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// needXML is the usage error of a subcommand or flag that needs the XML.
func needXML(stderr io.Writer, what string) int {
	return fail(stderr, 2, "%s needs the vendor XML (--xml / MEDEWI_XML_PATH)", what)
}
