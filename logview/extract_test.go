package logview

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// extractT0 is the start of the first synthetic raw-log file.
var extractT0 = time.Date(2026, 11, 14, 6, 5, 52, 0, time.UTC)

// rawSrc builds a raw-log Source starting at start, named like rawlog names
// its files, from events.
func rawSrc(start time.Time, evs ...fixture.Event) Source {
	return Source{
		Path:    "/raw/" + start.Format("20060102T150405.000Z") + ".jsonl.gz",
		Start:   start,
		HasWall: true,
		Events:  evs,
	}
}

func rxEv(t int64, s string) fixture.Event {
	return fixture.Event{T: t, Dir: fixture.RX, Data: []byte(s)}
}

func txEv(t int64, s string) fixture.Event {
	return fixture.Event{T: t, Dir: fixture.TX, Data: []byte(s)}
}

func noteEv(t int64, s string) fixture.Event {
	return fixture.Event{T: t, Dir: fixture.Note, Data: []byte(s)}
}

func msec(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

func TestSessionStart(t *testing.T) {
	cases := []struct {
		name string
		src  Source
		want bool
	}{
		{"note first", rawSrc(extractT0, noteEv(0, "xml: X"), rxEv(10, "$id: leva! (3.2)\r\n")), false},
		{"banner first", rawSrc(extractT0, rxEv(0, "$id: leva! (3.2)\r\n"), noteEv(5, "xml: X")), true},
		{"note not at t=0", rawSrc(extractT0, noteEv(3, "xml: X")), true},
		{"other note first", rawSrc(extractT0, noteEv(0, "mark")), true},
		{"empty", rawSrc(extractT0), true},
	}
	for _, c := range cases {
		if got := SessionStart(c.src); got != c.want {
			t.Errorf("%s: SessionStart = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExtractRebasesAndPrependsXMLNote(t *testing.T) {
	src := rawSrc(extractT0,
		rxEv(0, "$id: leva! (3.2)\r\n"),
		txEv(100, "MC?\r\n"),
		noteEv(300, "xml: X"),
		rxEv(1000, "{a}\r\n"),
		txEv(1500, "MCr"),
		noteEv(2000, "S Backflush"),
		rxEv(2500, "{b}\r\n"),
		rxEv(9000, "{c}\r\n"),
	)
	got, err := Extract([]Source{src}, extractT0.Add(msec(900)), extractT0.Add(msec(3000)))
	if err != nil {
		t.Fatal(err)
	}
	want := []fixture.Event{
		noteEv(0, "xml: X"),
		rxEv(0, "{a}\r\n"),
		txEv(500, "MCr"),
		noteEv(1000, "S Backflush"),
		rxEv(1500, "{b}\r\n"),
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
	if got.XMLName != "X" {
		t.Errorf("XMLName = %q, want X", got.XMLName)
	}
	if !got.First.Equal(extractT0.Add(msec(1000))) || !got.Last.Equal(extractT0.Add(msec(2500))) {
		t.Errorf("First, Last = %v, %v", got.First, got.Last)
	}
	if want := []string{"20261114T060552.000Z.jsonl.gz"}; !reflect.DeepEqual(got.Files, want) {
		t.Errorf("Files = %v, want %v", got.Files, want)
	}
	if got.Firmware != "$id: leva! (3.2)" {
		t.Errorf("Firmware = %q", got.Firmware)
	}
	if len(got.Notes) != 0 {
		t.Errorf("Notes = %q, want none", got.Notes)
	}
}

func TestExtractXMLNoteBeforeWindowIsPrepended(t *testing.T) {
	src := rawSrc(extractT0,
		rxEv(0, "$id: leva! (3.2)\r\n"),
		noteEv(10, "xml: OLD"),
		rxEv(1000, "{a}\r\n"),
		noteEv(1500, "xml: NEW"),
		rxEv(2000, "{b}\r\n"),
	)
	got, err := Extract([]Source{src}, extractT0.Add(msec(900)), extractT0.Add(msec(3000)))
	if err != nil {
		t.Fatal(err)
	}
	want := []fixture.Event{
		noteEv(0, "xml: OLD"),
		rxEv(0, "{a}\r\n"),
		noteEv(500, "xml: NEW"),
		rxEv(1000, "{b}\r\n"),
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
	if got.XMLName != "OLD" {
		t.Errorf("XMLName = %q, want OLD", got.XMLName)
	}
	if hasNote(got.Notes, "XML named only") {
		t.Errorf("Notes = %q", got.Notes)
	}
}

func TestExtractXMLNoteOnlyInWindowIsNotPrepended(t *testing.T) {
	src := rawSrc(extractT0,
		rxEv(0, "$id: leva! (3.2)\r\n"),
		txEv(100, "MC?\r\n"),
		noteEv(1500, "xml: X"),
		rxEv(2000, "{b}\r\n"),
	)
	got, err := Extract([]Source{src}, extractT0, extractT0.Add(msec(3000)))
	if err != nil {
		t.Fatal(err)
	}
	want := []fixture.Event{
		rxEv(0, "$id: leva! (3.2)\r\n"),
		txEv(100, "MC?\r\n"),
		noteEv(1500, "xml: X"),
		rxEv(2000, "{b}\r\n"),
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
	if got.XMLName != "X" {
		t.Errorf("XMLName = %q, want X", got.XMLName)
	}
	at := extractT0.Add(msec(1500)).Format(time.RFC3339Nano)
	if !hasNote(got.Notes, "XML named only at "+at) {
		t.Errorf("Notes = %q, want one naming %s", got.Notes, at)
	}
}

func TestExtractStartsAtLineBoundary(t *testing.T) {
	src := rawSrc(extractT0,
		rxEv(0, "$id: leva! (3.2)\r\n"),
		noteEv(1, "xml: X"),
		rxEv(1000, "{first-half"),            // a long report in rawlog pieces
		rxEv(2000, "-second-half}\r\n{next"), // the window starts here
		rxEv(2001, "-line}\r\n"),
	)
	got, err := Extract([]Source{src}, extractT0.Add(msec(1500)), extractT0.Add(msec(5000)))
	if err != nil {
		t.Fatal(err)
	}
	want := []fixture.Event{noteEv(0, "xml: X"), rxEv(0, "{next"), rxEv(1, "-line}\r\n")}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
	if !got.First.Equal(extractT0.Add(msec(2000))) {
		t.Errorf("First = %v", got.First)
	}
	if !hasNote(got.Notes, "window starts mid-line: dropped 15 bytes") {
		t.Errorf("Notes = %q", got.Notes)
	}
}

func TestExtractStartsAtLineBoundaryDropsWholePiece(t *testing.T) {
	src := rawSrc(extractT0,
		rxEv(0, "$id: leva! (3.2)\r\n"),
		rxEv(1000, "{a"),
		txEv(1500, "MCr"), // tx is kept as it is
		rxEv(2000, "bbb"), // still the same line: dropped entirely
		rxEv(3000, "c}\r\n{d}\r\n"),
	)
	got, err := Extract([]Source{src}, extractT0.Add(msec(1200)), extractT0.Add(msec(5000)))
	if err != nil {
		t.Fatal(err)
	}
	want := []fixture.Event{txEv(0, "MCr"), rxEv(1500, "{d}\r\n")}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
	if !hasNote(got.Notes, "window starts mid-line: dropped 7 bytes") {
		t.Errorf("Notes = %q", got.Notes)
	}
}

func TestExtractDropsTrailingPartialLine(t *testing.T) {
	src := rawSrc(extractT0,
		rxEv(0, "$id: leva! (3.2)\r\n"),
		noteEv(1, "xml: X"),
		rxEv(1000, "{a}\r\n"),
		rxEv(2000, "{b}\r\n{c"),
		noteEv(2500, "mark"),
		rxEv(2600, "-more"), // no CRLF at all: dropped
		rxEv(9000, "}\r\n"), // after the window
	)
	got, err := Extract([]Source{src}, extractT0.Add(msec(500)), extractT0.Add(msec(3000)))
	if err != nil {
		t.Fatal(err)
	}
	want := []fixture.Event{
		noteEv(0, "xml: X"),
		rxEv(0, "{a}\r\n"),
		rxEv(1000, "{b}\r\n"),
		noteEv(1500, "mark"),
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
	if !got.Last.Equal(extractT0.Add(msec(2500))) {
		t.Errorf("Last = %v", got.Last)
	}
	if !hasNote(got.Notes, "window ends mid-line: dropped 7 bytes") {
		t.Errorf("Notes = %q", got.Notes)
	}
}

func TestExtractJoinsRotation(t *testing.T) {
	t1 := extractT0.Add(time.Hour)
	a := rawSrc(extractT0,
		rxEv(0, "$id: leva! (3.2)\r\n"),
		noteEv(10, "xml: X"),
		rxEv(3_599_000, "{a}\r\n"),
	)
	b := rawSrc(t1,
		noteEv(0, "xml: X"),
		rxEv(500, "{b}\r\n"),
	)
	got, err := Extract([]Source{a, b}, extractT0.Add(msec(3_598_000)), t1.Add(msec(1000)))
	if err != nil {
		t.Fatal(err)
	}
	want := []fixture.Event{
		noteEv(0, "xml: X"),
		rxEv(0, "{a}\r\n"),
		rxEv(1500, "{b}\r\n"),
	}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
	wantFiles := []string{"20261114T060552.000Z.jsonl.gz", "20261114T070552.000Z.jsonl.gz"}
	if !reflect.DeepEqual(got.Files, wantFiles) {
		t.Errorf("Files = %v, want %v", got.Files, wantFiles)
	}
}

func TestExtractStartsAtLineBoundaryAcrossRotation(t *testing.T) {
	t1 := extractT0.Add(time.Hour)
	a := rawSrc(extractT0, rxEv(0, "$id: leva! (3.2)\r\n"), noteEv(10, "xml: X"), rxEv(3_599_000, "{a"))
	b := rawSrc(t1, noteEv(0, "xml: X"), rxEv(100, "a}\r\n{b}\r\n"))
	got, err := Extract([]Source{a, b}, t1, t1.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := []fixture.Event{noteEv(0, "xml: X"), rxEv(0, "{b}\r\n")}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
	if !hasNote(got.Notes, "window starts mid-line: dropped 4 bytes") {
		t.Errorf("Notes = %q", got.Notes)
	}
}

func TestExtractRefusesSessionBoundary(t *testing.T) {
	t1 := extractT0.Add(30 * time.Minute)
	a := rawSrc(extractT0, rxEv(0, "$id: leva! (3.2)\r\n"), noteEv(10, "xml: X"), rxEv(1000, "{a}\r\n"))
	b := rawSrc(t1, rxEv(0, "$id: leva! (3.2)\r\n"), rxEv(1000, "{b}\r\n"))
	_, err := Extract([]Source{a, b}, extractT0, t1.Add(time.Minute))
	var sbe *SessionBoundaryError
	if !errors.As(err, &sbe) {
		t.Fatalf("err = %v, want a SessionBoundaryError", err)
	}
	if !sbe.At.Equal(t1) {
		t.Errorf("At = %v, want %v", sbe.At, t1)
	}
	if !strings.Contains(err.Error(), t1.Format(time.RFC3339)) {
		t.Errorf("message %q does not name %v", err, t1)
	}
}

func TestExtractSessionBeforeWindowIsNoBoundary(t *testing.T) {
	t1 := extractT0.Add(30 * time.Minute)
	a := rawSrc(extractT0, rxEv(0, "$id: leva! (3.1)\r\n"), noteEv(10, "xml: OLD"), rxEv(1000, "{a"))
	b := rawSrc(t1, rxEv(0, "$id: leva! (3.2)\r\n"), noteEv(10, "xml: X"), rxEv(1000, "{b}\r\n"))
	got, err := Extract([]Source{a, b}, t1.Add(msec(500)), t1.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.Firmware != "$id: leva! (3.2)" || got.XMLName != "X" {
		t.Errorf("Firmware, XMLName = %q, %q", got.Firmware, got.XMLName)
	}
	// The new session starts at a line: the old session's cut line is not
	// carried over.
	want := []fixture.Event{noteEv(0, "xml: X"), rxEv(0, "{b}\r\n")}
	if !reflect.DeepEqual(got.Events, want) {
		t.Errorf("Events = %s\nwant   %s", showEvents(got.Events), showEvents(want))
	}
}

func TestExtractFirmwareFromSessionStart(t *testing.T) {
	t1 := extractT0.Add(time.Hour)
	first := rawSrc(extractT0, rxEv(0, "$id: leva! (3.2)\r\n"), noteEv(10, "xml: X"), rxEv(1000, "{a}\r\n"))
	rotated := rawSrc(t1, noteEv(0, "xml: X"), rxEv(500, "{b}\r\n"))
	since, until := t1, t1.Add(time.Minute)

	got, err := Extract([]Source{first, rotated}, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if got.Firmware != "$id: leva! (3.2)" {
		t.Errorf("Firmware = %q", got.Firmware)
	}
	if want := []string{"20261114T070552.000Z.jsonl.gz"}; !reflect.DeepEqual(got.Files, want) {
		t.Errorf("Files = %v, want %v", got.Files, want)
	}

	got, err = Extract([]Source{rotated}, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if got.Firmware != "" {
		t.Errorf("Firmware = %q, want empty", got.Firmware)
	}
	if !hasNote(got.Notes, "firmware unknown") || !hasNote(got.Notes, "20261114T070552.000Z.jsonl.gz") {
		t.Errorf("Notes = %q", got.Notes)
	}
	if got.XMLName != "X" {
		t.Errorf("XMLName = %q", got.XMLName)
	}
}

func TestExtractCounts(t *testing.T) {
	src := rawSrc(extractT0,
		rxEv(0, "$id: leva! (3.2)\r\n"),
		noteEv(1, "xml: X"),
		rxEv(1000, "{a}\r\n<BKL frame>\r\n"),
		rxEv(1100, "{b-piece"),
		rxEv(1200, "-rest}\r\n[READY]\r\n"),
		txEv(1300, "MC@"),
		noteEv(1400, "mark 1"),
		rxEv(1500, "<BKL frame>\r\n"),
		noteEv(1600, "mark 2"),
	)
	got, err := Extract([]Source{src}, extractT0.Add(msec(500)), extractT0.Add(msec(2000)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Reports != 2 || got.Frames != 2 || got.NoteEvents != 3 {
		t.Errorf("Reports, Frames, NoteEvents = %d, %d, %d; want 2, 2, 3 (the xml note counts)",
			got.Reports, got.Frames, got.NoteEvents)
	}
}

func TestExtractEmptyWindow(t *testing.T) {
	src := rawSrc(extractT0, rxEv(0, "$id: leva! (3.2)\r\n"))
	if _, err := Extract([]Source{src}, extractT0.Add(time.Hour), extractT0.Add(2*time.Hour)); err == nil {
		t.Error("err = nil, want an error for a window without events")
	}
}

func hasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// showEvents renders events readably for a failure message.
func showEvents(evs []fixture.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		fmt.Fprintf(&b, "{%d %s %q} ", ev.T, ev.Dir, ev.Data)
	}
	return b.String()
}
