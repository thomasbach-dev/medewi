package fixture

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

	// Pin the on-disk keys: the snake_case names are part of the format.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("Unmarshal manifest: %v", err)
	}
	wantKeys := []string{"scenario", "firmware_version", "xml_name", "xml_sha256", "machine_addr", "notes", "recorded"}
	if len(keys) != len(wantKeys) {
		t.Errorf("manifest has %d keys, want %d: %v", len(keys), len(wantKeys), keys)
	}
	for _, k := range wantKeys {
		if _, ok := keys[k]; !ok {
			t.Errorf("manifest missing key %q", k)
		}
	}
}

func TestWriteCaptureWritesBoth(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "cap")
	events := []Event{
		{T: 0, Dir: TX, Data: []byte("MCr\r\n")},
		{T: 250, Dir: RX, Data: []byte{0x00, 0x7b, 0xff}},
	}
	m := Manifest{
		Scenario:    "s",
		MachineAddr: "machine.example:23",
		Notes:       "n",
		Recorded:    time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC),
	}
	if err := WriteCapture(dir, "log.jsonl", "manifest.json", events, m); err != nil {
		t.Fatalf("WriteCapture: %v", err)
	}
	f, err := os.Open(filepath.Join(dir, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := ReadLog(f)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if !reflect.DeepEqual(got, events) {
		t.Errorf("events = %v, want %v", got, events)
	}
	gm, err := LoadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if !gm.Recorded.Equal(m.Recorded) {
		t.Errorf("recorded = %v, want %v", gm.Recorded, m.Recorded)
	}
	gm.Recorded, m.Recorded = time.Time{}, time.Time{}
	if gm != m {
		t.Errorf("manifest = %+v, want %+v", gm, m)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("dir has %d entries, want exactly the two files", len(entries))
	}
}

func TestWriteCaptureKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.jsonl")
	if err := os.WriteFile(logPath, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory in the manifest's temp slot makes the manifest write fail.
	if err := os.Mkdir(filepath.Join(dir, ".manifest.json.tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := WriteCapture(dir, "log.jsonl", "manifest.json",
		[]Event{{T: 0, Dir: RX, Data: []byte("x")}}, Manifest{})
	if err == nil {
		t.Fatal("WriteCapture succeeded, want error")
	}
	got, rerr := os.ReadFile(logPath)
	if rerr != nil || string(got) != "old" {
		t.Errorf("log.jsonl = %q, %v; want untouched", got, rerr)
	}
	// No temp file is left behind (it would make the next extract refuse
	// a non-empty --out).
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("%s left behind", e.Name())
		}
	}
}
