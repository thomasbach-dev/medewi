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
		errs != "levalog: no run 99 in this input\n" {
		t.Errorf("--run 99: exit %d, output %q, stderr %q", code, out, errs)
	}
	if _, out, errs := runCmd(t, "--xml", xml, "events", "--dir", dir, "--run", "2,7-9"); out == "" ||
		errs != "levalog: no run 7-9 in this input\n" {
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
	want := "t,wall,src,pump,temperature_pid1,heat_pid\n10,2026-11-10T10:00:10.000Z," + files[0] + ":3,1,93.5,50\n"
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
		for _, w := range []string{"src", "run_t", "medewi adds", "fields", "runs", "events", "UTC", "directory of NAME.xml", "T WALL DIR EVENT", "# xml: NAME", "PEAK_BAR", "do not change the exit code", "needs --run or --run-kind"} {
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
	if len(lines) < 3 || lines[0] != "run_t,pump,pressure" {
		t.Fatalf("csv:\n%s", out[:min(len(out), 300)])
	}
	row := strings.Split(lines[1], ",")
	if row[0] != "0" || row[1] != "1" {
		t.Errorf("first row = %q, want run_t 0 and pump 1", lines[1])
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
