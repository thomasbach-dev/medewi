package sim

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

const time10s = 10 * time.Second

func rx(t int64, data string) fixture.Event {
	return fixture.Event{T: t, Dir: fixture.RX, Data: []byte(data)}
}

func replayCfg() Config {
	return Config{
		Speed: 10000,
		Stream: []fixture.Event{
			rx(0, "a1\r\n"),
			{T: 5, Dir: fixture.TX, Data: []byte("ignored\r\n")},
			rx(10, "a2\r\n"),
		},
	}
}

func send(t *testing.T, c net.Conn, cmd string) {
	t.Helper()
	if _, err := c.Write([]byte(cmd)); err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, r *bufio.Reader, c net.Conn, want string) {
	t.Helper()
	if got := readLine(t, r, c); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMCrStartsReplayForAllClients(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	s := start(t, cfg)
	c1, r1 := dial(t, s)
	c2, r2 := dial(t, s)
	readLine(t, r1, c1)
	readLine(t, r2, c2)
	send(t, c1, "MCr\r\n")
	for _, p := range []struct {
		r *bufio.Reader
		c net.Conn
	}{{r1, c1}, {r2, c2}} {
		expect(t, p.r, p.c, "a1\r\n") // TX event skipped
		expect(t, p.r, p.c, "a2\r\n")
	}
}

func TestReplayLoops(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	for i := 0; i < 3; i++ {
		expect(t, r, c, "a1\r\n")
		expect(t, r, c, "a2\r\n")
	}
}

func TestMCSpaceStopsGlobally(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	cfg.Responses = map[string][]string{"MCh": {"pong"}}
	s := start(t, cfg)
	cA, rA := dial(t, s)
	cB, rB := dial(t, s)
	readLine(t, rA, cA)
	readLine(t, rB, cB)
	send(t, cB, "MCr\r\n") // B starts
	expect(t, rA, cA, "a1\r\n")
	send(t, cA, "MC \r\n") // A stops
	// Drain until the stream goes quiet: a sentinel response marks the end.
	send(t, cA, "MCh\r\n")
	for line := readLine(t, rA, cA); line != "pong\r\n"; line = readLine(t, rA, cA) {
		_ = line // drain in-flight replay items
	}
	// After the sentinel nothing but our next sentinel may arrive.
	send(t, cA, "MCh\r\n")
	expect(t, rA, cA, "pong\r\n")
	s.mu.Lock()
	running := s.stopReplay != nil
	s.mu.Unlock()
	if running {
		t.Fatal("replayer still registered after MC space")
	}
}

// slowCfg replays x1 immediately and x2 only after ~10s, so anything that
// arrives between the two can only come from MCR.
func slowCfg() Config {
	return Config{
		Speed:       1,
		IdleTimeout: time10s * 6,
		Stream:      []fixture.Event{rx(0, "x1\r\n"), rx(10000, "x2\r\n")},
		Responses:   map[string][]string{"MCh": {"pong"}},
	}
}

func TestMCRResendsLast(t *testing.T) {
	s := start(t, slowCfg())
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	expect(t, r, c, "x1\r\n")
	send(t, c, "MCR\r\n")
	expect(t, r, c, "x1\r\n") // x2 is ~10s away: only MCR can produce this
}

func TestMCRIgnoredAfterStop(t *testing.T) {
	s := start(t, slowCfg())
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	expect(t, r, c, "x1\r\n")
	send(t, c, "MC \r\n")
	send(t, c, "MCR\r\n")
	send(t, c, "MCh\r\n")
	expect(t, r, c, "pong\r\n") // nothing may precede the sentinel response
}

func TestMCRWhenIdleSendsNothing(t *testing.T) {
	cfg := Config{Speed: 1000, IdleTimeout: time10s, Responses: map[string][]string{"MCh": {"pong"}}}
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCR\r\n")
	send(t, c, "MCh\r\n")
	expect(t, r, c, "pong\r\n")
}

func TestEmptyStreamReportsNothing(t *testing.T) {
	cfg := Config{Speed: 1000, IdleTimeout: time10s, Responses: map[string][]string{"MCh": {"pong"}}}
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	send(t, c, "MCR\r\n")
	send(t, c, "MCh\r\n")
	expect(t, r, c, "pong\r\n")
}

func TestReportingSurvivesDisconnectAndNoDoubleReplayer(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	s := start(t, cfg)
	c1, r1 := dial(t, s)
	readLine(t, r1, c1)
	send(t, c1, "MCr\r\n")
	send(t, c1, "MCr\r\n") // second start must not spawn a second replayer
	expect(t, r1, c1, "a1\r\n")
	_ = c1.Close()
	c2, r2 := dial(t, s)
	readLine(t, r2, c2)
	// The stream continues for a late joiner, strictly alternating a1/a2.
	for i := 0; i < 5; i++ {
		first := readLine(t, r2, c2)
		if first == "a2\r\n" {
			expect(t, r2, c2, "a1\r\n")
			continue
		}
		expect(t, r2, c2, "a2\r\n")
	}
}

func TestStopTerminatesReplayer(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	s := New(cfg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	expect(t, r, c, "a1\r\n")
	done := make(chan struct{})
	go func() { _ = s.Stop(); close(done) }()
	<-done // Stop joins the replayer via wg; hangs (test timeout) on a leak
}

// displayFrame is a 69-byte LCD frame (MC@) carrying i in its text.
func displayFrame(i int) string {
	return fmt.Sprintf("<%-67d>\r\n", i)
}

// mixedStream alternates report lines and display frames, 50 ms apart, as
// a raw log of a client switching between MCr and MC@ records them. Each
// line carries its t.
func mixedStream(n int) []fixture.Event {
	evs := make([]fixture.Event, n)
	for i := range evs {
		t := int64(i * 50)
		if i%2 == 0 {
			evs[i] = rx(t, fmt.Sprintf("{%d}\r\n", t))
		} else {
			evs[i] = rx(t, displayFrame(int(t)))
		}
	}
	return evs
}

// lineT returns the t a mixedStream line carries.
func lineT(t *testing.T, line string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(strings.TrimRight(line, "\r\n"), "{}<>")))
	if err != nil {
		t.Fatalf("line %q carries no t: %v", line, err)
	}
	return n
}

func TestMCAtReplaysFramesOnly(t *testing.T) {
	s := start(t, Config{Speed: 10, IdleTimeout: time10s, Stream: mixedStream(20)})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MC@\r\n")
	for i := range 6 {
		if got := readLine(t, r, c); !strings.HasPrefix(got, "<") || len(got) != 71 {
			t.Fatalf("line %d = %q, want a display frame", i, got)
		}
	}
}

func TestMCrSkipsFrames(t *testing.T) {
	s := start(t, Config{Speed: 10, IdleTimeout: time10s, Stream: mixedStream(20)})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	for i := range 12 { // more than one loop of the stream
		if got := readLine(t, r, c); !strings.HasPrefix(got, "{") {
			t.Fatalf("line %d = %q, want a report", i, got)
		}
	}
}

// Switching from reports to display does not restart the timeline: the
// first frame is the current LCD, at least the frame recorded just before
// the last report seen, not the stream's first frame.
func TestStreamSwitchKeepsTimeline(t *testing.T) {
	s := start(t, Config{Speed: 10, Once: true, IdleTimeout: time10s, Stream: mixedStream(400)})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	var last int
	for range 3 {
		line := readLine(t, r, c)
		if !strings.HasPrefix(line, "{") {
			t.Fatalf("got %q, want a report", line)
		}
		last = lineT(t, line)
	}
	send(t, c, "MC@\r\n")
	line := readLine(t, r, c)
	for strings.HasPrefix(line, "{") { // reports in flight before MC@
		line = readLine(t, r, c)
	}
	if !strings.HasPrefix(line, "<") {
		t.Fatalf("got %q, want a display frame", line)
	}
	// mixedStream: frames sit 50 ms before each report after the first.
	if got := lineT(t, line); got < last-50 {
		t.Errorf("first frame has t %d, before the frame at %d the last report followed: the timeline restarted", got, last-50)
	}
}

func TestMCSpaceThenMCAtResumesFrames(t *testing.T) {
	cfg := Config{Speed: 10, IdleTimeout: time10s, Stream: mixedStream(20), Responses: map[string][]string{"MCh": {"pong"}}}
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MC@\r\n")
	if got := readLine(t, r, c); !strings.HasPrefix(got, "<") {
		t.Fatalf("got %q, want a display frame", got)
	}
	send(t, c, "MC \r\n")
	send(t, c, "MCh\r\n")
	for line := readLine(t, r, c); line != "pong\r\n"; line = readLine(t, r, c) {
		_ = line // drain in-flight frames
	}
	send(t, c, "MC@\r\n")
	for i := range 3 {
		if got := readLine(t, r, c); !strings.HasPrefix(got, "<") {
			t.Fatalf("line %d = %q, want a display frame", i, got)
		}
	}
}

func TestNoHeartbeatWhileDisplay(t *testing.T) {
	cfg := Config{
		BannerInterval: 20 * time.Millisecond,
		Once:           true, // a loop would put frame 1 back on the LCD 1 ms after frame 2
		IdleTimeout:    time10s,
		Stream:         []fixture.Event{rx(0, displayFrame(1)), rx(100, "{r}\r\n"), rx(300, displayFrame(2))},
	}
	s := start(t, cfg)
	c, r := dial(t, s)
	send(t, c, "MC@\r\n")
	// Banners queued before MC@ was processed are fine; skip to the first frame.
	for line := readLine(t, r, c); line != displayFrame(1); line = readLine(t, r, c) {
		if line != bannerLine {
			t.Fatalf("unexpected line %q", line)
		}
	}
	// The LCD repeats every 100 ms; until frame 2 replaces it (300 ms, 15
	// banner intervals) any banner or the report would show up in between.
	for line := readLine(t, r, c); line != displayFrame(2); line = readLine(t, r, c) {
		if line != displayFrame(1) {
			t.Fatalf("unexpected line %q while the display mirror is on", line)
		}
	}
}

// realFrame is a 69-byte frame with a record start leva.SplitLine
// recognises ("<1" + two digits), so it can be glued onto other records.
var realFrame = fmt.Sprintf("<102%-64s>", "SOAK")

// firstAfter sends cmd and returns the first replayed line (Once replay,
// so the replayer starts at event 0).
func firstAfter(t *testing.T, stream []fixture.Event, cmd string) string {
	t.Helper()
	s := start(t, Config{Speed: 10, Once: true, IdleTimeout: time10s, Stream: stream})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, cmd+"\r\n")
	return readLine(t, r, c)
}

// A stream switch glues records: the record that completes an event
// decides its class, since that is the stream the machine was sending.
// Report mode shows the classification; in display mode recorded frames
// only update the LCD (TestFragmentsNeverBecomeLCD).
func TestGluedEventClassifiedByLastRecord(t *testing.T) {
	cutFrameThenReport := "<102SOAK 1.0b  " + "{ - report}\r\n"
	cutReportThenFrame := "{ - PID  93.0" + realFrame + "\r\n"
	report := "{ - r}\r\n"
	frame := realFrame + "\r\n"
	for _, tc := range []struct {
		name   string
		stream []fixture.Event
		cmd    string
		want   string
	}{
		{"frame cut, report glued: MCr", []fixture.Event{rx(0, cutFrameThenReport), rx(10, frame), rx(20, report)}, "MCr", cutFrameThenReport},
		{"report cut, frame glued: MCr", []fixture.Event{rx(0, cutReportThenFrame), rx(10, frame), rx(20, report)}, "MCr", report},
		// A continuation piece (no record start) follows the event before it.
		{"frame continued: MCr", []fixture.Event{rx(0, "<102SOAK "), rx(5, "rest>\r\n"), rx(10, report)}, "MCr", report},
		{"report continued: MCr", []fixture.Event{rx(0, "{ - r1"), rx(5, "2}\r\n"), rx(10, frame)}, "MCr", "{ - r12}\r\n"},
		// A continuation piece with a glued record takes that record's class
		// (chunked captures: the switch can fall inside a read).
		{"report ends, frame glued in a piece: MCr", []fixture.Event{rx(0, "{ - r1"), rx(5, "2}\r\n<102SOAK"), rx(10, " rest>\r\n"), rx(15, report)}, "MCr", "{ - r1" + report},
		{"frame ends, report glued in a piece: MCr", []fixture.Event{rx(0, "<102SOAK 1.0b"), rx(5, "  { - PID"), rx(10, " 93}\r\n"), rx(15, frame)}, "MCr", "  { - PID 93}\r\n"},
		// Notices stay report-stream traffic.
		{"notice: MCr", []fixture.Event{rx(0, frame), rx(10, "[READY]\r\n")}, "MCr", "[READY]\r\n"},
		// An event that completes an LCD frame is display traffic, also
		// with a report glued on after it: never both LCD and report item.
		{"whole frame, report glued: MCr", []fixture.Event{rx(0, frame+"{ - r1}\r\n"), rx(10, report)}, "MCr", report},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstAfter(t, tc.stream, tc.cmd); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// waitUntil polls cond (which may take s.mu) until it holds or 5 s pass.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
