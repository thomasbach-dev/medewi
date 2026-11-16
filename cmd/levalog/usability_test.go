package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// TestExtractRefusalTagsEmptyPart: a part without events is listed with
// "# no events", so its -s/-u line need not be tried.
func TestExtractRefusalTagsEmptyPart(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := threeSessionDir(t)
	writeRaw(t, dir, winFrom.Add(15*time.Second)) // an empty session
	code, _, stderr := runCmd(t, extractArgs(dir, "--out", filepath.Join(t.TempDir(), "o"))...)
	if code != 1 || !strings.Contains(stderr, "  -s '2026-11-14 08:58:45.000' -u '2026-11-14 08:58:50.000'  # no events\n") ||
		!strings.Contains(stderr, "  -s '2026-11-14 08:58:50.000' -u '2026-11-14 08:59:00.000'\n") {
		t.Errorf("exit %d, stderr %q; want the empty part tagged, the others not", code, stderr)
	}
}

// overlapDir is extractDir plus a new session at 08:58:40 local while
// f3, the file before it, still holds events up to 08:59:10: their wall
// times overlap, and a window after 08:58:40 holds events of both.
func overlapDir(t *testing.T) string {
	t.Helper()
	dir := extractDir(t)
	writeRaw(t, dir, winFrom.Add(10*time.Second),
		ev(0, fixture.RX, "$id: leva! (3.3)\r\n"),
		ev(20000, fixture.RX, "{new}\r\n"))
	return dir
}

// TestExtractOverlappingSessions: when the window holds events of two
// sessions but splits into fewer than two parts (overlapping wall times),
// the refusal names the boundary in local time with its zone and a next
// step, in both modes; it does not panic.
func TestExtractOverlappingSessions(t *testing.T) {
	noEnv(t)
	inCET(t)
	win := []string{"-s", "2026-11-14 08:58:45", "-u", "2026-11-14 08:59:15"}
	for _, mode := range [][]string{nil, {"--split-sessions"}} {
		args := append(append(append([]string{"extract", "--dir", overlapDir(t)}, win...), mode...),
			"--out", filepath.Join(t.TempDir(), "o"))
		code, _, stderr := runCmd(t, args...)
		if code != 1 || !strings.Contains(stderr, "2026-11-14 08:58:40.000 +01:00") ||
			!strings.Contains(stderr, "FILE arguments") || strings.Contains(stderr, "T07:58:40") {
			t.Errorf("%v: exit %d, stderr %q; want 1, the boundary in local time and a next step", mode, code, stderr)
		}
	}
}

// TestRefuseBoundariesFewParts: the refusal never panics on fewer than
// two parts.
func TestRefuseBoundariesFewParts(t *testing.T) {
	inCET(t)
	at := winFrom.Add(10 * time.Second)
	for _, n := range []int{0, 1} {
		var b strings.Builder
		parts := make([]partInfo, n)
		if code := refuseBoundaries(&b, parts, at); code != 1 || !strings.Contains(b.String(), "2026-11-14 08:58:40.000 +01:00") {
			t.Errorf("%d parts: exit %d, %q", n, code, b.String())
		}
	}
}

// TestExtractNoEventsNextStep: "no events" says what to run next, in
// both modes; so does an input without raw logs.
func TestExtractNoEventsNextStep(t *testing.T) {
	noEnv(t)
	inCET(t)
	for _, mode := range [][]string{nil, {"--split-sessions"}} {
		args := append(append([]string{"extract", "--dir", extractDir(t), "-s", "2026-11-14 08:58:36", "-u", "2026-11-14 08:58:40"},
			mode...), "--out", filepath.Join(t.TempDir(), "o"))
		code, _, stderr := runCmd(t, args...)
		if code != 1 || !strings.Contains(stderr, "no events in [2026-11-14 08:58:36.000, 2026-11-14 08:58:40.000)") ||
			!strings.Contains(stderr, "levalog summary") {
			t.Errorf("%v: exit %d, stderr %q; want no events and a next step", mode, code, stderr)
		}
	}
	code, _, stderr := runCmd(t, "extract", "--dir", t.TempDir(), "-s", "2026-11-14 08:58:36", "-u", "2026-11-14 08:58:40", "--out", "-")
	if code != 1 || !strings.Contains(stderr, "no raw logs to extract from") || !strings.Contains(stderr, "levalog summary") {
		t.Errorf("no raw logs: exit %d, stderr %q; want a next step", code, stderr)
	}
}

// TestExtractSplitSessionsExactly99: 99 parts is the most, and accepted.
func TestExtractSplitSessionsExactly99(t *testing.T) {
	noEnv(t)
	inCET(t)
	if maxParts != 99 {
		t.Fatalf("maxParts = %d, want 99 (two digits)", maxParts)
	}
	dir := t.TempDir()
	for i := range 99 {
		writeRaw(t, dir, winFrom.Add(time.Duration(i)*100*time.Millisecond), ev(0, fixture.RX, "$id: leva! (3.2)\r\n"))
	}
	out := filepath.Join(t.TempDir(), "o")
	code, stdout, stderr := runCmd(t, extractArgs(dir, "--split-sessions", "--out", out)...)
	if code != 0 || strings.Count(stdout, "manifest.json") != 99 || !strings.Contains(stdout, filepath.Join(out, "99", "log.jsonl")) {
		t.Errorf("exit %d, stderr %q; want 99 parts written", code, stderr)
	}
}

// TestMarginZeroWithoutWindow: --margin 0s without a window or run is the
// same usage error in extract and events: the flag given counts, not its
// value.
func TestMarginZeroWithoutWindow(t *testing.T) {
	noEnv(t)
	for _, args := range [][]string{
		{"extract", "--dir", t.TempDir(), "--margin", "0s", "--out", "-"},
		{"--xml", synth, "events", "--dir", t.TempDir(), "--margin", "0s"},
	} {
		code, _, stderr := runCmd(t, args...)
		if code != 2 || !strings.Contains(stderr, "--margin needs a window") {
			t.Errorf("%q: exit %d, stderr %q; want 2", args, code, stderr)
		}
	}
}

// TestXMLMismatchWarnsOnce: an --xml file named differently from the
// session's XML warns once for the session, not once per raw log.
func TestXMLMismatchWarnsOnce(t *testing.T) {
	noEnv(t)
	inCET(t)
	code, _, stderr := runCmd(t, "--xml", synth, "events", "--dir", extractDir(t), "-s", "2026-11-14 06:00", "-u", "2026-11-14 09:00")
	if code != 0 || strings.Count(stderr, "log names "+xmlName) != 1 || !strings.Contains(stderr, "(also in 2 more files)") {
		t.Errorf("exit %d, stderr %q; want one warning for the three files", code, stderr)
	}
}
