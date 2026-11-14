package logview

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/rawlog"
)

// crlf ends every line the machine sends.
var crlf = []byte("\r\n")

// SessionStart reports whether src begins a raw-log session: its first
// event is not the "xml: " note at t = 0. Once Session.SetXML ran, rawlog
// starts every file of a session with that note, so a file without it
// starts a new session (a session without an XML counts each of its files
// as a session of its own).
func SessionStart(src Source) bool {
	if len(src.Events) == 0 {
		return true
	}
	return !isXMLNoteAtZero(src.Events[0])
}

// isXMLNoteAtZero reports whether ev is the XML note rawlog writes at the
// start of a rotated file.
func isXMLNoteAtZero(ev fixture.Event) bool {
	return ev.T == 0 && ev.Dir == fixture.Note && bytes.HasPrefix(ev.Data, []byte(rawlog.XMLNotePrefix))
}

// Extracted is a window cut out of raw logs, ready to become a fixture.
type Extracted struct {
	Events      []fixture.Event // t rebased: first kept event at 0; XML note first
	First, Last time.Time       // wall times of the first and last kept event
	Files       []string        // basenames of the sources the events come from
	// XMLName is from the session's last xml note before the window (the
	// one prepended); without one, from the first xml note in the window,
	// kept where it is; "" if neither.
	XMLName  string
	Firmware string // last "$id:" banner line of the session before or in the window, CRLF trimmed; "" if unknown
	// Reports and Frames count the kept rx lines starting with '{' and
	// '<'; NoteEvents counts the note events in Events, the XML note
	// included.
	Reports, Frames, NoteEvents int
	Notes                       []string // e.g. "window starts mid-line: dropped 120 bytes", "firmware unknown: …"
}

// SessionBoundaryError reports that the window crosses into a new session
// at At.
type SessionBoundaryError struct{ At time.Time }

func (e *SessionBoundaryError) Error() string {
	return fmt.Sprintf("logview: the window crosses a raw-log session boundary at %s", e.At.Format(time.RFC3339Nano))
}

// keptEvent is a kept event with its wall time, before rebasing.
type keptEvent struct {
	ev   fixture.Event
	wall time.Time
	file string
}

// Extract cuts [since, until) out of srcs: raw logs of one directory in
// start order, possibly beginning before the window (for the firmware).
// Every event with its wall time in the window is kept verbatim, except
// that the first kept rx bytes start a line and the last ones end one,
// and the XML note at t = 0 of a rotated file is replaced by one XML note
// in front: the session's last one before the first kept event, if any.
// Without rx bytes before the window (its first input file is a rotated
// one), the window is taken to start at a line: rawlog emits rx events at
// line boundaries. A window that holds events of two sessions yields a
// *SessionBoundaryError; one without events an error.
func Extract(srcs []Source, since, until time.Time) (Extracted, error) {
	var (
		out          Extracted
		kept         []keptEvent
		firmware     string
		xmlBefore    string    // the session's last xml note before anything kept
		xmlIn        string    // the first xml note kept in the window
		xmlInAt      time.Time // its wall time
		sessionFirst string    // basename of the session's first source in srcs
		sessionKnown bool      // the session's first file is in srcs
		lineStart    = true
		started      bool // an rx event was kept
		dropped      int
	)
	for _, src := range srcs {
		base := filepath.Base(src.Path)
		switch {
		case SessionStart(src):
			if len(kept) > 0 && src.Start.Before(until) {
				return Extracted{}, &SessionBoundaryError{At: src.Start}
			}
			firmware, xmlBefore, xmlIn, lineStart = "", "", "", true
			sessionFirst, sessionKnown = base, true
		case sessionFirst == "":
			sessionFirst = base // a rotated file: the session began earlier
		}
		for i, ev := range src.Events {
			wall := src.Wall(ev)
			if !wall.Before(until) {
				break
			}
			if ev.Dir == fixture.RX {
				if b, ok := lastBanner(ev.Data); ok {
					firmware = b
				}
			}
			name, isXML := strings.CutPrefix(string(ev.Data), rawlog.XMLNotePrefix)
			isXML = isXML && ev.Dir == fixture.Note
			if wall.Before(since) || (i == 0 && isXMLNoteAtZero(ev)) {
				if isXML && len(kept) == 0 {
					xmlBefore = name
				}
				if ev.Dir == fixture.RX {
					lineStart = bytes.HasSuffix(ev.Data, crlf)
				}
				continue
			}
			if ev.Dir == fixture.RX && !started {
				data := ev.Data
				if !lineStart {
					cut := bytes.Index(data, crlf)
					if cut < 0 {
						dropped += len(data)
						continue // still the cut line
					}
					dropped += cut + len(crlf)
					data = data[cut+len(crlf):]
					lineStart = true
					if len(data) == 0 {
						continue
					}
				}
				started = true
				ev.Data = data
			}
			if isXML && xmlIn == "" {
				xmlIn, xmlInAt = name, wall
			}
			kept = append(kept, keptEvent{ev: ev, wall: wall, file: base})
		}
	}
	if dropped > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("window starts mid-line: dropped %d bytes", dropped))
	}
	kept, dropped = trimTail(kept)
	if dropped > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("window ends mid-line: dropped %d bytes", dropped))
	}
	if len(kept) == 0 {
		return Extracted{}, fmt.Errorf("logview: no events in [%s, %s)",
			since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano))
	}

	out.First, out.Last = kept[0].wall, kept[len(kept)-1].wall
	out.Firmware = firmware
	if firmware == "" {
		if sessionKnown {
			out.Notes = append(out.Notes, "firmware unknown: no $id: banner in the session")
		} else {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"firmware unknown: the session's first file, before %s, is not in the input", sessionFirst))
		}
	}
	switch {
	case xmlBefore != "":
		out.XMLName = xmlBefore
		out.Events = append(out.Events, fixture.Event{T: 0, Dir: fixture.Note, Data: []byte(rawlog.XMLNotePrefix + xmlBefore)})
	case xmlIn != "":
		out.XMLName = xmlIn
		out.Notes = append(out.Notes, fmt.Sprintf("XML named only at %s, inside the window", xmlInAt.Format(time.RFC3339Nano)))
	}
	var rxData []byte
	for _, k := range kept {
		ev := k.ev
		ev.T = k.wall.Sub(out.First).Milliseconds()
		out.Events = append(out.Events, ev)
		if n := len(out.Files); n == 0 || out.Files[n-1] != k.file {
			out.Files = append(out.Files, k.file)
		}
		if ev.Dir == fixture.RX {
			rxData = append(rxData, ev.Data...)
		}
	}
	for _, ev := range out.Events {
		if ev.Dir == fixture.Note {
			out.NoteEvents++
		}
	}
	for _, line := range bytes.Split(rxData, crlf) {
		switch {
		case bytes.HasPrefix(line, []byte("{")):
			out.Reports++
		case bytes.HasPrefix(line, []byte("<")):
			out.Frames++
		}
	}
	return out, nil
}

// trimTail drops the rx bytes after the last CRLF of kept: an incomplete
// last line, possibly spread over several rx events. tx and note events
// stay. It returns the kept events and the number of bytes dropped.
func trimTail(kept []keptEvent) ([]keptEvent, int) {
	dropped := 0
	for i := len(kept) - 1; i >= 0; i-- {
		ev := kept[i].ev
		if ev.Dir != fixture.RX {
			continue
		}
		if bytes.HasSuffix(ev.Data, crlf) {
			break
		}
		cut := bytes.LastIndex(ev.Data, crlf)
		if cut >= 0 {
			dropped += len(ev.Data) - cut - len(crlf)
			kept[i].ev.Data = ev.Data[:cut+len(crlf)]
			break
		}
		dropped += len(ev.Data)
		kept = append(kept[:i], kept[i+1:]...)
	}
	return kept, dropped
}

// lastBanner returns the last line of data starting with "$id:", CRLF
// trimmed.
func lastBanner(data []byte) (string, bool) {
	var banner []byte
	for _, line := range bytes.Split(data, crlf) {
		if bytes.HasPrefix(line, []byte("$id:")) {
			banner = line
		}
	}
	return string(banner), banner != nil
}
