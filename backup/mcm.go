package backup

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// MCm end records: the Intel HEX end-of-file record, and the form leva! 3.2
// actually sends, one hex digit short of it.
const (
	hexEOF         = ":00000001FF"
	hexEOFFirmware = ":00000001F"
)

// MCm is a parsed firmware dump.
type MCm struct {
	// Image covers address 0 to the end of the last good contiguous record.
	Image    []byte
	Records  int // good contiguous data records
	Problems []Problem
}

// ParseMCm parses and checks the lines of an MCm dump: data records
// contiguous from 0x0000 with valid checksums, and an end record as the
// last line and nowhere else. It never fails; damage is in Problems.
func ParseMCm(lines []string) *MCm {
	m := &MCm{}
	problem := func(line int, format string, args ...any) {
		m.Problems = append(m.Problems, Problem{Line: line, Kind: KindMCm, Text: fmt.Sprintf(format, args...)})
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		problem(0, "empty dump")
		return m
	}
	var (
		next   int
		broken bool // a data problem was found: the image stops growing
		sawEnd bool
	)
	for i, raw := range lines {
		n := i + 1
		l := strings.TrimSpace(raw)
		if l == hexEOF || l == hexEOFFirmware {
			sawEnd = true
			if i != len(lines)-1 {
				problem(n, "end record is not last")
			}
			continue
		}
		addr, data, err := dataRecord(l)
		switch {
		case err != "":
			problem(n, "%s", err)
			broken = true
		case addr != next:
			problem(n, "record at address 0x%04X, want 0x%04X", addr, next)
			broken = true
			next = addr + len(data)
		default:
			next = addr + len(data)
			if !broken && !sawEnd {
				m.Image = append(m.Image, data...)
				m.Records++
			}
		}
	}
	if !sawEnd {
		problem(len(lines), "end record missing")
	}
	if m.Records == 0 && len(m.Problems) == 0 {
		problem(0, "no data records")
	}
	return m
}

// dataRecord parses an Intel HEX data record (type 00). err is empty on
// success, else says what is wrong.
func dataRecord(l string) (addr int, data []byte, err string) {
	if len(l) < 11 || l[0] != ':' {
		return 0, nil, "not a data record"
	}
	b, herr := hex.DecodeString(l[1:])
	if herr != nil || len(b) != 5+int(b[0]) || b[3] != 0x00 {
		return 0, nil, "not a data record"
	}
	var sum byte
	for _, c := range b {
		sum += c
	}
	if sum != 0 {
		return 0, nil, "bad record checksum"
	}
	return int(b[1])<<8 | int(b[2]), b[4 : 4+int(b[0])], ""
}
