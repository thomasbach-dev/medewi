package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/sim"
)

func synthLine(temp string) []byte {
	line := []byte(strings.Repeat(" ", 54))
	line[0] = '{'
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
	cfg.Responses = map[string][]string{
		"MCh": {"MCh help", "MCu setup dump", "MCr reports"},
		"MC?": {"XML=leva!-65FCB201"},
		"MCu": {"setup"}, "MCm": {"m"}, "MCb": {"b"}, "MCf": {"f"},
	}
	s := sim.New(cfg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

func TestConformAgainstSim(t *testing.T) {
	s := startSim(t, sim.Config{})
	t.Setenv("MEDEWI_XML_PATH", "../../testdata/spec/synthetic.xml")
	var out, errb bytes.Buffer
	code := run([]string{"conform", "-addr", s.Addr().String(), "-yes", "-max", "stream"}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:%s\nstderr:%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "reports-decode") {
		t.Errorf("output lacks reports-decode: %s", out.String())
	}
}

func TestConformRefuses2323(t *testing.T) {
	s := startSim(t, sim.Config{TripwireAddr: "127.0.0.1:2323"})
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
	for _, c := range []string{"MCh", "MC?", "MCu", "MCm", "MCb", "MCf"} {
		if !strings.Contains(tx.String(), c) {
			t.Errorf("tx lacks %s", c)
		}
	}
	if rx == 0 {
		t.Error("no rx events")
	}
	m, err := fixture.LoadManifest(filepath.Join(dir, "commands", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Scenario != "commands" || m.FirmwareVersion == "" || m.XMLName != "leva!-65FCB201" ||
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
	if _, err := os.Stat(filepath.Join(dir, "stream", "log.jsonl")); err != nil {
		t.Error(err)
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
