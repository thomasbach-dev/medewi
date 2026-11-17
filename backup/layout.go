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

var layout32 = Layout{Firmware: "3.2", Entries: []Entry{
	{0x0022, "u8", 1, "run counter?", "", false, "guess", "+5 = runs 5-9"},
	{0x0027, "u24", 1, "flow-meter ticks, lifetime?", "", false, "likely", "+1006 = sum of field_31 over runs 5-9"},
	{0x002F, "u24", 1, "flow-meter ticks, copy or trip", "", false, "likely", "same values as 0x0027"},
	{0x0033, "u16", 1, "flow-meter ticks, resettable?", "", false, "likely", "+1006"},
	{0x0037, "u8", 1, "shot counter?", "", false, "guess", "+1 = one shot"},
	{0x0173, "u16", 0.1, "PID 1 Kc (%/°)", "PID 1/Kc", true, "confirmed", "matches MCu 8.9 -> 8.8"},
	{0x0175, "u16", 0.1, "PID 1 Ti (s)", "PID 1/Ti", true, "confirmed", "67.2 -> 72.3"},
	{0x0177, "u16", 0.1, "PID 1 Td (s)", "PID 1/Td", true, "confirmed", "16.8 -> 18.1"},
	{0x0179, "u16", 0.1, "PID 1 windup limit (%)", "PID 1/WU limit", true, "likely", "200 = MCu 20.0 %, unchanged"},
	{0x04F9, "u8", 1, "Profiles: Use (slot)", "Profiles/Use", false, "likely", "1 -> 3, MCu SOAK 1.0b -> DARK"},
}}

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
// entry lies (partly) past the image.
func (e Entry) Decode(img []byte) (raw uint32, value float64, ok bool) {
	w := e.Width()
	if w == 0 || e.Offset < 0 || e.Offset+w > len(img) {
		return 0, 0, false
	}
	for i := w - 1; i >= 0; i-- {
		raw = raw<<8 | uint32(img[e.Offset+i])
	}
	return raw, float64(raw) * e.Scale, true
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
		_, got, ok := e.Decode(m.Image)
		if !ok {
			continue
		}
		if math.Abs(want-got) > e.Scale/2+1e-9 {
			probs = append(probs, Problem{Line: s.Line, Kind: KindPair, Text: fmt.Sprintf(
				"%s: MCu says %s, EEPROM %#04x says %s", e.MCuPath, strings.TrimSpace(s.Value),
				e.Offset, e.Format(got))})
		}
	}
	return probs
}

// Format prints value with the decimals implied by Scale (0.1 -> 1, 1 -> 0).
func (e Entry) Format(value float64) string {
	dec := 0
	for s := e.Scale; s > 0 && s < 1-1e-9 && dec < 6; s *= 10 {
		dec++
	}
	return strconv.FormatFloat(value, 'f', dec, 64)
}
