package backup_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/backup"
	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func rec(addr int, data []byte) string { return backuptest.Rec(addr, data) }

func bytes16(start byte) []byte {
	b := make([]byte, 16)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

func TestParseMCmGood(t *testing.T) {
	r0, r1 := rec(0, bytes16(1)), rec(16, bytes16(0x80))
	for name, lines := range map[string][]string{
		"firmware EOF": {r0, r1, ":00000001F"},
		"standard EOF": {r0, r1, ":00000001FF"},
		"valid helper": backuptest.ValidMCm(),
		"crlf+blank": backup.SplitLines([]byte(
			r0 + "\r\n" + r1 + "\r\n:00000001F\r\n\r\n")),
	} {
		t.Run(name, func(t *testing.T) {
			m := backup.ParseMCm(lines)
			if len(m.Problems) != 0 || m.Records != 2 || len(m.Image) != 32 {
				t.Fatalf("records=%d image=%d problems=%+v", m.Records, len(m.Image), m.Problems)
			}
		})
	}
}

func TestParseMCmLittleEndianImage(t *testing.T) {
	m := backup.ParseMCm([]string{rec(0, []byte{0x11, 0x22}), ":00000001F"})
	if len(m.Image) != 2 || m.Image[0x01] != 0x22 {
		t.Errorf("image = %x", m.Image)
	}
}

func TestParseMCmProblems(t *testing.T) {
	r0, r1 := rec(0, bytes16(1)), rec(16, bytes16(2))
	badSum := r1[:len(r1)-2] + "00"
	if badSum == r1 {
		badSum = r1[:len(r1)-2] + "01"
	}
	tests := []struct {
		name  string
		lines []string
		line  int
		want  string
	}{
		{"bad checksum", []string{r0, badSum, ":00000001F"}, 2, "checksum"},
		{"gap", []string{r0, rec(0x20, bytes16(2)), ":00000001F"}, 2, "address"},
		{"end missing", []string{r0, r1}, 2, "end record"},
		{"end not last", []string{r0, ":00000001F", r1}, 2, "not last"},
		{"non-record line", []string{r0, "garbage", ":00000001F"}, 2, "record"},
		{"empty", nil, 0, "empty"},
		{"end only", []string{":00000001FF"}, 0, "no data records"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := backup.ParseMCm(tc.lines)
			if len(m.Problems) != 1 {
				t.Fatalf("problems: %+v", m.Problems)
			}
			p := m.Problems[0]
			if p.Line != tc.line || p.Kind != backup.KindMCm || !strings.Contains(p.Text, tc.want) {
				t.Errorf("problem = %+v, want line %d containing %q", p, tc.line, tc.want)
			}
		})
	}
}

func TestParseMCmImageStopsAtProblem(t *testing.T) {
	r0 := rec(0, bytes16(1))
	m := backup.ParseMCm([]string{r0, rec(0x20, bytes16(2)), ":00000001F"})
	if m.Records != 1 || len(m.Image) != 16 {
		t.Errorf("records=%d image=%d", m.Records, len(m.Image))
	}
}

func TestParseMCuProblemsSortedByLine(t *testing.T) {
	// A length problem on line 1 and a last-line problem on line 2; the
	// result must be ordered by line.
	u := backup.ParseMCu([]string{
		backuptest.Cell(0, "Alpha") + "x",
		backuptest.Cell(0, "Other"),
	})
	if len(u.Problems) < 2 {
		t.Fatalf("want >= 2 problems: %+v", u.Problems)
	}
	for i := 1; i < len(u.Problems); i++ {
		if u.Problems[i-1].Line > u.Problems[i].Line {
			t.Fatalf("not sorted: %+v", u.Problems)
		}
	}
}

func TestParseMCuLastLineProblemAtDumpEnd(t *testing.T) {
	lines := append(slices.Clone(backuptest.ValidMCu()[:8]), "|garbage")
	u := backup.ParseMCu(lines)
	var found bool
	for _, p := range u.Problems {
		if strings.Contains(p.Text, "not Cancel") {
			found = true
			if p.Line != len(lines) {
				t.Errorf("line = %d, want %d", p.Line, len(lines))
			}
		}
	}
	if !found {
		t.Fatalf("no 'not Cancel' problem: %+v", u.Problems)
	}
}
