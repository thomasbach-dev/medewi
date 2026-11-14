package conformance

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

// frameLen is the length of a display frame (docs/machine-behaviour.md).
const frameLen = 69

// classify names one record of received traffic (CRLF stripped, already
// split by leva.SplitLine). A report or frame cut off by a stream switch is
// a "fragment"; unknown text is not ok.
func classify(line []byte) (kind string, ok bool) {
	switch {
	case len(line) == 0:
		return "", false
	case line[0] == '{':
		if line[len(line)-1] == '}' {
			return "report", true
		}
		return "fragment", true
	case line[0] == '<':
		if len(line) == frameLen && line[len(line)-1] == '>' {
			return "frame", true
		}
		return "fragment", true
	case line[0] == '[' && line[len(line)-1] == ']':
		return "notice", true
	case bytes.HasPrefix(line, []byte("$id:")), bytes.HasPrefix(line, []byte("XML=")):
		return "banner", true
	}
	return "", false
}

// TestCommittedFixturesShape guards every real-machine fixture in
// testdata/fixtures/ against rot and against private-data leaks. Shape
// checks always run; full decoding runs only when MEDEWI_XML_PATH points
// at the vendor XML (never committed), so CI still validates structure.
func TestCommittedFixturesShape(t *testing.T) {
	dirs, err := filepath.Glob("../testdata/fixtures/*")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no fixture dirs: %v", err)
	}
	var spec *leva.RecordSpec
	if p := os.Getenv("MEDEWI_XML_PATH"); p != "" {
		spec, err = leva.LoadSpecFile(p)
		if err != nil {
			t.Fatalf("load spec: %v", err)
		}
	} else {
		t.Log("MEDEWI_XML_PATH not set; skipping decode assertions")
	}
	leaks := [][]byte{[]byte("fritz.box"), []byte("rancilio"), []byte("Nextcloud"), []byte("/home/")}
	// The trailing space in "MC " is the real stop command (MC + space).
	txAllowed := map[string]bool{"MC?": true, "MCr": true, "MC ": true, "MC@": true} //nolint:gocritic // mapKey: intentional

	for _, dir := range dirs {
		if filepath.Base(dir) == "synthetic" {
			continue // covered by TestSyntheticFixtures*
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, "log.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			events, err := fixture.ReadLog(f)
			if err != nil || len(events) == 0 {
				t.Fatalf("ReadLog: %v (%d events)", err, len(events))
			}
			m, err := fixture.LoadManifest(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatalf("manifest: %v", err)
			}
			var rx []byte
			for _, e := range events {
				for _, leak := range leaks {
					if bytes.Contains(e.Data, leak) {
						t.Fatalf("private string %q in event data", leak)
					}
				}
				switch e.Dir {
				case fixture.TX:
					cmd := string(bytes.TrimSuffix(e.Data, []byte("\r\n")))
					if !txAllowed[cmd] {
						t.Fatalf("unexpected TX command %q", cmd)
					}
				case fixture.RX:
					rx = append(rx, e.Data...)
				}
			}
			for _, leak := range leaks {
				if bytes.Contains([]byte(m.MachineAddr+m.Notes), leak) {
					t.Fatalf("private string %q in manifest", leak)
				}
			}
			reports, fragments := 0, 0
			for _, line := range bytes.Split(rx, []byte("\r\n")) {
				for _, p := range leva.SplitLine(line) {
					if len(p.Data) == 0 {
						continue
					}
					kind, ok := classify(p.Data)
					if !ok {
						t.Errorf("unclassified rx line: %q", p.Data[:min(len(p.Data), 60)])
						continue
					}
					switch kind {
					case "fragment":
						fragments++
					case "report":
						reports++
						if len(p.Data) >= 3 && p.Data[len(p.Data)-3] == '~' {
							t.Errorf("unexpected '~' at len-3: %q", p.Data[:40])
						}
						if spec != nil {
							payload, err := leva.VerifyChecksum(p.Data)
							if err != nil {
								t.Fatalf("checksum: %v", err)
							}
							if _, err := spec.Decode(payload); err != nil {
								t.Fatalf("decode: %v", err)
							}
						}
					}
				}
			}
			t.Logf("%d fragments", fragments)
			if reports == 0 {
				t.Fatal("fixture contains no report lines")
			}
		})
	}
}

func TestFixtureGuardClassifies(t *testing.T) {
	report := "{ -" + strings.Repeat("x", 285) + "}"
	frame := "<100" + strings.Repeat("a", 64) + ">"
	cut := "<100" + strings.Repeat("b", 36)
	tests := []struct {
		name string
		line string
		want []string // kinds per piece, "" = not ok
	}{
		{"report", report, []string{"report"}},
		{"frame", frame, []string{"frame"}},
		{"notice", "[READY]", []string{"notice"}},
		{"banner", "$id: leva! (3.2)", []string{"banner"}},
		{"glued", cut + report, []string{"fragment", "report"}},
		{"fragment", cut, []string{"fragment"}},
		{"junk", "hello", []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, p := range leva.SplitLine([]byte(tt.line)) {
				kind, _ := classify(p.Data)
				got = append(got, kind)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
	if _, ok := classify([]byte("hello")); ok {
		t.Fatal("hello classified ok")
	}
}
