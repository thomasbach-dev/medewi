// Package fixture defines the on-disk format for recorded controller traffic:
// a JSON Lines event log plus a JSON manifest describing the capture.
package fixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Dir is the direction of a recorded chunk, relative to the client.
type Dir string

const (
	// TX marks bytes sent to the machine.
	TX Dir = "tx"
	// RX marks bytes received from the machine.
	RX Dir = "rx"
	// Note marks a plain-text annotation by the recorder (for example
	// "dropped 3 lines"). It is not traffic: replay ignores it.
	Note Dir = "note"
)

// Event is a single recorded chunk of raw traffic.
// Data marshals as base64, so arbitrary binary content round-trips exactly.
type Event struct {
	T    int64  `json:"t"`    // milliseconds since session start
	Dir  Dir    `json:"dir"`  // TX, RX or Note
	Data []byte `json:"data"` // raw bytes
}

// WriteLog writes events as JSON Lines, one event per line.
func WriteLog(w io.Writer, events []Event) error {
	enc := json.NewEncoder(w)
	for i, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return fmt.Errorf("encode event %d: %w", i, err)
		}
	}
	return nil
}

// ReadLog reads all JSON Lines events from r.
func ReadLog(r io.Reader) ([]Event, error) {
	dec := json.NewDecoder(r)
	var events []Event
	for {
		var ev Event
		err := dec.Decode(&ev)
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return nil, fmt.Errorf("decode event %d: %w", len(events), err)
		}
		events = append(events, ev)
	}
}

// Manifest describes a recording session.
type Manifest struct {
	Scenario        string    `json:"scenario"`
	FirmwareVersion string    `json:"firmware_version"`
	XMLName         string    `json:"xml_name"`
	XMLSHA256       string    `json:"xml_sha256"`
	MachineAddr     string    `json:"machine_addr"`
	Notes           string    `json:"notes"`
	Recorded        time.Time `json:"recorded"`
}

// Save writes the manifest to path as pretty-printed JSON.
func (m Manifest) Save(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

// LoadManifest reads a manifest from path.
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	return m, nil
}
