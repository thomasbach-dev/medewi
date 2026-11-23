package program

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

// replay feeds a committed fixture to a Detector.
func replay(t *testing.T, name string) []Run {
	t.Helper()
	path := os.Getenv("MEDEWI_XML_PATH")
	if path == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	spec, err := leva.LoadSpecFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join("..", "testdata", "fixtures", name, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	events, err := fixture.ReadLog(f)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDetector()
	var buf []byte
	for _, ev := range events {
		if ev.Dir != fixture.RX {
			continue
		}
		at := t0.Add(time.Duration(ev.T) * time.Millisecond)
		buf = append(buf, ev.Data...)
		for {
			line, rest, ok := bytes.Cut(buf, []byte("\r\n"))
			if !ok {
				break
			}
			buf = rest
			if len(line) > 0 && line[0] == '{' {
				payload, err := leva.VerifyChecksum(line)
				if err != nil {
					t.Fatal(err)
				}
				rec, err := spec.Decode(payload)
				if err != nil {
					t.Fatal(err)
				}
				d.Sample(SampleFrom(rec, payload, at, nil))
			}
		}
	}
	return d.Runs()
}

func TestFixtures(t *testing.T) {
	for name, c := range map[string]struct {
		kind   Kind
		pulses int
		open   bool
	}{
		"backflush":     {Backflush, 9, false},
		"descale-tail":  {Descale, 10, true},
		"shot-genlever": {}, "steam-eco": {}, "idle-stream": {},
	} {
		rs := replay(t, name)
		if c.kind == "" {
			if len(rs) != 0 {
				t.Errorf("%s: %+v, want no program", name, rs)
			}
			continue
		}
		if len(rs) != 1 || rs[0].Kind != c.kind || len(rs[0].Pulses) != c.pulses || rs[0].OpenStart != c.open {
			t.Errorf("%s: %+v, want one %s with %d pulses, open start %v", name, rs, c.kind, c.pulses, c.open)
		}
	}
}
