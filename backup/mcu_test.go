package backup

import (
	"slices"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

var cell = backuptest.Cell

func TestParseMCuPaths(t *testing.T) {
	u := ParseMCu(backuptest.ValidMCu())
	if len(u.Problems) != 0 {
		t.Fatalf("problems: %+v", u.Problems)
	}
	tests := []struct {
		path, label, value string
		menu               bool
	}{
		{"Alpha/Gain", "Gain", "1.0%°", false},
		{"Alpha/Ratio 1 :", "Ratio 1 :", "5.0", false},
		{"Alpha/Duration", "Duration", "00m:03s", false},
		{"Alpha/Beta", "Beta", "", true},
		{"Alpha/Beta/Reset", "Reset", "", false},
		{"Alpha/Beta/Reset#2", "Reset", "", false},
		{"Gamma", "Gamma", "-", false},
	}
	for _, tc := range tests {
		s, ok := u.Lookup(tc.path)
		if !ok {
			t.Errorf("%s: not found", tc.path)
			continue
		}
		if s.Label != tc.label || s.Value != tc.value || s.Menu != tc.menu {
			t.Errorf("%s: got label %q value %q menu %v", tc.path, s.Label, s.Value, s.Menu)
		}
	}
	for _, s := range u.Settings {
		want := strings.SplitN(s.Path, "/", 2)[0]
		if s.Section != want {
			t.Errorf("%s: section %q, want %q", s.Path, s.Section, want)
		}
	}
	if s, _ := u.Lookup("Alpha/Gain"); s.Raw != "Gain     1.0%° " || s.Line != 2 || s.Depth != 1 {
		t.Errorf("Gain: %+v", s)
	}
	if _, ok := u.Lookup("Nope"); ok {
		t.Error("Lookup found a missing path")
	}
}

func problemsAt(u *MCu, line int) []string {
	var out []string
	for _, p := range u.Problems {
		if p.Line == line {
			out = append(out, p.Text)
		}
	}
	return out
}

func TestCheckMCuGlued(t *testing.T) {
	lines := []string{
		cell(0, "Alpha..."),
		cell(1, "Gain     1.0%"),
		"|    ", // cut-off line
		cell(2, "Reset"),
		cell(0, "Cancel"),
	}
	u := ParseMCu(lines)
	if len(u.Problems) != 2 {
		t.Fatalf("problems: %+v", u.Problems)
	}
	if got := problemsAt(u, 3); len(got) != 1 || !strings.Contains(got[0], "5 bytes") {
		t.Errorf("line 3: %v", got)
	}
	if got := problemsAt(u, 4); len(got) != 1 || !strings.Contains(got[0], "deeper than a non-menu line") {
		t.Errorf("line 4: %v", got)
	}
}

func TestCheckMCuStructure(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		line  int
		text  string
	}{
		{"depth jump", []string{
			cell(0, "Alpha..."), cell(1, "Beta..."), cell(3, "Reset"), cell(2, "Reset"), cell(0, "Cancel"),
		}, 3, "jumps"},
		{"first line deep", []string{
			cell(1, "Solo"), cell(0, "Alpha"), cell(0, "Cancel"),
		}, 1, "first line"},
		{"last not Cancel", []string{
			cell(0, "Alpha"), cell(0, "Other"),
		}, 2, "Cancel"},
		{"control byte", []string{
			cell(0, "Alp\x01a"), cell(0, "Cancel"),
		}, 1, "control"},
		{"malformed prefix", []string{
			cell(0, "Alpha"), "|x " + cell(0, "Beta"), cell(0, "Cancel"),
		}, 2, "prefix"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := ParseMCu(tc.lines)
			var probs []Problem // the empty-segment follow-up is not under test here
			for _, p := range u.Problems {
				if !strings.Contains(p.Text, "empty segment") {
					probs = append(probs, p)
				}
			}
			if len(probs) != 1 {
				t.Fatalf("problems: %+v", u.Problems)
			}
			p := probs[0]
			if p.Line != tc.line || p.Kind != KindMCu || !strings.Contains(p.Text, tc.text) {
				t.Errorf("got %+v, want line %d containing %q", p, tc.line, tc.text)
			}
		})
	}
}

func TestSplitLinesCRLF(t *testing.T) {
	if got := SplitLines([]byte("a\r\nb\r\n\r\n")); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("got %q", got)
	}
	crlf := strings.Join(backuptest.ValidMCu(), "\r\n") + "\r\n"
	u := ParseMCu(SplitLines([]byte(crlf)))
	if len(u.Problems) != 0 {
		t.Errorf("problems: %+v", u.Problems)
	}
}

func TestParseMCuEmpty(t *testing.T) {
	u := ParseMCu(nil)
	if len(u.Problems) != 1 || u.Problems[0].Line != 0 {
		t.Errorf("problems: %+v", u.Problems)
	}
}

// Real dumps hold empty menus and menus whose label overflows the cell
// (it shows two dots, not three); neither is damage.
func TestParseMCuEmptyAndOverlongMenus(t *testing.T) {
	u := ParseMCu([]string{
		cell(0, "Alpha..."), cell(1, "Empty..."), cell(1, "Gain     1.0%"),
		cell(0, "Overlong name.."), cell(1, "Reset"), cell(0, "Cancel"),
	})
	if len(u.Problems) != 0 {
		t.Fatalf("problems: %+v", u.Problems)
	}
	for _, path := range []string{"Alpha/Empty", "Overlong name"} {
		if s, ok := u.Lookup(path); !ok || !s.Menu {
			t.Errorf("%s: %+v, %v", path, s, ok)
		}
	}
	if s, ok := u.Lookup("Overlong name/Reset"); !ok || s.Menu {
		t.Errorf("child of overlong menu: %+v, %v", s, ok)
	}
}

func TestParseMCuDropsTrailingBlankLines(t *testing.T) {
	lines := append(backuptest.ValidMCu(), "", "  ", "")
	if u := ParseMCu(lines); len(u.Problems) != 0 {
		t.Errorf("problems with trailing blank lines: %+v", u.Problems)
	}
	if u := ParseMCu([]string{"", " "}); len(u.Problems) != 1 || u.Problems[0].Text != "empty dump" {
		t.Errorf("blank-only dump: %+v", u.Problems)
	}
}

func TestParseMCuWrongLengthDotsIsNotAMenu(t *testing.T) {
	lines := backuptest.ValidMCu()
	lines[1] = cell(1, "Gain 1.0%") + "x.." // 17 cell bytes, ends in ..
	u := ParseMCu(lines)
	var got []string
	for _, p := range u.Problems {
		got = append(got, p.Text)
	}
	if len(got) != 1 || !strings.Contains(got[0], "bytes, want") {
		t.Fatalf("problems = %q, want only the length problem", got)
	}
	if s := u.Settings[1]; s.Menu {
		t.Errorf("wrong-length '..' line parsed as menu: %+v", s)
	}
}

func TestParseMCuLastLineMenu(t *testing.T) {
	lines := backuptest.ValidMCu()
	lines = append(lines[:8], cell(0, "Delta..."))
	u := ParseMCu(lines)
	if len(u.Problems) != 1 || !strings.Contains(u.Problems[0].Text, "last line is not Cancel") {
		t.Errorf("problems = %+v", u.Problems)
	}
}
