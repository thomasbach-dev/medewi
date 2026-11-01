package leva

import (
	"fmt"
	"strconv"
	"strings"
)

// Field is one decoded value of a report line.
type Field struct {
	Spec ValueSpec
	Raw  string // slice of the line with surrounding spaces trimmed; bytes are untouched
}

// Record is a decoded report line.
type Record struct {
	Fields []Field
}

// Field returns the field with the given export name.
func (r Record) Field(name string) (Field, bool) {
	for _, f := range r.Fields {
		if f.Spec.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// Float returns the named field parsed as a float.
func (r Record) Float(name string) (float64, bool) {
	f, ok := r.Field(name)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(f.Raw, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// Decode slices line (CR+LF already stripped) according to the spec.
//
// Decode does not verify checksums: callers must call VerifyChecksum first and
// pass the payload it returns, otherwise a trailing "~HH" can land inside the
// last field window.
//
// ASSUMPTION: the spec's At offsets are relative to the raw line as received,
// including the leading '{'. To be verified in the capture session.
func (s *RecordSpec) Decode(line []byte) (Record, error) {
	rec := Record{Fields: make([]Field, 0, len(s.Values))}
	for _, v := range s.Values {
		if v.At < 0 || v.Len < 0 || v.At+v.Len > len(line) {
			return Record{}, fmt.Errorf("line too short for value %d (%q): need %d bytes, have %d",
				v.Index, v.Name, v.At+v.Len, len(line))
		}
		raw := strings.Trim(string(line[v.At:v.At+v.Len]), " ")
		rec.Fields = append(rec.Fields, Field{Spec: v, Raw: raw})
	}
	return rec, nil
}

const hexDigits = "0123456789ABCDEF"

func hexVal(c byte) (byte, bool) {
	i := strings.IndexByte(hexDigits, c)
	if i < 0 {
		return 0, false
	}
	return byte(i), true
}

// SplitChecksum separates an optional trailing "~HH" (two uppercase hex
// digits) from the line. If absent, payload is the whole line.
func SplitChecksum(line []byte) (payload []byte, sum byte, present bool) {
	n := len(line)
	if n < 3 || line[n-3] != '~' {
		return line, 0, false
	}
	hi, ok1 := hexVal(line[n-2])
	lo, ok2 := hexVal(line[n-1])
	if !ok1 || !ok2 {
		return line, 0, false
	}
	return line[:n-3], hi<<4 | lo, true
}

// VerifyChecksum returns the payload of line, checking the trailing checksum
// if one is present. An absent checksum is valid, but a '~' in checksum
// position (third byte from the end) followed by anything other than two
// uppercase hex digits is a corrupt checksum and is rejected. Pass the
// returned payload, not line, to Decode, which never verifies checksums.
//
// ASSUMPTION: HH is the one's complement of the 8-bit sum of the payload
// bytes. To be verified in the capture session.
func VerifyChecksum(line []byte) (payload []byte, err error) {
	payload, sum, present := SplitChecksum(line)
	if !present {
		if n := len(line); n >= 3 && line[n-3] == '~' {
			return nil, fmt.Errorf("malformed checksum suffix %q", line[n-3:])
		}
		return payload, nil
	}
	var acc byte
	for _, b := range payload {
		acc += b
	}
	if want := ^acc; want != sum {
		return nil, fmt.Errorf("checksum mismatch: line has %02X, computed %02X", sum, want)
	}
	return payload, nil
}
