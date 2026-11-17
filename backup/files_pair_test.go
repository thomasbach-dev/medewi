package backup

import (
	"path/filepath"
	"testing"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func TestResolveFileArgsPairByStem(t *testing.T) {
	dir := t.TempDir()
	u := filepath.Join(dir, "2026-11-16T120000Z_3.2_x.mcu.txt")
	m := filepath.Join(dir, "2026-11-16T120000Z_3.2_x.mcm.hex")
	write(t, u, backuptest.ValidMCu())
	write(t, m, backuptest.ValidMCm())
	bks, notes, err := Resolve([]string{m, u})
	if err != nil || len(bks) != 1 || len(notes) != 0 {
		t.Fatalf("got %d backups, notes %v, err %v", len(bks), notes, err)
	}
	if bks[0].MCuPath != u || bks[0].MCmPath != m {
		t.Errorf("not paired: %+v", bks[0])
	}
}

func TestResolveFileArgsPairByTime(t *testing.T) {
	dir := t.TempDir()
	u := filepath.Join(dir, "2026-11-16T120000Z_3.2_a.mcu.txt")
	m := filepath.Join(dir, "2026-11-16T120200Z_3.2_b.mcm.hex")
	write(t, u, backuptest.ValidMCu())
	write(t, m, backuptest.ValidMCm())
	bks, notes, err := Resolve([]string{u, m})
	if err != nil || len(bks) != 1 || len(notes) != 0 {
		t.Fatalf("got %d backups, notes %v, err %v", len(bks), notes, err)
	}
	if bks[0].MCuPath != u || bks[0].MCmPath != m {
		t.Errorf("not paired: %+v", bks[0])
	}
}

func TestResolveFileArgsLoneAndAcrossDirs(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	lone := filepath.Join(d1, "2026-11-16T120000Z_3.2.mcu.txt")
	write(t, lone, backuptest.ValidMCu())
	bks, notes, err := Resolve([]string{lone})
	if err != nil || len(bks) != 1 || len(notes) != 1 {
		t.Fatalf("got %d backups, notes %v, err %v", len(bks), notes, err)
	}

	m := filepath.Join(d2, "2026-11-16T120000Z_3.2.mcm.hex")
	write(t, m, backuptest.ValidMCm())
	bks, notes, err = Resolve([]string{lone, m})
	if err != nil || len(bks) != 2 || len(notes) != 2 {
		t.Fatalf("different dirs: got %d backups, notes %v, err %v", len(bks), notes, err)
	}
}

func TestResolveFileArgsOrder(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "2026-11-16T090000Z_3.2.mcu.txt")
	u := filepath.Join(dir, "2026-11-16T120000Z_3.2.mcu.txt")
	m := filepath.Join(dir, "2026-11-16T120000Z_3.2.mcm.hex")
	for _, p := range []string{other, u} {
		write(t, p, backuptest.ValidMCu())
	}
	write(t, m, backuptest.ValidMCm())
	bks, _, err := Resolve([]string{m, other, u})
	if err != nil || len(bks) != 2 {
		t.Fatalf("got %d backups, err %v", len(bks), err)
	}
	if bks[0].MCmPath != m || bks[1].MCuPath != other {
		t.Errorf("order: %+v, %+v", bks[0], bks[1])
	}
}
