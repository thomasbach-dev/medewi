package brew

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

// replayFixture decodes the RX report lines of a committed fixture with the
// vendor spec and feeds them to a recorder. Each line is timestamped with
// the chunk that contains its CRLF.
func replayFixture(t *testing.T, spec *leva.RecordSpec, name string) (closed []Summary, water []WaterDraw) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "testdata", "fixtures", name, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	events, err := fixture.ReadLog(f)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRecorder(DefaultConfig())
	var buf []byte
	handle := func(line []byte, at time.Time) {
		if len(line) == 0 || line[0] != '{' {
			return
		}
		payload, err := leva.VerifyChecksum(line)
		if err != nil {
			t.Fatalf("checksum: %v", err)
		}
		rec, err := spec.Decode(payload)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		opt := func(name string) *float64 {
			if v, ok := rec.Float(name); ok {
				return &v
			}
			return nil
		}
		res := r.Feed(Sample{
			At:         at,
			Pumping:    payload[2] == '*',
			Temp:       opt("Temperature PID1"),
			Setpoint:   opt("Setpoint PID"),
			Pressure:   opt("Pressure"),
			PressureSP: opt("Pressure SP"),
			Flow:       opt("Flow rate"),
			Heat:       opt("Heat PID"),
			Volume:     opt("Volume"),
		})
		if res.Closed != nil {
			closed = append(closed, *res.Closed)
		}
		if res.Water != nil {
			water = append(water, *res.Water)
		}
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, ev := range events {
		if ev.Dir != fixture.RX {
			continue
		}
		buf = append(buf, ev.Data...)
		for {
			i := bytes.Index(buf, []byte("\r\n"))
			if i < 0 {
				break
			}
			handle(buf[:i], base.Add(time.Duration(ev.T)*time.Millisecond))
			buf = buf[i+2:]
		}
	}
	if s := r.Abort(TruncatedLost); s != nil {
		closed = append(closed, *s)
	}
	return closed, water
}

func TestFixtureRuns(t *testing.T) {
	path := os.Getenv("MEDEWI_XML_PATH")
	if path == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	spec, err := leva.LoadSpecFile(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("shot-genlever", func(t *testing.T) {
		closed, water := replayFixture(t, spec, "shot-genlever")
		if len(closed) != 2 {
			t.Fatalf("brews = %+v, want flush then shot", closed)
		}
		if c := closed[0]; c.Kind != Flush || c.VolumeML != 73 {
			t.Errorf("first = %+v, want flush 73 ml", c)
		}
		if c := closed[1]; c.Kind != Shot || c.VolumeML != 101 || c.PeakBar < 8.9 || c.PeakBar > 9.1 {
			t.Errorf("second = %+v, want shot 101 ml at about 9 bar", c)
		}
		if len(water) != 0 {
			t.Errorf("water = %+v, want none", water)
		}
	})

	t.Run("steam-eco", func(t *testing.T) {
		closed, water := replayFixture(t, spec, "steam-eco")
		if len(closed) != 0 {
			t.Errorf("brews = %+v, want none", closed)
		}
		if len(water) != 1 || water[0].VolumeML != 144 {
			t.Errorf("water = %+v, want one 144 ml draw", water)
		}
	})
}
