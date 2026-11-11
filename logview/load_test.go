package logview

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// writeRaw writes a raw-log file named for start into dir.
func writeRaw(t *testing.T, dir string, start time.Time, evs ...fixture.Event) string {
	t.Helper()
	var plain bytes.Buffer
	if err := fixture.WriteLog(&plain, evs); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(plain.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, start.UTC().Format("20060102T150405.000Z")+".jsonl.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var t0 = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)

func reportEv() fixture.Event { return ev(10, fixture.RX, string(testReport())+"\r\n") }

func TestLoadCarriesXML(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCatalog(xmlDir(t, "a"))
	if err != nil {
		t.Fatal(err)
	}
	p1 := writeRaw(t, dir, t0, ev(0, fixture.RX, "XML=a\r\n"), reportEv())
	p2 := writeRaw(t, dir, t0.Add(time.Hour), reportEv())
	l, err := Load([]string{p1, p2}, c, false)
	if err != nil {
		t.Fatal(err)
	}
	last := l.Entries[len(l.Entries)-1]
	if last.Record == nil || last.XML != "a" || len(l.Problems) != 0 {
		t.Errorf("last = %+v, problems %v", last, l.Problems)
	}
}

func TestLoadLooksBackForXML(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCatalog(xmlDir(t, "a"))
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, dir, t0, ev(0, fixture.RX, "XML=a\r\n"), reportEv())
	writeRaw(t, dir, t0.Add(time.Hour), reportEv())
	p3 := writeRaw(t, dir, t0.Add(2*time.Hour), reportEv())
	l, err := Load([]string{p3}, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 1 || l.Entries[0].Record == nil || l.Entries[0].XML != "a" {
		t.Errorf("entries = %+v", l.Entries)
	}
	if got, err := LastXML(dir, t0); err != nil || got != "" {
		t.Errorf("LastXML before the first = %q, %v", got, err)
	}
	if got, err := LastXML(dir, t0.Add(2*time.Hour)); err != nil || got != "a" {
		t.Errorf("LastXML = %q, %v", got, err)
	}
}

func TestLoadCaptureNoXML(t *testing.T) {
	c, err := LoadCatalog(xmlDir(t, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := Load([]string{"../testdata/fixtures/shot-genlever"}, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Problems) != 1 {
		t.Fatalf("problems = %v", l.Problems)
	}
	for _, e := range l.Entries {
		if e.Record != nil {
			t.Fatal("a report decoded without an XML")
		}
	}
}

func TestLoadSourceIndex(t *testing.T) {
	dir := t.TempDir()
	p1 := writeRaw(t, dir, t0, ev(0, fixture.TX, "MC?\r\n"))
	p2 := writeRaw(t, dir, t0.Add(time.Hour), ev(0, fixture.TX, "MC?\r\n"))
	l, err := Load([]string{p1, p2}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Sources) != 2 || len(l.Entries) != 2 || l.Entries[0].Source != 0 || l.Entries[1].Source != 1 {
		t.Errorf("sources %d entries %+v", len(l.Sources), l.Entries)
	}
}
