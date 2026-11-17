package backup

import (
	"reflect"
	"testing"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func TestDiffSectionOnOneSide(t *testing.T) {
	c := backuptest.Cell
	a := mcuBackup([]string{
		c(0, "Alpha..."), c(1, "Gain       1"), c(1, "Old        7"),
		c(0, "Beta..."), c(1, "Bone       3"), c(1, "Bend       4"), c(1, "Bolt       5"),
		c(0, "Gamma..."), c(1, "Grip       4"),
		c(0, "Cancel"),
	})
	b := mcuBackup([]string{
		c(0, "Alpha..."), c(1, "Gain       2"),
		c(0, "Gamma..."), c(1, "Grip       4"),
		c(0, "Delta..."), c(1, "Dial       5"), c(1, "Dust       6"),
		c(0, "Cancel"),
	})
	got := Diff(a, b, Layout{}, false)
	want := []Change{
		{"setting", "Beta/", "3 settings", "", "section only in A"},
		{"setting", "Delta/", "", "2 settings", "section only in B"},
		{"setting", "Alpha/Gain", "1", "2", ""},
		{"setting", "Alpha/Old", "7", "", "removed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestDiffSectionOnOneSideDamaged(t *testing.T) {
	c := backuptest.Cell
	lines := []string{
		c(0, "Alpha..."), c(1, "Gain       1"),
		c(0, "Beta..."), c(1, "Bone       3"),
		c(0, "Cancel"),
	}
	a := mcuBackup(lines)
	b := mcuBackup(lines[:2]) // truncated: Beta and Cancel lost
	b.MCu.Problems = append(b.MCu.Problems, Problem{Line: 3, Kind: KindMCu, Text: "dump ends early"})
	got := Diff(a, b, Layout{}, false)
	if len(got) == 0 || got[0].Key != "Beta/" || got[0].Note != "section only in A (damaged)" {
		t.Fatalf("A has the section: %+v", got)
	}
	got = Diff(b, a, Layout{}, false)
	if len(got) == 0 || got[0].Note != "section only in B (damaged)" {
		t.Fatalf("B has the section: %+v", got)
	}
}

func TestDiffDepthZeroLeafIsNoSection(t *testing.T) {
	c := backuptest.Cell
	a := mcuBackup([]string{c(0, "Alpha..."), c(1, "Gain       1"), c(0, "Zed        1"), c(0, "Cancel")})
	b := mcuBackup([]string{c(0, "Alpha..."), c(1, "Gain       1"), c(0, "Cancel")})
	got := Diff(a, b, Layout{}, false)
	want := []Change{{"setting", "Zed", "1", "", "removed"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestDiffDamagedLabelOnlyRowSaysSide(t *testing.T) {
	c := backuptest.Cell
	a := mcuBackup([]string{c(0, "Beta..."), c(1, "Wipe"), c(1, "Bone       3")})
	b := mcuBackup([]string{c(0, "Beta..."), c(1, "Bone       3")})
	b.MCu.Problems = append(b.MCu.Problems, Problem{Line: 3, Kind: KindMCu, Text: "x"})
	for _, ch := range Diff(a, b, Layout{}, false) {
		if ch.Key == "Beta/Wipe" && ch.Note != "damaged; only in A" {
			t.Errorf("note %q", ch.Note)
		}
	}
}
