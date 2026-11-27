package backup

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Checksum describes the settings checksum of a layout: the byte at
// Offset is an XOR over the settings, and [From, To) is the part of the
// image known to be covered. The zero Checksum means none is known.
type Checksum struct{ Offset, From, To int }

// EncodeMCm writes img the way the machine sends an MCm dump: Intel HEX
// data records of 16 bytes from address 0, upper-case, one per line, and
// the firmware's end record (one digit short of the Intel HEX one), so an
// unedited image gives the backup file back byte for byte. With
// standardEOF the end record is the Intel HEX ":00000001FF".
func EncodeMCm(img []byte, standardEOF bool) []byte {
	var b bytes.Buffer
	for addr := 0; addr < len(img); addr += 16 {
		data := img[addr:min(addr+16, len(img))]
		rec := append([]byte{byte(len(data)), byte(addr >> 8), byte(addr), 0}, data...)
		var sum byte
		for _, c := range rec {
			sum += c
		}
		fmt.Fprintf(&b, ":%X%02X\n", rec, -sum)
	}
	if standardEOF {
		b.WriteString(hexEOF + "\n")
	} else {
		b.WriteString(hexEOFFirmware + "\n")
	}
	return b.Bytes()
}

// Encode turns a value as the MCu prints it (a number, a unit after it
// is ignored) into the entry's raw integer. "-" is "not set" (raw 0) for
// an entry with a Sub. The value must be a multiple of Scale, not
// negative, and fit the entry's width.
func (e Entry) Encode(value string) (uint32, error) {
	w := e.Width()
	if w == 0 {
		return 0, fmt.Errorf("unknown type %q", e.Type)
	}
	value = strings.TrimSpace(value)
	if value == "-" {
		if e.Sub == 0 {
			return 0, errors.New(`"-" (not set) is not known for this entry`)
		}
		return 0, nil
	}
	v, ok := leadingNumber(value)
	if !ok {
		return 0, fmt.Errorf("%q is not a number", value)
	}
	if v < 0 {
		return 0, fmt.Errorf("%s is negative", value)
	}
	steps := math.Round(v / e.Scale)
	if math.Abs(steps*e.Scale-v) > 1e-9 {
		return 0, fmt.Errorf("%s is not a multiple of %s", value, e.Format(e.Scale))
	}
	if limit := float64(uint64(1)<<(8*w) - 1); steps+float64(e.Sub) > limit {
		return 0, fmt.Errorf("%s does not fit %s (at most %s)", value, e.Type,
			e.Format((limit-float64(e.Sub))*e.Scale))
	}
	return uint32(steps) + e.Sub, nil
}

// Set writes value into the entry of img that has the MCu path, and
// XORs the change into the checksum byte. It refuses, leaving img as it
// was: a path without an entry, an entry of confidence "guess", a value
// Encode rejects, and a change to a byte outside the region the
// checksum is known to cover (the state block lies outside it).
func (l Layout) Set(img []byte, path, value string) (Entry, error) {
	var e Entry
	found := false
	for _, c := range l.At(img) {
		if path != "" && c.MCuPath == path {
			e, found = c, true
			break
		}
	}
	if !found {
		return Entry{}, fmt.Errorf("no EEPROM entry for the path %q", path)
	}
	fail := func(format string, a ...any) (Entry, error) {
		return Entry{}, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, a...))
	}
	if e.Confidence == "guess" {
		return fail("the entry at %#04x is a guess", e.Offset)
	}
	raw, err := e.Encode(value)
	if err != nil {
		return fail("%v", err)
	}
	if e.Offset+e.Width() > len(img) {
		return fail("the entry at %#04x lies past the image", e.Offset)
	}
	cs := l.Checksum
	var sum byte
	for i := 0; i < e.Width(); i++ {
		off, b := e.Offset+i, byte(raw>>(8*i))
		if img[off] == b {
			continue
		}
		if off < cs.From || off >= cs.To || cs.Offset >= len(img) {
			return fail("byte %#04x is outside the region the checksum is known to cover", off)
		}
		sum ^= img[off] ^ b
	}
	for i := 0; i < e.Width(); i++ {
		img[e.Offset+i] = byte(raw >> (8 * i))
	}
	if sum != 0 {
		img[cs.Offset] ^= sum
	}
	return e, nil
}
