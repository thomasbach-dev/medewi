package logview

import (
	"bytes"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/rawlog"
)

// Kind classifies a decoded entry.
type Kind string

// The Kind values name what an entry is.
const (
	KindCmd      Kind = "cmd"
	KindReport   Kind = "report"
	KindFrame    Kind = "frame"
	KindBanner   Kind = "banner"
	KindNotice   Kind = "notice"
	KindReply    Kind = "reply"
	KindNote     Kind = "note"
	KindFragment Kind = "fragment"
	// KindPartial is an unterminated rx tail at the end of the file.
	KindPartial  Kind = "partial"
	KindRedacted Kind = "redacted"
)

// maxNotice is the longest "[…]" line that counts as a notice.
const maxNotice = 32

// Entry is one decoded item of a log.
type Entry struct {
	Offset  time.Duration
	Wall    time.Time
	HasWall bool
	Dir     fixture.Dir
	Kind    Kind
	Raw     []byte
	Record  *leva.Record // set for a report that decoded
	Frame   *leva.Frame  // set for an intact display frame
	Glued   bool         // the piece came from a line SplitLine split
	Err     error        // report decode error; Raw still holds the line
	Count   int          // hidden lines, for KindRedacted
}

// Options controls Decode.
type Options struct {
	Spec      *leva.RecordSpec // nil: reports stay raw, without errors
	ShowDumps bool             // keep MCu/MCm dump lines
	Since     time.Time        // zero: unbounded; used only with wall time
	Until     time.Time        // zero: unbounded; used only with wall time
}

type decoder struct {
	src  Source
	opt  Options
	out  []Entry
	buf  []byte // rx bytes of the current, unterminated line
	bufT int64  // time of its first byte

	lastDump int64
	hasDump  bool
	hidden   int // pending folded hidden lines
	hiddenT  int64
}

// Decode turns the events of src into entries: rx bytes are reassembled
// into lines, split where a stream switch glued records together, classified
// and decoded. Dump lines are folded into KindRedacted entries unless
// opt.ShowDumps is set; they never appear in any Entry.Raw.
func Decode(src Source, opt Options) []Entry {
	d := &decoder{src: src, opt: opt}
	for _, ev := range src.Events {
		switch ev.Dir {
		case fixture.TX:
			d.tx(ev)
		case fixture.RX:
			d.rx(ev)
		case fixture.Note:
			d.emit(ev.T, Entry{Dir: fixture.Note, Kind: KindNote, Raw: append([]byte(nil), ev.Data...)})
		}
	}
	if len(d.buf) > 0 {
		if d.hide(d.bufT, d.buf) {
			d.addHidden(d.bufT)
		} else {
			d.emit(d.bufT, Entry{Dir: fixture.RX, Kind: KindPartial, Raw: append([]byte(nil), d.buf...)})
		}
	}
	d.flushHidden()
	return d.out
}

func (d *decoder) tx(ev fixture.Event) {
	raw := bytes.TrimRight(ev.Data, "\r\n")
	for _, c := range bytes.FieldsFunc(ev.Data, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if rawlog.IsDumpCommand(c) {
			d.lastDump, d.hasDump = ev.T, true
		}
	}
	d.emit(ev.T, Entry{Dir: fixture.TX, Kind: KindCmd, Raw: append([]byte(nil), raw...)})
}

func (d *decoder) rx(ev fixture.Event) {
	for _, b := range ev.Data {
		if len(d.buf) == 0 {
			d.bufT = ev.T
		}
		if b != '\n' {
			d.buf = append(d.buf, b)
			continue
		}
		line := bytes.TrimSuffix(d.buf, []byte("\r"))
		if len(line) > 0 {
			d.line(d.bufT, line)
		}
		d.buf = d.buf[:0]
	}
}

// hide applies the redaction rule to a whole line and updates the dump clock.
func (d *decoder) hide(t int64, line []byte) bool {
	if d.opt.ShowDumps {
		return false
	}
	if rawlog.IsDumpLine(line) {
		d.lastDump, d.hasDump = t, true
		return true
	}
	quiet := d.hasDump && time.Duration(t-d.lastDump)*time.Millisecond < rawlog.DefaultDumpQuiet
	if rawlog.IsStreamLine(line) {
		// A stream switch can glue a dump reply behind a cut-off record;
		// inside the quiet period only whole records may be shown.
		if quiet && !allWhole(line) {
			d.lastDump = t
			return true
		}
		return false
	}
	return quiet
}

// allWhole reports whether every piece of line is a whole record in its
// strict shape: an intact frame, a report whose trailing checksum verifies,
// an exact "$id: leva! (N.N)" banner or a short notice without '|' or ':'.
func allWhole(line []byte) bool {
	for _, p := range leva.SplitLine(line) {
		d := p.Data
		switch {
		case p.Fragment || len(d) == 0:
			return false
		case d[0] == '{':
			if !reportSumOK(d) {
				return false
			}
		case d[0] == '$':
			if !isWholeBanner(d) {
				return false
			}
		case d[0] == '[':
			if !isWholeNotice(d) {
				return false
			}
		default:
			if _, err := leva.ParseFrame(d); err != nil {
				return false
			}
		}
	}
	return true
}

func (d *decoder) line(t int64, line []byte) {
	if d.hide(t, line) {
		d.addHidden(t)
		return
	}
	pieces := leva.SplitLine(line)
	for _, p := range pieces {
		e := Entry{Dir: fixture.RX, Raw: append([]byte(nil), p.Data...), Glued: len(pieces) > 1}
		d.classify(&e, p)
		d.emit(t, e)
	}
}

func (d *decoder) classify(e *Entry, p leva.Piece) {
	switch {
	case p.Fragment:
		e.Kind = KindFragment
	case p.Data[0] == '{':
		e.Kind = KindReport
		if d.opt.Spec != nil {
			payload, err := leva.VerifyChecksum(p.Data)
			if err == nil {
				var rec leva.Record
				if rec, err = d.opt.Spec.Decode(payload); err == nil {
					e.Record = &rec
				}
			}
			e.Err = err
		}
	case isIntactFrame(e):
		e.Kind = KindFrame
	case bytes.HasPrefix(p.Data, []byte("$id:")):
		e.Kind = KindBanner
	case p.Data[0] == '[' && len(p.Data) <= maxNotice:
		e.Kind = KindNotice
	default:
		e.Kind = KindReply
	}
}

func isIntactFrame(e *Entry) bool {
	f, err := leva.ParseFrame(e.Raw)
	if err != nil {
		return false
	}
	e.Frame = &f
	return true
}

func (d *decoder) addHidden(t int64) {
	if d.hidden == 0 {
		d.hiddenT = t
	}
	d.hidden++
}

func (d *decoder) flushHidden() {
	if d.hidden == 0 {
		return
	}
	n, t := d.hidden, d.hiddenT
	d.hidden = 0
	d.put(t, Entry{Dir: fixture.RX, Kind: KindRedacted, Count: n})
}

// emit adds a shown entry, first flushing a pending redaction group.
func (d *decoder) emit(t int64, e Entry) {
	d.flushHidden()
	d.put(t, e)
}

// put stamps e with time t and appends it if it is inside the window.
func (d *decoder) put(t int64, e Entry) {
	e.Offset = time.Duration(t) * time.Millisecond
	e.HasWall = d.src.HasWall
	if d.src.HasWall {
		e.Wall = d.src.Start.Add(e.Offset)
		if !d.opt.Since.IsZero() && e.Wall.Before(d.opt.Since) {
			return
		}
		if !d.opt.Until.IsZero() && !e.Wall.Before(d.opt.Until) {
			return
		}
	}
	d.out = append(d.out, e)
}
