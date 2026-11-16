package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// cet stands in for Europe/Berlin in November (UTC+1) without tzdata.
var cet = time.FixedZone("CET", 3600)

// inCET sets time.Local to cet for the test. Every test that passes
// winArgs or other local-time timestamps calls it, so it runs the same in
// any TZ (CI and the flake sandbox run in UTC).
func inCET(t *testing.T) {
	t.Helper()
	old := time.Local
	time.Local = cet
	t.Cleanup(func() { time.Local = old })
}

// writeRaw writes events as a gzip raw log named like rawlog names a file
// starting at start.
func writeRaw(t *testing.T, dir string, start time.Time, evs ...fixture.Event) {
	t.Helper()
	p := filepath.Join(dir, start.UTC().Format("20060102T150405.000Z")+".jsonl.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := fixture.WriteLog(gz, evs); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ev(t int64, d fixture.Dir, s string) fixture.Event {
	return fixture.Event{T: t, Dir: d, Data: []byte(s)}
}

const xmlName = "leva!-TEST"

var (
	// One session: it starts at f1 with the banner, rotates at f2 and f3.
	f1 = time.Date(2026, 11, 14, 5, 5, 52, 646e6, time.UTC)
	f2 = f1.Add(time.Hour)
	f3 = f2.Add(time.Hour) // 20261114T070552.646Z
	// The window, 08:58:30 to 08:59:00 local, lies in f3.
	winFrom = time.Date(2026, 11, 14, 7, 58, 30, 0, time.UTC)
)

// ms is the offset of at from file start f, in milliseconds.
func ms(f, at time.Time) int64 { return at.Sub(f).Milliseconds() }

// extractDir writes the session's three files into a new directory.
func extractDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeRaw(t, dir, f1,
		ev(0, fixture.RX, "$id: leva! (3.2)\r\n"),
		ev(200, fixture.TX, "MC?\r\n"),
		ev(400, fixture.Note, "xml: "+xmlName),
		ev(1000, fixture.RX, "{early}\r\n"))
	writeRaw(t, dir, f2,
		ev(0, fixture.Note, "xml: "+xmlName),
		ev(1000, fixture.RX, "{mid}\r\n"))
	writeRaw(t, dir, f3,
		ev(0, fixture.Note, "xml: "+xmlName),
		ev(ms(f3, winFrom)-500, fixture.RX, "{before}\r\n"),
		ev(ms(f3, winFrom)+100, fixture.RX, "{a}\r\n"),
		ev(ms(f3, winFrom)+300, fixture.TX, "MC@"),
		ev(ms(f3, winFrom)+400, fixture.RX, "<frame>\r\n"),
		ev(ms(f3, winFrom)+2000, fixture.Note, "S Backflush"),
		ev(ms(f3, winFrom)+5000, fixture.RX, "{b}\r\n"),
		ev(ms(f3, winFrom)+40000, fixture.RX, "{after}\r\n"))
	return dir
}

// wantCut is the cut of extractDir's window.
func wantCut() []fixture.Event {
	return []fixture.Event{
		ev(0, fixture.Note, "xml: "+xmlName),
		ev(0, fixture.RX, "{a}\r\n"),
		ev(200, fixture.TX, "MC@"),
		ev(300, fixture.RX, "<frame>\r\n"),
		ev(1900, fixture.Note, "S Backflush"),
		ev(4900, fixture.RX, "{b}\r\n"),
	}
}

var winArgs = []string{"-s", "2026-11-14 08:58:30", "-u", "2026-11-14 08:59:00"}

func extractArgs(dir string, more ...string) []string {
	return append(append([]string{"extract", "--dir", dir}, winArgs...), more...)
}

func TestExtractWritesCapture(t *testing.T) {
	noEnv(t)
	inCET(t)
	out := filepath.Join(t.TempDir(), "backflush")
	code, stdout, stderr := runCmd(t, extractArgs(extractDir(t), "--out", out, "--scenario", "backflush", "--notes", "the backflush")...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	want := out + "/log.jsonl: 6 events (2 reports, 1 frame, 2 notes, 1 command, 0 other)\n" + out + "/manifest.json\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	f, err := os.Open(filepath.Join(out, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := fixture.ReadLog(f)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantCut()) {
		t.Errorf("events = %v, want %v", got, wantCut())
	}
	m, err := fixture.LoadManifest(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Scenario != "backflush" || m.MachineAddr != "machine.example:23" || m.XMLName != xmlName ||
		m.FirmwareVersion != "$id: leva! (3.2)" || m.XMLSHA256 != "" {
		t.Errorf("manifest = %+v", m)
	}
	if !m.Recorded.Equal(winFrom.Add(5000 * time.Millisecond)) {
		t.Errorf("recorded = %v, want the last event's wall time", m.Recorded)
	}
	wantNotes := "the backflush\nextracted from 20261114T070552.646Z.jsonl.gz 2026-11-14 08:58:30.100 +01:00 – 2026-11-14 08:58:35.000 +01:00 by levalog extract"
	if m.Notes != wantNotes {
		t.Errorf("notes = %q, want %q", m.Notes, wantNotes)
	}
	if strings.Contains(m.Notes, "/") {
		t.Errorf("notes name a directory: %q", m.Notes)
	}

	// Without --scenario and --notes.
	out2 := filepath.Join(t.TempDir(), "x")
	if code, _, stderr := runCmd(t, extractArgs(extractDir(t), "--out", out2)...); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	m, err = fixture.LoadManifest(filepath.Join(out2, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Scenario != "extract" || !strings.HasPrefix(m.Notes, "extracted from ") {
		t.Errorf("defaults: scenario %q notes %q", m.Scenario, m.Notes)
	}
}

func TestExtractStdout(t *testing.T) {
	noEnv(t)
	inCET(t)
	code, stdout, stderr := runCmd(t, extractArgs(extractDir(t), "--out", "-")...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var want bytes.Buffer
	if err := fixture.WriteLog(&want, wantCut()); err != nil {
		t.Fatal(err)
	}
	if stdout != want.String() {
		t.Errorf("stdout = %q, want %q", stdout, want.String())
	}
	if strings.Count(stderr, "no manifest with --out -") != 1 {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestExtractRefusesNonEmptyOut(t *testing.T) {
	noEnv(t)
	inCET(t)
	out := t.TempDir()
	keep := filepath.Join(out, "keep")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCmd(t, extractArgs(extractDir(t), "--out", out)...)
	if code != 1 || !strings.Contains(stderr, "not empty") {
		t.Errorf("exit %d, stderr %q; want 1, not empty", code, stderr)
	}
	if b, _ := os.ReadFile(keep); string(b) != "mine" {
		t.Errorf("file changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(out, "log.jsonl")); err == nil {
		t.Error("log.jsonl written")
	}

	// An existing empty directory is fine.
	empty := t.TempDir()
	if code, _, stderr := runCmd(t, extractArgs(extractDir(t), "--out", empty)...); code != 0 {
		t.Errorf("empty dir: exit %d, stderr %q", code, stderr)
	}
}

func TestExtractUsageErrors(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := extractDir(t)
	capDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(capDir, "log.jsonl"), []byte(`{"t":0,"dir":"rx","data":"e2F9DQo="}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "o")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no --out", extractArgs(dir), "--out"},
		{"no window", []string{"extract", "--dir", dir, "--out", out}, "--since"},
		{"until <= since", []string{"extract", "--dir", dir, "-s", "2026-11-14 08:59", "-u", "2026-11-14 08:59", "--out", out}, "--until"},
		{"capture input", []string{"extract", "--run", "1", "--out", out, capDir}, "capture"},
		{"capture input, window", append(append([]string{"extract", "--out", out}, winArgs...), capDir), "capture"},
		{"margin without window or run", []string{"extract", "--dir", dir, "--margin", "5s", "--out", out}, "--margin"},
		{"run without xml", extractArgs(dir, "--run", "1", "--out", out), "XML"},
		{"bad run", extractArgs(dir, "--run", "1,2", "--out", out), "--run"},
		{"format", extractArgs(dir, "--format", "csv", "--out", out), "format"},
	}
	for _, c := range cases {
		args := c.args
		if c.name == "capture input" {
			args = append([]string{"--xml", runXML(t)}, args...)
		}
		code, _, stderr := runCmd(t, args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, stderr %q; want 2 naming %s", c.name, code, stderr, c.want)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output written on a usage error")
	}
}

func TestExtractEmptyWindow(t *testing.T) {
	noEnv(t)
	inCET(t)
	out := filepath.Join(t.TempDir(), "o")
	code, _, stderr := runCmd(t, "extract", "--dir", extractDir(t),
		"-s", "2026-11-14 08:58:36", "-u", "2026-11-14 08:58:40", "--out", out)
	want := "no events in [2026-11-14 08:58:36.000, 2026-11-14 08:58:40.000) +01:00;"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1, %s", code, stderr, want)
	}
	// An open end reads as such, not as a far-future date.
	code, _, stderr = runCmd(t, "extract", "--dir", extractDir(t),
		"-s", "2026-11-14 10:00:00", "--out", out)
	want = "no events in [2026-11-14 10:00:00.000, open end) +01:00;"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Errorf("open end: exit %d, stderr %q; want 1, %s", code, stderr, want)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output directory created")
	}
}

func TestExtractSessionBoundary(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := extractDir(t)
	// A reconnect at 08:58:40 local starts a new session inside the window.
	writeRaw(t, dir, winFrom.Add(10*time.Second),
		ev(0, fixture.RX, "$id: leva! (3.2)\r\n"),
		ev(100, fixture.RX, "{new}\r\n"))
	out := filepath.Join(t.TempDir(), "o")
	code, _, stderr := runCmd(t, extractArgs(dir, "--out", out)...)
	want := "levalog: the window crosses 1 raw-log session boundary (2026-11-14 08:58:40.000 +01:00); extract each part:\n" +
		"  (a raw-log session is one connection of the daemon to the machine; a reconnect or restart starts a new one, and a fixture must lie in one; --split-sessions --out DIR cuts every part)\n" +
		"  -s '2026-11-14 08:58:30.000' -u '2026-11-14 08:58:40.000'\n" +
		"  -s '2026-11-14 08:58:40.000' -u '2026-11-14 08:59:00.000'\n"
	if code != 1 || !strings.Contains(stderr, want) || !strings.Contains(stderr, "--split-sessions") {
		t.Errorf("exit %d, stderr %q; want 1 with %q and --split-sessions", code, stderr, want)
	}
	if lines := strings.Split(stderr, "\n"); len(lines) < 2 || !strings.Contains(lines[1], "one connection of the daemon to the machine") || !strings.Contains(lines[1], "--split-sessions --out DIR") {
		t.Errorf("stderr %q; want line 2 to explain a session and name --split-sessions --out DIR", stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output directory created")
	}
}

// threeSessionDir is extractDir with reconnects at 08:58:40 and 08:58:50
// local, each a new session with its own firmware banner.
func threeSessionDir(t *testing.T) string {
	t.Helper()
	dir := extractDir(t)
	writeRaw(t, dir, winFrom.Add(10*time.Second),
		ev(0, fixture.RX, "$id: leva! (3.3)\r\n"),
		ev(100, fixture.RX, "{new}\r\n"))
	writeRaw(t, dir, winFrom.Add(20*time.Second),
		ev(0, fixture.RX, "$id: leva! (3.4)\r\n"),
		ev(100, fixture.RX, "{newer}\r\n"))
	return dir
}

func TestExtractSessionBoundaries(t *testing.T) {
	noEnv(t)
	inCET(t)
	out := filepath.Join(t.TempDir(), "o")
	code, _, stderr := runCmd(t, extractArgs(threeSessionDir(t), "--out", out)...)
	want := "levalog: the window crosses 2 raw-log session boundaries (2026-11-14 08:58:40.000 +01:00, 2026-11-14 08:58:50.000 +01:00); extract each part:\n" +
		"  (a raw-log session is one connection of the daemon to the machine; a reconnect or restart starts a new one, and a fixture must lie in one; --split-sessions --out DIR cuts every part)\n" +
		"  -s '2026-11-14 08:58:30.000' -u '2026-11-14 08:58:40.000'\n" +
		"  -s '2026-11-14 08:58:40.000' -u '2026-11-14 08:58:50.000'\n" +
		"  -s '2026-11-14 08:58:50.000' -u '2026-11-14 08:59:00.000'\n"
	if code != 1 || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1 with %q", code, stderr, want)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output directory created")
	}

	// An open end leaves the last part's -u out. That part also holds f3's
	// {after} (08:59:10), past the later sessions' starts: overlapping
	// files, tagged so.
	code, _, stderr = runCmd(t, "extract", "--dir", threeSessionDir(t), "-s", "2026-11-14 08:58:30", "--out", out)
	if code != 1 || !strings.Contains(stderr, "  -s '2026-11-14 08:58:50.000'  # overlapping sessions\n") {
		t.Errorf("open end: exit %d, stderr %q", code, stderr)
	}
}

func TestExtractSplitSessions(t *testing.T) {
	noEnv(t)
	inCET(t)
	out := filepath.Join(t.TempDir(), "o")
	code, stdout, stderr := runCmd(t, extractArgs(threeSessionDir(t), "--split-sessions", "--out", out, "--scenario", "split")...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	p := func(n, f string) string { return filepath.Join(out, n, f) }
	want := p("01", "log.jsonl") + ": 6 events (2 reports, 1 frame, 2 notes, 1 command, 0 other)\n" + p("01", "manifest.json") + "\n" +
		p("02", "log.jsonl") + ": 2 events (1 report, 0 frames, 0 notes, 0 commands, 1 other)\n" + p("02", "manifest.json") + "\n" +
		p("03", "log.jsonl") + ": 2 events (1 report, 0 frames, 0 notes, 0 commands, 1 other)\n" + p("03", "manifest.json") + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	for _, c := range []struct{ n, fw, from string }{
		{"01", "$id: leva! (3.2)", "20261114T070552.646Z.jsonl.gz 2026-11-14 08:58:30.100 +01:00"},
		{"02", "$id: leva! (3.3)", "20261114T075840.000Z.jsonl.gz 2026-11-14 08:58:40.000 +01:00"},
		{"03", "$id: leva! (3.4)", "20261114T075850.000Z.jsonl.gz 2026-11-14 08:58:50.000 +01:00"},
	} {
		m, err := fixture.LoadManifest(p(c.n, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if m.FirmwareVersion != c.fw || m.Scenario != "split" || !strings.Contains(m.Notes, "extracted from "+c.from) {
			t.Errorf("%s: manifest = %+v", c.n, m)
		}
	}

	// A window inside one session gives 01 only.
	out = filepath.Join(t.TempDir(), "o")
	code, stdout, stderr = runCmd(t, extractArgs(extractDir(t), "--split-sessions", "--out", out)...)
	if code != 0 || stdout != p("01", "log.jsonl")+": 6 events (2 reports, 1 frame, 2 notes, 1 command, 0 other)\n"+p("01", "manifest.json")+"\n" {
		t.Errorf("one session: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 1 {
		t.Errorf("one session: %d entries in --out, want 01 only", len(entries))
	}
}

// TestExtractSplitSessionsSkipsEmpty: a part without events is skipped
// with a note; the numbering of the written parts stays contiguous.
func TestExtractSplitSessionsSkipsEmpty(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := threeSessionDir(t)
	// An empty raw log at 08:58:45 local, between the two reconnects, is
	// a session of its own: part 3 is empty, part 4 is session 3's.
	writeRaw(t, dir, winFrom.Add(15*time.Second))
	out := filepath.Join(t.TempDir(), "o")
	code, stdout, stderr := runCmd(t, extractArgs(dir, "--split-sessions", "--out", out)...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "part 3 [2026-11-14 08:58:45.000, 2026-11-14 08:58:50.000) +01:00 has no events; skipped") {
		t.Errorf("stderr = %q, want the skipped part named", stderr)
	}
	if strings.Count(stdout, "manifest.json") != 3 || strings.Contains(stdout, filepath.Join(out, "04")) {
		t.Errorf("stdout = %q, want 01 to 03", stdout)
	}
	m, err := fixture.LoadManifest(filepath.Join(out, "03", "manifest.json"))
	if err != nil || m.FirmwareVersion != "$id: leva! (3.4)" {
		t.Errorf("03: manifest %+v, err %v; want session 3's", m, err)
	}
}

func TestExtractSplitSessionsUsage(t *testing.T) {
	noEnv(t)
	inCET(t)
	code, _, stderr := runCmd(t, extractArgs(threeSessionDir(t), "--split-sessions", "--out", "-")...)
	if code != 2 || !strings.Contains(stderr, "--split-sessions") {
		t.Errorf("exit %d, stderr %q; want 2 naming --split-sessions", code, stderr)
	}
}

// TestExtractSplitSessionsTooMany: more than 99 parts is refused, and
// nothing is written.
func TestExtractSplitSessionsTooMany(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := t.TempDir()
	for i := range 100 {
		writeRaw(t, dir, winFrom.Add(time.Duration(i)*100*time.Millisecond), ev(0, fixture.RX, "$id: leva! (3.2)\r\n"))
	}
	out := filepath.Join(t.TempDir(), "o")
	code, _, stderr := runCmd(t, extractArgs(dir, "--split-sessions", "--out", out)...)
	if code != 1 || !strings.Contains(stderr, "100 session parts") || !strings.Contains(stderr, "narrow it") {
		t.Errorf("exit %d, stderr %q; want 1 refusing 100 parts", code, stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("output directory created")
	}
}

// TestExtractSplitSessionsClamped:a window starting before the first raw
// log is cut to its start, and stderr says so.
func TestExtractSplitSessionsClamped(t *testing.T) {
	noEnv(t)
	inCET(t)
	out := filepath.Join(t.TempDir(), "o")
	code, _, stderr := runCmd(t, "extract", "--dir", threeSessionDir(t), "-s", "2026-11-14 05:00", "-u", "2026-11-14 08:59",
		"--split-sessions", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "the window starts before the first raw log; part 1 starts at 2026-11-14 06:05:52.646 +01:00") {
		t.Errorf("stderr = %q, want the clamp named", stderr)
	}
	// Without parts to name, a plain extract keeps quiet about it.
	code, _, stderr = runCmd(t, "extract", "--dir", extractDir(t), "-s", "2026-11-14 05:00", "-u", "2026-11-14 08:59", "--out", "-")
	if code != 0 || strings.Contains(stderr, "part 1") {
		t.Errorf("plain: exit %d, stderr %q", code, stderr)
	}
}

// TestExtractFirmwareMissing: FILE arguments are the input as given; the
// session's first file is not looked for beyond them.
func TestExtractFirmwareMissing(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := extractDir(t)
	f3Path := filepath.Join(dir, f3.Format("20060102T150405.000Z")+".jsonl.gz")
	code, stdout, stderr := runCmd(t, append(append([]string{"extract", "--out", "-"}, winArgs...), f3Path)...)
	if code != 0 || stdout == "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "firmware unknown") || !strings.Contains(stderr, "20261114T070552.646Z.jsonl.gz") {
		t.Errorf("stderr = %q, want the missing first file named", stderr)
	}
}

func TestExtractXMLSHA(t *testing.T) {
	noEnv(t)
	inCET(t)
	xdir := t.TempDir()
	content := []byte("<cfg version=\"4\"><values></values></cfg>\n")
	match := filepath.Join(xdir, xmlName+".xml")
	other := filepath.Join(xdir, "other.xml")
	for _, p := range []string{match, other} {
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])
	for _, c := range []struct {
		name, xml, want string
	}{
		{"matching file", match, want},
		{"directory", xdir, want},
		{"other file", other, ""},
		{"none", "", ""},
	} {
		out := filepath.Join(t.TempDir(), "o")
		var args []string
		if c.xml != "" {
			args = []string{"--xml", c.xml}
		}
		code, _, stderr := runCmd(t, append(args, extractArgs(extractDir(t), "--out", out)...)...)
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %q", c.name, code, stderr)
		}
		m, err := fixture.LoadManifest(filepath.Join(out, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if m.XMLSHA256 != c.want {
			t.Errorf("%s: xml_sha256 = %q, want %q", c.name, m.XMLSHA256, c.want)
		}
	}
}

func TestExtractRun(t *testing.T) {
	noEnv(t)
	xml := runXML(t)
	// Run 2 pumps from step 42 (10.5 s) to step 81 (20.25 s).
	dir := runDir(t, time.Hour, concat(idleSteps(8), pumpRun(12, ""), idleSteps(16), pumpRun(40, "9.00"), idleSteps(16)))
	code, stdout, stderr := runCmd(t, "--xml", xml, "extract", "--dir", dir, "--run", "2", "--margin", "1s", "--out", "-")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	evs, err := fixture.ReadLog(strings.NewReader(stdout))
	if err != nil {
		t.Fatal(err)
	}
	// 9.5 s to 21.25 s inclusive: steps 38 to 85.
	if len(evs) != 48 || evs[0].T != 0 || evs[len(evs)-1].T != 11750 {
		t.Errorf("%d events, t %d to %d; want 48, 0 to 11750", len(evs), evs[0].T, evs[len(evs)-1].T)
	}

	code, _, stderr = runCmd(t, "--xml", xml, "extract", "--dir", dir, "--run", "3", "--out", "-")
	if code != 1 || !strings.Contains(stderr, "--run 3: no such run") {
		t.Errorf("no such run: exit %d, stderr %q", code, stderr)
	}
}

func TestExtractMarginWidensWindow(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := extractDir(t)
	day := "2026-11-14 "
	cases := []struct {
		name      string
		got, want []string
	}{
		{
			"both",
			[]string{"-s", day + "08:58:36", "-u", day + "08:58:40", "--margin", "6s"},
			[]string{"-s", day + "08:58:30", "-u", day + "08:58:46"},
		},
		{"since only", []string{"-s", day + "08:58:36", "--margin", "7s"}, []string{"-s", day + "08:58:29"}},
		{"until only", []string{"-u", day + "08:58:34", "--margin", "2s"}, []string{"-u", day + "08:58:36"}},
	}
	for _, c := range cases {
		run1 := func(w []string) string {
			args := append([]string{"extract", "--dir", dir, "--out", "-"}, w...)
			code, out, errs := runCmd(t, args...)
			if code != 0 {
				t.Fatalf("%s %v: exit %d, stderr %q", c.name, w, code, errs)
			}
			return out
		}
		got, want := run1(c.got), run1(c.want)
		if got == "" || got != want {
			t.Errorf("%s: --margin output differs from the widened window\n got %q\nwant %q", c.name, got, want)
		}
	}
}
