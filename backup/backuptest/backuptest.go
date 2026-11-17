// Package backuptest holds shared synthetic fixtures for backup tests.
// All menu labels are invented.
package backuptest

import (
	"fmt"
	"strings"
)

// Rec returns an Intel HEX data record (type 00) for data at addr, with
// its checksum.
func Rec(addr int, data []byte) string {
	b := []byte{byte(len(data)), byte(addr >> 8), byte(addr), 0}
	b = append(b, data...)
	var sum byte
	for _, c := range b {
		sum += c
	}
	return fmt.Sprintf(":%X%02X", b, -sum)
}

// ValidMCm returns a small invented MCm dump: two 16-byte records from
// 0x0000 and the firmware-form end record.
func ValidMCm() []string {
	a := make([]byte, 16)
	b := make([]byte, 16)
	for i := range a {
		a[i], b[i] = byte(i+1), byte(0x80+i)
	}
	return []string{Rec(0, a), Rec(16, b), ":00000001F"}
}

// Cell returns "| " x depth + s right-padded to 15 bytes.
func Cell(depth int, s string) string {
	for len(s) < 15 {
		s += " "
	}
	return strings.Repeat("| ", depth) + s
}

// ValidMCu returns a small invented MCu dump that passes every check. It
// ends with Cancel and contains a Latin-1 degree sign (0xB0).
func ValidMCu() []string {
	return []string{
		Cell(0, "Alpha..."),
		Cell(1, "Gain     1.0%\xb0"),
		Cell(1, "Ratio 1 :   5.0"),
		Cell(1, "Duration00m:03s"),
		Cell(1, "Beta..."),
		Cell(2, "Reset"),
		Cell(2, "Reset"),
		Cell(0, "Gamma        -"),
		Cell(0, "Cancel"),
	}
}
