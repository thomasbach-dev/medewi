package leva

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
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
	if f.Rows[0] != "SOAK 1.0b       " || f.Rows[1] != "1: 91.0°   READY" || f.Rows[2] != "▋             7%" {
		t.Errorf("rows = %q", f.Rows)
	}
}

func TestParseFrameGlyphs(t *testing.T) {
	// Unknown control bytes become spaces; known ones: TestParseFrameCustomGlyphs.
	line := "<002SOAK 1.0b       1: 91.0\xdf \x04 91.0\xdf\x01             5% 0.1b\x05  0s   0ml>"
	if len(line) != 69 {
		t.Fatalf("test frame is %d bytes", len(line))
	}
	f, err := ParseFrame([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if f.Rows[1] != "1: 91.0° – 91.0°" || f.Backlight {
		t.Errorf("row 1 = %q backlight %v", f.Rows[1], f.Backlight)
	}
}

func TestParseFrameCustomGlyphs(t *testing.T) {
	line := "<002" +
		"1: 94.1\xdf \x04 94.0\xdf" +
		" PUMP \x03\x03\x03\x01      " +
		"\x01             4%" +
		" 1.8b\x05  0s 233ml" + ">"
	f, err := ParseFrame([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	want := [4]string{"1: 94.1° – 94.0°", " PUMP ███▍      ", "▍             4%", " 1.8b✓  0s 233ml"}
	if f.Rows != want {
		t.Errorf("rows = %q, want %q", f.Rows, want)
	}
	for i, r := range f.Rows {
		if n := utf8.RuneCountInString(r); n != 16 {
			t.Errorf("row %d has %d runes", i, n)
		}
	}
	other := "<002a\x00b\x06c\x07d         " + strings.Repeat(" ", 48) + ">"
	g, err := ParseFrame([]byte(other))
	if err != nil {
		t.Fatal(err)
	}
	if g.Rows[0] != "a b c d         " {
		t.Errorf("row 0 = %q", g.Rows[0])
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
