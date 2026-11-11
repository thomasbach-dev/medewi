package logview

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const wallLayout = "2006-01-02 15:04:05.000"

// Format writes one text line for e: OFFSET WALL DIR KIND TEXT, separated by
// single spaces. Wall times are shown in loc.
func Format(w io.Writer, e Entry, loc *time.Location) error {
	wall := "-"
	if e.HasWall {
		wall = e.Wall.In(loc).Format(wallLayout)
	}
	kind := string(e.Kind)
	if e.Glued {
		kind += "+"
	}
	_, err := fmt.Fprintf(w, "%10.3f %s %-4s %-8s %s\n",
		e.Offset.Seconds(), wall, string(e.Dir), kind, entryText(e))
	return err
}

func entryText(e Entry) string {
	switch {
	case e.Err != nil:
		return strconv.Quote(string(e.Raw)) + "  # error: " + e.Err.Error()
	case e.Kind == KindRedacted:
		return fmt.Sprintf("dump redacted (%d lines)", e.Count)
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
