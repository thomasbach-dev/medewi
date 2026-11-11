package logview

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
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
	l, err := loadAll([]string{p1, p2}, c)
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
	l, err := loadAll([]string{p3}, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 1 || l.Entries[0].Record == nil || l.Entries[0].XML != "a" {
		t.Errorf("entries = %+v", l.Entries)
	}
	if got, _, err := LastXML(dir, t0); err != nil || got != "" {
		t.Errorf("LastXML before the first = %q, %v", got, err)
	}
	if got, _, err := LastXML(dir, t0.Add(2*time.Hour)); err != nil || got != "a" {
		t.Errorf("LastXML = %q, %v", got, err)
	}
}

func TestLoadCaptureNoXML(t *testing.T) {
	c, err := LoadCatalog(xmlDir(t, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := loadAll([]string{"../testdata/fixtures/shot-genlever"}, c)
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
	l, err := loadAll([]string{p1, p2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Sources) != 2 || len(l.Entries) != 2 || l.Entries[0].Source != 0 || l.Entries[1].Source != 1 {
		t.Errorf("sources %d entries %+v", len(l.Sources), l.Entries)
	}
}

func TestLastXMLSkipsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, t0, ev(0, fixture.RX, "XML=a\r\n"))
	bad := writeRaw(t, dir, t0.Add(time.Hour), reportEv(), reportEv())
	var plain bytes.Buffer
	plain.WriteString("garbage\n")
	if err := fixture.WriteLog(&plain, []fixture.Event{reportEv(), reportEv()}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(plain.Bytes())
	_ = gz.Close()
	if err := os.WriteFile(bad, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	name, warns, err := LastXML(dir, t0.Add(2*time.Hour))
	if err != nil || name != "a" {
		t.Fatalf("name %q err %v", name, err)
	}
	if len(warns) != 1 || !strings.HasPrefix(warns[0], bad+": ") {
		t.Errorf("warnings = %v", warns)
	}
}

func TestLoadResetsAfterCapture(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCatalog(xmlDir(t, "a"))
	if err != nil {
		t.Fatal(err)
	}
	p1 := writeRaw(t, dir, t0, ev(0, fixture.RX, "XML=a\r\n"), reportEv())
	p3 := writeRaw(t, dir, t0.Add(2*time.Hour), reportEv())
	l, err := loadAll([]string{p1, "../testdata/fixtures/shot-genlever", p3}, c)
	if err != nil {
		t.Fatal(err)
	}
	last := l.Entries[len(l.Entries)-1]
	if last.Source != 2 || last.XML != "a" || last.Record == nil {
		t.Errorf("last = %+v", last)
	}
}

func TestStreamAddContinues(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCatalog(xmlDir(t, "a"))
	if err != nil {
		t.Fatal(err)
	}
	p1 := writeRaw(t, dir, t0, ev(0, fixture.RX, "XML=a\r\n"), reportEv())
	p2 := writeRaw(t, dir, t0.Add(time.Hour), reportEv(), ev(20, fixture.TX, "MC?\r\n"))
	var infos []SourceInfo
	var es []*Entry
	s := Stream{
		Catalog:  c,
		OnSource: func(si SourceInfo) error { infos = append(infos, si); return nil },
		OnEntry:  func(e *Entry) error { es = append(es, e); return nil },
	}
	if err := s.Add(p1); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(p2); err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[1].Index != 1 || infos[1].Events != 2 || infos[1].First != 10*time.Millisecond || infos[1].Last != 20*time.Millisecond {
		t.Fatalf("infos = %+v", infos)
	}
	if len(es) != 4 {
		t.Fatalf("entries = %d, want 4", len(es))
	}
	for i, e := range es {
		if e.Index != i {
			t.Errorf("entry %d has Index %d", i, e.Index)
		}
	}
	if e := es[2]; e.Source != 1 || e.XML != "a" || e.Record == nil {
		t.Errorf("first entry of the added file = %+v", e)
	}
}
