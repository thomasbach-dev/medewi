package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/rawlog"
)

// matchDir writes one raw-log session with marks, a notice, a report and
// a display frame (second row "EMPTY", fourth "PUMP").
func matchDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	l, err := rawlog.Open(rawlog.Config{Dir: dir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	s := l.Session()
	s.Note("mark: S Backflush")
	s.Note("mark: espresso")
	s.Note("mark: F backflush")
	s.Note("power on")
	s.Tap("rx", []byte("[READY] backflush done\r\n"))
	s.Tap("rx", append(testReport(), '\r', '\n'))
	frame := "<102" + fmt.Sprintf("%-16s%-16s%-16s%-16s", "row one", "EMPTY", "row three", "PUMP") + ">"
	s.Tap("rx", []byte(frame+"\r\n"))
	s.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEventsMatch(t *testing.T) {
	noEnv(t)
	dir := matchDir(t)
	count := strings.Count
	code, out, errs := runCmd(t, "--xml", synth, "events", "--dir", dir, "--event", "note", "--match", "backflush")
	if code != 0 || errs != "" {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if count(out, "mark: S Backflush") != 1 || count(out, "mark: F backflush") != 1 ||
		count(out, " note ") != 2 || strings.Contains(out, "espresso") || strings.Contains(out, "READY") {
		t.Errorf("--event note --match backflush:\n%s", out)
	}
	// Without --event the notice matches too.
	_, out, _ = runCmd(t, "--xml", synth, "events", "--dir", dir, "--match", "BACKFLUSH")
	if count(out, " note ")+count(out, " notice ") != 3 || count(out, " report ") != 0 {
		t.Errorf("--match BACKFLUSH:\n%s", out)
	}
	// A frame matches by its rows, joined with |.
	for _, m := range []string{"empty", "PUMP", "empty           |row three"} {
		_, out, _ = runCmd(t, "--xml", synth, "events", "--dir", dir, "--match", m)
		if count(out, " frame ") != 1 {
			t.Errorf("--match %q: want the frame:\n%s", m, out)
		}
	}
	// A report matches by its raw line; jsonl works too.
	_, out, _ = runCmd(t, "--xml", synth, "events", "--dir", dir, "--format", "jsonl", "--match", "PID  93.5")
	if count(out, `"event":"report"`) != 1 || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Errorf("--match on report, jsonl:\n%s", out)
	}
	// Nothing matches: no rows, and stderr says what the filter dropped.
	code, out, errs = runCmd(t, "--xml", synth, "events", "--dir", dir, "--match", "nonesuch")
	if code != 0 || out != "" || !strings.Contains(errs, "--match nonesuch: ") {
		t.Errorf("no match: exit %d, out %q, stderr %q", code, out, errs)
	}
}

func TestEventsMatchUsage(t *testing.T) {
	noEnv(t)
	for name, args := range map[string][]string{
		"empty":       {"events", "--match", ""},
		"with every":  {"--xml", synth, "events", "--every", "10s", "--match", "x"},
		"empty every": {"--xml", synth, "events", "--every", "10s", "--match", ""},
	} {
		code, _, errs := runCmd(t, args...)
		if code != 2 || !strings.Contains(errs, "--match") {
			t.Errorf("%s: exit %d, stderr %q", name, code, errs)
		}
	}
}
