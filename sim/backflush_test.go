package sim

import (
	"bufio"
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

func TestBackflushFixtureDisplayStream(t *testing.T) {
	s := start(t, Config{Speed: 100000, Once: true, IdleTimeout: time10s, Stream: backflushStream(t)})
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
	if len(lines) == 0 || empty == 0 {
		t.Errorf("%d frames, %d with EMPTY; want frames including the EMPTY prompt", len(lines), empty)
	}
}

func TestBackflushFixtureReportStream(t *testing.T) {
	s := start(t, Config{Speed: 100000, Once: true, IdleTimeout: time10s, Stream: backflushStream(t)})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	lines := drain(t, c, r)
	if len(lines) == 0 {
		t.Fatal("no reports")
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "<") {
			t.Fatalf("display frame %q in the report stream", l)
		}
	}
}
