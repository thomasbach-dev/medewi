package daemon

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/rawlog"
)

// rawDaemonConfig is daemonConfig with a raw traffic log in a temp dir.
func rawDaemonConfig(t *testing.T, addr string) (Config, *rawlog.Log, string) {
	t.Helper()
	dir := t.TempDir()
	rl, err := rawlog.Open(rawlog.Config{Dir: dir, FlushEvery: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("rawlog.Open: %v", err)
	}
	t.Cleanup(func() { _ = rl.Close() })
	cfg := daemonConfig(addr, openStore(t))
	cfg.Raw = rl
	return cfg, rl, dir
}

// readRaw closes the log and returns every event in dir.
func readRaw(t *testing.T, rl *rawlog.Log, dir string) []fixture.Event {
	t.Helper()
	if err := rl.Close(); err != nil {
		t.Fatalf("rawlog Close: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no log files in %s (err %v)", dir, err)
	}
	var all []fixture.Event
	for _, f := range files {
		evs, err := rawlog.ReadFile(f)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", f, err)
		}
		all = append(all, evs...)
	}
	return all
}

func TestRawLogRecordsSession(t *testing.T) {
	s := startSim(t, simConfig(idleLines(20)))
	cfg, rl, dir := rawDaemonConfig(t, s.Addr().String())
	d, stop := runDaemon(t, cfg)
	waitActive(t, d)
	stop()

	evs := readRaw(t, rl, dir)
	tx := map[string]bool{}
	reports := 0
	for _, e := range evs {
		switch e.Dir {
		case fixture.TX:
			tx[string(e.Data)] = true
		case fixture.RX:
			if strings.HasPrefix(string(e.Data), "{") {
				reports++
			}
		}
	}
	for _, want := range []string{"MC?\r\n", "MCr\r\n", "MC \r\n"} {
		if !tx[want] {
			t.Errorf("tx %q not logged; tx seen: %v", want, tx)
		}
	}
	if reports == 0 {
		t.Error("no rx report line logged")
	}
}

func TestRawLogDumpsNeverLogged(t *testing.T) {
	shrinkDumpTimings(t)
	s := startSim(t, dumpSim(idleLines(20), fakeMCm))
	cfg, rl, dir := rawDaemonConfig(t, s.Addr().String())
	d, stop := runDaemon(t, cfg)
	waitActive(t, d)

	if _, err := d.DumpMCm(reqCtx(t)); err != nil {
		t.Fatalf("DumpMCm: %v", err)
	}
	if _, err := d.DumpMCu(reqCtx(t)); err != nil {
		t.Fatalf("DumpMCu: %v", err)
	}
	stop()

	reports := 0
	for _, e := range readRaw(t, rl, dir) {
		if e.Dir != fixture.RX {
			continue
		}
		got := string(e.Data)
		for _, secret := range append([]string{"user setting"}, fakeMCm...) {
			if strings.Contains(got, secret) {
				t.Errorf("dump line leaked into the raw log: %q", got)
			}
		}
		if strings.HasPrefix(got, "{") {
			reports++
		}
	}
	if reports == 0 {
		t.Error("no report lines logged during the dumps")
	}
}

func TestRawLogNamesXML(t *testing.T) {
	s := startSim(t, simConfig(idleLines(20)))
	cfg, rl, dir := rawDaemonConfig(t, s.Addr().String())
	d, stop := runDaemon(t, cfg)
	waitActive(t, d)
	stop()

	for _, e := range readRaw(t, rl, dir) {
		if e.Dir == fixture.Note && string(e.Data) == rawlog.XMLNotePrefix+"test" {
			return
		}
	}
	t.Error(`no note "xml: test" in the raw log`)
}
