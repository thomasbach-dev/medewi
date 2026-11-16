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
	var b SessionBuilder
	for i, src := range srcs {
		b.Source(src.Info(i))
		for _, ev := range src.Events {
			if ev.Dir == fixture.RX {
				if line, ok := lastBanner(ev.Data); ok {
					b.banner(line)
				}
			}
			if name, ok := xmlNoteName(ev); ok {
				b.xml(name)
			}
		}
	}
	return b.Sessions()
}

// SessionBuilder groups raw logs into sessions one file at a time, as
// Sessions does, without holding their events: feed it each file's
// SourceInfo, then that file's decoded entries (Stream.OnSource and
// OnEntry). The zero value is ready to use.
type SessionBuilder struct {
	out    []Session
	noWall bool // a source without wall time (a capture) was fed
}

// Source starts the file si; files come in Start order.
func (b *SessionBuilder) Source(si SourceInfo) {
	if !si.HasWall {
		b.noWall = true
		return
	}
	if si.SessionStart || len(b.out) == 0 {
		b.out = append(b.out, Session{Start: si.Start, StartKnown: si.SessionStart})
	}
	s := &b.out[len(b.out)-1]
	s.Files = append(s.Files, filepath.Base(si.Path))
}

// Entry takes a decoded entry of the current file: a banner sets the
// session's firmware, an xml note its XML.
func (b *SessionBuilder) Entry(e *Entry) {
	switch e.Kind {
	case KindBanner:
		b.banner(string(e.Raw))
	case KindNote:
		if name, ok := strings.CutPrefix(string(e.Raw), rawlog.XMLNotePrefix); ok {
			b.xml(name)
		}
	}
}

func (b *SessionBuilder) banner(line string) {
	if len(b.out) > 0 && !b.noWall {
		b.out[len(b.out)-1].Firmware = line
	}
}

func (b *SessionBuilder) xml(name string) {
	if len(b.out) > 0 && !b.noWall {
		b.out[len(b.out)-1].XML = name
	}
}

// Sessions returns the sessions so far; nil once a source without wall
// time was fed, as such an input has no sessions.
func (b *SessionBuilder) Sessions() []Session {
	if b.noWall {
		return nil
	}
	return b.out
}

// xmlNoteName returns the name of an "xml: " note event.
func xmlNoteName(ev fixture.Event) (string, bool) {
	if ev.Dir != fixture.Note {
		return "", false
	}
	return strings.CutPrefix(string(ev.Data), rawlog.XMLNotePrefix)
}

// SplitWindow returns the parts of [since, until) per session, cut at each
// session's Start, in order; ss must be sorted by Start, as Sessions and
// SessionBuilder return them. The first part starts no earlier than the
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
