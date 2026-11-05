package leva

import (
	"context"
	"testing"
)

// A real frame (2026-11-05); \x02 and \x05 are custom bar-graph glyphs.
const readyFrame = "<102SOAK 1.0b       1: 91.0\xdf   READY\x02             7% 0.1b\x05  0s   0ml>"

func TestParseFrame(t *testing.T) {
	f, err := ParseFrame([]byte(readyFrame))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Backlight || f.Key != '0' || f.Layout != '2' {
		t.Errorf("header = %v %q %q", f.Backlight, f.Key, f.Layout)
	}
	if f.Rows[0] != "SOAK 1.0b       " || f.Rows[1] != "1: 91.0°   READY" || f.Rows[2] != "              7%" {
		t.Errorf("rows = %q", f.Rows)
	}
	if !f.Contains("READY") || f.Contains("ECO") {
		t.Error("Contains")
	}
}

func TestParseFrameGlyphs(t *testing.T) {
	// Custom glyphs (bar graph) are control bytes; they become spaces.
	line := "<002SOAK 1.0b       1: 91.0\xdf \x04 91.0\xdf\x01             5% 0.1b\x05  0s   0ml>"
	if len(line) != 69 {
		t.Fatalf("test frame is %d bytes", len(line))
	}
	f, err := ParseFrame([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if f.Rows[1] != "1: 91.0°   91.0°" || f.Backlight {
		t.Errorf("row 1 = %q backlight %v", f.Rows[1], f.Backlight)
	}
}

func TestParseFrameRejects(t *testing.T) {
	for _, line := range []string{"", "<102", readyFrame[:68], "{" + readyFrame[1:], readyFrame[:68] + "x"} {
		if _, err := ParseFrame([]byte(line)); err == nil {
			t.Errorf("ParseFrame(%q) accepted", line)
		}
	}
}

// The machine announces READY with an unsolicited "[READY]" line (seen
// twice on 2026-11-05). Bracketed lines are notices: never command output.
func TestNoticesNotCommandReplies(t *testing.T) {
	c, s := newPair(t)
	go func() {
		s.readLine(t) // MC?
		s.write(t, "[READY]\r\nXML=x\r\n")
	}()
	got, err := c.Command(context.Background(), "MC?")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "XML=x" {
		t.Errorf("command lines = %q", got)
	}
	select {
	case n := <-c.Notices():
		if string(n) != "[READY]" {
			t.Errorf("notice = %q", n)
		}
	default:
		t.Error("no notice delivered")
	}
}

// Frames reach DisplayFrames only between StartDisplay and the next
// StartReports or StopReports.
func TestDisplayFramesChannel(t *testing.T) {
	c, s := newPair(t)
	go func() {
		s.readLine(t) // MC@
		s.write(t, readyFrame+"\r\n")
	}()
	if err := c.StartDisplay(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := string(recvReport(t, c.DisplayFrames())); got != readyFrame {
		t.Errorf("frame = %q", got)
	}
	go func() {
		s.readLine(t) // MCr
		s.write(t, readyFrame+"\r\n{r}\r\n")
	}()
	reports, err := c.StartReports(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(recvReport(t, reports)); got != "{r}" {
		t.Errorf("report = %q", got)
	}
	select {
	case f := <-c.DisplayFrames():
		t.Errorf("frame %q delivered while reporting", f)
	default:
	}
}
