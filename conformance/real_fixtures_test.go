package conformance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

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
	txAllowed := map[string]bool{"MC?": true, "MCr": true, "MC ": true}

	for _, dir := range dirs {
		if filepath.Base(dir) == "synthetic" {
			continue // covered by TestSyntheticFixtures*
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, "log.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
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
			reports := 0
			for _, line := range bytes.Split(rx, []byte("\r\n")) {
				switch {
				case len(line) == 0:
				case line[0] == '{':
					reports++
					if len(line) >= 3 && line[len(line)-3] == '~' {
						t.Errorf("unexpected '~' at len-3: %q", line[:40])
					}
					if spec != nil {
						payload, err := leva.VerifyChecksum(line)
						if err != nil {
							t.Fatalf("checksum: %v", err)
						}
						if _, err := spec.Decode(payload); err != nil {
							t.Fatalf("decode: %v", err)
						}
					}
				case bytes.HasPrefix(line, []byte("$id:")), bytes.HasPrefix(line, []byte("XML=")):
				default:
					t.Errorf("unclassified rx line: %q", line[:min(len(line), 60)])
				}
			}
			if reports == 0 {
				t.Fatal("fixture contains no report lines")
			}
		})
	}
}
