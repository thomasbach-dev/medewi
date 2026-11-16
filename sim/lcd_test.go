package sim

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// The tests here run the sim on a fake clock (fakeclock_test.go): the
// mirror's cadence and the replay's timeline are asserted exactly, with no
// wall-clock tolerance. TestDisplayMirrorRealClock covers the real clock.

var pong = map[string][]string{"MCh": {"pong"}}

// dialFake connects to h and reads the banner.
func dialFake(t *testing.T, h *fakeSim) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, r := dial(t, h.Server)
	readLine(t, r, c)
	return c, r
}

// expectQuiet asserts that nothing but the sentinel answer comes next.
func expectQuiet(t *testing.T, r *bufio.Reader, c net.Conn) {
	t.Helper()
	send(t, c, "MCh\r\n")
	expect(t, r, c, "pong\r\n")
}

func TestMCAtSendsCurrentLCDAtOnce(t *testing.T) {
	stream := []fixture.Event{
		rx(0, displayFrame(1)),
		rx(500, "{500}\r\n"),
		rx(1000, displayFrame(2)),
		rx(1500, "{1500}\r\n"),
		rx(9000, "{9000}\r\n"),
	}
	h := startFake(t, Config{Speed: 10, IdleTimeout: time10s, Stream: stream})
	c, r := dialFake(t, h)
	send(t, c, "MCr\r\n")
	h.waitFor(t, "the replay", replayOn)
	h.advance(t, 150*time.Millisecond) // 1500 ms of the stream at Speed 10
	expect(t, r, c, "{500}\r\n")
	expect(t, r, c, "{1500}\r\n")
	send(t, c, "MC@\r\n")
	expect(t, r, c, displayFrame(2)) // the clock stands still: only MC@ itself sends
}

func TestMCAtBeforeFirstFrameSendsFirstFrame(t *testing.T) {
	stream := []fixture.Event{rx(0, "{0}\r\n"), rx(1000, displayFrame(1)), rx(2000, displayFrame(2))}
	h := startFake(t, Config{Speed: 1, IdleTimeout: time10s, Stream: stream})
	c, r := dialFake(t, h)
	send(t, c, "MC@\r\n")
	expect(t, r, c, displayFrame(1))
}

func TestDisplayRepeatsAtFramePeriod(t *testing.T) {
	const period = 10 * time.Millisecond // 100 ms at Speed 10
	h := startFake(t, Config{Speed: 10, IdleTimeout: time10s, Responses: pong, Stream: []fixture.Event{rx(0, displayFrame(7))}})
	c, r := dialFake(t, h)
	send(t, c, "MC@\r\n")
	h.waitFor(t, "the mirror", mirrorOn)
	h.advance(t, 7*period)
	for i := range 1 + 7 { // at once, then one per period
		if got := readLine(t, r, c); got != displayFrame(7) {
			t.Fatalf("frame %d = %q", i, got)
		}
	}
	expectQuiet(t, r, c)
	h.advance(t, period-time.Nanosecond)
	expectQuiet(t, r, c)
	h.advance(t, time.Nanosecond)
	expect(t, r, c, displayFrame(7))
	expectQuiet(t, r, c)
}

func TestDisplayAfterOnceKeepsLastFrame(t *testing.T) {
	stream := []fixture.Event{rx(0, displayFrame(1)), rx(100, "{r}\r\n"), rx(205, displayFrame(2))}
	h := startFake(t, Config{Speed: 10, Once: true, IdleTimeout: time10s, Responses: pong, Stream: stream})
	c, r := dialFake(t, h)
	send(t, c, "MC@\r\n")
	h.waitFor(t, "the mirror", mirrorOn)
	h.advance(t, 25*time.Millisecond)     // frame 2 at 20.5 ms ends the stream
	for _, want := range []int{1, 1, 1} { // at once, 10 ms, 20 ms
		expect(t, r, c, displayFrame(want))
	}
	h.waitFor(t, "the Once replay to end", replayEnded)
	h.advance(t, 50*time.Millisecond)
	for range 5 {
		expect(t, r, c, displayFrame(2))
	}
	expectQuiet(t, r, c)
}

func TestMCRResendsCurrentLCD(t *testing.T) {
	stream := []fixture.Event{rx(0, "{0}\r\n"), rx(10, displayFrame(1)), rx(10000, "{10000}\r\n")}
	h := startFake(t, Config{Speed: 1, IdleTimeout: time10s, Stream: stream})
	c, r := dialFake(t, h)
	send(t, c, "MCr\r\n")
	expect(t, r, c, "{0}\r\n")
	h.advance(t, 10*time.Millisecond)
	send(t, c, "MC@\r\n")
	expect(t, r, c, displayFrame(1))
	send(t, c, "MCR\r\n")
	// The clock stands still: only MCR can produce this, and it is the
	// LCD, not the last report.
	expect(t, r, c, displayFrame(1))
}

func TestSwitchResetsCurrentLCD(t *testing.T) {
	a := []fixture.Event{rx(0, displayFrame(1)), rx(10, displayFrame(2)), rx(10000, "{a}\r\n")}
	b := []fixture.Event{rx(0, "{b}\r\n"), rx(5000, displayFrame(3)), rx(6000, displayFrame(4))}
	h := startFake(t, Config{Speed: 1, IdleTimeout: time10s, Stream: a, Switch: map[string][]fixture.Event{"SW": b}})
	c, r := dialFake(t, h)
	send(t, c, "MCr\r\n")
	h.waitFor(t, "the replay", replayOn)
	h.advance(t, 10*time.Millisecond)
	if got := lcdOf(h.Server); got != displayFrame(2) {
		t.Fatalf("LCD %q, want %q", got, displayFrame(2))
	}
	send(t, c, "SW\r\n")
	h.waitFor(t, "the switch", func(s *Server) bool { return lcdOf(s) == displayFrame(3) })
	send(t, c, "MC@\r\n")
	expect(t, r, c, "{b}\r\n") // the new stream's first event, a report
	expect(t, r, c, displayFrame(3))
}

func TestSwitchWhileIdleResetsCurrentLCD(t *testing.T) {
	a := []fixture.Event{rx(0, displayFrame(1))}
	b := []fixture.Event{rx(0, "{b}\r\n"), rx(5000, displayFrame(3))}
	h := startFake(t, Config{Speed: 1, IdleTimeout: time10s, Stream: a, Switch: map[string][]fixture.Event{"SW": b}})
	c, r := dialFake(t, h)
	send(t, c, "SW\r\n")
	send(t, c, "MC@\r\n")
	expect(t, r, c, displayFrame(3))
}

func TestNoFramesNoDisplay(t *testing.T) {
	cfg := Config{Speed: 10, IdleTimeout: time10s, Responses: pong, Stream: []fixture.Event{rx(0, "{0}\r\n"), rx(10, "{10}\r\n")}}
	h := startFake(t, cfg)
	c, r := dialFake(t, h)
	send(t, c, "MC@\r\n")
	h.waitFor(t, "the mirror", mirrorOn)
	h.advance(t, 50*time.Millisecond) // five frame periods
	send(t, c, "MCR\r\n")
	expectQuiet(t, r, c)
}

// The real clock drives the mirror outside the tests above: frames keep
// coming, and they are all the LCD.
func TestDisplayMirrorRealClock(t *testing.T) {
	s := start(t, Config{Speed: 10, IdleTimeout: time10s, Stream: []fixture.Event{rx(0, displayFrame(7))}})
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MC@\r\n")
	for i := range 5 {
		if got := readLine(t, r, c); got != displayFrame(7) {
			t.Fatalf("frame %d = %q", i, got)
		}
	}
}

// Only a whole recorded frame becomes the LCD; cut or glued pieces never do.
func TestFragmentsNeverBecomeLCD(t *testing.T) {
	report := "{ - r}\r\n"
	for _, tc := range []struct {
		name   string
		stream []fixture.Event
	}{
		{"frame continued", []fixture.Event{rx(0, "<102SOAK "), rx(5, "rest>\r\n"), rx(10, report)}},
		{"frame cut by CRLF", []fixture.Event{rx(0, "<102SOAK 1.0b\r\n"), rx(10, report)}},
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

// lcdFrame is a frame leva.SplitLine finds glued after a cut-off record.
func lcdFrame(text string) string { return fmt.Sprintf("<102%-64s>", text) }

// levacap events are TCP reads: a read can hold two frames, half a frame,
// or a cut-off frame with the next one glued on. The sim reassembles the
// traffic across events; the last whole frame an event completes becomes
// the LCD when the timeline passes that event.
func TestLCDReassembledAcrossEvents(t *testing.T) {
	f0, f1, f2 := lcdFrame("F0"), lcdFrame("F1"), lcdFrame("F2")
	for _, tc := range []struct {
		name   string
		stream []fixture.Event // all after f0 at t=0, the end of it at t=200
		want   string
	}{
		{"two frames in one read", []fixture.Event{rx(200, f1+"\r\n"+f2+"\r\n")}, f2},
		{"frame split over two reads", []fixture.Event{rx(10, f1[:30]), rx(200, f1[30:]+"\r\n")}, f1},
		{"cut frame, next frame glued", []fixture.Event{rx(10, "<102SOAK 1.0b"), rx(200, f1+"\r\n")}, f1},
		{"report cut, frame glued", []fixture.Event{rx(200, "{ - PID  93.0"+f1+"\r\n")}, f1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := append([]fixture.Event{rx(0, f0+"\r\n")}, tc.stream...)
			stream = append(stream, rx(10000, "{ - end}\r\n"))
			h := startFake(t, Config{Speed: 1, IdleTimeout: time10s, Stream: stream})
			c, _ := dialFake(t, h)
			send(t, c, "MCr\r\n")
			h.waitFor(t, "the replay", replayOn)
			h.advance(t, 199*time.Millisecond)
			if got := lcdOf(h.Server); got != f0+"\r\n" {
				t.Fatalf("LCD %q before the frame completes, want %q", got, f0+"\r\n")
			}
			h.advance(t, time.Millisecond)
			if got := lcdOf(h.Server); got != tc.want+"\r\n" {
				t.Fatalf("LCD %q after the frame completes, want %q", got, tc.want+"\r\n")
			}
		})
	}
}

// recordedFrame is a whole frame of a fixture (CRLF included) and its T.
type recordedFrame struct {
	t    int64
	line string
}

// lcdAt is the expected current LCD at stream time ms: the latest whole
// frame recorded at or before it, the first frame before any; with a frame
// recorded exactly at ms, the one before it is fine too (a tie between the
// replay and the mirror).
func lcdAt(frames []recordedFrame, ms int64) (want []string) {
	i := 0
	for i+1 < len(frames) && frames[i+1].t <= ms {
		i++
	}
	want = []string{frames[i].line}
	if frames[i].t == ms && i > 0 {
		want = append(want, frames[i-1].line)
	}
	return want
}

// A read holding a report and then a whole frame keeps both: the report
// goes out in report mode, the frame onto the LCD.
func TestReportAndFrameInOneRead(t *testing.T) {
	f0, f1 := lcdFrame("F0"), lcdFrame("F1")
	stream := []fixture.Event{rx(0, f0+"\r\n"), rx(10, "{ - r0}\r\n"+f1+"\r\n"), rx(10000, "{ - end}\r\n")}
	h := startFake(t, Config{Speed: 1, IdleTimeout: time10s, Stream: stream})
	c, r := dialFake(t, h)
	send(t, c, "MCr\r\n")
	h.waitFor(t, "the replay", replayOn)
	h.advance(t, 10*time.Millisecond)
	expect(t, r, c, "{ - r0}\r\n")
	if got := lcdOf(h.Server); got != f1+"\r\n" {
		t.Fatalf("LCD %q, want %q", got, f1+"\r\n")
	}
}

// Frameless fixtures replay byte-for-byte as before the LCD: every RX
// event is one verbatim report item, and nothing reaches the LCD.
func TestFramelessFixturesReplayVerbatim(t *testing.T) {
	for _, name := range []string{"idle-stream", "shot-genlever", "steam-eco", "synthetic", "warmup-stream"} {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join("..", "testdata", "fixtures", name, "log.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			events, err := fixture.ReadLog(f)
			if err != nil {
				t.Fatal(err)
			}
			var rt router
			n := 0
			for i, ev := range events {
				if ev.Dir != fixture.RX {
					continue
				}
				n++
				got := rt.feed(ev.Data)
				if len(got.reports) != 1 || !bytes.Equal(got.reports[0], ev.Data) || len(got.frames) != 0 {
					t.Fatalf("event %d %q routed to %q, frames %q; want it verbatim", i, ev.Data, got.reports, got.frames)
				}
			}
			if n == 0 {
				t.Fatal("no RX events")
			}
		})
	}
}
