// Package logview opens raw-log files and captures and selects raw-log
// files for a time window, as the basis of offline decoding.
package logview

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/rawlog"
)

// Source is one opened log: a raw-log file or a capture.
type Source struct {
	Path    string
	Start   time.Time // file-name time for raw logs, zero for captures
	HasWall bool      // true only for raw logs
	Notes   string    // the capture manifest's notes, or empty
	Events  []fixture.Event
	Lines   []int // 1-based line in the decompressed file of each event
}

// Wall returns the wall-clock time of ev: Start + T ms. It is only
// meaningful when HasWall is true.
func (s Source) Wall(ev fixture.Event) time.Time {
	return s.Start.Add(time.Duration(ev.T) * time.Millisecond)
}

// Open reads path: a raw-log file (name parsed by rawlog.FileStart), a
// capture log.jsonl (plain or gzip) or a capture directory holding
// log.jsonl. A sibling manifest.json supplies Notes. A missing gzip trailer
// or a cut last line yields the events before it, without an error.
func Open(path string) (Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Source{}, fmt.Errorf("logview: %w", err)
	}
	if fi.IsDir() {
		path = filepath.Join(path, "log.jsonl")
	}
	src := Source{Path: path}
	if start, ok := rawlog.FileStart(filepath.Base(path)); ok {
		src.Start, src.HasWall = start, true
		src.Events, src.Lines, err = readEvents(path)
		if err != nil {
			return Source{}, err
		}
		return src, nil
	}
	src.Events, src.Lines, err = readEvents(path)
	if err != nil {
		return Source{}, err
	}
	if m, err := fixture.LoadManifest(filepath.Join(filepath.Dir(path), "manifest.json")); err == nil {
		src.Notes = m.Notes
	}
	return src, nil
}

// readEvents reads a plain or gzip JSON-lines log and returns the events
// with the 1-based line number of each. A cut last line or a missing gzip
// trailer is tolerated; a bad line in the middle is not.
func readEvents(path string) ([]fixture.Event, []int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("logview: %w", err)
	}
	data := raw
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return nil, nil, nil // no complete header yet
			}
			return nil, nil, fmt.Errorf("logview: %s: %w", path, err)
		}
		// A truncated stream returns the bytes read so far with an error.
		data, err = io.ReadAll(gz)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil, fmt.Errorf("logview: %s: %w", path, err)
		}
	}
	lines := bytes.Split(data, []byte("\n"))
	for len(lines) > 0 && len(bytes.TrimSpace(lines[len(lines)-1])) == 0 {
		lines = lines[:len(lines)-1]
	}
	var events []fixture.Event
	var nums []int
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev fixture.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			if i == len(lines)-1 {
				break // cut last line
			}
			return events, nums, fmt.Errorf("logview: %s: event %d: %w", path, len(events), err)
		}
		events = append(events, ev)
		nums = append(nums, i+1)
	}
	return events, nums, nil
}

// Select returns the raw-log files in dir that may hold events in
// [since, until), sorted by start. File i is kept when start_i < until (a
// zero until means no bound) and (start_{i+1} > since or i is the last
// file). Names rawlog.FileStart rejects are ignored. No match yields an
// empty slice.
func Select(dir string, since, until time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("logview: %w", err)
	}
	type file struct {
		path  string
		start time.Time
	}
	var files []file
	for _, e := range entries {
		if start, ok := rawlog.FileStart(e.Name()); ok {
			files = append(files, file{filepath.Join(dir, e.Name()), start})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].start.Before(files[j].start) })
	out := []string{}
	for i, f := range files {
		if !until.IsZero() && !f.start.Before(until) {
			continue
		}
		if i+1 < len(files) && !files[i+1].start.After(since) {
			continue
		}
		out = append(out, f.path)
	}
	return out, nil
}
