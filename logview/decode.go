package logview

import (
	"bytes"
	"strings"
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

	Path   string // the source's path
	Source int    // index of the source in Loaded.Sources
	Src    int    // line of the event holding the first byte (see Source.Lines)
	SrcEnd int    // line of the event holding the last byte
	XML    string // session XML name in effect for this entry
	Spec   *leva.RecordSpec
}

// Options controls Decode.
type Options struct {
	Catalog   *Catalog // nil: reports stay raw, without errors
	XML       string   // session XML name in effect at the start of the file
	ShowDumps bool     // keep MCu/MCm dump lines
}

// Decoded is the result of Decode.
type Decoded struct {
	Entries  []Entry
	XML      string   // session XML name in effect at the end
	Problems []string // "PATH: PROBLEM", one per XML name that failed Lookup
}

type decoder struct {
	src  Source
	opt  Options
	out  []Entry
	buf  []byte // rx bytes of the current, unterminated line
	bufT int64  // time of its first byte

	cur            int // line of the event being processed
	bufSrc, bufEnd int // lines of the first and last byte of buf
	xml            string
	spec           *leva.RecordSpec
	problem        string // Lookup problem of xml, empty when it resolved
	failed         map[string]bool
	problems       []string

	lastDump int64
	hasDump  bool
	hidden   int // pending folded hidden lines
	hiddenT  int64
	hiddenS  int // first and last source line of the pending group
	hiddenE  int
}

// Decode turns the events of src into entries: rx bytes are reassembled
// into lines, split where a stream switch glued records together, classified
// and decoded. Dump lines are folded into KindRedacted entries unless
// opt.ShowDumps is set; they never appear in any Entry.Raw.
//
// The session XML starts as opt.XML and changes at every rx reply
// "XML=NAME" and every note starting with rawlog.XMLNotePrefix; reports are
// decoded with the spec opt.Catalog holds for the name in effect. A name the
// catalog cannot resolve leaves its reports raw and adds one Problem.
func Decode(src Source, opt Options) Decoded {
	d := &decoder{src: src, opt: opt, failed: map[string]bool{}}
	d.setXML(opt.XML)
	for i, ev := range src.Events {
		d.cur = 0
		if i < len(src.Lines) {
			d.cur = src.Lines[i]
		}
		switch ev.Dir {
		case fixture.TX:
			d.tx(ev)
		case fixture.RX:
			d.rx(ev)
		case fixture.Note:
			if name, ok := strings.CutPrefix(string(ev.Data), rawlog.XMLNotePrefix); ok {
				d.setXML(strings.TrimSpace(name))
			}
			d.emit(ev.T, Entry{Dir: fixture.Note, Kind: KindNote, Raw: append([]byte(nil), ev.Data...), Src: d.cur, SrcEnd: d.cur})
		}
	}
	if len(d.buf) > 0 {
		if d.hide(d.bufT, d.buf) {
			d.addHidden(d.bufT, d.bufSrc, d.bufEnd)
		} else {
			d.emit(d.bufT, Entry{Dir: fixture.RX, Kind: KindPartial, Raw: append([]byte(nil), d.buf...), Src: d.bufSrc, SrcEnd: d.bufEnd})
		}
	}
	d.flushHidden()
	return Decoded{Entries: d.out, XML: d.xml, Problems: d.problems}
}

// setXML makes name the session XML and resolves its spec.
func (d *decoder) setXML(name string) {
	d.xml = name
	d.spec, d.problem = d.opt.Catalog.Lookup(name)
}

// reportProblem records the pending Lookup problem once per XML name.
func (d *decoder) reportProblem() {
	if d.problem == "" || d.failed[d.xml] {
		return
	}
	d.failed[d.xml] = true
	d.problems = append(d.problems, d.src.Path+": "+d.problem)
}

func (d *decoder) tx(ev fixture.Event) {
	raw := bytes.TrimRight(ev.Data, "\r\n")
	for _, c := range bytes.FieldsFunc(ev.Data, func(r rune) bool { return r == '\r' || r == '\n' }) {
		if rawlog.IsDumpCommand(c) {
			d.lastDump, d.hasDump = ev.T, true
		}
	}
	d.emit(ev.T, Entry{Dir: fixture.TX, Kind: KindCmd, Raw: append([]byte(nil), raw...), Src: d.cur, SrcEnd: d.cur})
}

func (d *decoder) rx(ev fixture.Event) {
	for _, b := range ev.Data {
		if len(d.buf) == 0 {
			d.bufT, d.bufSrc, d.bufEnd = ev.T, d.cur, d.cur
		}
		if b != '\n' {
			d.buf = append(d.buf, b)
			if b != '\r' {
				d.bufEnd = d.cur
			}
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
		d.addHidden(t, d.bufSrc, d.bufEnd)
		return
	}
	pieces := leva.SplitLine(line)
	for _, p := range pieces {
		e := Entry{Dir: fixture.RX, Raw: append([]byte(nil), p.Data...), Glued: len(pieces) > 1, Src: d.bufSrc, SrcEnd: d.bufEnd}
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
		d.reportProblem()
		if d.spec != nil {
			payload, err := leva.VerifyChecksum(p.Data)
			if err == nil {
				var rec leva.Record
				if rec, err = d.spec.Decode(payload); err == nil {
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
		if name, ok := leva.XMLName([]string{string(p.Data)}); ok {
			d.setXML(name)
		}
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

func (d *decoder) addHidden(t int64, first, last int) {
	if d.hidden == 0 {
		d.hiddenT, d.hiddenS = t, first
	}
	d.hiddenE = last
	d.hidden++
}

func (d *decoder) flushHidden() {
	if d.hidden == 0 {
		return
	}
	n, t := d.hidden, d.hiddenT
	d.hidden = 0
	d.put(t, Entry{Dir: fixture.RX, Kind: KindRedacted, Count: n, Src: d.hiddenS, SrcEnd: d.hiddenE})
}

// emit adds a shown entry, first flushing a pending redaction group.
func (d *decoder) emit(t int64, e Entry) {
	d.flushHidden()
	d.put(t, e)
}

// put stamps e with time t, source and session XML and appends it.
func (d *decoder) put(t int64, e Entry) {
	e.Offset = time.Duration(t) * time.Millisecond
	e.HasWall = d.src.HasWall
	if d.src.HasWall {
		e.Wall = d.src.Start.Add(e.Offset)
	}
	e.Path, e.XML, e.Spec = d.src.Path, d.xml, d.spec
	d.out = append(d.out, e)
}
