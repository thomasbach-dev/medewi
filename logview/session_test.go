package logview

import (
	"reflect"
	"testing"
	"time"
)

var sessT0 = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func sessSrcs() []Source {
	srcs := []Source{
		// first source is a rotated one: its session start is unknown
		rawSrc(sessT0, noteEv(0, "xml: a.xml"), rxEv(10, "$id: leva! (3.1)\r\n")),
		rawSrc(sessT0.Add(time.Hour), noteEv(0, "xml: a.xml"), rxEv(10, "{x}\r\n")),
		rawSrc(sessT0.Add(2*time.Hour), rxEv(5, "$id: leva! (3.2)\r\n"), noteEv(6, "xml: b.xml")),
		rawSrc(sessT0.Add(3*time.Hour), noteEv(0, "xml: b.xml"), rxEv(10, "{y}\r\n")),
		rawSrc(sessT0.Add(4*time.Hour), rxEv(5, "{z}\r\n")),
	}
	for i := range srcs {
		srcs[i].Path = "/d/f" + string(rune('0'+i)) + ".gz"
	}
	return srcs
}

func TestSessions(t *testing.T) {
	got := Sessions(sessSrcs())
	want := []Session{
		{Start: sessT0, Files: []string{"f0.gz", "f1.gz"}, StartKnown: false, Firmware: "$id: leva! (3.1)", XML: "a.xml"},
		{Start: sessT0.Add(2 * time.Hour), Files: []string{"f2.gz", "f3.gz"}, StartKnown: true, Firmware: "$id: leva! (3.2)", XML: "b.xml"},
		{Start: sessT0.Add(4 * time.Hour), Files: []string{"f4.gz"}, StartKnown: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sessions =\n%+v\nwant\n%+v", got, want)
	}
	if Sessions(nil) != nil {
		t.Error("Sessions(nil) != nil")
	}
}

// TestSessionBuilder: fed one file at a time (its SourceInfo, then its
// decoded entries), the builder groups as Sessions does.
func TestSessionBuilder(t *testing.T) {
	var b SessionBuilder
	for i, src := range sessSrcs() {
		b.Source(src.Info(i))
		if _, err := DecodeEach(src, Options{}, func(e *Entry) error { b.Entry(e); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := b.Sessions(), Sessions(sessSrcs()); !reflect.DeepEqual(got, want) {
		t.Errorf("builder =\n%+v\nSessions =\n%+v", got, want)
	}
	var none SessionBuilder
	if none.Sessions() != nil {
		t.Error("empty builder: Sessions != nil")
	}
	// A capture has no wall time: no sessions at all.
	b.Source(SourceInfo{Index: 5, Path: "c/log.jsonl"})
	if got := b.Sessions(); got != nil {
		t.Errorf("with a capture: %+v, want nil", got)
	}
}

func TestSplitWindow(t *testing.T) {
	ss := Sessions(sessSrcs())
	h := func(n int) time.Time { return sessT0.Add(time.Duration(n) * time.Hour) }
	got := SplitWindow(ss, h(1), h(5))
	want := []Window{{h(1), h(2), 0}, {h(2), h(4), 1}, {h(4), h(5), 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
	// window starting at a session start: the earlier part is empty, dropped
	got = SplitWindow(ss, h(2), h(3))
	if want := []Window{{h(2), h(3), 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
	// before the first source: clamped to it
	got = SplitWindow(ss, h(-5), h(1))
	if want := []Window{{h(0), h(1), 0}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
	if got := SplitWindow(ss, h(5), h(5)); len(got) != 0 {
		t.Errorf("empty window: %+v", got)
	}
}
