package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

const programXML = `<cfg version="4">
  <values>
    <value at="4" len="3" type="string">1:%</value>
    <value at="13" len="5" export="Setpoint PID" type="float" unit="C"></value>
    <value at="36" len="5" export="Pressure" type="float" unit="bar"></value>
    <value at="50" len="4" export="Volume" type="float" unit="ml"></value>
    <value at="60" len="3" export="Phase" type="float" unit="deg"></value>
    <value at="70" len="5" type="int" unit="ticks"></value>
  </values>
</cfg>
`

func programSpec(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "prog.xml")
	if err := os.WriteFile(p, []byte(programXML), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// progLog builds the events of a log, one report per 250 ms.
type progLog struct {
	t, ticks int
	evs      []fixture.Event
}

func (l *progLog) report(pump byte, phase string) {
	b := bytes.Repeat([]byte(" "), 289)
	b[0], b[288], b[2] = '{', '}', pump
	copy(b[4:], "CLD")
	copy(b[13:], "?????")
	copy(b[36:], " 0.10")
	copy(b[50:], fmt.Sprintf("%4d", l.ticks/2))
	copy(b[60:], phase)
	copy(b[70:], fmt.Sprintf("%5d", l.ticks))
	l.evs = append(l.evs, fixture.Event{T: int64(l.t), Dir: fixture.RX, Data: append(b, '\r', '\n')})
	l.t += 250
}

func (l *progLog) idle(sec int) {
	for range sec * 4 {
		l.report('-', "???")
	}
}

// purge is n purge pulses of 10 s, 20 s apart.
func (l *progLog) purge(n int) {
	for range n {
		for range 40 {
			l.ticks += 2
			l.report('*', "000")
		}
		l.idle(20)
	}
}

func (l *progLog) frame(rows ...string) {
	s := "<112"
	for _, r := range rows {
		s += fmt.Sprintf("%-16s", r)
	}
	l.evs = append(l.evs, fixture.Event{T: int64(l.t), Dir: fixture.RX, Data: []byte(s + ">\r\n")})
}

func (l *progLog) capture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var b bytes.Buffer
	if err := fixture.WriteLog(&b, l.evs); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "log.jsonl")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// purgeLog is a purge of three pulses, seen on the LCD, after some use.
func purgeLog() *progLog {
	l := &progLog{ticks: 400}
	l.idle(10)
	l.ticks = 0
	l.purge(1)
	l.frame(">Run", " Descale", " PURGE     250ml", " Backflush")
	l.purge(2)
	l.idle(90)
	return l
}

func TestProgramsFormats(t *testing.T) {
	noEnv(t)
	xml, c := programSpec(t), purgeLog().capture(t)
	code, out, errs := runCmd(t, "--xml", xml, "programs", "--format", "csv", c)
	want := "program,kind,start,t,duration,outcome,status,evidence,pulses,pump_s,volume_ml,peak_bar,settings_match,notes,xml,src,rule_version\n" +
		"1,purge,,10,69.75,unknown,confirmed,report+lcd,3,29.25,120,0.1,,,," + c + ":41,1\n"
	if code != 0 || errs != "" || out != want {
		t.Errorf("csv: exit %d, stderr %q\n%s\nwant\n%s", code, errs, out, want)
	}
	_, out, _ = runCmd(t, "--xml", xml, "programs", c)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") !=
		"PROGRAM KIND START T DURATION OUTCOME STATUS EVIDENCE PULSES PUMP_S VOLUME_ML PEAK_BAR SETTINGS_MATCH NOTES XML SRC" {
		t.Errorf("text:\n%s", out)
	}
	if f := strings.Fields(lines[len(lines)-1]); strings.Join(f[:7], " ") != "1 purge - 10.000 69.75 unknown confirmed" {
		t.Errorf("text row: %q", f)
	}
	_, out, _ = runCmd(t, "--xml", xml, "programs", "--format", "jsonl", c)
	var o map[string]any
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ps, _ := o["pulses_detail"].([]any)
	if o["kind"] != "purge" || o["pulses"] != 3.0 || o["rule_version"] != 1.0 || o["start"] != nil || o["figures"] != nil || len(ps) != 3 {
		t.Errorf("jsonl: %s", out)
	}
	p, _ := ps[1].(map[string]any)
	if p["t"] != 30.0 || p["duration"] != 9.75 || p["volume_ml"] != 40.0 || p["peak_bar"] != 0.1 || p["phase"] != 0.0 {
		t.Errorf("second pulse: %v", p)
	}
}

func TestProgramsNone(t *testing.T) {
	noEnv(t)
	l := &progLog{}
	l.idle(10)
	code, out, errs := runCmd(t, "--xml", programSpec(t), "programs", l.capture(t))
	if code != 0 || !strings.HasPrefix(out, "PROGRAM ") || errs != "levalog: no programs in 40 reports (pump marker * in 0)\n" {
		t.Errorf("exit %d\n%s\n%s", code, out, errs)
	}
	if code, _, errs = runCmd(t, "programs", l.capture(t)); code != 2 || !strings.Contains(errs, "XML") {
		t.Errorf("without XML: exit %d, %q", code, errs)
	}
}

// Captures have no common clock: two in one input stay apart.
func TestProgramsTwoCaptures(t *testing.T) {
	noEnv(t)
	a, b := purgeLog().capture(t), purgeLog().capture(t)
	_, out, _ := runCmd(t, "--xml", programSpec(t), "programs", "--format", "csv", a, b)
	rows := strings.Split(strings.TrimSpace(out), "\n")
	if len(rows) != 3 || !strings.Contains(rows[1], a) || !strings.Contains(rows[2], b) {
		t.Errorf("got\n%s", out)
	}
}

// rawPurge writes two raw logs of one session: the program crosses the
// file boundary at 10:00:40.
func rawPurge(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	start := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	l := purgeLog()
	cut := 0
	for cut < len(l.evs) && l.evs[cut].T < 40000 {
		cut++
	}
	writeRaw(t, dir, start, append([]fixture.Event{ev(0, fixture.RX, "$id: leva! (3.2)\r\n"), ev(0, fixture.Note, "xml: prog")}, l.evs[:cut]...)...)
	rest := l.evs[cut:]
	for i := range rest {
		rest[i].T -= 40000
	}
	// A rotated file of the same session opens with the xml note.
	rest = append([]fixture.Event{ev(0, fixture.Note, "xml: prog")}, rest...)
	writeRaw(t, dir, start.Add(40*time.Second), rest...)
	return dir
}

func TestProgramsWindow(t *testing.T) {
	noEnv(t)
	xml, dir := programSpec(t), rawPurge(t)
	count := func(args ...string) (int, string) {
		code, out, errs := runCmd(t, append([]string{"--xml", xml, "programs", "--format", "csv", "--dir", dir}, args...)...)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errs)
		}
		return strings.Count(out, "\n") - 1, out
	}
	if n, out := count("-u", "2026-11-10 10:00:20 UTC"); n != 1 || !strings.Contains(out, ",69.75,") {
		t.Errorf("a program crossing -u is listed whole, got\n%s", out)
	}
	if n, out := count("-s", "2026-11-10 10:00:20 UTC"); n != 0 {
		t.Errorf("a program that starts before -s is not listed, got\n%s", out)
	}
	if _, out := count(); !strings.Contains(out, "2026-11-10T10:00:10.000Z") {
		t.Errorf("start missing in\n%s", out)
	}
}

func TestEventsProgram(t *testing.T) {
	noEnv(t)
	xml, c := programSpec(t), purgeLog().capture(t)
	code, out, errs := runCmd(t, "--xml", xml, "events", "--program", "1", "--format", "csv", "--fields", "phase", c)
	rows := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || errs != "" || len(rows) < 3 {
		t.Fatalf("exit %d, %q\n%s", code, errs, out)
	}
	if f := strings.Split(rows[1], ","); f[3] != "1" || f[4] != "0" {
		t.Errorf("first row %q, want run 1 at run_t 0", rows[1])
	}
	if f := strings.Split(rows[len(rows)-1], ","); f[4] != "69.75" {
		t.Errorf("last row %q, want run_t 69.75", rows[len(rows)-1])
	}
	for _, other := range [][]string{{"--run", "1"}, {"--run-kind", "flush"}} {
		code, _, errs = runCmd(t, append([]string{"--xml", xml, "events", "--program", "1", c}, other...)...)
		if code != 2 || !strings.Contains(errs, "--program") {
			t.Errorf("with %v: exit %d, %q", other, code, errs)
		}
	}
	if code, _, errs = runCmd(t, "--xml", xml, "events", "--program", "x", c); code != 2 || !strings.Contains(errs, "--program") {
		t.Errorf("bad number: exit %d, %q", code, errs)
	}
	if _, _, errs = runCmd(t, "--xml", xml, "events", "--program", "7", c); !strings.Contains(errs, "--program 7: no such program (program 1)") {
		t.Errorf("missing program: %q", errs)
	}
	if code, out, _ = runCmd(t, "--xml", xml, "events", "--program", "1", "--margin", "5s", "--every", "10s", "--format", "csv", "--fields", "phase", c); code != 0 || strings.Count(out, "\n") < 8 {
		t.Errorf("--every: exit %d\n%s", code, out)
	}
	if code, _, errs = runCmd(t, "events", "--program", "1", c); code != 2 || !strings.Contains(errs, "--program") {
		t.Errorf("without XML: exit %d, %q", code, errs)
	}
}

// A reconnect between two pulses starts a new session: two programs, or
// none, but never one across it.
func TestProgramsReconnect(t *testing.T) {
	noEnv(t)
	dir := t.TempDir()
	start := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	l := purgeLog()
	half := len(l.evs) / 2
	open := func(evs []fixture.Event) []fixture.Event {
		return append([]fixture.Event{ev(0, fixture.RX, "$id: leva! (3.2)\r\n"), ev(0, fixture.Note, "xml: prog")}, evs...)
	}
	writeRaw(t, dir, start, open(l.evs[:half])...)
	m := purgeLog()
	writeRaw(t, dir, start.Add(time.Duration(l.evs[half].T)*time.Millisecond), open(m.evs)...)
	_, out, errs := runCmd(t, "--xml", programSpec(t), "programs", "--format", "csv", "--dir", dir)
	if rows := strings.Split(strings.TrimSpace(out), "\n"); len(rows) != 3 {
		t.Errorf("want two programs: %q\n%s", errs, out)
	}
}
