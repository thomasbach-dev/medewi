package sim

import (
	"bufio"
	"bytes"
	"net"
	"os"
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

// backflushReports counts the fixture's rx lines starting with '{': what a
// full MCr replay must deliver.
func backflushReports(events []fixture.Event) (reports int) {
	var rx []byte
	for _, ev := range events {
		if ev.Dir == fixture.RX {
			rx = append(rx, ev.Data...)
		}
	}
	for _, l := range bytes.Split(rx, []byte("\r\n")) {
		if bytes.HasPrefix(l, []byte("{")) {
			reports++
		}
	}
	return reports
}

// backflushSpeed replays the 510 s fixture in a quarter second of pacing
// (about 3 s in practice: timer granularity per event): slow enough for the
// client to keep up with its 256-item queue (an overflow disconnects it,
// which the counts below catch). The display mirror runs at its 1 ms floor.
const backflushSpeed = 2000

// The display mirror repeats the current LCD once per frame period
// (100 ms / Speed, floored at 1 ms) for as long as the replay runs, like
// the machine mirroring its LCD; the recorded frames only change what it
// shows.
func TestBackflushFixtureDisplayStream(t *testing.T) {
	events := backflushStream(t)
	s := start(t, Config{Speed: backflushSpeed, Once: true, IdleTimeout: time10s, Stream: events})
	period := s.displayPeriod()
	c, r := dial(t, s)
	readLine(t, r, c)
	replaying := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.stopReplay != nil
	}
	send(t, c, "MC@\r\n")
	waitUntil(t, "the replay to start", replaying)
	begin := time.Now()
	var lines []string
	for replaying() { // read along: the client must keep up with its queue
		lines = append(lines, strings.TrimRight(readLine(t, r, c), "\r\n"))
	}
	elapsed := time.Since(begin)
	send(t, c, "MC \r\n")
	lines = append(lines, drain(t, c, r)...)
	empty := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "<") {
			t.Fatalf("line %q is not a display frame", l)
		}
		if strings.Contains(l, "EMPTY") {
			empty++
		}
	}
	want := float64(elapsed/period) + 1
	t.Logf("%d frames (%d with EMPTY) in %v at a %v period, want about %.0f", len(lines), empty, elapsed, period, want)
	if n := float64(len(lines)); n < 0.9*want || n > 1.1*want {
		t.Errorf("%d frames in %v, want %.0f ± 10 %% (one per %v)", len(lines), elapsed, want, period)
	}
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
