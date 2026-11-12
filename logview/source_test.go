package logview

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/rawlog"
)

func TestOpenRawLog(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 11, 10, 9, 42, 0, 123_000_000, time.UTC)
	l, err := rawlog.Open(rawlog.Config{Dir: dir, Now: func() time.Time { return start }})
	if err != nil {
		t.Fatal(err)
	}
	s := l.Session()
	s.Tap("tx", []byte("MC?\r\n"))
	s.Note("hello")
	s.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %v, err = %v", files, err)
	}
	src, err := Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !src.HasWall {
		t.Error("HasWall = false")
	}
	if !src.Start.Equal(start) {
		t.Errorf("Start = %v, want %v", src.Start, start)
	}
	if len(src.Events) < 2 {
		t.Errorf("events = %d, want >= 2", len(src.Events))
	}
	ev := fixture.Event{T: 1500}
	if got, want := src.Wall(ev), start.Add(1500*time.Millisecond); !got.Equal(want) {
		t.Errorf("Wall = %v, want %v", got, want)
	}
}

func gzipEvents(t *testing.T, n int) []byte {
	t.Helper()
	var evs []fixture.Event
	for i := 0; i < n; i++ {
		evs = append(evs, fixture.Event{T: int64(i * 100), Dir: fixture.RX, Data: bytes.Repeat([]byte("{x}\r\n"), 40)})
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := fixture.WriteLog(gz, evs); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestOpenTruncated(t *testing.T) {
	data := gzipEvents(t, 3)
	cut := data[:len(data)*7/10]
	dir := t.TempDir()
	for _, name := range []string{"20261110T094200.000Z.jsonl.gz", "log.jsonl"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, cut, 0o600); err != nil {
			t.Fatal(err)
		}
		src, err := Open(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(src.Events) < 1 {
			t.Errorf("%s: no events", name)
		}
	}
}

func TestOpenPlainCapture(t *testing.T) {
	var buf bytes.Buffer
	evs := []fixture.Event{{T: 1, Dir: fixture.RX, Data: []byte("a")}, {T: 2, Dir: fixture.TX, Data: []byte("b")}}
	if err := fixture.WriteLog(&buf, evs); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "log.jsonl")
	// A cut last line is tolerated.
	if err := os.WriteFile(p, append(bytes.Clone(buf.Bytes()), []byte(`{"t":3,"dir":"r`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(src.Events, evs) {
		t.Errorf("events = %v, want %v", src.Events, evs)
	}
	// A bad line in the middle is an error.
	bad := append([]byte("garbage\n"), buf.Bytes()...)
	if err := os.WriteFile(p, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Error("bad middle line: want error")
	}
}

func TestOpenCapture(t *testing.T) {
	const dir = "../testdata/fixtures/shot-genlever"
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(filepath.Join(dir, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Events) == 0 || !reflect.DeepEqual(a.Events, b.Events) {
		t.Errorf("events differ: %d vs %d", len(a.Events), len(b.Events))
	}
	if a.HasWall || b.HasWall || !a.Start.IsZero() {
		t.Error("capture must have no wall clock")
	}
	m, err := fixture.LoadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Notes != m.Notes || b.Notes != m.Notes {
		t.Errorf("notes = %q / %q, want %q", a.Notes, b.Notes, m.Notes)
	}
}

func TestOpenCaptureNotes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (fixture.Manifest{Notes: "dialed in"}).Save(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	src, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if src.Notes != "dialed in" {
		t.Errorf("notes = %q", src.Notes)
	}
}

func TestSelect(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"20261110T100000.000Z.jsonl.gz",
		"20261110T110000.000Z.jsonl.gz",
		"20261110T120000.000Z.jsonl.gz",
		"junk.txt",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.UTC) }
	var zero time.Time
	tests := []struct {
		name         string
		since, until time.Time
		want         []string
	}{
		{"inside first", at(10, 42), at(10, 50), names[:1]},
		{"across", at(10, 42), at(11, 5), names[:2]},
		{"last open-ended", at(12, 30), zero, names[2:3]},
		{"all", zero, zero, names[:3]},
		{"before all", at(9, 0), at(9, 30), nil},
	}
	for _, tc := range tests {
		got, err := Select(dir, tc.since, tc.until)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got == nil {
			t.Errorf("%s: got nil, want empty slice", tc.name)
		}
		var base []string
		for _, p := range got {
			base = append(base, filepath.Base(p))
		}
		if len(base) != len(tc.want) || (len(base) > 0 && !reflect.DeepEqual(base, tc.want)) {
			t.Errorf("%s: got %v, want %v", tc.name, base, tc.want)
		}
	}
	if _, err := Select(filepath.Join(dir, "missing"), zero, zero); err == nil {
		t.Error("missing dir: want error")
	}
}

func TestOpenLines(t *testing.T) {
	var buf bytes.Buffer
	evs := []fixture.Event{{T: 1, Dir: fixture.RX, Data: []byte("a")}, {T: 2, Dir: fixture.TX, Data: []byte("b")}}
	if err := fixture.WriteLog(&buf, evs); err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(buf.Bytes(), []byte("\n"))
	data := bytes.Join([][]byte{lines[0], []byte("\n"), lines[1]}, nil)
	dir := t.TempDir()
	p := filepath.Join(dir, "log.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(src.Lines, []int{1, 3}) {
		t.Errorf("Lines = %v, want [1 3]", src.Lines)
	}
	// Raw logs, gzip: one line per event.
	gz := filepath.Join(dir, "20261110T094200.000Z.jsonl.gz")
	if err := os.WriteFile(gz, gzipEvents(t, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	if src, err = Open(gz); err != nil || !reflect.DeepEqual(src.Lines, []int{1, 2, 3}) {
		t.Errorf("raw Lines = %v, err %v", src.Lines, err)
	}
}
