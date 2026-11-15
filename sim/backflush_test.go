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

// backflushCounts counts the fixture's rx lines starting with '<' (display
// frames) and '{' (reports): what a full MC@ or MCr replay must deliver.
func backflushCounts(events []fixture.Event) (frames, reports int) {
	var rx []byte
	for _, ev := range events {
		if ev.Dir == fixture.RX {
			rx = append(rx, ev.Data...)
		}
	}
	for _, l := range bytes.Split(rx, []byte("\r\n")) {
		switch {
		case bytes.HasPrefix(l, []byte("<")):
			frames++
		case bytes.HasPrefix(l, []byte("{")):
			reports++
		}
	}
	return frames, reports
}

// backflushSpeed replays the 510 s fixture in a quarter second of pacing
// (about 3 s in practice: timer granularity per event): slow enough for the
// client to keep up with its 256-item queue (an overflow disconnects it,
// which the exact counts below catch).
const backflushSpeed = 2000

func TestBackflushFixtureDisplayStream(t *testing.T) {
	events := backflushStream(t)
	wantFrames, _ := backflushCounts(events)
	s := start(t, Config{Speed: backflushSpeed, Once: true, IdleTimeout: time10s, Stream: events})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MC@\r\n")
	lines := drain(t, c, r)
	empty := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "<") {
			t.Fatalf("line %q is not a display frame", l)
		}
		if strings.Contains(l, "EMPTY") {
			empty++
		}
	}
	if len(lines) != wantFrames || empty == 0 {
		t.Errorf("%d frames, %d with EMPTY; want %d frames including the EMPTY prompt", len(lines), empty, wantFrames)
	}
}

func TestBackflushFixtureReportStream(t *testing.T) {
	events := backflushStream(t)
	_, wantReports := backflushCounts(events)
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
