// Package backuptest holds shared synthetic fixtures for backup tests.
// All menu labels are invented.
package backuptest

import "strings"

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
