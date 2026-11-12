package logview

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
)

// summaryLine is testReport with pump marker pump and mode text mode.
func summaryLine(pump byte, mode string) []byte {
	b := testReport()
	b[2] = pump
	copy(b[4:], mode)
	return b
}

func rxLine(ms int64, line []byte) fixture.Event {
	return fixture.Event{T: ms, Dir: fixture.RX, Data: append(append([]byte(nil), line...), '\r', '\n')}
}

// writeLog writes events to path, creating its directory.
func writeLog(t *testing.T, path string, evs ...fixture.Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := fixture.WriteLog(&b, evs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// summarize streams paths into a SummaryScanner, with the synthetic spec.
func summarize(t *testing.T, paths ...string) []Summary {
	t.Helper()
	sc := NewSummaryScanner()
	s := Stream{
		Catalog:  testCatalog(t),
		OnSource: func(si SourceInfo) error { sc.Source(si); return nil },
		OnEntry:  func(e *Entry) error { sc.Entry(e); return nil },
	}
	if err := s.Add(paths...); err != nil {
		t.Fatal(err)
	}
	return sc.Summaries(nil)
}

func TestSummaryCounts(t *testing.T) {
	dir := t.TempDir()
	rec := time.Date(2026, 11, 2, 6, 46, 33, 916_000_000, time.UTC)
	m := fixture.Manifest{FirmwareVersion: "fw from manifest", Notes: "a note", Recorded: rec}
	if err := m.Save(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	writeLog(t, filepath.Join(dir, "log.jsonl"),
		rxLine(0, summaryLine('-', "PID")),
		rxLine(100, []byte("$id: leva! (3.2)")),
		rxLine(250, summaryLine('-', "PID")),
		rxLine(500, summaryLine('-', "PID")),
		rxLine(1500, []byte("[READY]")),
		rxLine(3500, summaryLine('-', "PID")),
	)
	ss := summarize(t, dir)
	if len(ss) != 1 {
		t.Fatalf("summaries = %+v", ss)
	}
	s := ss[0]
	wantEv := map[Kind]int{KindReport: 4, KindBanner: 1, KindNotice: 1}
	if len(s.Events) != len(wantEv) {
		t.Errorf("events = %v, want %v", s.Events, wantEv)
	}
	for k, n := range wantEv {
		if s.Events[k] != n {
			t.Errorf("events[%s] = %d, want %d", k, s.Events[k], n)
		}
	}
	if !slices.Equal(s.Ready, []time.Duration{1500 * time.Millisecond}) {
		t.Errorf("ready = %v", s.Ready)
	}
	if s.Gaps != 1 || s.GapMax != 3*time.Second {
		t.Errorf("gaps = %d, gap_max = %v", s.Gaps, s.GapMax)
	}
	if s.Firmware != "fw from manifest" || s.Notes != "a note" || s.Kind != "capture" {
		t.Errorf("firmware %q notes %q kind %q", s.Firmware, s.Notes, s.Kind)
	}
	// levacap takes recorded just after the last event (3.5 s): the
	// capture started that much earlier.
	if want := rec.Add(-3500 * time.Millisecond); !s.HasStart || !s.Start.Equal(want) {
		t.Errorf("start = %v (%v), want %v", s.Start, s.HasStart, want)
	}
	if s.Duration != 3500*time.Millisecond || !s.Decoded || s.Src != filepath.Join(dir, "log.jsonl") {
		t.Errorf("duration %v decoded %v src %q", s.Duration, s.Decoded, s.Src)
	}
	// The 3 s pause is left out of the modes.
	if s.Modes["PID"] != 500*time.Millisecond || len(s.Modes) != 1 {
		t.Errorf("modes = %v", s.Modes)
	}
}

func TestSummaryModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c", "log.jsonl")
	writeLog(t, path,
		rxLine(0, summaryLine('-', "PID")),
		rxLine(250, summaryLine('*', "PID")),
		rxLine(500, summaryLine('-', "ECO")),
		rxLine(750, summaryLine('?', "???")),
		rxLine(1000, summaryLine('-', "PID")),
	)
	s := summarize(t, path)[0]
	want := map[string]time.Duration{"PID": 500 * time.Millisecond, "ECO": 250 * time.Millisecond, "standby": 250 * time.Millisecond}
	if len(s.Modes) != len(want) {
		t.Errorf("modes = %v, want %v", s.Modes, want)
	}
	for k, d := range want {
		if s.Modes[k] != d {
			t.Errorf("modes[%s] = %v, want %v", k, s.Modes[k], d)
		}
	}
	if s.Gaps != 0 || s.GapMax != 250*time.Millisecond {
		t.Errorf("gaps = %d, gap_max = %v", s.Gaps, s.GapMax)
	}
}

func TestSummaryMaskedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c", "log.jsonl")
	writeLog(t, path, rxLine(0, summaryLine('-', "???")), rxLine(250, summaryLine('-', "PID")))
	s := summarize(t, path)[0]
	if s.Modes["?"] != 250*time.Millisecond {
		t.Errorf("modes = %v, want ?=250ms", s.Modes)
	}
}

func TestSummaryMasked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c", "log.jsonl")
	r := func(pump byte, heat, pressure string) []byte {
		b := summaryLine(pump, "PID")
		copy(b[18:], heat)
		copy(b[36:], pressure)
		return b
	}
	writeLog(t, path,
		rxLine(0, r('-', "???", " 9.00")),
		rxLine(250, r('?', "???", "?????")),
		rxLine(500, r('*', "???", " 8.00")),
	)
	s := summarize(t, path)[0]
	if !slices.Equal(s.Masked, []string{"heat_pid"}) {
		t.Errorf("masked = %q, want [heat_pid]", s.Masked)
	}

	// Only standby reports: nothing is masked in every non-standby one.
	path = filepath.Join(t.TempDir(), "c", "log.jsonl")
	writeLog(t, path, rxLine(0, r('?', "???", "?????")))
	if s := summarize(t, path)[0]; len(s.Masked) != 0 {
		t.Errorf("standby only: masked = %q", s.Masked)
	}
}

func TestSummaryRunsGlobal(t *testing.T) {
	var l loaded
	addSource(t, &l, runT0, true, concat(
		idle(4, 0, 0), pumping(8, "", 2, 3), idle(12, 21, 0),
		pumping(10, "9.0", 9, 4), idle(14, 36, 0),
	))
	addSource(t, &l, runT0.Add(time.Hour), true, concat(
		idle(4, 0, 0), pumping(10, "9.0", 9, 4), idle(14, 36, 0),
	))
	runs := findRuns(l, brew.DefaultConfig(), time.Time{}, time.Time{})
	if len(runs) != 3 {
		t.Fatalf("runs = %+v", runs)
	}
	sc := NewSummaryScanner()
	k := 0
	for _, si := range l.Sources {
		sc.Source(si)
		for ; k < len(l.Entries) && l.Entries[k].Source == si.Index; k++ {
			sc.Entry(&l.Entries[k])
		}
	}
	ss := sc.Summaries(runs)
	if len(ss) != 2 {
		t.Fatalf("summaries = %d", len(ss))
	}
	if r := ss[0].Runs; r[RunShot] != 1 || r[RunFlush] != 1 || r[RunDraw] != 0 || !slices.Equal(ss[0].RunNums, []int{1, 2}) {
		t.Errorf("first: runs %v nums %v", r, ss[0].RunNums)
	}
	if r := ss[1].Runs; r[RunShot] != 1 || r[RunFlush] != 0 || len(r) != 3 || !slices.Equal(ss[1].RunNums, []int{3}) {
		t.Errorf("second: runs %v nums %v", r, ss[1].RunNums)
	}
	if ss[1].Kind != "rawlog" || !ss[1].HasStart || !ss[1].Start.Equal(runT0.Add(time.Hour)) {
		t.Errorf("second: kind %q start %v", ss[1].Kind, ss[1].Start)
	}
}

func TestSummaryFirmwareFromBanner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "20261110T100000.000Z.jsonl.gz")
	writeLog(t, path,
		rxLine(0, []byte("$id: leva! (3.2)")),
		rxLine(1000, []byte("$id: leva! (9.9)")),
	)
	s := summarize(t, path)[0]
	if s.Firmware != "$id: leva! (3.2)" || s.Kind != "rawlog" {
		t.Errorf("firmware %q kind %q", s.Firmware, s.Kind)
	}
	if !s.HasStart || !s.Start.Equal(time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v", s.Start)
	}
	// No reports: nothing to decode, so the record is not reduced.
	if !s.Decoded || len(s.Runs) != 3 {
		t.Errorf("decoded %v runs %v", s.Decoded, s.Runs)
	}
}

func TestSummaryWithoutXML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c", "log.jsonl")
	writeLog(t, path, rxLine(0, summaryLine('-', "???")), rxLine(250, summaryLine('-', "PID")))
	sc := NewSummaryScanner()
	s := Stream{
		OnSource: func(si SourceInfo) error { sc.Source(si); return nil },
		OnEntry:  func(e *Entry) error { sc.Entry(e); return nil },
	}
	if err := s.Add(path); err != nil {
		t.Fatal(err)
	}
	got := sc.Summaries(nil)[0]
	if got.Decoded || len(got.Modes) != 0 || len(got.Runs) != 0 || len(got.Masked) != 0 || got.Events[KindReport] != 2 {
		t.Errorf("summary = %+v", got)
	}
}

func testSummary() Summary {
	return Summary{
		Src: "c/log.jsonl", Kind: "capture",
		Start: time.Date(2026, 11, 2, 6, 46, 33, 916_000_000, time.UTC), HasStart: true,
		Duration: 3500 * time.Millisecond, Firmware: "$id: leva! (3.2)", XML: []string{"a", "b"},
		Notes:   "two words",
		Events:  map[Kind]int{KindNotice: 1, KindReport: 4, KindBanner: 1},
		Modes:   map[string]time.Duration{"standby": 250 * time.Millisecond, "PID": 1500 * time.Millisecond, "ECO": 1234567 * time.Microsecond},
		Ready:   []time.Duration{1500 * time.Millisecond, 2 * time.Second},
		Runs:    map[RunKind]int{RunShot: 2, RunFlush: 1, RunDraw: 0},
		RunNums: []int{4, 5, 6}, Masked: []string{"heat_pid", "pressure"},
		GapMax: 3 * time.Second, Gaps: 1, Decoded: true,
	}
}

func TestWriteSummariesText(t *testing.T) {
	bare := Summary{Src: "r.jsonl.gz", Kind: "rawlog", Events: map[Kind]int{}}
	var b bytes.Buffer
	if err := WriteSummaries(&b, FormatText, []Summary{testSummary(), bare}, time.UTC); err != nil {
		t.Fatal(err)
	}
	want := `src: c/log.jsonl
kind: capture
start: 2026-11-02 06:46:33.916
duration: 3.5
firmware: $id: leva! (3.2)
xml: a b
notes: two words
events: report=4 banner=1 notice=1
modes: ECO=1.235 PID=1.5 standby=0.25
ready: 2 (1.5 2)
runs: shot=2 flush=1 draw=0 (4-6)
masked: heat_pid pressure
gap_max: 3
gaps: 1

src: r.jsonl.gz
kind: rawlog
start:
duration: 0
firmware:
xml:
notes:
events:
modes:
ready: 0
runs:
masked:
gap_max: 0
gaps: 0
`
	if b.String() != want {
		t.Errorf("text:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestWriteSummariesJSONL(t *testing.T) {
	var b bytes.Buffer
	if err := WriteSummaries(&b, FormatJSONL, []Summary{testSummary()}, time.UTC); err != nil {
		t.Fatal(err)
	}
	want := `{"src":"c/log.jsonl","kind":"capture","start":"2026-11-02T06:46:33.916Z","duration":3.5,` +
		`"firmware":"$id: leva! (3.2)","xml":["a","b"],"notes":"two words",` +
		`"events":{"report":4,"banner":1,"notice":1},"modes":{"ECO":1.235,"PID":1.5,"standby":0.25},` +
		`"ready":[1.5,2],"runs":{"shot":2,"flush":1,"draw":0,"numbers":[4,5,6]},` +
		`"masked":["heat_pid","pressure"],"gap_max":3,"gaps":1}` + "\n"
	if b.String() != want {
		t.Errorf("jsonl:\n%s\nwant:\n%s", b.String(), want)
	}
	var o map[string]any
	if err := json.Unmarshal(b.Bytes(), &o); err != nil {
		t.Fatal(err)
	}

	b.Reset()
	bare := Summary{Src: "r", Kind: "rawlog"}
	if err := WriteSummaries(&b, FormatJSONL, []Summary{bare}, time.UTC); err != nil {
		t.Fatal(err)
	}
	want = `{"src":"r","kind":"rawlog","start":null,"duration":0,"firmware":null,"xml":[],"notes":null,` +
		`"events":{},"modes":{},"ready":[],"runs":{},"masked":[],"gap_max":0,"gaps":0}` + "\n"
	if b.String() != want {
		t.Errorf("bare jsonl:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestWriteSummariesCSV(t *testing.T) {
	var b bytes.Buffer
	if err := WriteSummaries(&b, FormatCSV, []Summary{testSummary()}, time.UTC); err != nil {
		t.Fatal(err)
	}
	got := lines(b.String())
	want := []string{
		"src,kind,start,duration,firmware,xml,notes,events,modes,ready,runs,masked,gap_max,gaps",
		"c/log.jsonl,capture,2026-11-02T06:46:33.916Z,3.5,$id: leva! (3.2),a b,two words," +
			"report=4 banner=1 notice=1,ECO=1.235 PID=1.5 standby=0.25,2 (1.5 2),shot=2 flush=1 draw=0 (4-6)," +
			"heat_pid pressure,3,1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("csv:\n%s\nwant:\n%s", b.String(), strings.Join(want, "\n"))
	}
}

func TestRunRanges(t *testing.T) {
	for _, tc := range []struct {
		nums []int
		want string
	}{
		{nil, ""},
		{[]int{3}, "3"},
		{[]int{1, 2, 3, 5, 7, 8}, "1-3,5,7-8"},
	} {
		if got := runRanges(tc.nums); got != tc.want {
			t.Errorf("runRanges(%v) = %q, want %q", tc.nums, got, tc.want)
		}
	}
}

func TestSummaryWindow(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "20261110T100000.000Z.jsonl.gz")
	writeLog(t, first,
		rxLine(0, summaryLine('-', "PID")),
		rxLine(1000, summaryLine('-', "PID")),
		rxLine(1250, summaryLine('-', "ECO")),
		rxLine(1500, []byte("[READY]")),
		rxLine(5000, summaryLine('-', "PID")),
	)
	second := filepath.Join(dir, "20261110T110000.000Z.jsonl.gz")
	writeLog(t, second, rxLine(0, summaryLine('-', "PID")))
	t0 := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	sc := NewSummaryScanner()
	sc.Since, sc.Until = t0.Add(time.Second), t0.Add(2*time.Second)
	s := Stream{
		Catalog:  testCatalog(t),
		OnSource: func(si SourceInfo) error { sc.Source(si); return nil },
		OnEntry:  func(e *Entry) error { sc.Entry(e); return nil },
	}
	if err := s.Add(first, second); err != nil {
		t.Fatal(err)
	}
	ss := sc.Summaries(nil)
	if len(ss) != 1 || ss[0].Src != first {
		t.Fatalf("summaries = %+v", ss)
	}
	got := ss[0]
	if got.Events[KindReport] != 2 || got.Events[KindNotice] != 1 || len(got.Events) != 2 {
		t.Errorf("events = %v", got.Events)
	}
	if !got.Start.Equal(t0.Add(time.Second)) || got.Duration != 500*time.Millisecond {
		t.Errorf("start %v duration %v", got.Start, got.Duration)
	}
	if got.Modes["PID"] != 250*time.Millisecond || len(got.Modes) != 1 || got.Gaps != 0 || got.GapMax != 250*time.Millisecond {
		t.Errorf("modes %v gaps %d gap_max %v", got.Modes, got.Gaps, got.GapMax)
	}
}
