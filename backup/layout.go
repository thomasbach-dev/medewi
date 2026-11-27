package backup

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Entry is one known value in the EEPROM image (MCm).
type Entry struct {
	Offset     int
	Type       string // "u8", "u16", "u24"
	Scale      float64
	Sub        uint32 // value = (raw - Sub) * Scale; with Sub > 0, raw 0 means "not set"
	Name       string // our words
	MCuPath    string // "" if none
	Compare    bool   // MCu value is a number to check against the EEPROM
	Confidence string // "confirmed", "likely", "guess"
	Evidence   string // one line
}

// Layout is the set of known entries for one firmware version.
type Layout struct {
	Firmware string
	Entries  []Entry
}

// LayoutFor returns the known layout for a firmware version.
func LayoutFor(firmware string) (Layout, bool) {
	if firmware == "3.2" {
		return layout32, true
	}
	return Layout{}, false
}

// Width is the entry's size in bytes.
func (e Entry) Width() int {
	switch e.Type {
	case "u8":
		return 1
	case "u16":
		return 2
	case "u24":
		return 3
	}
	return 0
}

// Decode reads the entry little-endian from img. ok is false when the
// entry lies (partly) past the image. value is NaN when the entry has a
// Sub and raw lies below it: 0 means "not set", anything else is an
// encoding the layout does not know.
func (e Entry) Decode(img []byte) (raw uint32, value float64, ok bool) {
	w := e.Width()
	if w == 0 || e.Offset < 0 || e.Offset+w > len(img) {
		return 0, 0, false
	}
	for i := w - 1; i >= 0; i-- {
		raw = raw<<8 | uint32(img[e.Offset+i])
	}
	if raw < e.Sub {
		return raw, math.NaN(), true
	}
	return raw, float64(raw-e.Sub) * e.Scale, true
}

// leadingNumber parses the number at the start of s, ignoring any unit.
func leadingNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.' ||
		end == 0 && (s[end] == '-' || s[end] == '+')) {
		end++
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	return f, err == nil
}

// CrossCheck compares the MCu value of each entry that has an MCuPath
// with the value decoded from the EEPROM image. Entries missing from
// either side, or with a non-numeric MCu value, are skipped.
func CrossCheck(u *MCu, m *MCm, l Layout) []Problem {
	var probs []Problem
	for _, e := range l.Entries {
		if e.MCuPath == "" || !e.Compare {
			continue
		}
		s, ok := u.Lookup(e.MCuPath)
		if !ok {
			continue
		}
		want, ok := leadingNumber(s.Value)
		if !ok {
			continue
		}
		raw, got, ok := e.Decode(m.Image)
		if !ok || math.IsNaN(got) && raw != 0 {
			continue
		}
		if math.IsNaN(got) || math.Abs(want-got) > e.Scale/2+1e-9 {
			probs = append(probs, Problem{Line: s.Line, Kind: KindPair, Text: fmt.Sprintf(
				"%s: MCu says %s, EEPROM %#04x says %s", e.MCuPath, strings.TrimSpace(s.Value),
				e.Offset, orNotSet(e.Format(got)))})
		}
	}
	return probs
}

// Format prints value with the decimals implied by Scale (0.1 -> 1, 1 -> 0),
// and nothing for a value Decode could not give.
func (e Entry) Format(value float64) string {
	if math.IsNaN(value) {
		return ""
	}
	dec := 0
	for s := e.Scale; s > 0 && s < 1-1e-9 && dec < 6; s *= 10 {
		dec++
	}
	return strconv.FormatFloat(value, 'f', dec, 64)
}

func orNotSet(s string) string {
	if s == "" {
		return "it is not set"
	}
	return s
}
