package logview

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
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

// jsonObj builds one JSON object with keys in insertion order.
type jsonObj struct{ b bytes.Buffer }

func (o *jsonObj) key(k string) {
	if o.b.Len() == 0 {
		o.b.WriteByte('{')
	} else {
		o.b.WriteByte(',')
	}
	o.b.WriteString(jsonString(k))
	o.b.WriteByte(':')
}

func (o *jsonObj) raw(k, v string) { o.key(k); o.b.WriteString(v) }
func (o *jsonObj) str(k, s string) { o.raw(k, jsonString(s)) }

// strOrNull adds s, or null when s is empty.
func (o *jsonObj) strOrNull(k, s string) {
	if s == "" {
		o.raw(k, "null")
	} else {
		o.str(k, s)
	}
}

func (o *jsonObj) line() []byte {
	if o.b.Len() == 0 {
		o.b.WriteByte('{')
	}
	o.b.WriteString("}\n")
	return o.b.Bytes()
}

// jsonString encodes s as a JSON string without HTML escaping.
func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

type jsonWriter struct {
	base
	w io.Writer
}

func (j *jsonWriter) want(k string) bool {
	return len(j.opt.Fields) == 0 || k == "src" || k == "event" || slices.Contains(j.opt.Fields, k)
}

func (j *jsonWriter) Write(r Row) error {
	e := r.Entry
	var o jsonObj
	if j.want("t") {
		o.raw("t", seconds(e.Offset))
	}
	if j.want("wall") {
		if e.HasWall {
			o.str("wall", utcWall(e.Wall))
		} else {
			o.raw("wall", "null")
		}
	}
	o.str("src", srcRef(e.Path, e.Src))
	if j.want("src_end") {
		if e.SrcEnd != e.Src {
			o.str("src_end", srcRef(e.Path, e.SrcEnd))
		} else {
			o.raw("src_end", "null")
		}
	}
	if j.want("dir") {
		o.str("dir", string(e.Dir))
	}
	o.str("event", string(e.Kind))
	if len(j.opt.Fields) == 0 || e.Glued {
		o.raw("glued", strconv.FormatBool(e.Glued))
	}
	if j.want("run") {
		if r.Run != nil {
			o.raw("run", strconv.Itoa(r.Run.N))
		} else {
			o.raw("run", "null")
		}
	}
	if j.want("run_t") {
		if r.RunT != nil {
			o.raw("run_t", seconds(*r.RunT))
		} else {
			o.raw("run_t", "null")
		}
	}
	if j.want("xml") {
		o.strOrNull("xml", e.XML)
	}
	switch {
	case e.Kind == KindReport && e.Record != nil && e.Spec != nil:
		sc, err := j.specCols(e.Spec)
		if err != nil {
			return err
		}
		for _, k := range j.reportKeys(sc) {
			if k == "pump" {
				switch pumpByte(e) {
				case '*':
					o.raw(k, "true")
				case '-':
					o.raw(k, "false")
				default:
					o.raw(k, "null")
				}
				continue
			}
			v, ok := j.field(e, sc, k)
			switch {
			case !ok || v.masked || v.bad:
				o.raw(k, "null")
			case v.num:
				o.raw(k, v.s)
			default:
				o.str(k, v.s)
			}
		}
	case e.Frame != nil:
		o.raw("bkl", strconv.FormatBool(e.Frame.Backlight))
		o.raw("key", frameJSON(e.Frame.Key))
		o.raw("layout", frameJSON(e.Frame.Layout))
		rows, _ := json.Marshal(e.Frame.Rows) // [4]string always encodes
		o.raw("rows", string(rows))
	case e.Kind == KindRedacted:
		o.str("text", redactedText(e))
	default:
		o.str("text", latin1(e.Raw))
		if e.Err != nil {
			o.str("error", e.Err.Error())
		}
	}
	_, err := j.w.Write(o.line())
	return err
}

// frameJSON is a frame's key or layout byte: a digit as a number, any
// other byte as a one-character string.
func frameJSON(c byte) string {
	if c >= '0' && c <= '9' {
		return string(c)
	}
	return jsonString(latin1([]byte{c}))
}

func (j *jsonWriter) Close() error { return nil }

// csvMedewi are the medewi columns CSV prints before the report fields.
var csvMedewi = []string{"t", "wall", "src", "src_end", "run", "run_t", "pump"}

type csvWriter struct {
	base
	cw     *csv.Writer
	header []string
	set    string // key set of the header's spec, without Fields
}

func (c *csvWriter) Write(r Row) error {
	e := r.Entry
	if e.Kind != KindReport {
		return nil
	}
	if e.Record == nil || e.Spec == nil {
		if e.Err != nil {
			c.skipErr++
		} else {
			c.skipRaw++
		}
		return nil
	}
	sc, err := c.specCols(e.Spec)
	if err != nil {
		return err
	}
	if c.header == nil {
		c.header = c.opt.Fields
		if len(c.header) == 0 {
			c.header = append(slices.Clone(csvMedewi), sc.keys...)
			c.set = sc.set
		}
		if err := c.cw.Write(c.header); err != nil {
			return err
		}
	} else if len(c.opt.Fields) == 0 && sc.set != c.set {
		return fmt.Errorf("report at %s: XML %s has other fields than the first report's; use --fields",
			srcRef(e.Path, e.Src), orDash(e.XML))
	}
	rec := make([]string, len(c.header))
	for i, k := range c.header {
		rec[i] = c.cell(r, sc, k)
	}
	return c.cw.Write(rec)
}

func (c *csvWriter) cell(r Row, sc *specCols, k string) string {
	e := r.Entry
	switch k {
	case "t":
		return seconds(e.Offset)
	case "wall":
		if e.HasWall {
			return utcWall(e.Wall)
		}
		return ""
	case "src":
		return srcRef(e.Path, e.Src)
	case "src_end":
		if e.SrcEnd != e.Src {
			return srcRef(e.Path, e.SrcEnd)
		}
		return ""
	case "dir":
		return string(e.Dir)
	case "event":
		return string(e.Kind)
	case "run":
		if r.Run != nil {
			return strconv.Itoa(r.Run.N)
		}
		return ""
	case "run_t":
		if r.RunT != nil {
			return seconds(*r.RunT)
		}
		return ""
	case "pump":
		switch pumpByte(e) {
		case '*':
			return "1"
		case '-':
			return "0"
		}
		return ""
	case "xml":
		return e.XML
	}
	v, ok := c.field(e, sc, k)
	if !ok || v.masked || v.bad {
		return ""
	}
	return v.s
}

func (c *csvWriter) Close() error {
	if c.header == nil {
		h := c.opt.Fields
		if len(h) == 0 {
			h = csvMedewi
		}
		if err := c.cw.Write(h); err != nil {
			return err
		}
	}
	c.cw.Flush()
	return c.cw.Error()
}

// medewiNames and medewiUnits describe the medewi columns in WriteColumns.
var (
	medewiNames = map[string]string{
		"t": "(offset)", "wall": "(wall clock)", "src": "(source line)", "src_end": "(source end)",
		"dir": "(direction)", "event": "(event kind)", "run": "(run number)", "run_t": "(run time)",
		"pump": "(pump marker)", "xml": "(xml name)",
	}
	medewiUnits = map[string]string{"t": "s", "run_t": "s"}
)

// WriteColumns lists cols of the XML named xml: KEY NAME UNIT FACTOR AT LEN
// SOURCE (text, after a "# xml: NAME" line), or the same lowercase keys plus
// xml per object or row (jsonl, csv).
func WriteColumns(w io.Writer, f Format, xml string, cols []Column) error {
	type colRow struct{ key, name, unit, factor, at, len, source string }
	rows := make([]colRow, 0, len(cols))
	for _, c := range cols {
		r := colRow{key: c.Key, name: c.Name, unit: c.Unit, source: c.Source}
		if c.Source == "medewi" {
			r.name, r.unit = medewiNames[c.Key], medewiUnits[c.Key]
		}
		factor := c.Factor
		if factor == 0 {
			factor = 1
		}
		r.factor = FormatNum(factor)
		if c.At >= 0 {
			r.at = strconv.Itoa(c.At)
		}
		if c.Len >= 0 {
			r.len = strconv.Itoa(c.Len)
		}
		rows = append(rows, r)
	}
	switch f {
	case FormatJSONL:
		for _, r := range rows {
			var o jsonObj
			o.str("key", r.key)
			o.strOrNull("name", r.name)
			o.strOrNull("unit", r.unit)
			o.raw("factor", r.factor)
			o.raw("at", orNull(r.at))
			o.raw("len", orNull(r.len))
			o.str("source", r.source)
			o.strOrNull("xml", xml)
			if _, err := w.Write(o.line()); err != nil {
				return err
			}
		}
		return nil
	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"key", "name", "unit", "factor", "at", "len", "source", "xml"})
		for _, r := range rows {
			_ = cw.Write([]string{r.key, r.name, r.unit, r.factor, r.at, r.len, r.source, xml})
		}
		cw.Flush()
		return cw.Error()
	}
	if xml != "" {
		if _, err := fmt.Fprintf(w, "# xml: %s\n", xml); err != nil {
			return err
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KEY\tNAME\tUNIT\tFACTOR\tAT\tLEN\tSOURCE")
	for _, r := range rows {
		factor := r.factor
		if factor == "1" {
			factor = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.key, orDash(r.name), orDash(r.unit), factor, orDash(r.at), orDash(r.len), r.source)
	}
	return tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}

// WriteRuns lists runs: RUN KIND START T DURATION PEAK_BAR VOLUME_ML
// TRUNCATED XML SRC, all of the run's first sample in l. START is the wall
// time in loc (text) or UTC (jsonl, csv), absent for captures.
func WriteRuns(w io.Writer, f Format, l Loaded, runs []Run, loc *time.Location) error {
	if loc == nil {
		loc = time.Local
	}
	header := []string{"run", "kind", "start", "t", "duration", "peak_bar", "volume_ml", "truncated", "xml", "src"}
	var tw *tabwriter.Writer
	var cw *csv.Writer
	switch f {
	case FormatJSONL:
	case FormatCSV:
		cw = csv.NewWriter(w)
		_ = cw.Write(header)
	default:
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, strings.ToUpper(strings.Join(header, "\t")))
	}
	for _, r := range runs {
		e := l.Entries[r.First]
		start, peak := "", ""
		if e.HasWall {
			start = utcWall(e.Wall)
			if tw != nil {
				start = e.Wall.In(loc).Format(wallLayout)
			}
		}
		if r.PeakBar != nil {
			peak = FormatNum(*r.PeakBar)
		}
		n, vol := strconv.Itoa(r.N), FormatNum(math.Round(r.VolumeML*1e6)/1e6)
		src := srcRef(e.Path, e.Src)
		switch {
		case tw != nil:
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%.3f\t%s\t%s\t%s\t%s\t%s\t%s\n",
				n, r.Kind, orDash(start), e.Offset.Seconds(), seconds(r.Duration), orDash(peak), vol,
				yesNo(r.Truncated), orDash(r.XML), src)
		case cw != nil:
			_ = cw.Write([]string{
				n, string(r.Kind), start, seconds(e.Offset), seconds(r.Duration), peak, vol,
				strconv.FormatBool(r.Truncated), r.XML, src,
			})
		default:
			var o jsonObj
			o.raw("run", n)
			o.str("kind", string(r.Kind))
			o.strOrNull("start", start)
			o.raw("t", seconds(e.Offset))
			o.raw("duration", seconds(r.Duration))
			o.raw("peak_bar", orNull(peak))
			o.raw("volume_ml", vol)
			o.raw("truncated", strconv.FormatBool(r.Truncated))
			o.strOrNull("xml", r.XML)
			o.str("src", src)
			if _, err := w.Write(o.line()); err != nil {
				return err
			}
		}
	}
	if tw != nil {
		return tw.Flush()
	}
	if cw != nil {
		cw.Flush()
		return cw.Error()
	}
	return nil
}
