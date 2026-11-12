package logview

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

// writeSpec is a Go-built spec: mode, two numbers, a maskable setpoint and
// an unnamed string (field_4).
var writeSpec = &leva.RecordSpec{Values: []leva.ValueSpec{
	{Index: 0, Name: "", At: 4, Len: 3, Type: leva.String},
	{Index: 1, Name: "Temperature PID1", At: 7, Len: 6, Type: leva.Float, Unit: "°C"},
	{Index: 2, Name: "Pressure", At: 36, Len: 5, Type: leva.Float, Unit: "bar"},
	{Index: 3, Name: "Pressure SP", At: 13, Len: 5, Type: leva.Float, Unit: "bar"},
	{Index: 4, Name: "", At: 50, Len: 10, Type: leva.String},
}}

var writeWall = time.Date(2026, 11, 10, 7, 58, 12, 300_000_000, time.UTC)

// wReport builds a decoded report entry on writeSpec at path "p", line 3.
func wReport(t *testing.T, pump byte, pressure, sp, label string) *Entry {
	t.Helper()
	b := bytes.Repeat([]byte(" "), 289)
	b[0], b[288] = '{', '}'
	b[2] = pump
	copy(b[4:], "PID")
	copy(b[7:], "  93.5")
	copy(b[36:], pressure)
	copy(b[13:], sp)
	copy(b[50:], label)
	rec, err := writeSpec.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return &Entry{
		Offset: 712300 * time.Millisecond, Wall: writeWall, HasWall: true,
		Dir: fixture.RX, Kind: KindReport, Raw: b, Record: &rec,
		Path: "p", Src: 3, SrcEnd: 3, XML: "x1", Spec: writeSpec,
	}
}

func writeRows(t *testing.T, f Format, opt WriterOptions, rows ...Row) (string, Writer) {
	t.Helper()
	var b bytes.Buffer
	w := NewWriter(&b, f, opt)
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String(), w
}

func lines(s string) []string { return strings.Split(strings.TrimSuffix(s, "\n"), "\n") }

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"text", "jsonl", "csv"} {
		if f, err := ParseFormat(s); err != nil || string(f) != s {
			t.Errorf("%s: %v %v", s, f, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("xml accepted")
	}
}

func TestTextKeysAndPump(t *testing.T) {
	out, _ := writeRows(t, FormatText, WriterOptions{Loc: time.UTC},
		Row{Entry: wReport(t, '*', " 9.00", "?????", "SOAK")})
	ls := lines(out)
	want := "   712.300 2026-11-10 07:58:12.300 rx   report   " +
		"pump=* mode=PID temperature_pid1=93.5 pressure=9 pressure_sp=? field_4=SOAK"
	if ls[len(ls)-1] != want {
		t.Errorf("got  %q\nwant %q", ls[len(ls)-1], want)
	}
}

func TestTextRunColsAndSrc(t *testing.T) {
	run := &Run{N: 2}
	rt := -1500 * time.Millisecond
	out, _ := writeRows(t, FormatText, WriterOptions{Loc: time.UTC, RunCols: true, Fields: []string{"src", "pump", "pressure"}},
		Row{Entry: wReport(t, '-', " 9.00", "?????", "")},
		Row{Entry: wReport(t, '-', " 9.00", "?????", ""), Run: run, RunT: &rt})
	ls := lines(out)
	if !strings.HasSuffix(ls[len(ls)-2], "report   -        - src=p:3 pump=- pressure=9") {
		t.Errorf("no run: %q", ls[len(ls)-2])
	}
	if !strings.HasSuffix(ls[len(ls)-1], "report   2   -1.500 src=p:3 pump=- pressure=9") {
		t.Errorf("run: %q", ls[len(ls)-1])
	}
}

func TestTextHideMasked(t *testing.T) {
	out, _ := writeRows(t, FormatText, WriterOptions{Loc: time.UTC, HideMasked: true},
		Row{Entry: wReport(t, '?', "?????", "?????", "")})
	l := lines(out)[2]
	if strings.Contains(l, "?") || !strings.HasSuffix(l, "report   mode=PID temperature_pid1=93.5 field_4=\"\"") {
		t.Errorf("got %q", l)
	}
}

func TestTextRawValues(t *testing.T) {
	out, _ := writeRows(t, FormatText, WriterOptions{Loc: time.UTC, RawValues: true, Fields: []string{"pressure"}},
		Row{Entry: wReport(t, '*', "09.00", "?????", "")})
	if l := lines(out)[2]; !strings.HasSuffix(l, " pressure=09.00") {
		t.Errorf("got %q", l)
	}
}

func TestTextHeaders(t *testing.T) {
	a := wReport(t, '-', " 9.00", "?????", "")
	b := *a
	b.XML = "x2"
	c := b
	c.Path = "q"
	note := Entry{Dir: fixture.Note, Kind: KindNote, Raw: []byte("hi"), Path: "q", XML: "x2"}
	out, _ := writeRows(t, FormatText, WriterOptions{Loc: time.UTC},
		Row{Entry: a}, Row{Entry: a}, Row{Entry: &b}, Row{Entry: &c}, Row{Entry: &note})
	var hdr []string
	for _, l := range lines(out) {
		if strings.HasPrefix(l, "#") {
			hdr = append(hdr, l)
		}
	}
	want := []string{"# p", "# xml: x1", "# xml: x2", "# q"}
	if strings.Join(hdr, "|") != strings.Join(want, "|") {
		t.Errorf("headers = %q, want %q", hdr, want)
	}
	if !strings.HasSuffix(out, "note note     hi\n") {
		t.Errorf("note: %q", out)
	}
}

func TestTextNotes(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b, FormatText, WriterOptions{Loc: time.UTC})
	w.Source(SourceInfo{Path: "p", Notes: "warm"})
	e := wReport(t, '-', " 9.00", "?????", "")
	for range 2 {
		if err := w.Write(Row{Entry: e}); err != nil {
			t.Fatal(err)
		}
	}
	w.Source(SourceInfo{Path: "q"})
	q := *e
	q.Path = "q"
	if err := w.Write(Row{Entry: &q}); err != nil {
		t.Fatal(err)
	}
	var hdr []string
	for _, l := range lines(b.String()) {
		if strings.HasPrefix(l, "#") {
			hdr = append(hdr, l)
		}
	}
	if strings.Join(hdr, "|") != "# p|# notes: warm|# xml: x1|# q" {
		t.Errorf("headers = %q", hdr)
	}
}

func TestTextRawReport(t *testing.T) {
	e := Entry{Dir: fixture.RX, Kind: KindReport, Raw: []byte("{bad}"), Path: "p"}
	out, _ := writeRows(t, FormatText, WriterOptions{Loc: time.UTC}, Row{Entry: &e})
	if l := lines(out)[1]; l != "     0.000 - rx   report   {bad}" {
		t.Errorf("got %q", l)
	}
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%v: %q", err, s)
	}
	return m
}

func TestJSONLReport(t *testing.T) {
	run := &Run{N: 1}
	rt := 250 * time.Millisecond
	out, _ := writeRows(t, FormatJSONL, WriterOptions{},
		Row{Entry: wReport(t, '*', " 9.00", "?????", "SOAK"), Run: run, RunT: &rt})
	m := decodeJSON(t, out)
	if m["pump"] != true || m["pressure"] != 9.0 || m["pressure_sp"] != nil || m["src"] != "p:3" {
		t.Errorf("values: %v", m)
	}
	if _, ok := m["pressure_sp"]; !ok {
		t.Error("masked key missing")
	}
	if m["wall"] != "2026-11-10T07:58:12.300Z" || m["t"] != 712.3 || m["src_end"] != nil {
		t.Errorf("medewi: %v", m)
	}
	if m["run"] != 1.0 || m["run_t"] != 0.25 || m["event"] != "report" || m["glued"] != false || m["xml"] != "x1" || m["field_4"] != "SOAK" {
		t.Errorf("medewi: %v", m)
	}
	if !strings.HasPrefix(out, `{"t":712.3,"wall":"2026-11-10T07:58:12.300Z","src":"p:3","run":1,"run_t":0.25,"src_end":null,"dir":"rx","event":"report","glued":false,"xml":"x1","pump":true,"mode":"PID",`) {
		t.Errorf("key order: %s", out)
	}
}

func TestJSONLNoRun(t *testing.T) {
	e := wReport(t, '?', " 9.00", "?????", "")
	e.HasWall, e.SrcEnd = false, 4
	out, _ := writeRows(t, FormatJSONL, WriterOptions{}, Row{Entry: e})
	m := decodeJSON(t, out)
	for _, k := range []string{"run", "run_t", "wall", "pump"} {
		if v, ok := m[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v)", k, v, ok)
		}
	}
	if m["src_end"] != "p:4" {
		t.Errorf("src_end = %v", m["src_end"])
	}
}

func TestJSONLFrameAndCmd(t *testing.T) {
	frame := Entry{
		Dir: fixture.RX, Kind: KindFrame, Path: "p", Src: 1, SrcEnd: 1,
		Frame: &leva.Frame{Backlight: true, Key: '3', Layout: '2', Rows: [4]string{"a", "b", "c", "d"}},
	}
	cmd := Entry{Dir: fixture.TX, Kind: KindCmd, Raw: []byte("MCr"), Path: "p", Src: 2, SrcEnd: 2}
	bad := Entry{Dir: fixture.RX, Kind: KindReport, Raw: []byte("{x}"), Err: errText("short"), Path: "p", Src: 3, SrcEnd: 3}
	out, _ := writeRows(t, FormatJSONL, WriterOptions{}, Row{Entry: &frame}, Row{Entry: &cmd}, Row{Entry: &bad})
	ls := lines(out)
	f := decodeJSON(t, ls[0])
	if rows, ok := f["rows"].([]any); !ok || len(rows) != 4 || f["bkl"] != true || f["key"] != 3.0 || f["layout"] != 2.0 {
		t.Errorf("frame: %v", f)
	}
	if c := decodeJSON(t, ls[1]); c["text"] != "MCr" || c["dir"] != "tx" || c["event"] != "cmd" {
		t.Errorf("cmd: %v", c)
	}
	if e := decodeJSON(t, ls[2]); e["text"] != "{x}" || e["error"] != "short" {
		t.Errorf("error: %v", e)
	}
}

type errText string

func (e errText) Error() string { return string(e) }

func TestJSONLFields(t *testing.T) {
	out, _ := writeRows(t, FormatJSONL, WriterOptions{Fields: []string{"pressure"}},
		Row{Entry: wReport(t, '*', " 9.00", "?????", "")})
	m := decodeJSON(t, out)
	if len(m) != 8 || m["src"] != "p:3" || m["event"] != "report" || m["glued"] != false || m["pressure"] != 9.0 {
		t.Errorf("got %v", m)
	}
	// The identifying columns lead, also when --fields names them, once.
	out, _ = writeRows(t, FormatJSONL, WriterOptions{Fields: []string{"pressure", "run_t", "t"}},
		Row{Entry: wReport(t, '*', " 9.00", "?????", "")})
	want := `{"t":712.3,"wall":"2026-11-10T07:58:12.300Z","src":"p:3","run":null,"run_t":null,"event":"report","glued":false,"pressure":9}` + "\n"
	if out != want {
		t.Errorf("got %s\nwant %s", out, want)
	}
}

func TestJSONLRawValues(t *testing.T) {
	out, _ := writeRows(t, FormatJSONL, WriterOptions{RawValues: true, Fields: []string{"pressure", "pressure_sp"}},
		Row{Entry: wReport(t, '*', "09.00", "?????", "")})
	m := decodeJSON(t, out)
	if m["pressure"] != "09.00" || m["pressure_sp"] != nil {
		t.Errorf("got %v", m)
	}
}

func TestCSVHeaderAndRows(t *testing.T) {
	run := &Run{N: 1}
	rt := 500 * time.Millisecond
	note := Entry{Dir: fixture.Note, Kind: KindNote, Raw: []byte("hi")}
	out, _ := writeRows(t, FormatCSV, WriterOptions{Fields: []string{"run_t", "pump", "pressure", "pressure_sp"}},
		Row{Entry: &note},
		Row{Entry: wReport(t, '*', " 9.00", "?????", ""), Run: run, RunT: &rt},
		Row{Entry: wReport(t, '-', " 1.50", "?????", "")})
	want := "t,wall,src,run,run_t,pump,pressure,pressure_sp\n" +
		"712.3,2026-11-10T07:58:12.300Z,p:3,1,0.5,1,9,\n712.3,2026-11-10T07:58:12.300Z,p:3,,,0,1.5,\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestCSVDefaultHeader(t *testing.T) {
	out, _ := writeRows(t, FormatCSV, WriterOptions{}, Row{Entry: wReport(t, '?', " 9.00", " 9.00", "a,b")})
	want := "t,wall,src,run,run_t,src_end,pump,mode,temperature_pid1,pressure,pressure_sp,field_4\n" +
		"712.3,2026-11-10T07:58:12.300Z,p:3,,,,,PID,93.5,9,9,\"a,b\"\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
}

func TestCSVSpecChangeError(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b, FormatCSV, WriterOptions{})
	if err := w.Write(Row{Entry: wReport(t, '-', " 9.00", "?????", "")}); err != nil {
		t.Fatal(err)
	}
	other := modeReport("PID", " 9.00")
	other.Path, other.Src, other.XML = "q", 7, "x2"
	err := w.Write(Row{Entry: &other})
	if err == nil || !strings.Contains(err.Error(), "--fields") || !strings.Contains(err.Error(), "q:7") {
		t.Errorf("err = %v", err)
	}
	// The same key set from another spec is fine.
	same := *writeSpec
	e := wReport(t, '-', " 9.00", "?????", "")
	e.Spec = &same
	if err := w.Write(Row{Entry: e}); err != nil {
		t.Errorf("same keys: %v", err)
	}
}

func TestCSVMissingKeyAndSkips(t *testing.T) {
	other := modeReport("PID", " 9.00") // modeSpecT has no temperature_pid1
	bad := Entry{Dir: fixture.RX, Kind: KindReport, Raw: []byte("{x}"), Err: errText("short")}
	raw := Entry{Dir: fixture.RX, Kind: KindReport, Raw: []byte("{x}")}
	out, w := writeRows(t, FormatCSV, WriterOptions{Fields: []string{"pressure", "temperature_pid1"}},
		Row{Entry: wReport(t, '-', " 9.00", "?????", "")}, Row{Entry: &other}, Row{Entry: &other},
		Row{Entry: &bad}, Row{Entry: &raw}, Row{Entry: &raw})
	if want := "t,wall,src,run,run_t,pressure,temperature_pid1\n712.3,2026-11-10T07:58:12.300Z,p:3,,,9,93.5\n" +
		"0,,:0,,,1,\n0,,:0,,,1,\n"; out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
	want := []string{
		"1 reports that failed to decode skipped",
		"2 reports without XML skipped",
		"key temperature_pid1: 2 reports whose XML lacks it",
	}
	if got := w.Warnings(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("warnings = %q\nwant %q", got, want)
	}
}

func TestCSVLatin1Valid(t *testing.T) {
	out, _ := writeRows(t, FormatCSV, WriterOptions{Fields: []string{"field_4"}},
		Row{Entry: wReport(t, '-', " 9.00", "?????", "SOAK 1\xb0")})
	if !utf8.ValidString(out) || !strings.Contains(out, "SOAK 1°") {
		t.Errorf("got %q", out)
	}
}

func TestCSVEmpty(t *testing.T) {
	out, _ := writeRows(t, FormatCSV, WriterOptions{Fields: []string{"pressure", "t"}})
	if out != "t,wall,src,run,run_t,pressure\n" {
		t.Errorf("got %q", out)
	}
}

func TestBadValueWarning(t *testing.T) {
	for _, f := range []Format{FormatText, FormatJSONL, FormatCSV} {
		r := Row{Entry: wReport(t, '-', "9?.00", "?????", "")}
		out, w := writeRows(t, f, WriterOptions{Loc: time.UTC}, r, r)
		want := []string{"key pressure: 2 unparseable values (raw in text, null in jsonl, empty in csv)"}
		if got := w.Warnings(); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: warnings = %q", f, got)
		}
		switch f {
		case FormatText:
			if !strings.Contains(out, " pressure=9?.00 ") {
				t.Errorf("text: %q", out)
			}
		case FormatJSONL:
			if m := decodeJSON(t, lines(out)[0]); m["pressure"] != nil {
				t.Errorf("jsonl: %v", m)
			}
		}
	}
}

func TestWriteColumnsText(t *testing.T) {
	spec := &leva.RecordSpec{Values: []leva.ValueSpec{
		{Index: 0, Name: "", At: 4, Len: 3, Type: leva.String},
		{Index: 1, Name: "Pressure", At: 36, Len: 5, Type: leva.Float, Unit: "bar", Factor: 1},
		{Index: 2, Name: "Pressure SP", At: 13, Len: 3, Type: leva.Int, Unit: "bar", Factor: 0.0166},
	}}
	cols, err := Columns(spec)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteColumns(&b, FormatText, "x1", cols); err != nil {
		t.Fatal(err)
	}
	ls := lines(b.String())
	want := map[int]string{
		0:  "# xml: x1",
		1:  "KEY          NAME           UNIT  FACTOR  AT  LEN  SOURCE",
		2:  "t            (offset)       s     -       -   -    medewi",
		10: "pump         (pump marker)  -     -       2   1    medewi",
		12: "mode         -              -     -       4   3    xml",
		13: "pressure     Pressure       bar   -       36  5    xml",
		14: "pressure_sp  Pressure SP    bar   0.0166  13  3    xml",
	}
	for i, w := range want {
		if i >= len(ls) || ls[i] != w {
			t.Errorf("line %d:\n got %q\nwant %q", i, ls[min(i, len(ls)-1)], w)
		}
	}
	b.Reset()
	if err := WriteColumns(&b, FormatJSONL, "x1", cols); err != nil {
		t.Fatal(err)
	}
	m := decodeJSON(t, lines(b.String())[12])
	if m["key"] != "pressure_sp" || m["factor"] != 0.0166 || m["at"] != 13.0 || m["source"] != "xml" || m["xml"] != "x1" {
		t.Errorf("jsonl: %v", m)
	}
	b.Reset()
	if err := WriteColumns(&b, FormatCSV, "x1", cols); err != nil {
		t.Fatal(err)
	}
	if ls := lines(b.String()); ls[0] != "key,name,unit,factor,at,len,source,xml" || ls[1] != "t,(offset),s,1,,,medewi,x1" {
		t.Errorf("csv: %q", ls[:2])
	}
}

func TestWriteRunsText(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, true, concat(
		idle(4, 0, 0), pumping(10, "9.0", 9, 4), idle(14, 40, 0), // shot
		idle(10, 40, 2), idle(12, 58, 0), // draw
	))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 2 || runs[1].Kind != RunDraw {
		t.Fatalf("runs = %+v", runs)
	}
	var b bytes.Buffer
	if err := WriteRuns(&b, FormatText, runs, RunsOptions{Loc: time.UTC}); err != nil {
		t.Fatal(err)
	}
	ls := lines(b.String())
	if !strings.HasPrefix(ls[0], "RUN  KIND  START                    T      DURATION  PEAK_BAR  VOLUME_ML  TRUNCATED  XML  SRC") {
		t.Errorf("header %q", ls[0])
	}
	if !strings.HasPrefix(ls[1], "1    shot  2026-11-10 07:58:01.000  1.000  ") || !strings.HasSuffix(ls[1], "  no         x    src0:5") {
		t.Errorf("shot %q", ls[1])
	}
	if f := strings.Fields(ls[2]); len(f) != 11 || f[1] != "draw" || f[6] != "-" {
		t.Errorf("draw %q", ls[2])
	}
	b.Reset()
	if err := WriteRuns(&b, FormatJSONL, runs, RunsOptions{Loc: time.UTC}); err != nil {
		t.Fatal(err)
	}
	m := decodeJSON(t, lines(b.String())[1])
	if m["run"] != 2.0 || m["kind"] != "draw" || m["peak_bar"] != nil || m["truncated"] != false || m["start"] == nil || m["src"] == nil {
		t.Errorf("jsonl %v", m)
	}
	b.Reset()
	if err := WriteRuns(&b, FormatCSV, runs, RunsOptions{Loc: time.UTC}); err != nil {
		t.Fatal(err)
	}
	if ls := lines(b.String()); ls[0] != "run,kind,start,t,duration,peak_bar,volume_ml,truncated,xml,src" || !strings.HasPrefix(ls[1], "1,shot,2026-11-10T07:58:01.000Z,1,") {
		t.Errorf("csv %q", ls)
	}
}

func TestFrameRealLine(t *testing.T) {
	fr, err := leva.ParseFrame(testFrame())
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{Dir: fixture.RX, Kind: KindFrame, Frame: &fr, Path: "p", Src: 1, SrcEnd: 1}
	out, _ := writeRows(t, FormatText, WriterOptions{Loc: time.UTC}, Row{Entry: &e})
	if !strings.Contains(out, " bkl=1 key=1 layout=2 |Line one") {
		t.Errorf("text: %q", out)
	}
	out, _ = writeRows(t, FormatJSONL, WriterOptions{}, Row{Entry: &e})
	if m := decodeJSON(t, out); m["bkl"] != true || m["key"] != 1.0 || m["layout"] != 2.0 {
		t.Errorf("jsonl: %v", m)
	}
	// A byte that is not a digit is shown as the character.
	odd := e
	odd.Frame = &leva.Frame{Key: 'x', Layout: '2'}
	out, _ = writeRows(t, FormatText, WriterOptions{Loc: time.UTC}, Row{Entry: &odd})
	if !strings.Contains(out, " bkl=0 key=x layout=2 ") {
		t.Errorf("text odd: %q", out)
	}
	out, _ = writeRows(t, FormatJSONL, WriterOptions{}, Row{Entry: &odd})
	if m := decodeJSON(t, out); m["key"] != "x" {
		t.Errorf("jsonl odd: %v", m)
	}
}

func TestRunTNoNegativeZero(t *testing.T) {
	run := &Run{N: 1}
	rt := -100 * time.Microsecond
	r := Row{Entry: wReport(t, '-', " 9.00", "?????", ""), Run: run, RunT: &rt}
	out, _ := writeRows(t, FormatText, WriterOptions{Loc: time.UTC, RunCols: true, Fields: []string{"pump"}}, r)
	if !strings.Contains(out, "report   1    0.000 pump=-") {
		t.Errorf("text: %q", out)
	}
	out, _ = writeRows(t, FormatJSONL, WriterOptions{}, r)
	if m := decodeJSON(t, out); !strings.Contains(out, `"run_t":0,`) || m["run_t"] != 0.0 {
		t.Errorf("jsonl: %s", out)
	}
	out, _ = writeRows(t, FormatCSV, WriterOptions{Fields: []string{"run_t"}}, r)
	if out != "t,wall,src,run,run_t\n712.3,2026-11-10T07:58:12.300Z,p:3,1,0\n" {
		t.Errorf("csv: %q", out)
	}
}

func TestTextJSONLLatin1Valid(t *testing.T) {
	r := Row{Entry: wReport(t, '-', " 9.00", "?????", "SOAK 1\xb0")}
	for _, f := range []Format{FormatText, FormatJSONL} {
		out, _ := writeRows(t, f, WriterOptions{Loc: time.UTC, Fields: []string{"field_4"}}, r)
		if !utf8.ValidString(out) || !strings.Contains(out, "SOAK 1°") {
			t.Errorf("%s: %q", f, out)
		}
	}
}

func TestWriteRunsDefaultText(t *testing.T) {
	var b bytes.Buffer
	if err := WriteRuns(&b, Format("bogus"), nil, RunsOptions{Loc: time.UTC}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b.String(), "RUN  KIND") {
		t.Errorf("got %q", b.String())
	}
}

func TestWriteRunsStats(t *testing.T) {
	l, runs := twoShots(t)
	aggs, _ := ParseAggs("n,max")
	st := NewRunStats(runs, []string{"pressure"}, [][]Agg{aggs})
	// Run 2 gets no values: n 0, max empty.
	for i := range l.Entries {
		if l.Entries[i].Index < 20 {
			st.Entry(&l.Entries[i])
		}
	}
	opt := RunsOptions{Loc: time.UTC, Stats: st}
	var b bytes.Buffer
	if err := WriteRuns(&b, FormatText, runs, opt); err != nil {
		t.Fatal(err)
	}
	ls := lines(b.String())
	tail := func(line string, n int) string {
		f := strings.Fields(line)
		return strings.Join(f[max(len(f)-n, 0):], " ")
	}
	if tail(ls[0], 3) != "SRC pressure_n pressure_max" {
		t.Errorf("header %q", ls[0])
	}
	if tail(ls[1], 3) != "src0:5 3 9" || tail(ls[2], 2) != "0 ?" {
		t.Errorf("rows %q", ls[1:])
	}
	b.Reset()
	if err := WriteRuns(&b, FormatJSONL, runs, opt); err != nil {
		t.Fatal(err)
	}
	ls = lines(b.String())
	if !strings.HasSuffix(ls[0], `,"src":"src0:5","pressure_n":3,"pressure_max":9}`) ||
		!strings.HasSuffix(ls[1], `,"pressure_n":0,"pressure_max":null}`) {
		t.Errorf("jsonl %q", ls)
	}
	b.Reset()
	if err := WriteRuns(&b, FormatCSV, runs, opt); err != nil {
		t.Fatal(err)
	}
	ls = lines(b.String())
	if !strings.HasSuffix(ls[0], ",src,pressure_n,pressure_max") || !strings.HasSuffix(ls[1], ",src0:5,3,9") || !strings.HasSuffix(ls[2], ",0,") {
		t.Errorf("csv %q", ls)
	}
}
