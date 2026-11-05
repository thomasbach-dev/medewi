package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// readyFrame is a real display frame (2026-11-05) showing READY.
const readyFrame = "<102SOAK 1.0b       1: 91.0\xdf   READY\x02             7% 0.1b\x05  0s   0ml>"

// withNotice inserts the machine's "[READY]" line after the first n lines.
func withNotice(lines [][]byte, n int) [][]byte {
	out := append([][]byte(nil), lines[:n]...)
	out = append(out, []byte("[READY]"))
	return append(out, lines[n:]...)
}

func TestMachineReadyFromNotice(t *testing.T) {
	out := filepath.Join(t.TempDir(), "events")
	cfg := simConfig(withNotice(idleLines(40), 5))
	cfg.Once = true
	s := startSim(t, cfg)
	dcfg := daemonConfig(s.Addr().String(), openStore(t))
	dcfg.EventExec = fmt.Sprintf(`echo "$MEDEWI_EVENT" >> '%s'`, out)
	d, _ := runDaemon(t, dcfg)
	waitFor(t, "machine READY", func() bool { return d.Status().MachineReady })
	waitFor(t, "MACHINE_READY in the hook's output", func() bool {
		b, _ := os.ReadFile(out)
		return strings.Contains(string(b), "MACHINE_READY\n")
	})
}

func TestMachineReadyClearedByEco(t *testing.T) {
	lines := withNotice(idleLines(10), 5)
	lines = append(lines, repeat(30, line('-', "ECO", 85, 79, 0, -1, 0, 0))...)
	cfg := simConfig(lines)
	cfg.Once = true
	s := startSim(t, cfg)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitFor(t, "machine READY", func() bool { return d.Status().MachineReady })
	waitFor(t, "eco clears it", func() bool {
		st := d.Status()
		return st.Power == PowerEco && !st.MachineReady
	})
}

func TestDisplaySampling(t *testing.T) {
	cfg := simConfig(idleLines(40))
	cfg.Responses["MC@"] = []string{readyFrame}
	s := startSim(t, cfg)
	dcfg := daemonConfig(s.Addr().String(), openStore(t))
	dcfg.DisplayEvery, dcfg.DisplayFor = 100*time.Millisecond, 30*time.Millisecond
	d, _ := runDaemon(t, dcfg)
	waitFor(t, "a sampled frame in the status", func() bool {
		ds := d.Status().Display
		return ds != nil && ds.Rows[1] == "1: 91.0°   READY" && !ds.At.IsZero()
	})
	waitFor(t, "two samples", func() bool { return count(s.Received(), "MC@") >= 2 })
	cmds := s.Received()
	// Every sample switches back to reports; at most one may be in flight.
	if at, r := count(cmds, "MC@"), count(cmds, "MCr"); r < at {
		t.Errorf("MC@ %d times but MCr only %d times: %q", at, r, cmds)
	}
	assertOnlyAllowed(t, s, "MC@")
}

// The final "MC " must be the last thing sent, also when a sample is in
// flight at shutdown; otherwise the machine keeps streaming after medewid
// exits. Samples are made long and frequent to hit that window.
func TestShutdownStopsAfterSample(t *testing.T) {
	for range 5 {
		cfg := simConfig(idleLines(40))
		cfg.Responses["MC@"] = []string{readyFrame}
		s := startSim(t, cfg)
		dcfg := daemonConfig(s.Addr().String(), openStore(t))
		dcfg.DisplayEvery, dcfg.DisplayFor = 20*time.Millisecond, 15*time.Millisecond
		d, stop := runDaemon(t, dcfg)
		waitActive(t, d)
		waitFor(t, "a sample", func() bool { return count(s.Received(), "MC@") >= 1 })
		stop()
		time.Sleep(50 * time.Millisecond) // let the sim read what was sent
		if cmds := s.Received(); cmds[len(cmds)-1] != "MC " {
			t.Fatalf("last command %q, want \"MC \": %q", cmds[len(cmds)-1], cmds[max(0, len(cmds)-4):])
		}
	}
}

// Sampled frames reach the raw traffic log through the daemon.
func TestSampledFramesInRawLog(t *testing.T) {
	cfg := simConfig(idleLines(40))
	cfg.Responses["MC@"] = []string{readyFrame}
	s := startSim(t, cfg)
	dcfg, rl, dir := rawDaemonConfig(t, s.Addr().String())
	dcfg.DisplayEvery, dcfg.DisplayFor = 100*time.Millisecond, 30*time.Millisecond
	d, stop := runDaemon(t, dcfg)
	waitFor(t, "a sampled frame", func() bool { return d.Status().Display != nil })
	stop()
	for _, e := range readRaw(t, rl, dir) {
		if e.Dir == fixture.RX && strings.HasPrefix(string(e.Data), "<1") {
			return
		}
	}
	t.Error("no display frame in the raw log")
}

func TestNoSamplingByDefault(t *testing.T) {
	cfg := simConfig(idleLines(20))
	cfg.Responses["MC@"] = []string{readyFrame}
	s := startSim(t, cfg)
	d, stop := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)
	time.Sleep(200 * time.Millisecond)
	stop()
	assertOnlyAllowed(t, s)
}

func TestMarkWritesNote(t *testing.T) {
	s := startSim(t, simConfig(idleLines(20)))
	cfg, rl, dir := rawDaemonConfig(t, s.Addr().String())
	d, stop := runDaemon(t, cfg)
	waitActive(t, d)
	if err := d.Mark("pre-infusion over"); err != nil {
		t.Fatal(err)
	}
	stop()
	var found bool
	for _, e := range readRaw(t, rl, dir) {
		if e.Dir == fixture.Note && string(e.Data) == "mark: pre-infusion over" {
			found = true
		}
	}
	if !found {
		t.Error("mark note not in the raw log")
	}
}

func TestAPIMark(t *testing.T) {
	s := startSim(t, simConfig(idleLines(20)))
	cfg, _, _ := rawDaemonConfig(t, s.Addr().String())
	d, _ := runDaemon(t, cfg)
	waitActive(t, d)
	h := NewHandler(d, nil)
	if rec := apiDo(t, h, "POST", "/api/mark", `{"text":"shot start"}`); rec.Code != 200 {
		t.Errorf("mark: code %d body %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{"empty": `{"text":""}`, "not json": `nope`, "multi-line": `{"text":"a\nb"}`} {
		if rec := apiDo(t, h, "POST", "/api/mark", body); rec.Code != 400 {
			t.Errorf("%s: code %d, want 400", name, rec.Code)
		}
	}
	if rec := apiDoHeaders(t, h, "POST", "/api/mark", `{"text":"x"}`, nil); rec.Code != 415 {
		t.Errorf("no JSON content type: code %d, want 415", rec.Code)
	}
	if rec := apiDoHeaders(t, h, "POST", "/api/mark", `{"text":"x"}`,
		map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); rec.Code != 403 {
		t.Errorf("cross-site: code %d, want 403", rec.Code)
	}

	_, h2, _ := apiSetup(t, simConfig(idleLines(20))) // no raw log
	if rec := apiDo(t, h2, "POST", "/api/mark", `{"text":"x"}`); rec.Code != 409 {
		t.Errorf("without raw log: code %d, want 409", rec.Code)
	}
}

func TestMarkRefusals(t *testing.T) {
	s := startSim(t, simConfig(idleLines(20)))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)
	if err := d.Mark("x"); !errors.Is(err, ErrNoRawLog) {
		t.Errorf("without raw log: %v, want ErrNoRawLog", err)
	}

	s2 := startSim(t, simConfig(idleLines(20)))
	cfg, _, _ := rawDaemonConfig(t, s2.Addr().String())
	d2, _ := runDaemon(t, cfg)
	waitActive(t, d2)
	for _, text := range []string{"", "two\nlines", strings.Repeat("a", 201)} {
		if err := d2.Mark(text); !errors.Is(err, ErrInvalidMark) {
			t.Errorf("Mark(%.20q) = %v, want ErrInvalidMark", text, err)
		}
	}
}
