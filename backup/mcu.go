package backup

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Kind names which part of a backup a Problem concerns.
type Kind string

const (
	// KindMCu marks a problem in the MCu dump.
	KindMCu Kind = "mcu"
	// KindMCm marks a problem in the MCm dump.
	KindMCm Kind = "mcm"
	// KindPair marks a problem between the two dumps.
	KindPair Kind = "pair"
)

// Problem is one thing wrong with a backup. Line 0 means the whole file.
type Problem struct {
	Line int
	Kind Kind
	Text string
}

// Setting is one cell of the MCu menu tree.
type Setting struct {
	Line, Depth int
	Menu        bool   // cell ends in "..."
	Path        string // labels joined by "/", "#N" for repeated sibling labels
	Label       string
	Value       string
	Raw         string // the 15-char cell as UTF-8
	Section     string // the depth-0 label it lies under
	Damaged     bool   // its Line lies in one of MCu.Regions
}

// Region is a stretch of an MCu dump whose paths are unreliable: lines
// may be lost there and the surviving lines may sit under wrong parents.
type Region struct {
	From, To int    // first and last line, 1-based, inclusive
	Section  string // the depth-0 label the region starts in ("" if none)
}

// MCu is a parsed menu dump.
type MCu struct {
	Settings []Setting
	Problems []Problem
	Regions  []Region // sorted by From; see Region
}

const cellWidth = 15

// Latin1 converts s, read as Latin-1 bytes, to UTF-8.
func Latin1(s string) string {
	r := make([]rune, len(s))
	for i := 0; i < len(s); i++ {
		r[i] = rune(s[i])
	}
	return string(r)
}

// SplitLines splits data at \n, strips a trailing \r from each line and
// drops trailing blank lines.
func SplitLines(data []byte) []string {
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Lookup returns the setting with the given path.
func (u *MCu) Lookup(path string) (Setting, bool) {
	for _, s := range u.Settings {
		if s.Path == path {
			return s, true
		}
	}
	return Setting{}, false
}

// ParseMCu parses and checks the lines of an MCu dump. It never fails;
// damage is reported in Problems. Trailing blank lines are ignored.
func ParseMCu(lines []string) *MCu {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	u := &MCu{}
	problem := func(line int, format string, args ...any) {
		u.Problems = append(u.Problems, Problem{Line: line, Kind: KindMCu, Text: fmt.Sprintf(format, args...)})
	}
	if len(lines) == 0 {
		problem(0, "empty dump")
		return u
	}
	var (
		stack   []string           // unique path per depth of the current branch
		padded  []bool             // stack entry invented for a missing ancestor
		section string             // current depth-0 label
		seen    = map[string]int{} // base path -> sibling count
		prev    *Setting
	)
	for i, line := range lines {
		n := i + 1
		depth, rest := 0, line
		for strings.HasPrefix(rest, "| ") {
			depth++
			rest = rest[2:]
		}
		if strings.HasPrefix(rest, "|") {
			problem(n, "malformed prefix %q", Latin1(line[:min(len(line), 2*depth+3)]))
			continue
		}
		if want := cellWidth + 2*depth; len(line) != want {
			problem(n, "line is %d bytes, want %d", len(line), want)
		}
		if strings.IndexFunc(rest, func(r rune) bool { return r < 0x20 }) >= 0 {
			problem(n, "control byte in cell")
		}

		trimmed := strings.TrimRight(rest, " ")
		s := Setting{Line: n, Depth: depth, Raw: Latin1(rest)}
		switch {
		case strings.HasSuffix(trimmed, "..."):
			s.Menu = true
			s.Label = Latin1(strings.TrimRight(strings.TrimSuffix(trimmed, "..."), " "))
		case len(rest) == cellWidth && strings.HasSuffix(rest, ".."):
			// A menu label too long for the cell loses its last dot.
			s.Menu = true
			s.Label = Latin1(strings.TrimRight(strings.TrimSuffix(rest, ".."), " "))
		default:
			label, value := splitLeaf(trimmed)
			s.Label, s.Value = Latin1(label), Latin1(value)
		}

		// Structure.
		switch {
		case prev == nil && depth != 0:
			problem(n, "first line is at depth %d, want 0", depth)
		case prev != nil && depth > prev.Depth+1:
			problem(n, "depth jumps from %d to %d", prev.Depth, depth)
		case prev != nil && depth == prev.Depth+1 && !prev.Menu:
			problem(n, "deeper than a non-menu line")
		}
		// A menu without children is valid: the machine shows empty ones
		// (e.g. no stored presets) as a menu followed by its sibling.

		// Path.
		if depth == 0 {
			section = s.Label
		}
		if depth < len(stack) {
			stack, padded = stack[:depth], padded[:depth]
		}
		for len(stack) < depth {
			stack, padded = append(stack, ""), append(padded, true)
		}
		base := s.Label
		if depth > 0 {
			base = stack[depth-1] + "/" + s.Label
		}
		seen[base]++
		s.Path = base
		if seen[base] > 1 {
			s.Path = fmt.Sprintf("%s#%d", base, seen[base])
		}
		orphan := slices.Contains(padded, true) // an ancestor line is missing
		stack, padded = append(stack, s.Path), append(padded, false)
		s.Section = section
		if orphan {
			problem(n, "path has an empty segment: %q (a parent line is missing)", s.Path)
		}

		u.Settings = append(u.Settings, s)
		prev = &u.Settings[len(u.Settings)-1]
	}
	if prev == nil {
		return u
	}
	if prev.Depth != 0 || prev.Label != "Cancel" {
		problem(len(lines), "last line is not Cancel at depth 0")
	}
	sort.SliceStable(u.Problems, func(i, j int) bool { return u.Problems[i].Line < u.Problems[j].Line })
	u.markRegions(len(lines))
	return u
}

// markRegions derives Regions from the line problems and flags the
// settings inside them. Each problem starts a region that runs to the
// line before the next depth-0 setting, or to the last line.
func (u *MCu) markRegions(last int) {
	for _, p := range u.Problems { // sorted by line
		if p.Line == 0 {
			continue
		}
		to := last
		for _, s := range u.Settings {
			if s.Depth == 0 && s.Line > p.Line {
				to = s.Line - 1
				break
			}
		}
		if n := len(u.Regions); n > 0 && p.Line <= u.Regions[n-1].To+1 {
			u.Regions[n-1].To = max(u.Regions[n-1].To, to)
			continue
		}
		sec := ""
		for _, s := range u.Settings {
			if s.Line > p.Line {
				break
			}
			sec = s.Section
		}
		u.Regions = append(u.Regions, Region{From: p.Line, To: to, Section: sec})
	}
	for i := range u.Settings {
		for _, r := range u.Regions {
			if u.Settings[i].Line >= r.From && u.Settings[i].Line <= r.To {
				u.Settings[i].Damaged = true
			}
		}
	}
}

// splitLeaf splits trailing-space-trimmed leaf text into label and value:
// at the last run of two or more spaces, else the last single space, else
// the first letter-to-digit boundary, else label only. The single-space
// split is skipped when the part after the space starts with a lower-case
// letter ("Clear weight" is a label); the whole text is then the label.
func splitLeaf(s string) (label, value string) {
	if i := strings.LastIndex(s, "  "); i >= 0 {
		return strings.TrimRight(s[:i], " "), strings.TrimLeft(s[i:], " ")
	}
	if i := strings.LastIndex(s, " "); i >= 0 {
		if looksLikeValue(s[i+1:]) {
			return s[:i], s[i+1:]
		}
		return s, ""
	}
	for i := 1; i < len(s); i++ {
		if isLetter(s[i-1]) && s[i] >= '0' && s[i] <= '9' {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

func looksLikeValue(v string) bool {
	return v != "" && (v[0] < 'a' || v[0] > 'z')
}

// SplitNumber splits a setting value into a leading decimal number and
// the unit after it: "88.0°" → "88.0", "°"; "2.000°/cycle" → "2.000",
// "°/cycle"; "-5" → "-5", "". ok is false (both empty) when the value
// does not start with a number or the rest holds a digit ("1:30").
func SplitNumber(value string) (number, unit string, ok bool) {
	i := 0
	if i < len(value) && (value[i] == '-' || value[i] == '+') {
		i++
	}
	digits := 0
	for i < len(value) && value[i] >= '0' && value[i] <= '9' {
		i++
		digits++
	}
	if i < len(value) && value[i] == '.' {
		i++
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return "", "", false
	}
	rest := strings.TrimSpace(value[i:])
	if strings.ContainsAny(rest, "0123456789") {
		return "", "", false
	}
	return value[:i], rest, true
}
