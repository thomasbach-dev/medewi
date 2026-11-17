package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func TestParseName(t *testing.T) {
	ts := time.Date(2026, 11, 16, 11, 25, 55, 0, time.UTC)
	tests := []struct {
		in   string
		ok   bool
		want Name
	}{
		{"2026-11-16T112555Z_3.2_post-autotune.mcu.txt", true, Name{ts, "3.2", "post-autotune", KindMCu}},
		{"2026-11-16T112555Z_3.2.mcm.hex", true, Name{ts, "3.2", "", KindMCm}},
		{"2026-11-16T112555Z_3.2_x_y.mcu.txt", true, Name{ts, "3.2", "x_y", KindMCu}},
		{"2026-11-16T112555Z_3.2.txt", false, Name{}},
		{"2026-11-16T112555Z_3.2.mcu.txt~", false, Name{}},
		{"2026-13-16T112555Z_3.2.mcu.txt", false, Name{}},
		{"yesterday_3.2.mcu.txt", false, Name{}},
		{"2026-11-16T112555Z.mcu.txt", false, Name{}},
	}
	for _, tt := range tests {
		got, ok := ParseName(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("ParseName(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func write(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writePair(t *testing.T, dir, stem string) {
	t.Helper()
	write(t, filepath.Join(dir, stem+".mcu.txt"), backuptest.ValidMCu())
	write(t, filepath.Join(dir, stem+".mcm.hex"), backuptest.ValidMCm())
}

func TestResolvePairsByStem(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "2026-11-16T120000Z_3.2_second")
	writePair(t, dir, "2026-11-16T110000Z_3.2_first")
	bks, notes, err := Resolve([]string{dir})
	if err != nil || len(notes) != 0 || len(bks) != 2 {
		t.Fatalf("got %d backups, notes %v, err %v", len(bks), notes, err)
	}
	if bks[0].Note != "first" || bks[1].Note != "second" {
		t.Errorf("order/notes: %q %q", bks[0].Note, bks[1].Note)
	}
	if bks[0].MCuPath == "" || bks[0].MCmPath == "" || bks[0].Src != bks[0].MCuPath || bks[0].Firmware != "3.2" {
		t.Errorf("bad backup %+v", bks[0])
	}
}

func TestResolvePairsByTime(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "2026-11-16T063357Z_3.2.mcu.txt"), backuptest.ValidMCu())
	write(t, filepath.Join(dir, "2026-11-16T063401Z_3.2.mcm.hex"), backuptest.ValidMCm())
	bks, notes, err := Resolve([]string{dir})
	if err != nil || len(bks) != 1 || len(notes) != 0 {
		t.Fatalf("got %d backups, notes %v, err %v", len(bks), notes, err)
	}
	if bks[0].MCuPath == "" || bks[0].MCmPath == "" {
		t.Errorf("not paired: %+v", bks[0])
	}
	write(t, filepath.Join(dir, "2026-11-16T064001Z_3.2.mcm.hex"), backuptest.ValidMCm())
	bks, notes, err = Resolve([]string{dir})
	if err != nil || len(bks) != 2 || len(notes) != 1 {
		t.Fatalf("got %d backups, notes %v, err %v", len(bks), notes, err)
	}
	if bks[1].MCuPath != "" || bks[1].MCmPath == "" || bks[1].Src != bks[1].MCmPath {
		t.Errorf("second: %+v", bks[1])
	}
}

func TestResolveParentOfBackupDirs(t *testing.T) {
	dir := t.TempDir()
	writePair(t, filepath.Join(dir, "pre"), "2026-11-16T120000Z_3.2")
	writePair(t, filepath.Join(dir, "post"), "2026-11-16T110000Z_3.2")
	bks, _, err := Resolve([]string{dir})
	if err != nil || len(bks) != 2 {
		t.Fatalf("got %d backups, err %v", len(bks), err)
	}
	if filepath.Base(filepath.Dir(bks[0].Src)) != "post" || filepath.Base(filepath.Dir(bks[1].Src)) != "pre" {
		t.Errorf("order: %s, %s", bks[0].Src, bks[1].Src)
	}
}

func TestResolveSkipsStrays(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "2026-11-16T120000Z_3.2")
	for _, n := range []string{".medewictl-123.tmp", "README", "x.mcu.txt~"} {
		write(t, filepath.Join(dir, n), []string{"junk"})
	}
	bks, notes, err := Resolve([]string{dir})
	if err != nil || len(bks) != 1 || len(notes) != 3 {
		t.Fatalf("got %d backups, notes %v, err %v", len(bks), notes, err)
	}
}

func TestResolveSniffsKind(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "dump.txt"), filepath.Join(dir, "other.txt")
	write(t, a, backuptest.ValidMCm())
	write(t, b, backuptest.ValidMCu())
	bks, _, err := Resolve([]string{a, b})
	if err != nil || len(bks) != 2 {
		t.Fatalf("got %d backups, err %v", len(bks), err)
	}
	if bks[0].MCmPath != a || bks[0].MCuPath != "" || bks[1].MCuPath != b {
		t.Errorf("sniff: %+v %+v", bks[0], bks[1])
	}
}

func TestResolveManyPairsInDir(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "2026-11-16T100000Z_3.2")
	writePair(t, dir, "2026-11-16T110000Z_3.2")
	writePair(t, dir, "2026-11-16T120000Z_3.2")
	write(t, filepath.Join(dir, "2026-11-16T130000Z_3.2.mcu.txt"), backuptest.ValidMCu())
	bks, notes, err := Resolve([]string{dir})
	if err != nil || len(bks) != 4 || len(notes) != 1 {
		t.Fatalf("got %d backups, notes %v, err %v", len(bks), notes, err)
	}
}

func TestResolveMissing(t *testing.T) {
	if _, _, err := Resolve([]string{filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Error("want error")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "2026-11-16T120000Z_3.2")
	bks, _, err := Resolve([]string{dir})
	if err != nil || len(bks) != 1 {
		t.Fatal(bks, err)
	}
	if err := bks[0].Load(); err != nil {
		t.Fatal(err)
	}
	if bks[0].MCu == nil || bks[0].MCm == nil || len(bks[0].MCu.Settings) == 0 {
		t.Errorf("not loaded: %+v", bks[0])
	}
	bad := Backup{MCuPath: filepath.Join(dir, "gone.mcu.txt")}
	if err := bad.Load(); err == nil || !strings.Contains(err.Error(), "gone.mcu.txt") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveDirOrdersByTimeAcrossSubdirs(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, "2026-11-16T120000Z_3.2")
	writePair(t, filepath.Join(dir, "old"), "2026-11-16T100000Z_3.2")
	bks, _, err := Resolve([]string{dir})
	if err != nil || len(bks) != 2 {
		t.Fatalf("got %d backups, err %v", len(bks), err)
	}
	if !bks[0].Time.Before(bks[1].Time) {
		t.Errorf("order: %v then %v", bks[0].Time, bks[1].Time)
	}
}
