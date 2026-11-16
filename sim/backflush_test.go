package sim

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// drain reads lines until the stream goes quiet for 500 ms.
func drain(t *testing.T, c net.Conn, r *bufio.Reader) []string {
	t.Helper()
	var lines []string
	for {
		if err := c.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		line, err := r.ReadString('\n')
		if err != nil {
			return lines
		}
		lines = append(lines, strings.TrimRight(line, "\r\n"))
	}
}

func backflushStream(t *testing.T) []fixture.Event {
	t.Helper()
	f, err := os.Open("../testdata/fixtures/backflush/log.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	events, err := fixture.ReadLog(f)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// backflushLines counts the fixture's rx lines starting with prefix: '{'
// for what a full MCr replay must deliver, '<' for the recorded frames.
func backflushLines(events []fixture.Event, prefix string) (n int) {
	var rx []byte
	for _, ev := range events {
		if ev.Dir == fixture.RX {
			rx = append(rx, ev.Data...)
		}
	}
	for _, l := range bytes.Split(rx, []byte("\r\n")) {
		if bytes.HasPrefix(l, []byte(prefix)) {
			n++
		}
	}
	return n
}

func backflushReports(events []fixture.Event) int { return backflushLines(events, "{") }

// backflushRecordedFrames returns the fixture's whole frames with their T,
// and the span of its RX timeline (first to last RX event) in ms.
func backflushRecordedFrames(events []fixture.Event) (frames []recordedFrame, first, last int64) {
	first = -1
	for _, ev := range events {
		if ev.Dir != fixture.RX {
			continue
		}
		if first < 0 {
			first = ev.T
		}
		last = ev.T
		for _, l := range strings.SplitAfter(string(ev.Data), "\r\n") {
			if len(l) == 71 && strings.HasPrefix(l, "<") && strings.HasSuffix(l, ">\r\n") {
				frames = append(frames, recordedFrame{t: ev.T - first, line: l})
			}
		}
	}
	return frames, first, last
}

// backflushSpeed replays the 510 s fixture in a quarter second of pacing
// (about 3 s in practice: timer granularity per event): slow enough for the
// client to keep up with its 256-item queue (an overflow disconnects it,
// which the count below catches).
const backflushSpeed = 2000

// The display mirror repeats the current LCD once per frame period
// (100 ms at Speed 1) for as long as the replay runs, like the machine
// mirroring its LCD; the recorded frames only change what it shows. On the
// fake clock the whole 510 s timeline replays exactly: one frame at MC@,
// then one per period, each the latest frame recorded by then.
func TestBackflushFixtureDisplayStream(t *testing.T) {
	events := backflushStream(t)
	recorded, first, last := backflushRecordedFrames(events)
	if len(recorded) != 507 {
		t.Fatalf("%d whole frames in the fixture, want 507", len(recorded))
	}
	h := startFake(t, Config{Speed: 1, Once: true, IdleTimeout: time10s, Responses: pong, Stream: events})
	period := h.displayPeriod()
	c, r := dialFake(t, h)
	type result struct {
		lines []string
		err   error
	}
	done := make(chan result, 1)
	go func() { // read along: the client must keep up with its queue
		var res result
		for {
			_ = c.SetReadDeadline(time.Now().Add(time10s))
			line, err := r.ReadString('\n')
			if err != nil || line == "pong\r\n" {
				res.err = err
				done <- res
				return
			}
			res.lines = append(res.lines, line)
		}
	}()
	send(t, c, "MC@\r\n")
	h.waitFor(t, "the mirror", mirrorOn)
	span := time.Duration(last-first) * time.Millisecond
	h.advance(t, span)
	h.waitFor(t, "the Once replay to end", replayEnded)
	send(t, c, "MCh\r\n")
	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	lines := res.lines

	if want := 1 + int(span/period); len(lines) != want {
		t.Errorf("%d frames over the %v replay, want exactly %d (one at MC@, one per %v)", len(lines), span, want, period)
	}
	empty := 0
	for k, l := range lines {
		ms := int64(time.Duration(k) * period / time.Millisecond)
		if want := lcdAt(recorded, ms); !slices.Contains(want, l) {
			t.Fatalf("frame %d (at %d ms) = %q, want the LCD recorded by then %q", k, ms, l, want)
		}
		if strings.Contains(l, "EMPTY") {
			empty++
		}
	}
	// Spot check: the frame at the first period after the EMPTY prompt
	// was recorded shows it.
	for _, f := range recorded {
		if strings.Contains(f.line, "EMPTY") {
			k := int((time.Duration(f.t)*time.Millisecond + period - 1) / period)
			if k >= len(lines) || !strings.Contains(lines[k], "EMPTY") {
				t.Errorf("frame %d, just after the EMPTY prompt at %d ms, does not show it", k, f.t)
			}
			break
		}
	}
	t.Logf("%d frames, %d with EMPTY, over %v at a %v period", len(lines), empty, span, period)
	if empty == 0 {
		t.Error("no frame shows the EMPTY prompt")
	}
}

func TestBackflushFixtureReportStream(t *testing.T) {
	events := backflushStream(t)
	wantReports := backflushReports(events)
	s := start(t, Config{Speed: backflushSpeed, Once: true, IdleTimeout: time10s, Stream: events})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	lines := drain(t, c, r)
	for _, l := range lines {
		if !strings.HasPrefix(l, "{") {
			t.Fatalf("line %q is not a report", l)
		}
	}
	if len(lines) != wantReports {
		t.Errorf("%d reports, want %d", len(lines), wantReports)
	}
}
