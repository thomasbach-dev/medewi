package logview

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/rawlog"
)

// Session is one raw-log session as inferred from the files.
type Session struct {
	Start      time.Time // wall time of its first file's start
	Files      []string  // basenames, in order
	StartKnown bool      // its first file is among the inputs
	Firmware   string    // its last "$id:" banner seen in the inputs ("" if none)
	XML        string    // its last xml note name seen in the inputs ("" if none)
}

// Window is a part of a time window that lies in one session.
type Window struct {
	Since, Until time.Time
	Session      int // index into the sessions passed to SplitWindow
}

// Sessions groups srcs (raw logs sorted by Start) into sessions by the
// SessionStart rule. A first source that continues a session (it starts
// with the xml note) opens a session whose start is not known.
func Sessions(srcs []Source) []Session {
	var out []Session
	for _, src := range srcs {
		start := SessionStart(src)
		if start || len(out) == 0 {
			out = append(out, Session{Start: src.Start, StartKnown: start})
		}
		s := &out[len(out)-1]
		s.Files = append(s.Files, filepath.Base(src.Path))
		for _, ev := range src.Events {
			if ev.Dir == fixture.RX {
				if b, ok := lastBanner(ev.Data); ok {
					s.Firmware = b
				}
			}
			if name, ok := xmlNoteName(ev); ok {
				s.XML = name
			}
		}
	}
	return out
}

// xmlNoteName returns the name of an "xml: " note event.
func xmlNoteName(ev fixture.Event) (string, bool) {
	if ev.Dir != fixture.Note {
		return "", false
	}
	return strings.CutPrefix(string(ev.Data), rawlog.XMLNotePrefix)
}

// SplitWindow returns the parts of [since, until) per session, cut at each
// session's Start, in order. The first part starts no earlier than the
// first session's Start. Empty parts are dropped.
func SplitWindow(ss []Session, since, until time.Time) []Window {
	var out []Window
	for i, s := range ss {
		lo, hi := since, until
		if s.Start.After(lo) {
			lo = s.Start
		}
		if i+1 < len(ss) && ss[i+1].Start.Before(hi) {
			hi = ss[i+1].Start
		}
		if lo.Before(hi) {
			out = append(out, Window{Since: lo, Until: hi, Session: i})
		}
	}
	return out
}
