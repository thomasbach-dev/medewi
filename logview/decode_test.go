package logview

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := LoadCatalog("../testdata/spec/synthetic.xml")
	if err != nil {
		t.Fatal(err)
	}
	return c
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
	es := Decode(src, Options{Catalog: testCatalog(t)}).Entries
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
	es := Decode(src, Options{Catalog: testCatalog(t)}).Entries
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
	es := Decode(src, Options{Catalog: testCatalog(t)}).Entries
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
	es := Decode(src, Options{Catalog: testCatalog(t)}).Entries
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
	es := Decode(src, Options{}).Entries
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
	es := Decode(dumpSource(), Options{Catalog: testCatalog(t)}).Entries
	wantKinds(t, es, KindCmd, KindRedacted, KindReport)
	if es[1].Count != 4 || es[1].Offset != 10*time.Millisecond || es[1].Raw != nil {
		t.Errorf("redacted = %+v", es[1])
	}
	es = Decode(dumpSource(), Options{Catalog: testCatalog(t), ShowDumps: true}).Entries
	wantKinds(t, es, KindCmd, KindReply, KindReply, KindReply, KindReply, KindReport)
}

func TestDecodeRedactsQuietPeriodEnds(t *testing.T) {
	src := dumpSource()
	src.Events = append(src.Events, ev(70_000, fixture.RX, "late reply\r\n"))
	es := Decode(src, Options{}).Entries
	wantKinds(t, es, KindCmd, KindRedacted, KindReport, KindReply)
}

func TestDecodeRedactsPartialTail(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.TX, "MCm\r\n"),
		ev(10, fixture.RX, ":abc secret"),
	}}
	es := Decode(src, Options{}).Entries
	wantKinds(t, es, KindCmd, KindRedacted)
	for _, e := range es {
		if bytes.Contains(e.Raw, []byte("secret")) {
			t.Error("dump bytes in entry")
		}
	}
}

func TestDecodeTail(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.RX, "P 12\r\n"),
		ev(10, fixture.RX, "{abc"),
	}}
	es := Decode(src, Options{}).Entries
	wantKinds(t, es, KindReply, KindPartial)
	if !strings.HasPrefix(string(es[1].Raw), "{abc") {
		t.Errorf("raw = %q", es[1].Raw)
	}
}

func TestDecodeSrcSpansEvents(t *testing.T) {
	r := testReport()
	src := Source{Lines: []int{1, 2, 3}, Events: []fixture.Event{
		ev(0, fixture.RX, string(r[:100])),
		ev(10, fixture.RX, string(r[100:200])),
		ev(20, fixture.RX, string(r[200:])+"\r\n"),
	}}
	es := Decode(src, Options{}).Entries
	wantKinds(t, es, KindReport)
	if es[0].Src != 1 || es[0].SrcEnd != 3 {
		t.Errorf("Src, SrcEnd = %d, %d, want 1, 3", es[0].Src, es[0].SrcEnd)
	}
}

func TestDecodeSrcGlued(t *testing.T) {
	r := string(testReport())
	src := Source{Lines: []int{4, 5}, Events: []fixture.Event{
		ev(0, fixture.TX, "MCr\r\n"),
		ev(10, fixture.RX, r+r+"\r\n"),
	}}
	es := Decode(src, Options{}).Entries
	wantKinds(t, es, KindCmd, KindReport, KindReport)
	if es[0].Src != 4 || es[0].SrcEnd != 4 {
		t.Errorf("cmd Src, SrcEnd = %d, %d", es[0].Src, es[0].SrcEnd)
	}
	for _, e := range es[1:] {
		if e.Src != 5 || e.SrcEnd != 5 {
			t.Errorf("glued Src, SrcEnd = %d, %d, want 5, 5", e.Src, e.SrcEnd)
		}
	}
}

func TestDecodeSrcNoteAndRedacted(t *testing.T) {
	src := dumpSource()
	src.Events = append(src.Events, ev(1100, fixture.Note, "n"))
	src.Lines = make([]int, len(src.Events))
	for i := range src.Lines {
		src.Lines[i] = 10 + i
	}
	es := Decode(src, Options{}).Entries
	wantKinds(t, es, KindCmd, KindRedacted, KindReport, KindNote)
	if es[1].Src != 11 || es[1].SrcEnd != 14 {
		t.Errorf("redacted Src, SrcEnd = %d, %d, want 11, 14", es[1].Src, es[1].SrcEnd)
	}
	if last := es[3]; last.Src != last.SrcEnd || last.Src != 10+len(src.Events)-1 {
		t.Errorf("note Src, SrcEnd = %d, %d", last.Src, last.SrcEnd)
	}
}

func TestDecodeSwitchesXMLOnReply(t *testing.T) {
	r := string(testReport()) + "\r\n"
	c, err := LoadCatalog(xmlDir(t, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	src := Source{Path: "p", Events: []fixture.Event{
		ev(0, fixture.RX, r),
		ev(10, fixture.RX, r),
		ev(20, fixture.RX, "XML=b\r\n"),
		ev(30, fixture.RX, r),
	}}
	d := Decode(src, Options{Catalog: c, XML: "a"})
	wantKinds(t, d.Entries, KindReport, KindReport, KindReply, KindReport)
	for i, want := range []string{"a", "a", "b", "b"} {
		e := d.Entries[i]
		if e.XML != want || e.Spec == nil || e.Path != "p" {
			t.Errorf("entry %d: XML %q spec %v path %q, want %q", i, e.XML, e.Spec, e.Path, want)
		}
	}
	if d.XML != "b" || len(d.Problems) != 0 {
		t.Errorf("XML %q problems %v", d.XML, d.Problems)
	}
	if d.Entries[3].Record == nil {
		t.Error("report after the switch not decoded")
	}
}

func TestDecodeXMLNote(t *testing.T) {
	r := string(testReport()) + "\r\n"
	c, err := LoadCatalog(xmlDir(t, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	src := Source{Events: []fixture.Event{
		ev(0, fixture.Note, "xml: a"),
		ev(10, fixture.RX, r),
	}}
	d := Decode(src, Options{Catalog: c})
	if d.XML != "a" || d.Entries[1].XML != "a" || d.Entries[1].Record == nil || len(d.Problems) != 0 {
		t.Errorf("decoded = %+v", d)
	}
}

func TestDecodeXMLProblemOnce(t *testing.T) {
	r := string(testReport()) + "\r\n"
	src := Source{Path: "log.jsonl", Events: []fixture.Event{
		ev(0, fixture.RX, "XML=other\r\n"),
		ev(10, fixture.RX, r),
		ev(20, fixture.RX, r),
		ev(30, fixture.RX, r),
	}}
	d := Decode(src, Options{Catalog: testCatalog(t)})
	for _, e := range d.Entries[1:] {
		if e.Record != nil || e.Err != nil || e.Kind != KindReport {
			t.Errorf("entry = %+v, want raw report", e)
		}
	}
	if len(d.Problems) != 1 || !strings.HasPrefix(d.Problems[0], "log.jsonl: log names other") {
		t.Errorf("problems = %q", d.Problems)
	}
}

func TestDecodeNoProblemWithoutReports(t *testing.T) {
	c, err := LoadCatalog(xmlDir(t, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	src := Source{Events: []fixture.Event{ev(0, fixture.RX, "P 12\r\n")}}
	if d := Decode(src, Options{Catalog: c}); len(d.Problems) != 0 {
		t.Errorf("problems = %q", d.Problems)
	}
}
