package backup

import (
	"os"
	"testing"
)

// TestRealBackups is the acceptance test on the operator's two real
// backups (pre and post autotune). It decides whether the daemon may
// refuse dumps that fail the check, so the expectations are exact.
func TestRealBackups(t *testing.T) {
	dir := os.Getenv("MEDEWI_BACKUP_DIR")
	if dir == "" {
		t.Skip("MEDEWI_BACKUP_DIR not set")
	}
	bks, _, err := Resolve([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(bks) != 2 {
		t.Fatalf("want 2 backups, got %d", len(bks))
	}
	pre, post := &bks[0], &bks[1]
	for _, b := range []*Backup{pre, post} {
		if err := b.Load(); err != nil {
			t.Fatal(err)
		}
	}

	if len(pre.MCu.Problems) == 0 {
		t.Fatal("pre MCu: want a problem, got none")
	}
	if first := pre.MCu.Problems[0]; first.Line != 1094 {
		t.Errorf("pre MCu: first problem at line %d, want 1094", first.Line)
	}
	for _, p := range pre.MCu.Problems {
		if p.Line < 1094 {
			t.Errorf("pre MCu: problem before line 1094: line %d", p.Line)
		}
	}
	if len(post.MCu.Problems) != 0 {
		t.Errorf("post MCu problems: %v", post.MCu.Problems)
	}
	if len(post.MCm.Problems) != 0 {
		t.Errorf("post MCm problems: %v", post.MCm.Problems)
	}

	l, ok := LayoutFor("3.2")
	if !ok {
		t.Fatal("no layout for 3.2")
	}
	if ps := CrossCheck(post.MCu, post.MCm, l); len(ps) != 0 {
		t.Errorf("post cross-check problems: %v", ps)
	}

	want := []Change{
		{"setting", "PID 1/Kc", "8.9%/°", "8.8%/°", ""},
		{"setting", "PID 1/Ti", "67.2s", "72.3s", ""},
		{"setting", "PID 1/Td", "16.8s", "18.1s", ""},
		{"eeprom", "0x0173", "8.9", "8.8", ""},
		{"eeprom", "0x0027", "683425", "684431", ""},
	}
	got := Diff(pre, post, l, true)
	for _, w := range want {
		found := false
		for _, c := range got {
			if c.Section == w.Section && c.Key == w.Key {
				found = true
				if c.A != w.A || c.B != w.B {
					t.Errorf("%s %s: %q -> %q, want %q -> %q", c.Section, c.Key, c.A, c.B, w.A, w.B)
				}
				if w.Section == "setting" && c.Note != "" {
					t.Errorf("%s %s: note %q, want empty", c.Section, c.Key, c.Note)
				}
			}
		}
		if !found {
			t.Errorf("diff lacks %s %s", w.Section, w.Key)
		}
	}
}
