package backup

import (
	"reflect"
	"testing"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func mcuBackup(lines []string) *Backup {
	return &Backup{MCu: ParseMCu(lines)}
}

func mcmBackup(img []byte) *Backup {
	var recs []string
	for a := 0; a < len(img); a += 16 {
		e := min(a+16, len(img))
		recs = append(recs, backuptest.Rec(a, img[a:e]))
	}
	return &Backup{MCm: ParseMCm(append(recs, ":00000001F"))}
}

func gainLines(value string, extra ...string) []string {
	l := []string{
		backuptest.Cell(0, "Alpha..."),
		backuptest.Cell(1, "Gain     "+value+"%\xb0"),
	}
	l = append(l, extra...)
	return append(l, backuptest.Cell(0, "Beta..."), backuptest.Cell(1, "Limit      5"),
		backuptest.Cell(0, "Cancel"))
}

func TestDiffSettings(t *testing.T) {
	a := mcuBackup(gainLines("1.0", backuptest.Cell(1, "Old       7")))
	b := mcuBackup(gainLines("2.0", backuptest.Cell(1, "New       8")))
	got := Diff(a, b, Layout{}, false)
	want := []Change{
		{"setting", "Alpha/Gain", "1.0%°", "2.0%°", ""},
		{"setting", "Alpha/New", "", "8", "added"},
		{"setting", "Alpha/Old", "7", "", "removed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestDiffDamaged(t *testing.T) {
	bad := gainLines("1.0", backuptest.Cell(1, "Old       7"))
	bad[1] = bad[1][:len(bad[1])-3] // short line: problem inside Alpha
	a := mcuBackup(bad)
	if len(a.MCu.Problems) == 0 {
		t.Fatal("setup: expected problems")
	}
	b := mcuBackup(gainLines("2.0", backuptest.Cell(1, "New       8")))
	for i := range b.MCu.Settings {
		if b.MCu.Settings[i].Path == "Beta/Limit" {
			b.MCu.Settings[i].Value = "6"
		}
	}
	got := Diff(a, b, Layout{}, false)
	notes := map[string]string{}
	for _, c := range got {
		notes[c.Key] = c.Note
	}
	if notes["Alpha/New"] != "damaged; only in B" || notes["Alpha/Old"] != "damaged; only in A" {
		t.Errorf("one-sided in damaged section: %+v", got)
	}
	if n, ok := notes["Beta/Limit"]; !ok || n != "" {
		t.Errorf("other section: %+v", got)
	}
}

func TestDiffDamagedChangedValue(t *testing.T) {
	bad := gainLines("1.0", backuptest.Cell(1, "Old       7"))
	bad[2] = bad[2][:len(bad[2])-3]
	a := mcuBackup(bad)
	b := mcuBackup(gainLines("2.0"))
	got := Diff(a, b, Layout{}, false)
	if len(got) == 0 || got[0].Key != "Alpha/Gain" || got[0].Note != "damaged" {
		t.Errorf("got %+v", got)
	}
}

var testLayout = Layout{Firmware: "t", Entries: []Entry{
	{0x0000, "u16", 0.1, "Kc", "", false, "confirmed", ""},
}}

func TestDiffEEPROM(t *testing.T) {
	ia := make([]byte, 32)
	ib := make([]byte, 32)
	ia[0], ib[0] = 89, 88
	ia[0x10], ia[0x11] = 0xcd, 0xce
	ib[0x10], ib[0x11] = 0xa0, 0xa1
	got := Diff(mcmBackup(ia), mcmBackup(ib), testLayout, true)
	want := []Change{
		{"eeprom", "0x0000", "8.9", "8.8", "Kc (confirmed)"},
		{"eeprom", "0x0010", "cdce", "a0a1", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	short := append([]byte(nil), ib[:17]...)
	got = Diff(mcmBackup(ia), mcmBackup(short), Layout{}, false)
	last := got[len(got)-1]
	if last.Note != "only in A" || last.Key != "0x0011" || last.B != "" {
		t.Errorf("past end: %+v", got)
	}
}

func TestDiffNoLayout(t *testing.T) {
	ia := make([]byte, 16)
	ib := make([]byte, 16)
	ia[0], ib[0] = 89, 88
	ia[1], ib[1] = 1, 2
	got := Diff(mcmBackup(ia), mcmBackup(ib), testLayout, false)
	want := []Change{{"eeprom", "0x0000", "5901", "5802", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestDiffDamagedAcrossSections(t *testing.T) {
	c := backuptest.Cell
	b := mcuBackup([]string{
		c(0, "Alpha..."), c(1, "Gain       1"), c(1, "Tail       2"),
		c(0, "Beta..."), c(1, "Bone       3"),
		c(0, "Gamma..."), c(1, "Grip       4"),
		c(0, "Delta..."), c(1, "Dial       5"),
		c(0, "Cancel"),
	})
	glued := c(1, "Grip       4")
	a := mcuBackup([]string{
		c(0, "Alpha..."), c(1, "Gain       1"),
		glued[:len(glued)-4], // cut-off remainder glued under Alpha
		c(0, "Delta..."), c(1, "Dial       6"),
		c(0, "Cancel"),
	})
	if len(a.MCu.Problems) == 0 {
		t.Fatal("setup: expected problems in A")
	}
	notes := map[string]string{}
	for _, ch := range Diff(a, b, Layout{}, false) {
		notes[ch.Key] = ch.Note
	}
	for k, want := range map[string]string{"Alpha/Tail": "damaged; only in B", "Alpha/Grip": "damaged; only in A"} {
		if notes[k] != want {
			t.Errorf("%s: note %q, want %q (all: %v)", k, notes[k], want, notes)
		}
	}
	// Sections missing on A are one row each, not per-path rows.
	for _, k := range []string{"Beta/", "Gamma/"} {
		if notes[k] != "section only in B (damaged)" || notes[k+"Bone"] != "" {
			t.Errorf("%s: note %q", k, notes[k])
		}
	}
	if n, ok := notes["Delta/Dial"]; !ok || n != "" {
		t.Errorf("Delta/Dial: %q, %v", n, ok)
	}
	for k, n := range notes {
		if n == "added" || n == "removed" {
			t.Errorf("%s: %q", k, n)
		}
	}
}

func TestDiffMissingHalves(t *testing.T) {
	a := mcuBackup(gainLines("1.0"))
	b := mcmBackup(make([]byte, 16))
	if got := Diff(a, b, testLayout, true); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
	if got := Diff(b, a, testLayout, true); len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
