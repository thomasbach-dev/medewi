package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/rawlog"
)

const synth = "../../testdata/spec/synthetic.xml"

// noEnv keeps the developer's .envrc out of the tests.
func noEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MEDEWID_XML_PATH", "MEDEWI_XML_PATH", "MEDEWID_RAW_DIR"} {
		t.Setenv(k, "")
	}
}

// testReport is a 289-byte report the synthetic spec decodes.
func testReport() []byte {
	b := bytes.Repeat([]byte(" "), 289)
	b[0], b[288] = '{', '}'
	b[2] = '*'
	copy(b[7:], "  93.5")
	return b
}

// rawDir writes two raw-log sessions, at 10:00 and 10:30 UTC on 2026-11-10,
// each with notes at +0 s, +10 s and +20 s and a report at +10 s.
func rawDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var mu sync.Mutex
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	set := func(at time.Time) { mu.Lock(); defer mu.Unlock(); now = at }
	l, err := rawlog.Open(rawlog.Config{Dir: dir, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []time.Time{
		time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 10, 10, 30, 0, 0, time.UTC),
	} {
		set(start)
		s := l.Session()
		s.Note("first " + start.Format("15:04"))
		set(start.Add(10 * time.Second))
		s.Note("second " + start.Format("15:04"))
		s.Tap("rx", append(testReport(), '\r', '\n'))
		set(start.Add(20 * time.Second))
		s.Note("third " + start.Format("15:04"))
		s.Close()
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestDecodeCmdWindow(t *testing.T) {
	t.Skip("window returns in Task 10")
	noEnv(t)
	dir := rawDir(t)
	code, out, errs := runCmd(t, "--xml", synth, "decode", "--dir", dir,
		"-s", "2026-11-10 10:00:05 UTC", "--until=2026-11-10 10:30:15 UTC")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if errs != "" {
		t.Errorf("stderr = %q, want empty", errs)
	}
	var notes, headers []string
	reports := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		switch {
		case strings.HasPrefix(line, "# "):
			headers = append(headers, line)
		case strings.Contains(line, " note "):
			// OFFSET DATE TIME DIR KIND TEXT...
			notes = append(notes, strings.Join(strings.Fields(line)[5:], " "))
		case strings.Contains(line, " report "):
			reports++
			if !strings.Contains(line, "93.5") || strings.Contains(line, "error") {
				t.Errorf("report not decoded: %q", line)
			}
		}
	}
	want := []string{"second 10:00", "third 10:00", "first 10:30", "second 10:30"}
	if strings.Join(notes, "|") != strings.Join(want, "|") {
		t.Errorf("notes = %q, want %q\n%s", notes, want, out)
	}
	if reports != 2 {
		t.Errorf("reports = %d, want 2\n%s", reports, out)
	}
	if len(headers) != 2 || !strings.HasPrefix(headers[0], "# "+dir) ||
		!strings.Contains(headers[0], "20261110T100000") || !strings.Contains(headers[1], "20261110T103000") {
		t.Errorf("headers = %q", headers)
	}
}

func TestDecodeCmdFileArgsInStartOrder(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %v, err = %v", files, err)
	}
	code, out, _ := runCmd(t, "--xml", synth, "decode", files[1], files[0])
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	i, j := strings.Index(out, "# "+files[0]), strings.Index(out, "# "+files[1])
	if i < 0 || j < 0 || i > j {
		t.Errorf("headers out of start order:\n%s", out)
	}
}

func TestDecodeCmdEnvDir(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	t.Setenv("MEDEWID_RAW_DIR", dir)
	t.Setenv("MEDEWI_XML_PATH", synth)
	code, out, errs := runCmd(t, "decode")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if strings.Count(out, " note ") != 6 {
		t.Errorf("want all 6 notes:\n%s", out)
	}
}

func TestDecodeCmdNoXML(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	code, out, errs := runCmd(t, "decode", "--dir", dir)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	want := "levalog: no vendor XML (--xml / MEDEWI_XML_PATH); reports shown raw\n"
	if errs != want {
		t.Errorf("stderr = %q, want %q", errs, want)
	}
	if strings.Count(out, " report ") != 2 {
		t.Errorf("want 2 raw reports:\n%s", out)
	}
}

func TestDecodeCmdCapture(t *testing.T) {
	noEnv(t)
	code, out, errs := runCmd(t, "--xml", synth, "decode", "../../testdata/fixtures/shot-genlever")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if !strings.HasPrefix(out, "# ../../testdata/fixtures/shot-genlever/log.jsonl\n") {
		t.Errorf("no header: %q", out[:min(len(out), 200)])
	}
	if strings.Contains(out, "# notes:") {
		t.Error("empty notes printed")
	}
	if !strings.Contains(out, " report ") {
		t.Error("no report lines")
	}

	_, out, _ = runCmd(t, "--xml", synth, "decode", "../../testdata/fixtures/warmup-stream")
	if !strings.Contains(out, "\n# notes: MC? returned no XML= line\n") {
		t.Errorf("no notes header: %q", out[:min(len(out), 300)])
	}
}

// dumpCapture writes a capture directory whose log.jsonl holds an MCu dump
// with "secret" in every reply line, then a report after the quiet period.
func dumpCapture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	add := func(ms int64, d, data string) {
		fmt.Fprintf(&b, "{\"t\":%d,\"dir\":%q,\"data\":%q}\n", ms, d, base64.StdEncoding.EncodeToString([]byte(data)))
	}
	add(0, "tx", "MCu\r\n")
	add(10, "rx", "|0001 secret one\r\n")
	add(20, "rx", "|0002 secret two\r\n")
	add(30, "rx", ":abc secret three\r\n")
	add(70_000, "rx", string(testReport())+"\r\n")
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDecodeCmdRedactsDumps(t *testing.T) {
	noEnv(t)
	dir := dumpCapture(t)
	code, out, errs := runCmd(t, "--xml", synth, "decode", dir)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if strings.Contains(out, "secret") || strings.Contains(errs, "secret") {
		t.Errorf("dump bytes leaked:\n%s%s", out, errs)
	}
	if !strings.Contains(out, "dump redacted (") || !strings.Contains(out, " report ") {
		t.Errorf("want a redaction line and the report:\n%s", out)
	}

	code, out, errs = runCmd(t, "--xml", synth, "decode", "--show-dumps", dir)
	if code != 0 {
		t.Fatalf("--show-dumps: exit %d, stderr %q", code, errs)
	}
	if strings.Count(out, "secret") != 3 {
		t.Errorf("--show-dumps: want all 3 dump lines:\n%s", out)
	}
}

func TestDecodeCmdUsageErrors(t *testing.T) {
	noEnv(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			"capture with since",
			[]string{"--xml", synth, "decode", "-s", "-5min", "../../testdata/fixtures/shot-genlever"},
			"levalog: --since/--until need raw logs with wall time; ../../testdata/fixtures/shot-genlever/log.jsonl is a capture\n",
		},
		{
			"capture with until",
			[]string{"decode", "--until", "now", "../../testdata/fixtures/shot-genlever/log.jsonl"},
			"is a capture",
		},
		{"single-dash long option", []string{"-ca"}, "unknown shorthand flag"},
		{"single-dash long sub option", []string{"decode", "-dir", "x"}, "unknown shorthand flag"},
		{"bad since", []string{"decode", "--since", "bogus"}, "--since"},
		{"no subcommand", nil, "usage:"},
		{"unknown subcommand", []string{"encode"}, "unknown subcommand"},
	}
	for _, tc := range tests {
		code, _, errs := runCmd(t, tc.args...)
		if code != 2 {
			t.Errorf("%s: exit %d, want 2 (stderr %q)", tc.name, code, errs)
		}
		if !strings.Contains(errs, tc.want) {
			t.Errorf("%s: stderr %q, want %q", tc.name, errs, tc.want)
		}
	}
}

func TestDecodeCmdReadErrors(t *testing.T) {
	noEnv(t)
	missing := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][]string{
		{"--xml", synth, "decode", "--dir", missing},
		{"--xml", synth, "decode", missing},
		{"--xml", missing, "decode", "../../testdata/fixtures/shot-genlever"},
	} {
		if code, _, errs := runCmd(t, args...); code != 1 || errs == "" {
			t.Errorf("%q: exit %d, stderr %q; want 1 and a message", args, code, errs)
		}
	}
}

func TestHelp(t *testing.T) {
	noEnv(t)
	for _, args := range [][]string{{"--help"}, {"decode", "--help"}} {
		code, _, errs := runCmd(t, args...)
		if code != 0 {
			t.Errorf("%q: exit %d", args, code)
		}
		for _, w := range []string{"MEDEWI_XML_PATH", "MEDEWID_RAW_DIR", "capture directory", "raw-log directory"} {
			if !strings.Contains(errs, w) {
				t.Errorf("%q: help does not name %s:\n%s", args, w, errs)
			}
		}
	}
}
