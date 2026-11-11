package logview

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const wallLayout = "2006-01-02 15:04:05.000"

// FormatLine writes one text line for e in the layout of levalog decode:
// OFFSET WALL DIR KIND TEXT, separated by single spaces, report fields as
// NAME=RAW. Wall times are shown in loc. New code uses NewWriter.
func FormatLine(w io.Writer, e Entry, loc *time.Location) error {
	_, err := fmt.Fprintf(w, "%s %s\n", linePrefix(e, loc), entryText(e))
	return err
}

// linePrefix is the T WALL DIR EVENT part of a text line.
func linePrefix(e Entry, loc *time.Location) string {
	wall := "-"
	if e.HasWall {
		wall = e.Wall.In(loc).Format(wallLayout)
	}
	kind := string(e.Kind)
	if e.Glued {
		kind += "+"
	}
	return fmt.Sprintf("%10.3f %s %-4s %-8s", e.Offset.Seconds(), wall, string(e.Dir), kind)
}

func entryText(e Entry) string {
	switch {
	case e.Err != nil:
		return strconv.Quote(string(e.Raw)) + "  # error: " + e.Err.Error()
	case e.Kind == KindRedacted:
		return redactedText(e)
	case e.Record != nil:
		parts := make([]string, 0, len(e.Record.Fields))
		for _, f := range e.Record.Fields {
			name := f.Spec.Name
			if name == "" {
				name = "#" + strconv.Itoa(f.Spec.Index)
			}
			parts = append(parts, name+"="+f.Raw)
		}
		return strings.Join(parts, "; ")
	case e.Frame != nil:
		bkl := 0
		if e.Frame.Backlight {
			bkl = 1
		}
		return fmt.Sprintf("bkl=%d key=%d layout=%d |%s|%s|%s|%s|", bkl, e.Frame.Key, e.Frame.Layout,
			e.Frame.Rows[0], e.Frame.Rows[1], e.Frame.Rows[2], e.Frame.Rows[3])
	}
	return rawText(e.Raw)
}

func redactedText(e Entry) string {
	return fmt.Sprintf("dump redacted (%d lines)", e.Count)
}

// rawText returns b as it is when every byte is printable ASCII or Latin-1
// (0xA0-0xFF, converted to runes so the output is valid UTF-8), else quoted.
func rawText(b []byte) string {
	for _, c := range b {
		if c < 0x20 || (c >= 0x7f && c < 0xa0) {
			return strconv.Quote(string(b))
		}
	}
	return latin1(b)
}

// latin1 converts Latin-1 bytes to the equivalent runes (valid UTF-8).
func latin1(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

// textWriter writes T WALL DIR EVENT [RUN RUN_T], then src, src_end and xml
// when Fields names them, then the entry's text; reports as key=value. A
// "# PATH" line precedes a change of source, "# xml: NAME" a change of XML.
type textWriter struct {
	base
	w       io.Writer
	started bool
	path    string
	xml     string
}

func (t *textWriter) Write(r Row) error {
	e := r.Entry
	var b strings.Builder
	if !t.started || e.Path != t.path {
		fmt.Fprintf(&b, "# %s\n", e.Path)
		t.path = e.Path
	}
	if e.XML != t.xml {
		fmt.Fprintf(&b, "# xml: %s\n", orDash(e.XML))
		t.xml = e.XML
	}
	t.started = true

	parts := []string{linePrefix(e, t.opt.Loc)}
	if t.opt.RunCols {
		run, runT := "-", "-"
		if r.Run != nil {
			run = strconv.Itoa(r.Run.N)
		}
		if r.RunT != nil {
			runT = fmt.Sprintf("%.3f", r.RunT.Seconds())
		}
		parts = append(parts, fmt.Sprintf("%-3s %6s", run, runT))
	}
	for _, k := range t.opt.Fields {
		switch k {
		case "src":
			parts = append(parts, "src="+srcRef(e.Path, e.Src))
		case "src_end":
			end := "-"
			if e.SrcEnd != e.Src {
				end = srcRef(e.Path, e.SrcEnd)
			}
			parts = append(parts, "src_end="+end)
		case "xml":
			parts = append(parts, "xml="+orDash(e.XML))
		}
	}
	if e.Kind == KindReport && e.Record != nil && e.Spec != nil {
		fields, err := t.reportText(e)
		if err != nil {
			return err
		}
		parts = append(parts, fields...)
	} else {
		parts = append(parts, entryText(e))
	}
	b.WriteString(strings.TrimRight(strings.Join(parts, " "), " "))
	b.WriteByte('\n')
	_, err := io.WriteString(t.w, b.String())
	return err
}

func (t *textWriter) reportText(e Entry) ([]string, error) {
	sc, err := t.specCols(e.Spec)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range t.reportKeys(sc) {
		if k == "pump" {
			p := pumpByte(e)
			if p == '?' && t.opt.HideMasked {
				continue
			}
			out = append(out, "pump="+string(p))
			continue
		}
		v, ok := t.field(e, sc, k)
		switch {
		case !ok || v.masked && t.opt.HideMasked:
		case v.masked:
			out = append(out, k+"=?")
		case v.num:
			out = append(out, k+"="+v.s)
		default:
			out = append(out, k+"="+textQuote(v.s))
		}
	}
	return out, nil
}

// textQuote quotes s when it would not read back as one key=value token.
func textQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"") {
		return strconv.Quote(s)
	}
	return s
}

func (t *textWriter) Close() error { return nil }

func srcRef(path string, line int) string { return path + ":" + strconv.Itoa(line) }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
