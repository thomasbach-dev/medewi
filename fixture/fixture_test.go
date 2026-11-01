package fixture

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLogRoundTrip(t *testing.T) {
	in := []Event{
		{T: 0, Dir: TX, Data: []byte("$id?\r\n")},
		{T: 12, Dir: RX, Data: []byte{0x00, 0x0d, 0x0a, 0xdf, 0xff}},
		{T: 1500, Dir: RX, Data: []byte("$id: leva!\r\n\x00")},
	}

	var buf bytes.Buffer
	if err := WriteLog(&buf, in); err != nil {
		t.Fatalf("WriteLog: %v", err)
	}

	out, err := ReadLog(&buf)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}

	if len(out) != len(in) {
		t.Fatalf("got %d events, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].T != in[i].T {
			t.Errorf("event %d: T = %d, want %d", i, out[i].T, in[i].T)
		}
		if out[i].Dir != in[i].Dir {
			t.Errorf("event %d: Dir = %q, want %q", i, out[i].Dir, in[i].Dir)
		}
		if !bytes.Equal(out[i].Data, in[i].Data) {
			t.Errorf("event %d: Data = %x, want %x", i, out[i].Data, in[i].Data)
		}
	}
}

func TestManifestRoundTrip(t *testing.T) {
	in := Manifest{
		Scenario:        "boot-and-brew",
		FirmwareVersion: "3.2",
		XMLName:         "leva!-65FCB201.xml",
		XMLSHA256:       "deadbeef",
		MachineAddr:     "rancilio:23",
		Notes:           "first capture",
		Recorded:        time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
	}

	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := in.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	if !reflect.DeepEqual(out, in) {
		t.Errorf("LoadManifest = %+v, want %+v", out, in)
	}
}
