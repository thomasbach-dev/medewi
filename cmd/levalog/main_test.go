package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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
	copy(b[4:], "PID  93.5 91.0 50")
	copy(b[36:], " 9.00")
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

func TestEventsWindow(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	code, out, errs := runCmd(t, "--xml", synth, "events", "--dir", dir,
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

func TestEventsFileArgsInStartOrder(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %v, err = %v", files, err)
	}
	code, out, _ := runCmd(t, "--xml", synth, "events", files[1], files[0])
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	i, j := strings.Index(out, "# "+files[0]), strings.Index(out, "# "+files[1])
	if i < 0 || j < 0 || i > j {
		t.Errorf("headers out of start order:\n%s", out)
	}
}

func TestEventsEnvDir(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	t.Setenv("MEDEWID_RAW_DIR", dir)
	t.Setenv("MEDEWI_XML_PATH", synth)
	code, out, errs := runCmd(t, "events")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if strings.Count(out, " note ") != 6 {
		t.Errorf("want all 6 notes:\n%s", out)
	}
}

func TestEventsNoXML(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	code, out, errs := runCmd(t, "events", "--dir", dir)
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

func TestEventsCapture(t *testing.T) {
	noEnv(t)
	code, out, errs := runCmd(t, "--xml", synth, "events", "../../testdata/fixtures/shot-genlever")
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

	_, out, _ = runCmd(t, "--xml", synth, "events", "../../testdata/fixtures/warmup-stream")
	if !strings.HasPrefix(out, "# ../../testdata/fixtures/warmup-stream/log.jsonl\n# notes: MC? returned no XML= line\n") {
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

func TestEventsRedactsDumps(t *testing.T) {
	noEnv(t)
	dir := dumpCapture(t)
	code, out, errs := runCmd(t, "--xml", synth, "events", dir)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if strings.Contains(out, "secret") || strings.Contains(errs, "secret") {
		t.Errorf("dump bytes leaked:\n%s%s", out, errs)
	}
	if !strings.Contains(out, "dump redacted (") || !strings.Contains(out, " report ") {
		t.Errorf("want a redaction line and the report:\n%s", out)
	}

	code, out, errs = runCmd(t, "--xml", synth, "events", "--show-dumps", dir)
	if code != 0 {
		t.Fatalf("--show-dumps: exit %d, stderr %q", code, errs)
	}
	if strings.Count(out, "secret") != 3 {
		t.Errorf("--show-dumps: want all 3 dump lines:\n%s", out)
	}
}

func TestEventsUsageErrors(t *testing.T) {
	noEnv(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			"capture with since",
			[]string{"--xml", synth, "events", "-s", "-5min", "../../testdata/fixtures/shot-genlever"},
			"levalog: --since/--until need raw logs with wall time; ../../testdata/fixtures/shot-genlever/log.jsonl is a capture\n",
		},
		{
			"capture with until",
			[]string{"events", "--until", "now", "../../testdata/fixtures/shot-genlever/log.jsonl"},
			"is a capture",
		},
		{"single-dash long option", []string{"-ca"}, "unknown shorthand flag"},
		{"single-dash long sub option", []string{"events", "-dir", "x"}, "unknown shorthand flag"},
		{"bad since", []string{"events", "--since", "bogus"}, "--since"},
		{"no subcommand", nil, "usage:"},
		{"unknown subcommand", []string{"encode"}, "unknown subcommand"},
		{"bad format", []string{"--xml", synth, "events", "--format", "xml"}, "--format"},
		{"bad event", []string{"--xml", synth, "events", "--event", "report,bogus"}, "--event"},
		{"bad run", []string{"--xml", synth, "events", "--run", "3-x"}, "--run"},
		{"bad run range", []string{"--xml", synth, "events", "--run", "5-3"}, "--run"},
		{"bad run-kind", []string{"--xml", synth, "events", "--run-kind", "espresso"}, "--run-kind"},
		{"bad kind", []string{"--xml", synth, "runs", "--kind", "espresso"}, "--kind"},
		{"bad margin", []string{"--xml", synth, "events", "--margin", "-5s"}, "--margin"},
		{"margin without runs", []string{"--xml", synth, "events", "--margin", "2s"}, "--margin needs --run or --run-kind"},
		{"hide-masked with jsonl", []string{"--xml", synth, "events", "--format", "jsonl", "--hide-masked"}, "--hide-masked"},
		{"csv with other events", []string{"--xml", synth, "events", "--format", "csv", "--event", "report,note"}, "--event"},
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

func TestEventsReadErrors(t *testing.T) {
	noEnv(t)
	missing := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][]string{
		{"--xml", synth, "events", "--dir", missing},
		{"--xml", synth, "events", missing},
		{"--xml", missing, "events", "../../testdata/fixtures/shot-genlever"},
	} {
		if code, _, errs := runCmd(t, args...); code != 1 || errs == "" {
			t.Errorf("%q: exit %d, stderr %q; want 1 and a message", args, code, errs)
		}
	}
}

func TestHelp(t *testing.T) {
	noEnv(t)
	for _, args := range [][]string{{"--help"}, {"events", "--help"}} {
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

func TestEventsRenamed(t *testing.T) {
	noEnv(t)
	code, _, errs := runCmd(t, "--xml", synth, "decode")
	if code != 2 || !strings.Contains(errs, `levalog: unknown subcommand "decode"`) {
		t.Errorf("exit %d, stderr %q; want 2 and unknown subcommand", code, errs)
	}
}

func TestFieldsSynthetic(t *testing.T) {
	noEnv(t)
	t.Setenv("MEDEWID_RAW_DIR", filepath.Join(t.TempDir(), "missing"))
	wantKeys := []string{
		"t", "wall", "src", "src_end", "dir", "event", "run", "run_t", "pump", "xml",
		"mode", "temperature_pid1", "setpoint_pid", "pressure", "heat_pid", "field_5",
	}
	for _, args := range [][]string{
		{"--xml", synth, "fields"},                     // no readable input: the file's columns
		{"--xml", synth, "fields", "--dir", rawDir(t)}, // the XML of the last raw log
		{"--xml", synth, "fields", "-s", "2026-11-10 10:30 UTC", "--dir", rawDir(t)},
	} {
		code, out, errs := runCmd(t, args...)
		if code != 0 || errs != "" {
			t.Fatalf("%q: exit %d, stderr %q", args, code, errs)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if strings.HasPrefix(lines[0], "# xml: ") {
			lines = lines[1:]
		}
		if !strings.HasPrefix(lines[0], "KEY ") {
			t.Fatalf("%q: no header:\n%s", args, out)
		}
		var keys []string
		for _, l := range lines[1:] {
			keys = append(keys, strings.Fields(l)[0])
		}
		if strings.Join(keys, " ") != strings.Join(wantKeys, " ") {
			t.Errorf("%q: keys = %q\nwant %q", args, keys, wantKeys)
		}
		if !strings.Contains(out, "temperature_pid1  Temperature PID1") {
			t.Errorf("%q: no name column:\n%s", args, out)
		}
	}

	code, out, _ := runCmd(t, "--xml", synth, "fields", "--format", "jsonl", "--dir", rawDir(t))
	if code != 0 || !strings.Contains(out, `{"key":"pressure","name":"Pressure","unit":"bar"`) {
		t.Errorf("jsonl: exit %d\n%s", code, out)
	}
}

func TestFieldsNoXML(t *testing.T) {
	noEnv(t)
	for _, sub := range []string{"fields", "runs"} {
		code, _, errs := runCmd(t, sub, "--dir", rawDir(t))
		if code != 2 || !strings.Contains(errs, "levalog: "+sub+" needs the vendor XML") {
			t.Errorf("%s: exit %d, stderr %q; want 2", sub, code, errs)
		}
	}
}

// runXML writes a spec with the fields brew reads for runs: Pressure,
// Pressure SP and Volume.
func runXML(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "runs.xml")
	const x = `<cfg version="4"><values>
<value at="7" len="5" export="Pressure" type="float" unit="bar"></value>
<value at="13" len="5" export="Pressure SP" type="float" unit="bar"></value>
<value at="19" len="5" export="Volume" type="float" unit="ml"></value>
</values></cfg>
`
	if err := os.WriteFile(p, []byte(x), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// step is one synthetic report of the run spec.
type step struct {
	pump   byte
	sp     string // "" masks Pressure SP
	volume float64
}

func runReport(s step) []byte {
	b := bytes.Repeat([]byte(" "), 289)
	b[0], b[288] = '{', '}'
	b[2] = s.pump
	copy(b[7:], " 9.00")
	sp := "?????"
	if s.sp != "" {
		sp = fmt.Sprintf("%5s", s.sp)
	}
	copy(b[13:], sp)
	copy(b[19:], fmt.Sprintf("%5.1f", s.volume))
	return b
}

// pumpRun is n pumping steps (a shot with a setpoint, else a flush) and
// then n/2 idle steps at the end volume.
func pumpRun(n int, sp string) []step {
	var out []step
	for i := range n {
		out = append(out, step{pump: '*', sp: sp, volume: float64(i)})
	}
	for range n / 2 {
		out = append(out, step{pump: '-', volume: float64(n - 1)})
	}
	return out
}

func idleSteps(n int) []step {
	out := make([]step, n)
	for i := range out {
		out[i] = step{pump: '-'}
	}
	return out
}

// runDir writes steps as one session from 10:00:00 UTC on 2026-11-10, one
// report every 250 ms, into raw logs that rotate every rotate.
func runDir(t *testing.T, rotate time.Duration, steps []step) string {
	t.Helper()
	dir := t.TempDir()
	var mu sync.Mutex
	start := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	l, err := rawlog.Open(rawlog.Config{Dir: dir, Now: clock, RotateEvery: rotate})
	if err != nil {
		t.Fatal(err)
	}
	s := l.Session()
	for i, st := range steps {
		mu.Lock()
		now = start.Add(time.Duration(i) * 250 * time.Millisecond)
		mu.Unlock()
		s.Tap("rx", append(runReport(st), '\r', '\n'))
	}
	s.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunsAndEventsAgree(t *testing.T) {
	noEnv(t)
	xml := runXML(t)
	dir := runDir(t, time.Hour, concat(
		idleSteps(8), pumpRun(12, ""), idleSteps(16), pumpRun(40, "9.00"), idleSteps(16), pumpRun(32, "9.00"), idleSteps(8),
	))
	code, out, errs := runCmd(t, "--xml", xml, "runs", "--format", "jsonl", "--dir", dir)
	if code != 0 || errs != "" {
		t.Fatalf("runs: exit %d, stderr %q", code, errs)
	}
	type runRow struct {
		Run  int     `json:"run"`
		Kind string  `json:"kind"`
		T    float64 `json:"t"`
	}
	var runs []runRow
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var r runRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		runs = append(runs, r)
	}
	if len(runs) != 3 || runs[0].Kind != "flush" || runs[1].Kind != "shot" || runs[2].Kind != "shot" {
		t.Fatalf("runs = %+v", runs)
	}

	_, out, _ = runCmd(t, "--xml", xml, "runs", "--kind", "shot", "--dir", dir)
	if n := strings.Count(out, " shot "); n != 2 || strings.Contains(out, "flush") {
		t.Errorf("--kind shot:\n%s", out)
	}

	for _, r := range runs {
		code, out, errs := runCmd(t, "--xml", xml, "events", "--format", "jsonl", "--run", fmt.Sprint(r.Run), "--dir", dir)
		if code != 0 || errs != "" {
			t.Fatalf("events --run %d: exit %d, stderr %q", r.Run, code, errs)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		for i, line := range lines {
			var row struct {
				Run  *int     `json:"run"`
				T    float64  `json:"t"`
				RunT *float64 `json:"run_t"`
			}
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			if row.Run == nil || *row.Run != r.Run {
				t.Errorf("events --run %d: row run %v: %s", r.Run, row.Run, line)
			}
			if i == 0 && (row.T != r.T || row.RunT == nil || *row.RunT != 0) {
				t.Errorf("events --run %d: first row %s, want t %v run_t 0", r.Run, line, r.T)
			}
		}
	}

	// A window starting at a file's start: the shot that is pumping across
	// that file edge started before the window and gets no number, in runs
	// and in events with a margin alike.
	xdir := runDir(t, 10*time.Second, concat(
		idleSteps(8), pumpRun(40, "9.00"), idleSteps(52), pumpRun(24, "9.00"), idleSteps(20), pumpRun(12, ""), idleSteps(8),
	))
	since := []string{"-s", "2026-11-10 10:00:10 UTC"}
	code, out, errs = runCmd(t, append([]string{"--xml", xml, "runs", "--format", "jsonl", "--dir", xdir}, since...)...)
	if code != 0 || errs != "" {
		t.Fatalf("runs across files: exit %d, stderr %q", code, errs)
	}
	type xrun struct {
		Run   int    `json:"run"`
		Kind  string `json:"kind"`
		Start string `json:"start"`
		Src   string `json:"src"`
	}
	var xruns []xrun
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var r xrun
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		xruns = append(xruns, r)
	}
	if len(xruns) != 2 || xruns[0].Kind != "shot" || xruns[0].Start != "2026-11-10T10:00:30.000Z" || xruns[1].Kind != "flush" {
		t.Fatalf("runs across files = %+v, want the shot at 10:00:30 and the flush", xruns)
	}
	for _, r := range xruns {
		args := append([]string{"--xml", xml, "events", "--format", "jsonl", "--dir", xdir, "--run", fmt.Sprint(r.Run), "--margin", "2s"}, since...)
		code, out, errs := runCmd(t, args...)
		if code != 0 || errs != "" {
			t.Fatalf("events --run %d --margin 2s: exit %d, stderr %q", r.Run, code, errs)
		}
		first := ""
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			var row struct {
				Run  *int     `json:"run"`
				RunT *float64 `json:"run_t"`
				Src  string   `json:"src"`
			}
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			if row.Run == nil || *row.Run != r.Run {
				t.Errorf("events --run %d --margin 2s: row %s", r.Run, line)
			}
			if row.RunT != nil && *row.RunT == 0 && first == "" {
				first = row.Src
			}
		}
		if first != r.Src {
			t.Errorf("events --run %d --margin 2s: first sample at %q, runs says %q", r.Run, first, r.Src)
		}
	}

	// A run number that does not exist selects nothing and says so.
	if code, out, errs := runCmd(t, "--xml", xml, "events", "--dir", dir, "--run", "99"); code != 0 || out != "" ||
		errs != "levalog: --run 99: no such run (runs 1-3)\n" {
		t.Errorf("--run 99: exit %d, output %q, stderr %q", code, out, errs)
	}
	if _, out, errs := runCmd(t, "--xml", xml, "events", "--dir", dir, "--run", "2,7-9"); out == "" ||
		errs != "levalog: --run 7-9: no such run (runs 1-3)\n" {
		t.Errorf("--run 2,7-9: output %d bytes, stderr %q", len(out), errs)
	}

	// Runs as a list, a range, by kind and as a union.
	for _, tc := range []struct {
		sel  []string
		want string
	}{
		{[]string{"--run", "1,3"}, "1,3"},
		{[]string{"--run", "2-3"}, "2,3"},
		{[]string{"--run-kind", "shot"}, "2,3"},
		{[]string{"--run", "1", "--run-kind", "shot"}, "1,2,3"},
	} {
		args := append([]string{"--xml", xml, "events", "--format", "jsonl", "--dir", dir}, tc.sel...)
		_, out, _ := runCmd(t, args...)
		var got []string
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			var row struct {
				Run int `json:"run"`
			}
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			if n := fmt.Sprint(row.Run); len(got) == 0 || got[len(got)-1] != n {
				got = append(got, n)
			}
		}
		if strings.Join(got, ",") != tc.want {
			t.Errorf("%q: runs %v, want %s", tc.sel, got, tc.want)
		}
	}
}

func concat(parts ...[]step) []step {
	var out []step
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestEventsRunAcrossFiles(t *testing.T) {
	noEnv(t)
	xml := runXML(t)
	// The shot pumps from 2 s to 12 s; files rotate every 10 s.
	dir := runDir(t, 10*time.Second, concat(idleSteps(8), pumpRun(40, "9.00"), idleSteps(8)))
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	code, out, errs := runCmd(t, "--xml", xml, "events", "--dir", dir,
		"-s", "2026-11-10 10:00 UTC", "-u", "2026-11-10 10:00:05 UTC", "--run", "1", "--event", "report")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if !strings.Contains(out, "# "+files[1]+"\n") {
		t.Errorf("second file not read:\n%s", out)
	}
	reports := strings.Count(out, " report ")
	if reports != 40 {
		t.Errorf("reports = %d, want the whole shot (40)\n%s", reports, out)
	}
	// RUN and RUN_T follow EVENT in text with a run selection.
	if !strings.Contains(out, " report   1    0.000 pump=* ") || !strings.Contains(out, " report   1    9.750 pump=* ") {
		t.Errorf("no run columns:\n%s", out)
	}

	// Without a run selection the window cuts.
	_, out, _ = runCmd(t, "--xml", xml, "events", "--dir", dir,
		"-s", "2026-11-10 10:00 UTC", "-u", "2026-11-10 10:00:05 UTC", "--event", "report")
	if n := strings.Count(out, " report "); n != 20 {
		t.Errorf("window: reports = %d, want 20", n)
	}
}

func TestEventsCSV(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	code, out, errs := runCmd(t, "--xml", synth, "events", "--format", "csv",
		"--fields", "t,wall,src,pump,temperature_pid1,heat_pid", files[0])
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	want := "t,wall,src,run,run_t,pump,temperature_pid1,heat_pid\n10,2026-11-10T10:00:10.000Z," + files[0] + ":3,1,0,1,93.5,50\n"
	if out != want {
		t.Errorf("csv:\n got %q\nwant %q", out, want)
	}

	code, _, errs = runCmd(t, "--xml", synth, "events", "--fields", "t,bogus,temperature_pid1,nope", files[0])
	if code != 2 || !strings.Contains(errs, "--fields") || !strings.Contains(errs, "bogus, nope") {
		t.Errorf("unknown keys: exit %d, stderr %q", code, errs)
	}
}

func TestEventsUsageWithoutXML(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	for _, flags := range [][]string{
		{"--fields", "t"}, {"--run", "1"}, {"--run-kind", "shot"}, {"--changes"}, {"--format", "csv"},
	} {
		args := append([]string{"events", "--dir", dir}, flags...)
		code, out, errs := runCmd(t, args...)
		if code != 2 || !strings.Contains(errs, flags[0]) || !strings.Contains(errs, "vendor XML") {
			t.Errorf("%q: exit %d, stderr %q; want 2 naming the flag", flags, code, errs)
		}
		if out != "" {
			t.Errorf("%q: output %q", flags, out)
		}
	}
}

func TestEventsXMLDirTwoNoName(t *testing.T) {
	noEnv(t)
	x, err := os.ReadFile(synth)
	if err != nil {
		t.Fatal(err)
	}
	xmls := t.TempDir()
	for _, n := range []string{"a.xml", "b.xml"} {
		if err := os.WriteFile(filepath.Join(xmls, n), x, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	capture := t.TempDir()
	line := fmt.Sprintf("{\"t\":0,\"dir\":\"rx\",\"data\":%q}\n", base64.StdEncoding.EncodeToString(append(testReport(), '\r', '\n')))
	if err := os.WriteFile(filepath.Join(capture, "log.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runCmd(t, "--xml", xmls, "events", capture)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	want := "levalog: " + filepath.Join(capture, "log.jsonl") + ": no XML name in the log and 2 XMLs in " + xmls + "\n"
	if errs != want {
		t.Errorf("stderr = %q, want %q", errs, want)
	}
	if !strings.Contains(out, " report   {") || strings.Contains(out, "temperature_pid1") {
		t.Errorf("want the raw report:\n%s", out)
	}
}

func TestHelpExplainsColumns(t *testing.T) {
	noEnv(t)
	for _, args := range [][]string{{"--help"}, {"events", "--help"}, {"runs", "--help"}, {"fields", "--help"}} {
		code, _, errs := runCmd(t, args...)
		if code != 0 {
			t.Errorf("%q: exit %d", args, code)
		}
		for _, w := range []string{"src", "run_t", "medewi adds", "fields", "runs", "events", "UTC", "directory of NAME.xml", "T WALL DIR EVENT", "# xml: NAME", "PEAK_BAR", "do not change the exit code", "needs --run or --run-kind", "--every 1d starts at 00:00 UTC", "negative t"} {
			if !strings.Contains(errs, w) {
				t.Errorf("%q: help does not mention %q", args, w)
			}
		}
	}
}

func TestEventsFixtureCSV(t *testing.T) {
	xml := os.Getenv("MEDEWI_XML_PATH")
	if xml == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	noEnv(t)
	code, out, errs := runCmd(t, "--xml", xml, "events", "--run", "2", "--format", "csv",
		"--fields", "run_t,pump,pressure", "../../testdata/fixtures/shot-genlever")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 3 || lines[0] != "t,wall,src,run,run_t,pump,pressure" {
		t.Fatalf("csv:\n%s", out[:min(len(out), 300)])
	}
	row := strings.Split(lines[1], ",")
	if row[3] != "2" || row[4] != "0" || row[5] != "1" {
		t.Errorf("first row = %q, want run 2, run_t 0 and pump 1", lines[1])
	}
}

func TestEventsSelectionFlags(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	_, out, _ := runCmd(t, "--xml", synth, "events", "--dir", dir, "--event", "note")
	if n := strings.Count(out, " note "); n != 6 || strings.Contains(out, " report ") {
		t.Errorf("--event note:\n%s", out)
	}
	// Both reports carry the same values: --changes keeps the first.
	_, out, _ = runCmd(t, "--xml", synth, "events", "--dir", dir, "--changes", "--fields", "pump,temperature_pid1")
	if n := strings.Count(out, " report "); n != 1 || !strings.Contains(out, " report   pump=* temperature_pid1=93.5\n") {
		t.Errorf("--changes:\n%s", out)
	}
}

func TestEventsExtendCap(t *testing.T) {
	noEnv(t)
	xml := runXML(t)
	// One report per file: the shot spans 40 files.
	dir := runDir(t, 250*time.Millisecond, concat(idleSteps(4), pumpRun(40, "9.00")))
	code, out, errs := runCmd(t, "--xml", xml, "events", "--dir", dir, "--event", "report",
		"-u", "2026-11-10 10:00:01.5 UTC", "--run", "1")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if !strings.Contains(errs, "levalog: a run is still open after 24 more raw logs; stopped reading at ") {
		t.Errorf("stderr = %q, want the cap warning", errs)
	}
	// Files up to 10:00:01.25 hold reports 0..5 (run from 1 s), the next
	// file is read for detection, then 24 more.
	if n := strings.Count(out, " report "); n != 2+1+24 {
		t.Errorf("reports = %d, want 27", n)
	}
}

// peakHeap runs levalog with args, output discarded, and returns the peak
// heap in use above what was in use before, sampled every millisecond.
func peakHeap(t *testing.T, args ...string) uint64 {
	t.Helper()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base, peak := ms.HeapInuse, ms.HeapInuse
	done := make(chan struct{})
	sampled := make(chan uint64)
	go func() {
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		var ms runtime.MemStats
		for {
			select {
			case <-done:
				sampled <- peak
				return
			case <-tick.C:
				runtime.ReadMemStats(&ms)
				peak = max(peak, ms.HeapInuse)
			}
		}
	}()
	var errb bytes.Buffer
	code := run(args, io.Discard, &errb)
	close(done)
	p := <-sampled
	if code != 0 {
		t.Fatalf("%q: exit %d, stderr %q", args, code, errb.String())
	}
	return p - min(p, base)
}

func TestMemoryBoundedByFile(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 24 raw logs")
	}
	noEnv(t)
	// The run fields plus 50 string fields: reports as heavy as real ones.
	xml := filepath.Join(t.TempDir(), "heavy.xml")
	var x strings.Builder
	x.WriteString(`<cfg version="4"><values>
<value at="7" len="5" export="Pressure" type="float" unit="bar"></value>
<value at="13" len="5" export="Pressure SP" type="float" unit="bar"></value>
<value at="19" len="5" export="Volume" type="float" unit="ml"></value>
`)
	for i := range 50 {
		fmt.Fprintf(&x, "<value at=\"%d\" len=\"4\" export=\"Aux %d\" type=\"string\"></value>\n", 30+5*i, i)
	}
	x.WriteString("</values></cfg>\n")
	if err := os.WriteFile(xml, []byte(x.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	// 2000 reports per file (500 s at 4/s), a shot every 140 reports.
	const perFile = 2000
	var day []step
	for len(day) < 24*perFile {
		day = append(day, concat(idleSteps(80), pumpRun(40, "9.00"))...)
	}
	few := runDir(t, perFile*250*time.Millisecond, day[:2*perFile])
	many := runDir(t, perFile*250*time.Millisecond, day[:24*perFile])
	for _, args := range [][]string{
		{"runs"},
		{"events", "--format", "jsonl"},
		{"events", "--format", "csv", "--run-kind", "shot"},
		{"events", "--event", "report"},
	} {
		a := append([]string{"--xml", xml}, args...)
		pf := peakHeap(t, append(a, "--dir", few)...)
		pm := peakHeap(t, append(a, "--dir", many)...)
		t.Logf("%q: peak heap %d MB for 2 files, %d MB for 24", args, pf>>20, pm>>20)
		if pm > 64<<20 || pm > pf+8<<20 {
			t.Errorf("%q: peak heap %d MB for 24 files vs %d MB for 2: grows with the input", args, pm>>20, pf>>20)
		}
	}
}

// statsXML is runXML's spec plus a mode text field at offset 4.
func statsXML(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stats.xml")
	const x = `<cfg version="4"><values>
<value at="4" len="3" type="string">1:%</value>
<value at="7" len="5" export="Pressure" type="float" unit="bar"></value>
<value at="13" len="5" export="Pressure SP" type="float" unit="bar"></value>
<value at="19" len="5" export="Volume" type="float" unit="ml"></value>
</values></cfg>
`
	if err := os.WriteFile(p, []byte(x), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// statsCapture writes a capture with one shot whose pressure rises to
// 9.50 bar at its 7th pumping report and then falls; reports every 250 ms.
func statsCapture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	k := 0
	add := func(pump byte, bar float64, sp string, volume float64) {
		r := runReport(step{pump: pump, sp: sp, volume: volume})
		copy(r[4:], "PID")
		copy(r[7:], fmt.Sprintf("%5.2f", bar))
		fmt.Fprintf(&b, "{\"t\":%d,\"dir\":\"rx\",\"data\":%q}\n", k*250,
			base64.StdEncoding.EncodeToString(append(r, '\r', '\n')))
		k++
	}
	for range 8 {
		add('-', 0, "", 0)
	}
	for i, bar := range []float64{1, 2, 4, 6, 8, 9, 9.5, 9.2, 9, 8.5, 8, 7} {
		add('*', bar, "9.00", float64(i))
	}
	for range 16 {
		add('-', 0, "", 11)
	}
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunsStats(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), statsCapture(t)
	code, out, errs := runCmd(t, "--xml", xml, "runs", "--stats", "pressure", "--agg", "max", capture)
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	ls := strings.Split(strings.TrimSpace(out), "\n")
	if len(ls) != 2 {
		t.Fatalf("output:\n%s", out)
	}
	h, r := strings.Fields(ls[0]), strings.Fields(ls[1])
	if h[len(h)-1] != "pressure_max" || r[len(r)-1] != "9.5" || len(h) != len(r) {
		t.Errorf("output:\n%s", out)
	}

	code, out, errs = runCmd(t, "--xml", xml, "runs", "--format", "jsonl", "--stats", "pressure,pump",
		"--agg", "n,last", capture)
	if code != 0 || errs != "" {
		t.Fatalf("pump: exit %d, stderr %q", code, errs)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(out), &row); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if row["pressure_n"] != 12.0 || row["pressure_last"] != 7.0 || row["pump_n"] != 12.0 || row["pump_last"] != "*" {
		t.Errorf("jsonl: %s", out)
	}
	code, out, errs = runCmd(t, "--xml", xml, "runs", "--format", "csv", "--stats", "pressure",
		"--agg", "tmax,srcmax", capture)
	if code != 0 || !strings.HasSuffix(strings.TrimSpace(out), ",1.5,"+filepath.Join(capture, "log.jsonl")+":15") {
		t.Errorf("csv: exit %d, stderr %q\n%s", code, errs, out)
	}
}

// A capture's run START is placed by the manifest's recorded time, taken
// when levacap wrote the capture, just after its last event.
func TestRunsCaptureStart(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), statsCapture(t)
	_, out, _ := runCmd(t, "--xml", xml, "runs", "--format", "csv", capture)
	if row := strings.Split(strings.Split(out, "\n")[1], ","); row[2] != "" {
		t.Errorf("no manifest: start %q, want empty", row[2])
	}
	m := `{"scenario":"shot","recorded":"2026-11-02T08:46:33.5+01:00"}`
	if err := os.WriteFile(filepath.Join(capture, "manifest.json"), []byte(m), 0o600); err != nil {
		t.Fatal(err)
	}
	// Last event at 8.75 s, the run's first sample at 2 s.
	code, out, errs := runCmd(t, "--xml", xml, "runs", "--format", "jsonl", capture)
	if code != 0 || !strings.Contains(out, `"start":"2026-11-02T07:46:26.750Z","t":2,`) {
		t.Errorf("exit %d, stderr %q\n%s", code, errs, out)
	}
	_, out, _ = runCmd(t, "--xml", xml, "runs", capture)
	if !strings.Contains(out, " 2026-11-02 ") {
		t.Errorf("text START without a date:\n%s", out)
	}
}

func TestRunsStatsUsage(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), statsCapture(t)
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--agg", "max"}, 2, "--agg needs --stats"},
		{[]string{"--stats", "nosuch"}, 2, "levalog fields"},
		{[]string{"--stats", "src"}, 2, "src"},
		{[]string{"--stats", "pressure", "--agg", "avg"}, 2, "tmax"},
		{[]string{"--stats", "mode", "--agg", "last"}, 0, ""},
	} {
		args := append(append([]string{"--xml", xml, "runs"}, tc.args...), capture)
		code, _, errs := runCmd(t, args...)
		if code != tc.code || !strings.Contains(errs, tc.want) || (tc.code == 0 && errs != "") {
			t.Errorf("%q: exit %d, stderr %q; want %d naming %q", tc.args, code, errs, tc.code, tc.want)
		}
	}
	// A text field with a numeric aggregate gets only key_last, and a note.
	code, out, errs := runCmd(t, "--xml", xml, "runs", "--format", "csv", "--stats", "pressure,pump,mode",
		"--agg", "n,max", capture)
	if code != 0 || errs != "levalog: pump is text: only pump_last\nlevalog: mode is text: only mode_last\n" {
		t.Errorf("text with max: exit %d, stderr %q", code, errs)
	}
	if h := strings.SplitN(out, "\n", 2)[0]; !strings.HasSuffix(h, ",src,pressure_n,pressure_max,pump_last,mode_last") {
		t.Errorf("text with max: header %q", h)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), ",12,9.5,*,PID") {
		t.Errorf("text with max:\n%s", out)
	}
}

func TestRunsStatsGenlever(t *testing.T) {
	xml := os.Getenv("MEDEWI_XML_PATH")
	if xml == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	noEnv(t)
	code, out, errs := runCmd(t, "--xml", xml, "runs", "--kind", "shot", "--format", "jsonl",
		"--stats", "pressure", "--agg", "n,max,tmax,srcmax", "../../testdata/fixtures/shot-genlever")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.Split(out, "\n")[0]), &row); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if row["pressure_max"] == nil || row["pressure_max"] != row["peak_bar"] {
		t.Errorf("pressure_max %v, peak_bar %v", row["pressure_max"], row["peak_bar"])
	}
	want := map[string]any{
		"run": 2.0, "pressure_n": 176.0, "pressure_max": 9.01, "pressure_tmax": 10.07,
		"pressure_srcmax": "../../testdata/fixtures/shot-genlever/log.jsonl:1815",
	}
	for k, v := range want {
		if row[k] != v {
			t.Errorf("%s = %v, want %v", k, row[k], v)
		}
	}
}

func TestEventsEvery(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), statsCapture(t)
	code, out, errs := runCmd(t, "--xml", xml, "events", "--every", "1s", "--fields", "pressure", "--agg", "max", capture)
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		got = append(got, strings.Join(strings.Fields(l), " "))
	}
	want := []string{
		"# " + filepath.Join(capture, "log.jsonl"),
		"t wall reports pressure_max",
		"0.000 - 4 0", "1.000 - 4 0", "2.000 - 4 6", "3.000 - 4 9.5", "4.000 - 4 9",
		"5.000 - 4 0", "6.000 - 4 0", "7.000 - 4 0", "8.000 - 4 0",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Default: plain keys, last value; duplicate keys collapse.
	code, out, errs = runCmd(t, "--xml", xml, "events", "--format", "csv", "--every", "2s",
		"--fields", "pressure,mode,pressure", "--run", "1", capture)
	if code != 0 || errs != "" {
		t.Fatalf("csv: exit %d, stderr %q", code, errs)
	}
	ls := strings.Split(strings.TrimSpace(out), "\n")
	logPath := filepath.Join(capture, "log.jsonl")
	if ls[0] != "t,wall,src,run,run_t,reports,pressure,mode" || ls[1] != "2,,"+logPath+",1,0,8,9.2,PID" || len(ls) != 3 {
		t.Errorf("csv:\n%s", out)
	}

	// No --fields: pump and every XML key, text fields only as _last.
	code, out, errs = runCmd(t, "--xml", xml, "events", "--format", "jsonl", "--every", "10s", "--agg", "max", capture)
	if code != 0 || errs != "" {
		t.Fatalf("jsonl: exit %d, stderr %q", code, errs)
	}
	want1 := `{"t":0,"wall":null,"src":"` + logPath + `","reports":36,"pump_last":"-","mode_last":"PID","pressure_max":9.5,"pressure_sp_max":9,"volume_max":11}`
	if strings.TrimSpace(out) != want1 {
		t.Errorf("jsonl:\n%s\nwant\n%s", out, want1)
	}
}

// Rows of several captures name their source: text with a "# PATH" line
// per table, jsonl and csv with src.
func TestEventsEverySources(t *testing.T) {
	noEnv(t)
	xml, a, b := statsXML(t), statsCapture(t), statsCapture(t)
	pa, pb := filepath.Join(a, "log.jsonl"), filepath.Join(b, "log.jsonl")
	code, out, errs := runCmd(t, "--xml", xml, "events", "--every", "10s", "--fields", "pressure", a, b)
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		got = append(got, strings.Join(strings.Fields(l), " "))
	}
	want := []string{"# " + pa, "t wall reports pressure", "0.000 - 36 0", "# " + pb, "t wall reports pressure", "0.000 - 36 0"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("text:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	_, out, _ = runCmd(t, "--xml", xml, "events", "--every", "10s", "--fields", "pressure", "--format", "csv", a, b)
	if want := "t,wall,src,reports,pressure\n0,," + pa + ",36,0\n0,," + pb + ",36,0\n"; out != want {
		t.Errorf("csv:\n%s\nwant\n%s", out, want)
	}
}

func TestEventsEveryUsage(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), statsCapture(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--every", "1s", "--event", "notice"}, "--event"},
		{[]string{"--every", "1s", "--changes"}, "--changes"},
		{[]string{"--agg", "max"}, "--every"},
		{[]string{"--every", "0"}, "--every"},
		{[]string{"--every", "1s", "--fields", "t"}, "t"},
		{[]string{"--every", "1s", "--agg", "avg"}, "tmax"},
		{[]string{"--every", "1s", "--show-dumps"}, "--show-dumps"},
	} {
		args := append(append([]string{"--xml", xml, "events"}, tc.args...), capture)
		code, _, errs := runCmd(t, args...)
		if code != 2 || !strings.Contains(errs, tc.want) {
			t.Errorf("%q: exit %d, stderr %q; want 2 naming %q", tc.args, code, errs, tc.want)
		}
	}
	code, _, errs := runCmd(t, "events", "--every", "1s", capture)
	if code != 2 || !strings.Contains(errs, "--every") {
		t.Errorf("without XML: exit %d, stderr %q", code, errs)
	}
	code, _, errs = runCmd(t, "--xml", xml, "events", "--every", "1s", "--event", "report", capture)
	if code != 0 || errs != "" {
		t.Errorf("--event report: exit %d, stderr %q", code, errs)
	}
	// A text field with a numeric aggregate gets only key_last, and a note.
	code, out, errs := runCmd(t, "--xml", xml, "events", "--format", "csv", "--every", "10s",
		"--fields", "mode,pressure", "--agg", "max", capture)
	if code != 0 || errs != "levalog: mode is text: only mode_last\n" {
		t.Errorf("mode with max: exit %d, stderr %q", code, errs)
	}
	if h := strings.SplitN(out, "\n", 2)[0]; h != "t,wall,src,reports,mode_last,pressure_max" {
		t.Errorf("mode with max: header %q", h)
	}
}

func TestEventsEveryGenlever(t *testing.T) {
	xml := os.Getenv("MEDEWI_XML_PATH")
	if xml == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	noEnv(t)
	code, out, errs := runCmd(t, "--xml", xml, "events", "--run", "2", "--every", "1s", "--fields", "pressure",
		"../../testdata/fixtures/shot-genlever")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	ls := strings.Split(strings.TrimSpace(out), "\n")
	if ls[0] != "# ../../testdata/fixtures/shot-genlever/log.jsonl" || !strings.HasPrefix(ls[1], "# xml: ") {
		t.Errorf("comments %q", ls[:2])
	}
	ls = ls[2:]
	if got := strings.Join(strings.Fields(ls[0]), " "); got != "t wall run run_t reports pressure" {
		t.Errorf("header %q", got)
	}
	if got := len(ls) - 1; got != 45 {
		t.Errorf("rows = %d, want 45", got)
	}
	if got := strings.Join(strings.Fields(ls[1]), " "); got != "108.384 - 2 0.000 4 0.64" {
		t.Errorf("first row %q", got)
	}
}

func TestRunsStatsDedup(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), statsCapture(t)
	code, out, errs := runCmd(t, "--xml", xml, "runs", "--format", "csv", "--stats", "pressure,pressure", "--agg", "max", capture)
	if code != 0 || errs != "" || !strings.HasSuffix(strings.Split(out, "\n")[0], ",src,pressure_max") {
		t.Errorf("exit %d, stderr %q\n%s", code, errs, out)
	}
}

// A repeated aggregate is one column: the JSON stays valid.
func TestAggDedup(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), statsCapture(t)
	for _, args := range [][]string{
		{"runs", "--stats", "pressure"},
		{"events", "--every", "10s", "--fields", "pressure"},
	} {
		args = append(append([]string{"--xml", xml}, args...), "--format", "jsonl", "--agg", "max,max", capture)
		code, out, errs := runCmd(t, args...)
		var row map[string]any
		if code != 0 || errs != "" || json.Unmarshal([]byte(out), &row) != nil || row["pressure_max"] != 9.5 ||
			strings.Count(out, "pressure_max") != 1 {
			t.Errorf("%q: exit %d, stderr %q\n%s", args, code, errs, out)
		}
	}
}

// Without --fields, --every takes the keys of every XML of the input, in
// first-seen order, mode after pump.
func TestEventsEveryKeysOfAllXMLs(t *testing.T) {
	noEnv(t)
	xmls := t.TempDir()
	for n, src := range map[string]string{"a.xml": runXML(t), "b.xml": statsXML(t)} {
		x, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(xmls, n), x, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	capture := t.TempDir()
	var b strings.Builder
	for k, data := range [][]byte{
		[]byte("XML=a\r\n"), append(runReport(step{pump: '-'}), '\r', '\n'),
		[]byte("XML=b\r\n"), append(runReport(step{pump: '-'}), '\r', '\n'),
	} {
		fmt.Fprintf(&b, "{\"t\":%d,\"dir\":\"rx\",\"data\":%q}\n", k*250, base64.StdEncoding.EncodeToString(data))
	}
	if err := os.WriteFile(filepath.Join(capture, "log.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runCmd(t, "--xml", xmls, "events", "--format", "csv", "--every", "10s", capture)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if h := strings.SplitN(out, "\n", 2)[0]; h != "t,wall,src,reports,pump,mode,pressure,pressure_sp,volume" {
		t.Errorf("header %q", h)
	}
}

// summaryCapture is statsCapture with a banner after the last report.
func summaryCapture(t *testing.T) string {
	t.Helper()
	dir := statsCapture(t)
	f, err := os.OpenFile(filepath.Join(dir, "log.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(f, "{\"t\":9100,\"dir\":\"rx\",\"data\":%q}\n",
		base64.StdEncoding.EncodeToString([]byte("$id: leva! (3.2)\r\n")))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSummaryCmd(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), summaryCapture(t)
	code, out, errs := runCmd(t, "--xml", xml, "summary", capture)
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	for _, w := range []string{
		"src: " + filepath.Join(capture, "log.jsonl") + "\n",
		"kind: capture\n",
		"firmware: $id: leva! (3.2)\n",
		"events: report=36 banner=1\n",
		"modes: PID=8.75\n",
		"runs: shot=1 flush=0 draw=0 (1)\n",
		"gaps: 0\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}

	code, out, errs = runCmd(t, "summary", "--format", "jsonl", capture)
	if code != 0 || !strings.HasPrefix(errs, "levalog: ") || !strings.Contains(errs, "XML") {
		t.Fatalf("no XML: exit %d, stderr %q", code, errs)
	}
	var o map[string]any
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if o["firmware"] != "$id: leva! (3.2)" || fmt.Sprint(o["modes"], o["runs"], o["masked"]) != "map[] map[] []" {
		t.Errorf("no XML: %s", out)
	}
	if ev, _ := o["events"].(map[string]any); ev["report"] != 36.0 {
		t.Errorf("no XML: events %v", o["events"])
	}
}

func TestSummaryGenlever(t *testing.T) {
	xml := os.Getenv("MEDEWI_XML_PATH")
	if xml == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	noEnv(t)
	code, out, errs := runCmd(t, "--xml", xml, "summary", "../../testdata/fixtures/shot-genlever")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	for _, w := range []string{
		"runs: shot=1 flush=1 draw=0 (1-2)\n",
		"events: report=644 cmd=3 reply=1 banner=1\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestSummaryWindow(t *testing.T) {
	noEnv(t)
	dir := rawDir(t)
	code, out, errs := runCmd(t, "--xml", synth, "summary", "--dir", dir, "--format", "jsonl",
		"-s", "2026-11-10 10:00:05 UTC", "-u", "2026-11-10 10:00:15 UTC")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("want one record:\n%s", out)
	}
	var o map[string]any
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if !strings.Contains(o["src"].(string), "20261110T100000") || o["start"] != "2026-11-10T10:00:10.000Z" || o["duration"] != 0.0 {
		t.Errorf("record: %s", out)
	}
	if ev := fmt.Sprint(o["events"]); ev != "map[note:1 report:1]" {
		t.Errorf("events = %s", ev)
	}
}

// A run open at the end of the window is read on into later raw logs by
// summary as by runs, so both classify it alike: its setpoint shows only
// after the window, which makes it a shot, not a flush.
func TestSummaryRunOpenAtEnd(t *testing.T) {
	noEnv(t)
	xml := runXML(t)
	steps := idleSteps(4)
	for i := range 12 {
		sp := "9.00"
		if i < 4 {
			sp = ""
		}
		steps = append(steps, step{pump: '*', sp: sp, volume: float64(i)})
	}
	dir := runDir(t, 250*time.Millisecond, concat(steps, idleSteps(8))) // one report per file
	until := "2026-11-10 10:00:01.5 UTC"
	code, out, errs := runCmd(t, "--xml", xml, "runs", "--dir", dir, "-u", until, "--format", "csv")
	if code != 0 || !strings.Contains(out, "\n1,shot,") {
		t.Fatalf("runs: exit %d, stderr %q\n%s", code, errs, out)
	}
	code, out, errs = runCmd(t, "--xml", xml, "summary", "--dir", dir, "-u", until)
	if code != 0 || errs != "" || !strings.Contains(out, "runs: shot=1 flush=0 draw=0 (1)\n") {
		t.Errorf("summary: exit %d, stderr %q\n%s", code, errs, out)
	}
}

// writeCapture writes a capture log.jsonl into dir/name with one report.
func writeCapture(t *testing.T, dir, name string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("{\"t\":0,\"dir\":\"rx\",\"data\":%q}\n", base64.StdEncoding.EncodeToString(append(testReport(), '\r', '\n')))
	if err := os.WriteFile(filepath.Join(d, "log.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureParentDir(t *testing.T) {
	noEnv(t)
	parent := t.TempDir()
	writeCapture(t, parent, "b")
	writeCapture(t, parent, "a")
	if err := os.MkdirAll(filepath.Join(parent, "c"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "c", "log.partial.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(parent, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runCmd(t, "--xml", synth, "events", parent)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	a, b := strings.Index(out, "# "+filepath.Join(parent, "a", "log.jsonl")+"\n"), strings.Index(out, "# "+filepath.Join(parent, "b", "log.jsonl")+"\n")
	if a < 0 || b < a {
		t.Errorf("want a then b:\n%s", out)
	}
	want := "levalog: skipped " + filepath.Join(parent, "c") + ": interrupted capture (log.partial.jsonl)\n" +
		"levalog: skipped " + filepath.Join(parent, "d") + ": not a capture\n"
	if errs != want {
		t.Errorf("stderr = %q, want %q", errs, want)
	}
	code, out, errs = runCmd(t, "--xml", synth, "summary", "--format", "jsonl", parent)
	if code != 0 || strings.Count(out, "\n") != 2 || errs != want {
		t.Errorf("summary: exit %d, stderr %q\n%s", code, errs, out)
	}
}

func TestNoLogsNamesDir(t *testing.T) {
	noEnv(t)
	empty, other := t.TempDir(), t.TempDir()
	want := "levalog: no logs in " + empty + " (no log.jsonl, no *.jsonl.gz, no capture subdirectories)\n"
	for _, sub := range []string{"events", "runs", "summary"} {
		code, out, errs := runCmd(t, "--xml", synth, sub, "--dir", other, empty)
		if code != 0 || out != "" || errs != want {
			t.Errorf("%s: exit %d, output %q, stderr %q, want %q", sub, code, out, errs, want)
		}
	}
	// Without arguments, --dir is the directory read.
	want = "levalog: no logs in " + other + " (no log.jsonl, no *.jsonl.gz, no capture subdirectories)\n"
	if code, _, errs := runCmd(t, "--xml", synth, "events", "--dir", other); code != 0 || errs != want {
		t.Errorf("--dir: exit %d, stderr %q, want %q", code, errs, want)
	}
}

// An empty directory among other inputs is named after the output.
func TestEmptyDirAmongOthers(t *testing.T) {
	noEnv(t)
	xml, capture, empty := statsXML(t), summaryCapture(t), t.TempDir()
	code, out, errs := runCmd(t, "--xml", xml, "summary", empty, capture)
	want := "levalog: no logs in " + empty + " (no log.jsonl, no *.jsonl.gz, no capture subdirectories)\n"
	if code != 0 || !strings.Contains(out, "src: "+filepath.Join(capture, "log.jsonl")+"\n") || errs != want {
		t.Errorf("exit %d, stderr %q, want %q\n%s", code, errs, want, out)
	}
	for _, sub := range []string{"fields", "runs", "events"} {
		code, out, errs := runCmd(t, "--xml", xml, sub, empty, capture)
		if code != 0 || out == "" || errs != want {
			t.Errorf("%s: exit %d, stderr %q, want %q\n%s", sub, code, errs, want, out)
		}
	}
}

func TestCaptureWithSubdirs(t *testing.T) {
	noEnv(t)
	dir := t.TempDir()
	writeCapture(t, dir, ".")
	writeCapture(t, dir, "sub")
	code, out, errs := runCmd(t, "--xml", synth, "events", dir)
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if !strings.HasPrefix(out, "# "+filepath.Join(dir, "log.jsonl")+"\n") || strings.Contains(out, "sub") {
		t.Errorf("want the capture only:\n%s", out)
	}
}

func TestEventsEmptyReason(t *testing.T) {
	noEnv(t)
	xml, capture := statsXML(t), statsCapture(t)
	code, out, errs := runCmd(t, "--xml", xml, "events", "--event", "notice", capture)
	want := "levalog: 36 events read; --event notice: 36 dropped; 0 left\n"
	if code != 0 || strings.Contains(out, "notice") || errs != want {
		t.Errorf("exit %d, stderr %q, want %q\n%s", code, errs, want, out)
	}
	code, out, errs = runCmd(t, "--xml", xml, "events", "--every", "1s", "--run-kind", "draw", capture)
	want = "levalog: 36 events read; --run-kind draw: 36 dropped; --every (reports only): 0 dropped; --every (no standby): 0 dropped; 0 left\n"
	if code != 0 || strings.Count(out, "\n") > 1 || errs != want {
		t.Errorf("--every: exit %d, stderr %q, want %q\n%s", code, errs, want, out)
	}
	// --fields repeats a key once, as --every and --stats do.
	_, out, _ = runCmd(t, "--xml", xml, "events", "--fields", "pressure,pressure", "--event", "report", capture)
	if l := strings.SplitN(out, "\n", 3)[1]; strings.Count(l, "pressure=") != 1 {
		t.Errorf("--fields pressure,pressure: %q", l)
	}
}

func TestRunsEmptyReason(t *testing.T) {
	noEnv(t)
	xml := statsXML(t)
	idle := t.TempDir()
	var b strings.Builder
	for k := range 10 {
		fmt.Fprintf(&b, "{\"t\":%d,\"dir\":\"rx\",\"data\":%q}\n", k*250,
			base64.StdEncoding.EncodeToString(append(runReport(step{pump: '-'}), '\r', '\n')))
	}
	if err := os.WriteFile(filepath.Join(idle, "log.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runCmd(t, "--xml", xml, "runs", idle)
	if want := "levalog: no runs in 10 reports (pump marker * in 0)\n"; code != 0 || errs != want {
		t.Errorf("idle: exit %d, stderr %q, want %q\n%s", code, errs, want, out)
	}
	code, out, errs = runCmd(t, "--xml", xml, "runs", "--kind", "draw", statsCapture(t))
	if want := "levalog: no runs of kind draw (of 1 runs)\n"; code != 0 || errs != want {
		t.Errorf("--kind draw: exit %d, stderr %q, want %q\n%s", code, errs, want, out)
	}
	// An XML directory without the session's XML decodes no report.
	xmls := t.TempDir()
	code, _, errs = runCmd(t, "--xml", xmls, "runs", idle)
	if want := "levalog: no runs: no report decoded (no XML for a session that names none)\n"; code != 0 || !strings.HasSuffix(errs, want) {
		t.Errorf("no XML: exit %d, stderr %q, want suffix %q", code, errs, want)
	}
}

func TestUsageExamples(t *testing.T) {
	noEnv(t)
	code, _, errs := runCmd(t, "--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, w := range []string{"Examples:", "summary captures/", "--event notice", "--stats pressure", "--every 1s"} {
		if !strings.Contains(errs, w) {
			t.Errorf("help does not mention %q", w)
		}
	}
	_, ex, ok := strings.Cut(errs, "Examples:\n")
	if !ok {
		t.Fatal("no Examples: section")
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields("pressure temperature_pid1 setpoint_pid heat_pid heat_pid_average pressure_sp pressure_band_min pressure_band_max mode pump t wall src src_end run run_t xml dir event") {
		allowed[k] = true
	}
	lines := 0
	for _, l := range strings.Split(strings.TrimRight(ex, "\n"), "\n") {
		if !strings.HasPrefix(l, "  levalog ") || !strings.Contains(l, " # ") {
			t.Errorf("example line %q: want a command with its comment on one line", l)
			continue
		}
		lines++
		f := strings.Fields(strings.SplitN(l, "#", 2)[0])
		for i, a := range f {
			if (a == "--fields" || a == "--stats") && i+1 < len(f) {
				for _, k := range strings.Split(f[i+1], ",") {
					if !allowed[k] {
						t.Errorf("example %q names key %q outside the allowed set", l, k)
					}
				}
			}
		}
	}
	if lines < 6 || lines > 8 {
		t.Errorf("%d example command lines, want 6 to 8", lines)
	}
}
