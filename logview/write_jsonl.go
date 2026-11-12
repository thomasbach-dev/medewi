package logview

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
)

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

// want reports whether optional column k is shown; idColumns, event and
// glued always are.
func (j *jsonWriter) want(k string) bool {
	return len(j.opt.Fields) == 0 || slices.Contains(j.opt.Fields, k)
}

func (j *jsonWriter) Write(r Row) error {
	e := r.Entry
	var o jsonObj
	o.raw("t", seconds(e.Offset))
	if e.HasWall {
		o.str("wall", utcWall(e.Wall))
	} else {
		o.raw("wall", "null")
	}
	o.str("src", srcRef(e.Path, e.Src))
	if r.Run != nil {
		o.raw("run", strconv.Itoa(r.Run.N))
	} else {
		o.raw("run", "null")
	}
	if r.RunT != nil {
		o.raw("run_t", seconds(*r.RunT))
	} else {
		o.raw("run_t", "null")
	}
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
	o.raw("glued", strconv.FormatBool(e.Glued)) // always, so keys never shift
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

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
