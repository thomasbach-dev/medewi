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

// startWithPeriod starts a sim whose display mirror repeats every period
// (a test hook: an hour means only MC@ itself and MCR emit).
func startWithPeriod(t *testing.T, cfg Config, period time.Duration) *Server {
	t.Helper()
	s := New(cfg)
	s.framePeriod = period
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

// readLineWithin reads one line, failing if it takes longer than d.
func readLineWithin(t *testing.T, r *bufio.Reader, c net.Conn, d time.Duration) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(d))
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("no line within %v: %v (got %q)", d, err, line)
	}
	return line
}

func TestMCAtSendsCurrentLCDAtOnce(t *testing.T) {
	stream := []fixture.Event{
		rx(0, displayFrame(1)),
		rx(500, "{500}\r\n"),
		rx(1000, displayFrame(2)),
		rx(1500, "{1500}\r\n"),
		rx(9000, "{9000}\r\n"),
	}
	s := start(t, Config{Speed: 10, IdleTimeout: time10s, Stream: stream})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	for line := readLine(t, r, c); line != "{1500}\r\n"; line = readLine(t, r, c) {
		if !strings.HasPrefix(line, "{") {
			t.Fatalf("got %q in report mode", line)
		}
	}
	send(t, c, "MC@\r\n")
	// Two frame periods (10 ms at Speed 10) plus scheduling slack.
	if got := readLineWithin(t, r, c, 2*10*time.Millisecond+50*time.Millisecond); got != displayFrame(2) {
		t.Fatalf("first frame after MC@ = %q, want the current LCD %q", got, displayFrame(2))
	}
}

func TestMCAtBeforeFirstFrameSendsFirstFrame(t *testing.T) {
	stream := []fixture.Event{rx(0, "{0}\r\n"), rx(1000, displayFrame(1)), rx(2000, displayFrame(2))}
	s := start(t, Config{Speed: 1, IdleTimeout: time10s, Stream: stream})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MC@\r\n")
	if got := readLineWithin(t, r, c, 2*100*time.Millisecond); got != displayFrame(1) {
		t.Fatalf("got %q, want the stream's first frame %q", got, displayFrame(1))
	}
}

func TestDisplayRepeatsAtFramePeriod(t *testing.T) {
	const period = 10 * time.Millisecond // 100 ms at Speed 10
	s := start(t, Config{Speed: 10, IdleTimeout: time10s, Stream: []fixture.Event{rx(0, displayFrame(7))}})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MC@\r\n")
	const n = 20
	at := make([]time.Time, n)
	for i := range at {
		if got := readLine(t, r, c); got != displayFrame(7) {
			t.Fatalf("frame %d = %q", i, got)
		}
		at[i] = time.Now()
	}
	if span, want := at[n-1].Sub(at[0]), time.Duration(n-1)*period*9/10; span < want {
		t.Errorf("%d frames in %v, want at least %v: faster than the frame period", n, span, want)
	}
	for i := 1; i < n; i++ {
		// Delivery jitter can squeeze one gap; half a period is never the cadence.
		if gap := at[i].Sub(at[i-1]); gap < period/2 {
			t.Errorf("gap %d = %v, want about %v", i, gap, period)
		}
	}
}

func TestDisplayAfterOnceKeepsLastFrame(t *testing.T) {
	stream := []fixture.Event{rx(0, displayFrame(1)), rx(100, "{r}\r\n"), rx(200, displayFrame(2))}
	s := start(t, Config{Speed: 10, Once: true, IdleTimeout: time10s, Stream: stream})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MC@\r\n")
	for line := readLine(t, r, c); line != displayFrame(2); line = readLine(t, r, c) {
		if line != displayFrame(1) {
			t.Fatalf("unexpected line %q", line)
		}
	}
	waitUntil(t, "the Once replay to end", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.stopReplay == nil
	})
	for i := range 5 {
		if got := readLine(t, r, c); got != displayFrame(2) {
			t.Fatalf("frame %d after the replay = %q, want the last frame held", i, got)
		}
	}
}

// lcdIs reports whether the sim's current LCD is frame.
func lcdIs(s *Server, frame string) func() bool {
	return func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return string(s.lcd) == frame
	}
}

func TestMCRResendsCurrentLCD(t *testing.T) {
	stream := []fixture.Event{rx(0, "{0}\r\n"), rx(10, displayFrame(1)), rx(10000, "{10000}\r\n")}
	s := startWithPeriod(t, Config{Speed: 1, IdleTimeout: time10s, Stream: stream}, time.Hour)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	expect(t, r, c, "{0}\r\n")
	waitUntil(t, "frame 1 to become the LCD", lcdIs(s, displayFrame(1)))
	send(t, c, "MC@\r\n")
	expect(t, r, c, displayFrame(1))
	send(t, c, "MCR\r\n")
	// The period is an hour: only MCR can produce this, and it is the LCD, not the last report.
	expect(t, r, c, displayFrame(1))
}

func TestSwitchResetsCurrentLCD(t *testing.T) {
	a := []fixture.Event{rx(0, displayFrame(1)), rx(10, displayFrame(2)), rx(10000, "{a}\r\n")}
	b := []fixture.Event{rx(0, "{b}\r\n"), rx(5000, displayFrame(3)), rx(6000, displayFrame(4))}
	s := startWithPeriod(t, Config{Speed: 1, IdleTimeout: time10s, Stream: a, Switch: map[string][]fixture.Event{"SW": b}}, time.Hour)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	waitUntil(t, "frame 2 to become the LCD", lcdIs(s, displayFrame(2)))
	send(t, c, "SW\r\n")
	waitUntil(t, "the switch", lcdIs(s, displayFrame(3)))
	send(t, c, "MC@\r\n")
	for line := readLine(t, r, c); line != displayFrame(3); line = readLine(t, r, c) {
		if !strings.HasPrefix(line, "{") {
			t.Fatalf("got %q, want the new stream's first frame %q", line, displayFrame(3))
		}
	}
}

func TestSwitchWhileIdleResetsCurrentLCD(t *testing.T) {
	a := []fixture.Event{rx(0, displayFrame(1))}
	b := []fixture.Event{rx(0, "{b}\r\n"), rx(5000, displayFrame(3))}
	s := startWithPeriod(t, Config{Speed: 1, IdleTimeout: time10s, Stream: a, Switch: map[string][]fixture.Event{"SW": b}}, time.Hour)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "SW\r\n")
	send(t, c, "MC@\r\n")
	expect(t, r, c, displayFrame(3))
}

func TestNoFramesNoDisplay(t *testing.T) {
	cfg := Config{
		Speed: 10, IdleTimeout: time10s, Responses: map[string][]string{"MCh": {"pong"}},
		Stream: []fixture.Event{rx(0, "{0}\r\n"), rx(10, "{10}\r\n")},
	}
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MC@\r\n")
	time.Sleep(50 * time.Millisecond) // five frame periods
	send(t, c, "MCR\r\n")
	send(t, c, "MCh\r\n")
	expect(t, r, c, "pong\r\n")
}

// Only a whole recorded frame becomes the LCD; cut or glued pieces never do.
func TestFragmentsNeverBecomeLCD(t *testing.T) {
	report := "{ - r}\r\n"
	for _, tc := range []struct {
		name   string
		stream []fixture.Event
	}{
		{"report cut, frame glued", []fixture.Event{rx(0, "{ - PID  93.0"+realFrame+"\r\n"), rx(10, report)}},
		{"frame continued", []fixture.Event{rx(0, "<102SOAK "), rx(5, "rest>\r\n"), rx(10, report)}},
		{"frame cut, report glued", []fixture.Event{rx(0, "<102SOAK 1.0b  "+report)}},
		{"frame ends in a piece", []fixture.Event{rx(0, "{ - r1"), rx(5, "2}\r\n<102SOAK"), rx(10, " rest>\r\n")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.stream = append(tc.stream, rx(100, displayFrame(9)))
			if got := firstAfter(t, tc.stream, "MC@"); got != displayFrame(9) {
				t.Errorf("got %q, want the first whole frame %q", got, displayFrame(9))
			}
		})
	}
}
