package backup

import (
	"slices"
	"testing"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func TestRegionsLostStretch(t *testing.T) {
	short := cell(1, "Mid2   5")
	lines := []string{
		cell(0, "Alpha..."),   // 1
		cell(1, "Gain   1.0"), // 2
		cell(0, "Beta..."),    // 3
		cell(1, "Mid    5"),   // 4
		short[:len(short)-3],  // 5: short line
		cell(3, "Deep   1"),   // 6: depth jump, empty segment
		cell(0, "Gamma..."),   // 7
		cell(1, "Zed    1"),   // 8
		cell(0, "Cancel"),     // 9
	}
	u := ParseMCu(lines)
	if len(u.Regions) != 1 || u.Regions[0] != (Region{From: 5, To: 6, Section: "Beta"}) {
		t.Fatalf("regions %+v, problems %+v", u.Regions, u.Problems)
	}
	var found bool
	for _, p := range u.Problems {
		if p.Line == 6 && p.Text == `path has an empty segment: "/Deep" (a parent line is missing)` {
			found = true
		}
	}
	if !found {
		t.Errorf("no empty-segment problem: %+v", u.Problems)
	}
	for _, s := range u.Settings {
		if want := s.Line == 5 || s.Line == 6; s.Damaged != want {
			t.Errorf("line %d Damaged = %v", s.Line, s.Damaged)
		}
	}
}

func TestRegionsCleanAndMerged(t *testing.T) {
	u := ParseMCu(backuptest.ValidMCu())
	if len(u.Regions) != 0 {
		t.Errorf("clean dump has regions %+v", u.Regions)
	}
	for _, s := range u.Settings {
		if s.Damaged {
			t.Errorf("line %d Damaged in a clean dump", s.Line)
		}
	}

	a, b := cell(1, "One     1"), cell(1, "Two     2")
	u = ParseMCu([]string{
		cell(0, "Alpha..."),
		a[:len(a)-1], // 2
		cell(1, "Mid    5"),
		b[:len(b)-1], // 4
		cell(1, "Tail   6"),
		cell(0, "Beta..."),
		cell(1, "Fine   7"),
		cell(0, "Cancel"),
	})
	if len(u.Regions) != 1 || u.Regions[0] != (Region{From: 2, To: 5, Section: "Alpha"}) {
		t.Fatalf("regions %+v", u.Regions)
	}
}

func TestRegionsTwoSectionsAndWholeFile(t *testing.T) {
	a, b := cell(1, "One     1"), cell(1, "Two     2")
	u := ParseMCu([]string{
		cell(0, "Alpha..."),
		a[:len(a)-1], // 2
		cell(0, "Beta..."),
		b[:len(b)-1], // 4
		cell(0, "Cancel"),
	})
	want := []Region{{2, 2, "Alpha"}, {4, 4, "Beta"}}
	if !slices.Equal(u.Regions, want) {
		t.Fatalf("regions %+v", u.Regions)
	}
	if u := ParseMCu(nil); len(u.Regions) != 0 {
		t.Errorf("whole-file problem made regions %+v", u.Regions)
	}
}

func TestSplitLeafRules(t *testing.T) {
	for _, tc := range []struct{ in, label, value string }{
		{"Clear weight", "Clear weight", ""},
		{"Gain 1.0%", "Gain", "1.0%"},
		{"Mode ON", "Mode", "ON"},
		{"Temp -5", "Temp", "-5"},
		{"Temp +5", "Temp", "+5"},
		{"Temp .5", "Temp", ".5"},
		{"Ratio 1:30", "Ratio", "1:30"},
		{"Unit \xb0", "Unit", "\xb0"},
		{"Gain     1.0%", "Gain", "1.0%"},
		{"Reset", "Reset", ""},
		{"Duration00m:03s", "Duration", "00m:03s"},
		{"Ratio 1 :   5.0", "Ratio 1 :", "5.0"},
	} {
		l, v := splitLeaf(tc.in)
		if l != tc.label || v != tc.value {
			t.Errorf("splitLeaf(%q) = %q, %q; want %q, %q", tc.in, l, v, tc.label, tc.value)
		}
	}
}

func TestSplitNumber(t *testing.T) {
	for _, tc := range []struct {
		in, num, unit string
		ok            bool
	}{
		{"88.0°", "88.0", "°", true},
		{"2.000°/cycle", "2.000", "°/cycle", true},
		{"-5", "-5", "", true},
		{"+5 s", "+5", "s", true},
		{".5%", ".5", "%", true},
		{"007", "007", "", true},
		{"-", "", "", false},
		{"ON", "", "", false},
		{"", "", "", false},
		{"1:30", "", "", false},
		{"5 m3", "", "", false},
		{".", "", "", false},
	} {
		n, u, ok := SplitNumber(tc.in)
		if n != tc.num || u != tc.unit || ok != tc.ok {
			t.Errorf("SplitNumber(%q) = %q, %q, %v; want %q, %q, %v", tc.in, n, u, ok, tc.num, tc.unit, tc.ok)
		}
	}
}
