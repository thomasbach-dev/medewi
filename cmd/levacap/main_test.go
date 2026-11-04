package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/sim"
)

func synthLine(temp string) []byte {
	line := []byte(strings.Repeat(" ", 54))
	line[0] = '{'
	line[2] = '-'
	copy(line[4:], "1:%")
	copy(line[7:], temp)
	copy(line[18:], " 42")
	copy(line[36:], " 9.10")
	copy(line[50:], "Out ")
	return append(line, '\r', '\n')
}

func startSim(t *testing.T, cfg sim.Config) *sim.Server {
	t.Helper()
	for i, temp := range []string{" 93.50", " 93.60", " 93.70", " 93.80"} {
		cfg.Stream = append(cfg.Stream, fixture.Event{T: int64(i * 10), Dir: fixture.RX, Data: synthLine(temp)})
	}
	cfg.Banner = "$id: leva! 3.2"
	if cfg.BannerInterval == 0 {
		cfg.BannerInterval = 30 * time.Millisecond
	}
	if cfg.Responses == nil {
		cfg.Responses = map[string][]string{
			"MCh": {"MCh help", "MCu setup dump", "MCr reports"},
			"MC?": {"XML=synthetic"},
			"MCu": {"setup"}, "MCm": {"m"}, "MCb": {"b"}, "MCf": {"f"},
		}
	}
	s := sim.New(cfg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

func startSimResponses(t *testing.T, r map[string][]string) *sim.Server {
	t.Helper()
	return startSim(t, sim.Config{Responses: r})
}

func TestConformAgainstSim(t *testing.T) {
	s := startSim(t, sim.Config{})
	t.Setenv("MEDEWI_XML_PATH", "../../testdata/spec/synthetic.xml")
	var out, errb bytes.Buffer
	code := run([]string{"conform", "-addr", s.Addr().String(), "-yes", "-max", "stream"}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:%s\nstderr:%s", code, out.String(), errb.String())
	}
	if !regexp.MustCompile(`(?m)^reports-decode\s+stream\s+ok$`).MatchString(out.String()) {
		t.Errorf("reports-decode not ok/stream: %s", out.String())
	}
}

func TestConformRefuses2323(t *testing.T) {
	s := sim.New(sim.Config{TripwireAddr: "127.0.0.1:2323"})
	if err := s.Start(); err != nil {
		t.Skipf("cannot bind 127.0.0.1:2323 (another levasim running?): %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	if s.TripwireAddr() == nil {
		t.Skip("tripwire not bound")
	}
	var out, errb bytes.Buffer
	code := run([]string{"conform", "-addr", "127.0.0.1:2323", "-yes"}, strings.NewReader(""), &out, &errb)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "2323") {
		t.Errorf("stderr lacks 2323: %q", errb.String())
	}
	time.Sleep(100 * time.Millisecond)
	if h := s.TripwireHits(); h != 0 {
		t.Errorf("tripwire hits = %d, want 0", h)
	}
}

func TestCaptureRefuses2323(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"capture", "-scenario", "commands", "-addr", "127.0.0.1:2323", "-out", t.TempDir()}, strings.NewReader(""), &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "2323") {
		t.Errorf("exit %d stderr %q", code, errb.String())
	}
}

func TestCaptureWritesLogAndManifest(t *testing.T) {
	old := cmdWait
	cmdWait = 50 * time.Millisecond
	t.Cleanup(func() { cmdWait = old })
	s := startSim(t, sim.Config{})
	xml := "../../testdata/spec/synthetic.xml"
	t.Setenv("MEDEWI_XML_PATH", xml)
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"capture", "-scenario", "commands", "-addr", s.Addr().String(), "-out", dir}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, errb.String())
	}
	f, err := os.Open(filepath.Join(dir, "commands", "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	evs, err := fixture.ReadLog(f)
	if err != nil {
		t.Fatal(err)
	}
	var tx strings.Builder
	var rx int
	for _, e := range evs {
		if e.Dir == fixture.TX {
			tx.Write(e.Data)
		} else {
			rx++
		}
	}
	want := []string{"MCh", "MC?", "MCu", "MCm", "MCb", "MCf"}
	if got := strings.Split(strings.TrimSuffix(tx.String(), "\r\n"), "\r\n"); !slices.Equal(got, want) {
		t.Errorf("TX commands = %q, want exactly %q", got, want)
	}
	if rx == 0 {
		t.Error("no rx events")
	}
	m, err := fixture.LoadManifest(filepath.Join(dir, "commands", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Scenario != "commands" || m.FirmwareVersion == "" || m.XMLName != "synthetic" ||
		len(m.XMLSHA256) != 64 || m.MachineAddr != s.Addr().String() || m.Recorded.IsZero() {
		t.Errorf("manifest: %+v", m)
	}
}

func TestCaptureStreamStopsOnEnter(t *testing.T) {
	old := cmdWait
	cmdWait = 50 * time.Millisecond
	t.Cleanup(func() { cmdWait = old })
	s := startSim(t, sim.Config{})
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run([]string{"capture", "-scenario", "stream", "-addr", s.Addr().String(), "-out", dir}, strings.NewReader("\n"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, errb.String())
	}
	f, err := os.Open(filepath.Join(dir, "stream", "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	evs, err := fixture.ReadLog(f)
	if err != nil {
		t.Fatal(err)
	}
	var tx strings.Builder
	rx := 0
	for _, e := range evs {
		if e.Dir == fixture.TX {
			tx.Write(e.Data)
		} else {
			rx++
		}
	}
	if got := tx.String(); got != "MC?\r\nMCr\r\nMC \r\n" {
		t.Errorf("TX = %q, want MC?, MCr, \"MC \"", got)
	}
	if rx == 0 {
		t.Error("no rx events")
	}
}

func TestUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if run(nil, strings.NewReader(""), &out, &errb) != 2 {
		t.Error("want exit 2")
	}
	if run([]string{"capture", "-scenario", "bogus"}, strings.NewReader(""), &out, &errb) != 2 {
		t.Error("bogus scenario want exit 2")
	}
}

func TestCaptureRefusesOverwriteAndForce(t *testing.T) {
	old := cmdWait
	cmdWait = 10 * time.Millisecond
	t.Cleanup(func() { cmdWait = old })
	s := startSim(t, sim.Config{})
	dir := t.TempDir()
	args := []string{"capture", "-scenario", "commands", "-addr", s.Addr().String(), "-out", dir}
	var out, errb bytes.Buffer
	if code := run(args, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("first run exit %d: %s", code, errb.String())
	}
	errb.Reset()
	if code := run(args, strings.NewReader(""), &out, &errb); code != 1 || !strings.Contains(errb.String(), "-force") {
		t.Errorf("second run exit %d stderr %q", code, errb.String())
	}
	if code := run(append(args, "-force"), strings.NewReader(""), &out, &errb); code != 0 {
		t.Errorf("-force run exit %d: %s", code, errb.String())
	}
}

func TestFailedCaptureWritesPartialAndKeepsGood(t *testing.T) {
	old := cmdWait
	cmdWait = 10 * time.Millisecond
	t.Cleanup(func() { cmdWait = old })
	oldT := cmdTimeout
	cmdTimeout = 300 * time.Millisecond
	t.Cleanup(func() { cmdTimeout = oldT })
	s := startSim(t, sim.Config{})
	dir := t.TempDir()
	good := filepath.Join(dir, "commands")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"log.jsonl", "manifest.json"} {
		if err := os.WriteFile(filepath.Join(good, n), []byte("GOOD"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Use a fresh scenario dir for the failing run: stop the sim mid-way by
	// pointing at a sim that does not answer MCm.
	s2 := startSimResponses(t, map[string][]string{"MCh": {"h"}, "MC?": {"XML=x"}, "MCu": {"u"}})
	cmdWait = 400 * time.Millisecond
	go func() {
		time.Sleep(700 * time.Millisecond)
		_ = s2.Stop() // connection dies mid-scenario
	}()
	var out, errb bytes.Buffer
	code := run([]string{"capture", "-scenario", "commands", "-addr", s2.Addr().String(), "-out", dir, "-force"}, strings.NewReader(""), &out, &errb)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr %s", code, errb.String())
	}
	b, _ := os.ReadFile(filepath.Join(good, "log.jsonl"))
	if string(b) != "GOOD" {
		t.Errorf("good log was modified: %q", b)
	}
	m, err := fixture.LoadManifest(filepath.Join(good, "manifest.partial.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Notes, "capture failed") {
		t.Errorf("notes = %q", m.Notes)
	}
	if _, err := os.Stat(filepath.Join(good, "log.partial.jsonl")); err != nil {
		t.Error(err)
	}
	_ = s
}

func TestCaptureMissingXMLWarns(t *testing.T) {
	old := cmdWait
	cmdWait = 10 * time.Millisecond
	t.Cleanup(func() { cmdWait = old })
	s := startSimResponses(t, map[string][]string{"MCh": {"h"}, "MC?": {"nothing"}, "MCu": {"u"}, "MCm": {"m"}, "MCb": {"b"}, "MCf": {"f"}})
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"capture", "-scenario", "commands", "-addr", s.Addr().String(), "-out", dir}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "no XML=") {
		t.Errorf("stderr %q", errb.String())
	}
	m, err := fixture.LoadManifest(filepath.Join(dir, "commands", "manifest.json"))
	if err != nil || !strings.Contains(m.Notes, "MC? returned no XML= line") {
		t.Errorf("manifest %+v err %v", m, err)
	}
}

func TestWriteOutputFailureLeavesGoodFilesUntouched(t *testing.T) {
	old := cmdWait
	cmdWait = 10 * time.Millisecond
	t.Cleanup(func() { cmdWait = old })
	s := startSim(t, sim.Config{})
	dir := t.TempDir()
	scen := filepath.Join(dir, "commands")
	// A directory squatting on the manifest temp name makes the manifest write
	// fail after the log temp file was written.
	if err := os.MkdirAll(filepath.Join(scen, ".manifest.json.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"log.jsonl", "manifest.json"} {
		if err := os.WriteFile(filepath.Join(scen, n), []byte("GOOD"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	code := run([]string{"capture", "-scenario", "commands", "-addr", s.Addr().String(), "-out", dir, "-force"}, strings.NewReader(""), &out, &errb)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, n := range []string{"log.jsonl", "manifest.json"} {
		b, _ := os.ReadFile(filepath.Join(scen, n))
		if string(b) != "GOOD" {
			t.Errorf("%s modified: %q", n, b)
		}
	}
}

func TestCaptureBadXMLPathFailsBeforeDial(t *testing.T) {
	s := startSim(t, sim.Config{TripwireAddr: "127.0.0.1:0"})
	t.Setenv("MEDEWI_XML_PATH", filepath.Join(t.TempDir(), "missing.xml"))
	var out, errb bytes.Buffer
	code := run([]string{"capture", "-scenario", "commands", "-addr", s.Addr().String(), "-out", t.TempDir()}, strings.NewReader(""), &out, &errb)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "MEDEWI_XML_PATH") {
		t.Errorf("stderr %q", errb.String())
	}
}

func TestConformXMLMismatchFails(t *testing.T) {
	s := startSimResponses(t, map[string][]string{
		"MCh": {"MCu", "MCr"},
		"MC?": {"XML=other-release"},
	})
	t.Setenv("MEDEWI_XML_PATH", "../../testdata/spec/synthetic.xml")
	var out, errb strings.Builder
	code := run([]string{"conform", "-addr", s.Addr().String(), "-max", "readonly", "-yes"},
		strings.NewReader(""), &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%q err=%q", code, out.String(), errb.String())
	}
	for _, want := range []string{"other-release", "synthetic"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not mention %q:\n%s", want, out.String())
		}
	}
}

func init() { bannerSettle = 50 * time.Millisecond }

func TestCaptureDisplayRecordsFramesAndMarks(t *testing.T) {
	old := cmdWait
	cmdWait = 50 * time.Millisecond
	t.Cleanup(func() { cmdWait = old })
	frame := "<002SOAK 1.0b       1: 91.0\xdf     PID|             4% 0.0b\x05  0s   0ml>"
	s := startSim(t, sim.Config{Responses: map[string][]string{
		"MC?": {"XML=synthetic"},
		"MC@": {frame},
	}})
	dir := t.TempDir()
	var out, errb bytes.Buffer
	// One mark; a mistyped line must not stop; an empty line does.
	in := &slowReader{lines: []string{"m\n", "M\n", "\n"}, gap: 250 * time.Millisecond}
	code := run([]string{
		"capture", "-scenario", "display", "-addr", s.Addr().String(), "-out", dir,
		"-display-every", "200ms", "-display-for", "50ms",
	}, in, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, errb.String())
	}
	f, err := os.Open(filepath.Join(dir, "display", "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	evs, err := fixture.ReadLog(f)
	if err != nil {
		t.Fatal(err)
	}
	var tx, rx strings.Builder
	for _, e := range evs {
		if e.Dir == fixture.TX {
			tx.Write(e.Data)
		} else {
			rx.Write(e.Data)
		}
	}
	// MCr and MC@ are mutually exclusive on the machine: every display
	// sample must switch back to reports, and "MC " comes last.
	if got := tx.String(); !regexp.MustCompile(`^MC\?\r\nMCr\r\n(MC@\r\nMCr\r\n)+(MC@\r\n)?MC \r\n$`).MatchString(got) {
		t.Errorf("TX = %q, want MC?, MCr, (MC@, MCr)+, \"MC \"", got)
	}
	if !strings.Contains(rx.String(), frame) {
		t.Error("display frame not recorded")
	}
	m, err := fixture.LoadManifest(filepath.Join(dir, "display", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Notes, "display sampled 50ms every 200ms") {
		t.Errorf("manifest notes %q lack the sampling", m.Notes)
	}
	if !regexp.MustCompile(`marks \(ms\): \d+`).MatchString(m.Notes) {
		t.Errorf("manifest notes %q lack the mark", m.Notes)
	}
	if !strings.Contains(out.String(), "an empty line to stop") {
		t.Errorf("stdout %q lacks the hint for a mistyped line", out.String())
	}
	if !strings.Contains(out.String(), "mark 1") {
		t.Errorf("stdout %q does not acknowledge the mark", out.String())
	}
}

// A connection lost mid-recording must end the capture at once, not wait for
// the operator's Enter: on 2026-11-04 a display capture kept "recording" for
// minutes after the machine had hung up.
func TestCaptureStreamLostConnectionStopsAtOnce(t *testing.T) {
	for _, scenario := range []string{"stream", "display"} {
		t.Run(scenario, func(t *testing.T) {
			s := startSim(t, sim.Config{Responses: map[string][]string{"MC?": {"XML=synthetic"}}})
			go func() {
				time.Sleep(1500 * time.Millisecond)
				_ = s.Stop()
			}()
			stdin, w := io.Pipe() // the operator never types anything
			t.Cleanup(func() { _ = w.Close() })
			dir := t.TempDir()
			var out, errb bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- run([]string{
					"capture", "-scenario", scenario, "-addr", s.Addr().String(), "-out", dir,
					"-display-every", "100ms", "-display-for", "30ms",
				}, stdin, &out, &errb)
			}()
			select {
			case code := <-done:
				if code != 1 {
					t.Errorf("exit %d, want 1", code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("capture still waiting for stdin after the connection was lost")
			}
			if !strings.Contains(errb.String(), "connection lost") {
				t.Errorf("stderr %q does not report the lost connection", errb.String())
			}
			if _, err := os.Stat(filepath.Join(dir, scenario, "log.partial.jsonl")); err != nil {
				t.Error(err)
			}
		})
	}
}

// slowReader hands out one line per Read, gap apart, then EOF.
type slowReader struct {
	lines []string
	gap   time.Duration
}

func (r *slowReader) Read(p []byte) (int, error) {
	if len(r.lines) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.gap)
	n := copy(p, r.lines[0])
	r.lines = r.lines[1:]
	return n, nil
}
