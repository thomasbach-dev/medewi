package logview

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// Format names an output format of levalog.
type Format string

// The output formats.
const (
	FormatText  Format = "text"
	FormatJSONL Format = "jsonl"
	FormatCSV   Format = "csv"
)

// ParseFormat returns the Format named s.
func ParseFormat(s string) (Format, error) {
	switch f := Format(s); f {
	case FormatText, FormatJSONL, FormatCSV:
		return f, nil
	}
	return "", fmt.Errorf("unknown format %q (want text, jsonl or csv)", s)
}

// WriterOptions controls a Writer.
type WriterOptions struct {
	// Fields limits and orders the columns by key; empty means all.
	Fields []string
	// RawValues prints report values as their raw substrings.
	RawValues bool
	// HideMasked omits masked report fields (text only).
	HideMasked bool
	// RunCols adds RUN and RUN_T after EVENT (text only).
	RunCols bool
	// Loc is the zone of wall times in text; nil means time.Local.
	Loc *time.Location
}

// Writer writes rows in one format. Close flushes; Warnings lists what was
// shown as masked, left empty or skipped, once per key or kind with a count.
type Writer interface {
	Write(Row) error
	Close() error
	Warnings() []string
}

// NewWriter returns a Writer of format f on w.
func NewWriter(w io.Writer, f Format, opt WriterOptions) Writer {
	if opt.Loc == nil {
		opt.Loc = time.Local
	}
	b := base{opt: opt, specs: map[*leva.RecordSpec]*specCols{}, bad: map[string]int{}, missing: map[string]int{}}
	switch f {
	case FormatJSONL:
		return &jsonWriter{base: b, w: w}
	case FormatCSV:
		return &csvWriter{base: b, cw: csv.NewWriter(w)}
	}
	return &textWriter{base: b, w: w}
}

// specCols are the XML keys of one spec.
type specCols struct {
	keys []string       // in output order: mode first, then spec order
	pos  map[string]int // key -> index in Record.Fields
	set  string         // the sorted keys, to compare key sets
}

// fval is one rendered report value.
type fval struct {
	masked bool   // all '?'
	bad    bool   // unparseable; s holds the raw text (text shows it, jsonl/csv mask it)
	num    bool   // s is a number literal
	s      string // the value as text
}

// base holds what all writers share: options, per-spec keys, warnings.
type base struct {
	opt     WriterOptions
	specs   map[*leva.RecordSpec]*specCols
	bad     map[string]int // unparseable values per key
	missing map[string]int // reports whose spec lacks a Fields key
	skipErr int            // CSV: reports that failed to decode
	skipRaw int            // CSV: reports without a spec
}

var medewiKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range MedewiColumns {
		m[c.Key] = true
	}
	return m
}()

func (b *base) specCols(spec *leva.RecordSpec) (*specCols, error) {
	if sc, ok := b.specs[spec]; ok {
		return sc, nil
	}
	cols, err := Columns(spec)
	if err != nil {
		return nil, err
	}
	sc := &specCols{pos: map[string]int{}}
	for i, c := range cols[len(MedewiColumns):] {
		sc.pos[c.Key] = i
		if c.Key == "mode" {
			sc.keys = append([]string{c.Key}, sc.keys...)
		} else {
			sc.keys = append(sc.keys, c.Key)
		}
	}
	sorted := slices.Clone(sc.keys)
	sort.Strings(sorted)
	sc.set = strings.Join(sorted, ",")
	b.specs[spec] = sc
	return sc, nil
}

// reportKeys are the report columns of a row: Fields minus medewi keys
// other than pump, or pump and all XML keys.
func (b *base) reportKeys(sc *specCols) []string {
	if len(b.opt.Fields) == 0 {
		return append([]string{"pump"}, sc.keys...)
	}
	var out []string
	for _, k := range b.opt.Fields {
		if k == "pump" || !medewiKeys[k] {
			out = append(out, k)
		}
	}
	return out
}

// field renders the report value of key k; ok is false when e's spec has
// no such key (counted as a warning).
func (b *base) field(e Entry, sc *specCols, k string) (fval, bool) {
	p, ok := sc.pos[k]
	if !ok || p >= len(e.Record.Fields) {
		b.missing[k]++
		return fval{}, false
	}
	f := e.Record.Fields[p]
	v := ParseField(f)
	switch {
	case v.Masked:
		return fval{masked: true}, true
	case b.opt.RawValues:
		return fval{s: latin1([]byte(f.Raw))}, true
	case v.Bad:
		b.bad[k]++
		return fval{bad: true, s: latin1([]byte(f.Raw))}, true
	case v.Num != nil:
		return fval{num: true, s: FormatNum(*v.Num)}, true
	}
	return fval{s: *v.Str}, true
}

func (b *base) Warnings() []string {
	var out []string
	for k, n := range b.bad {
		out = append(out, fmt.Sprintf("key %s: %d unparseable values (raw in text, null in jsonl, empty in csv)", k, n))
	}
	for k, n := range b.missing {
		out = append(out, fmt.Sprintf("key %s: %d reports whose XML lacks it", k, n))
	}
	if b.skipErr > 0 {
		out = append(out, fmt.Sprintf("%d reports that failed to decode skipped", b.skipErr))
	}
	if b.skipRaw > 0 {
		out = append(out, fmt.Sprintf("%d reports without XML skipped", b.skipRaw))
	}
	sort.Strings(out)
	return out
}

// pumpByte is report byte 2 when it is '*' or '-', else '?'.
func pumpByte(e Entry) byte {
	if len(e.Raw) > 2 && (e.Raw[2] == '*' || e.Raw[2] == '-') {
		return e.Raw[2]
	}
	return '?'
}

// roundMilli is d in seconds, rounded to milliseconds, never -0.
func roundMilli(d time.Duration) float64 {
	s := math.Round(d.Seconds()*1000) / 1000
	if s == 0 {
		s = 0 // no negative zero
	}
	return s
}

// seconds prints d in seconds, rounded to milliseconds.
func seconds(d time.Duration) string { return FormatNum(roundMilli(d)) }

const jsonWall = "2006-01-02T15:04:05.000Z"

func utcWall(t time.Time) string { return t.UTC().Format(jsonWall) }

// latin1 converts Latin-1 bytes to the equivalent runes (valid UTF-8).
func latin1(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

func srcRef(path string, line int) string { return path + ":" + strconv.Itoa(line) }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
