package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/fixture"
)

// runCmdIn is runCmd with stdin.
func runCmdIn(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

// frameLog is a capture log.jsonl with a banner, a display frame and a note.
func frameLog(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	frame := "<112" + "Line one        " + "Line two        " + "Line three      " + "Line four       " + ">\r\n"
	if err := fixture.WriteLog(&b, []fixture.Event{
		ev(0, fixture.RX, "$id: leva! (3.2)\r\n"),
		ev(100, fixture.TX, "MC@"),
		ev(300, fixture.RX, frame),
		ev(400, fixture.Note, "S Backflush"),
	}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestStdinEventsFrame(t *testing.T) {
	noEnv(t)
	code, out, errs := runCmdIn(t, frameLog(t), "events", "--event", "frame", "-")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	var data []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(l, "# ") {
			data = append(data, l)
		}
	}
	if !strings.HasPrefix(out, "# -\n") || len(data) != 1 || !strings.Contains(data[0], "frame") ||
		!strings.Contains(data[0], "Line one") {
		t.Errorf("output:\n%s", out)
	}
}

// stdinCSV compares a subcommand on a capture directory with the same on
// its log.jsonl from stdin, where the source path reads "-".
func stdinCSV(t *testing.T, xml, capture string, args ...string) {
	t.Helper()
	log, err := os.ReadFile(filepath.Join(capture, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	code, want, errs := runCmd(t, append(append([]string{"--xml", xml}, args...), capture)...)
	if code != 0 {
		t.Fatalf("%q on the directory: exit %d, stderr %q", args, code, errs)
	}
	want = strings.ReplaceAll(want, filepath.Join(capture, "log.jsonl"), "-")
	code, got, errs := runCmdIn(t, string(log), append(append([]string{"--xml", xml}, args...), "-")...)
	if code != 0 || errs != "" {
		t.Fatalf("%q on stdin: exit %d, stderr %q", args, code, errs)
	}
	if got != want {
		t.Errorf("%q on stdin:\n%s\nwant:\n%s", args, got, want)
	}
}

func TestStdinRunsSummary(t *testing.T) {
	noEnv(t)
	xml := statsXML(t)
	stdinCSV(t, xml, statsCapture(t), "runs", "--format", "csv", "--stats", "pressure")
	stdinCSV(t, xml, summaryCapture(t), "summary", "--format", "jsonl")
	stdinCSV(t, xml, statsCapture(t), "events", "--format", "jsonl", "--run", "1")
}

// A "-" mixes with other captures: it is one more capture, in argument
// order.
func TestStdinWithOtherCaptures(t *testing.T) {
	noEnv(t)
	capture := statsCapture(t)
	code, out, errs := runCmdIn(t, frameLog(t), "events", "--event", "frame,report", capture, "-")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	i, j := strings.Index(out, "# "+filepath.Join(capture, "log.jsonl")+"\n"), strings.Index(out, "# -\n")
	if i < 0 || j < i {
		t.Errorf("want the directory's header, then -'s:\n%s", out)
	}
}

func TestStdinUsageErrors(t *testing.T) {
	noEnv(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"events", "-", "-"}, "once"},
		{[]string{"--xml", synth, "runs", "-", "-"}, "once"},
		{[]string{"events", "-s", "2026-11-14 08:58", "-"}, "wall time"},
		{[]string{"summary", "-u", "2026-11-14 08:58", "-"}, "wall time"},
		{[]string{"extract", "-s", "2026-11-14 08:58", "-u", "2026-11-14 08:59", "--out", "x", "-"}, "stdin"},
	} {
		code, _, errs := runCmdIn(t, frameLog(t), c.args...)
		if code != 2 || !strings.Contains(errs, c.want) {
			t.Errorf("%q: exit %d, stderr %q; want 2 and %q", c.args, code, errs, c.want)
		}
	}
}

// The round trip: extract --out - piped into events - prints the same
// events as events on the capture extract --out DIR writes.
func TestStdinRoundTrip(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := extractDir(t)
	code, log, errs := runCmd(t, extractArgs(dir, "--out", "-")...)
	if code != 0 {
		t.Fatalf("extract --out -: exit %d, stderr %q", code, errs)
	}
	out := filepath.Join(t.TempDir(), "cap")
	if code, _, errs := runCmd(t, extractArgs(dir, "--out", out)...); code != 0 {
		t.Fatalf("extract --out DIR: exit %d, stderr %q", code, errs)
	}
	body := func(s string) string {
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(l, "# ") || strings.HasPrefix(l, "# xml: ") {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	code, want, errs := runCmd(t, "events", out)
	if code != 0 {
		t.Fatalf("events DIR: exit %d, stderr %q", code, errs)
	}
	code, got, errs := runCmdIn(t, log, "events", "-")
	if code != 0 {
		t.Fatalf("events -: exit %d, stderr %q", code, errs)
	}
	if body(got) != body(want) || !strings.Contains(got, "{b}") {
		t.Errorf("events -:\n%s\nwant:\n%s", got, want)
	}
}

func TestHelpStdin(t *testing.T) {
	noEnv(t)
	_, _, errs := runCmd(t, "--help")
	for _, w := range []string{"base64", "jq -r '.data | @base64d'", "--out - | levalog events --event frame -", "from stdin"} {
		if !strings.Contains(errs, w) {
			t.Errorf("help does not mention %q", w)
		}
	}
}
