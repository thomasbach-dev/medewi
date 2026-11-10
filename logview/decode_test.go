package logview

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

func testSpec(t *testing.T) *leva.RecordSpec {
	t.Helper()
	spec, err := leva.LoadSpecFile("../testdata/spec/synthetic.xml")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// testReport builds a 289-byte report line (CRLF not included).
func testReport() []byte {
	b := bytes.Repeat([]byte(" "), 289)
	b[0], b[288] = '{', '}'
	b[2] = '*'
	copy(b[7:], "  93.5")
	copy(b[13:], " 94.0")
	copy(b[18:], " 55")
	copy(b[36:], " 9.00")
	return b
}

func testFrame() []byte {
	return []byte("<112" + "Line one        " + "Line two        " + "Line three      " + "Line four       " + ">")
}

func ev(t int64, dir fixture.Dir, data string) fixture.Event {
	return fixture.Event{T: t, Dir: dir, Data: []byte(data)}
}

func kinds(es []Entry) []Kind {
	var out []Kind
	for _, e := range es {
		out = append(out, e.Kind)
	}
	return out
}

func wantKinds(t *testing.T, es []Entry, want ...Kind) {
	t.Helper()
	got := kinds(es)
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
	}
}

func TestDecodeKinds(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.TX, "MCr\r\n"),
		ev(10, fixture.RX, string(testReport())+"\r\n"),
		ev(20, fixture.RX, string(testFrame())+"\r\n"),
		ev(30, fixture.RX, "$id: leva! (3.2)\r\n"),
		ev(40, fixture.RX, "[READY]\r\n"),
		ev(50, fixture.RX, "P 12\r\n"),
		ev(60, fixture.Note, "mark"),
	}}
	es := Decode(src, Options{Spec: testSpec(t)})
	wantKinds(t, es, KindCmd, KindReport, KindFrame, KindBanner, KindNotice, KindReply, KindNote)
	if string(es[0].Raw) != "MCr" || es[0].Dir != fixture.TX {
		t.Errorf("cmd = %q %v", es[0].Raw, es[0].Dir)
	}
	if es[1].Record == nil || es[1].Err != nil {
		t.Errorf("report: record=%v err=%v", es[1].Record, es[1].Err)
	}
	if es[2].Frame == nil || es[2].Frame.Rows[0] != "Line one        " {
		t.Errorf("frame = %+v", es[2].Frame)
	}
	if string(es[6].Raw) != "mark" || es[6].Dir != fixture.Note {
		t.Errorf("note = %q %v", es[6].Raw, es[6].Dir)
	}
	if es[1].Offset != 10*time.Millisecond {
		t.Errorf("offset = %v", es[1].Offset)
	}
}

func TestDecodeReassembles(t *testing.T) {
	r := append(testReport(), '\r', '\n')
	src := Source{Events: []fixture.Event{
		ev(100, fixture.RX, string(r[:7])),
		ev(200, fixture.RX, string(r[7:150])),
		ev(300, fixture.RX, string(r[150:])),
	}}
	es := Decode(src, Options{Spec: testSpec(t)})
	wantKinds(t, es, KindReport)
	if es[0].Offset != 100*time.Millisecond || es[0].Record == nil {
		t.Errorf("entry = %+v", es[0])
	}
}

func TestDecodeGlued(t *testing.T) {
	cut := testFrame()[:30]
	src := Source{Events: []fixture.Event{
		ev(0, fixture.RX, string(cut)+string(testReport())+"\r\n"),
	}}
	es := Decode(src, Options{Spec: testSpec(t)})
	wantKinds(t, es, KindFragment, KindReport)
	if !es[0].Glued || !es[1].Glued {
		t.Errorf("glued = %v %v", es[0].Glued, es[1].Glued)
	}
}

func TestDecodeBadReport(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.RX, "{short}\r\n"),
		ev(10, fixture.RX, "[READY]\r\n"),
		ev(20, fixture.RX, string(testReport())+"\r\n"),
	}}
	es := Decode(src, Options{Spec: testSpec(t)})
	wantKinds(t, es, KindReport, KindNotice, KindReport)
	if es[0].Err == nil || es[0].Record != nil {
		t.Errorf("bad report: err=%v rec=%v", es[0].Err, es[0].Record)
	}
	if es[2].Err != nil || es[2].Record == nil {
		t.Errorf("good report: err=%v rec=%v", es[2].Err, es[2].Record)
	}
}

func TestDecodeNilSpec(t *testing.T) {
	src := Source{Events: []fixture.Event{ev(0, fixture.RX, "{short}\r\n")}}
	es := Decode(src, Options{})
	wantKinds(t, es, KindReport)
	if es[0].Err != nil || es[0].Record != nil {
		t.Errorf("entry = %+v", es[0])
	}
}

func dumpSource() Source {
	return Source{Events: []fixture.Event{
		ev(0, fixture.TX, "MCu\r\n"),
		ev(10, fixture.RX, "|0001 secret\r\n"),
		ev(20, fixture.RX, "|0002 secret\r\n"),
		ev(30, fixture.RX, "|0003 secret\r\n"),
		ev(40, fixture.RX, "some reply\r\n"),
		ev(1040, fixture.RX, string(sumReport())+"\r\n"),
	}}
}

func TestDecodeRedactsDumps(t *testing.T) {
	es := Decode(dumpSource(), Options{Spec: testSpec(t)})
	wantKinds(t, es, KindCmd, KindRedacted, KindReport)
	if es[1].Count != 4 || es[1].Offset != 10*time.Millisecond || es[1].Raw != nil {
		t.Errorf("redacted = %+v", es[1])
	}
	es = Decode(dumpSource(), Options{Spec: testSpec(t), ShowDumps: true})
	wantKinds(t, es, KindCmd, KindReply, KindReply, KindReply, KindReply, KindReport)
}

func TestDecodeRedactsQuietPeriodEnds(t *testing.T) {
	src := dumpSource()
	src.Events = append(src.Events, ev(70_000, fixture.RX, "late reply\r\n"))
	es := Decode(src, Options{})
	wantKinds(t, es, KindCmd, KindRedacted, KindReport, KindReply)
}

func TestDecodeRedactsPartialTail(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.TX, "MCm\r\n"),
		ev(10, fixture.RX, ":abc secret"),
	}}
	es := Decode(src, Options{})
	wantKinds(t, es, KindCmd, KindRedacted)
	for _, e := range es {
		if bytes.Contains(e.Raw, []byte("secret")) {
			t.Error("dump bytes in entry")
		}
	}
}

func TestDecodeWindow(t *testing.T) {
	start := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	src := Source{Start: start, HasWall: true, Events: []fixture.Event{
		ev(0, fixture.RX, "a\r\n"),
		ev(1000, fixture.RX, "b\r\n"),
		ev(2000, fixture.RX, "c\r\n"),
		ev(3000, fixture.RX, "d\r\n"),
	}}
	es := Decode(src, Options{Since: start.Add(time.Second), Until: start.Add(3 * time.Second)})
	wantKinds(t, es, KindReply, KindReply)
	if string(es[0].Raw) != "b" || string(es[1].Raw) != "c" || !es[0].HasWall || !es[0].Wall.Equal(start.Add(time.Second)) {
		t.Errorf("entries = %+v", es)
	}
	// Without wall time the bounds are ignored.
	src.HasWall = false
	es = Decode(src, Options{Since: start.Add(time.Second)})
	if len(es) != 4 {
		t.Errorf("len = %d, want 4", len(es))
	}
}

func TestDecodeTail(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.RX, "P 12\r\n"),
		ev(10, fixture.RX, "{abc"),
	}}
	es := Decode(src, Options{})
	wantKinds(t, es, KindReply, KindPartial)
	if !strings.HasPrefix(string(es[1].Raw), "{abc") {
		t.Errorf("raw = %q", es[1].Raw)
	}
}
