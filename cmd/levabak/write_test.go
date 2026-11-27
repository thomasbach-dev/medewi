package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/backup"
	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

// image32 returns a 4 KiB MCm dump (LF line ends, as medewictl stores
// them) with a few non-zero bytes, and its image.
func image32(t *testing.T, dir, name string) (path string, img []byte) {
	t.Helper()
	img = make([]byte, 4096)
	img[0x0087] = 0x5a
	img[0x0173] = 88
	img[0x08AA], img[0x08AC] = 45, 90
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, backup.EncodeMCm(img, false), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, img
}

func readImage(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := backup.ParseMCm(backup.SplitLines(data))
	if len(m.Problems) != 0 {
		t.Fatalf("%s: %+v", path, m.Problems)
	}
	return m.Image
}

func TestWriteRoundTrip(t *testing.T) {
	d := t.TempDir()
	src, _ := image32(t, d, "2026-11-10T094200Z_3.2.mcm.hex")
	out := filepath.Join(d, "out", "2026-11-10T094200Z_3.2_copy.mcm.hex")
	if err := os.Mkdir(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stdout, errs := run0(t, "write", "--out", out, src)
	if code != 0 || stdout != "" || !strings.Contains(errs, "no --set: the image is unchanged") {
		t.Fatalf("code %d out %q err %q", code, stdout, errs)
	}
	want, _ := os.ReadFile(src)
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, want) {
		t.Error("the copy differs from its source")
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600 (the image holds the license key)", st.Mode().Perm())
	}
}

func TestWriteSet(t *testing.T) {
	d := t.TempDir()
	src, img := image32(t, d, "2026-11-10T094200Z_3.2.mcm.hex")
	out := filepath.Join(d, "2026-11-10T094200Z_3.2_edit.mcm.hex")
	code, stdout, errs := run0(t, "write", "--format", "jsonl", "--out", out,
		"--set", "Profiles/Edit/4/Edit shot.../1/Time=7.0",
		"--set", "Profiles/Edit/4/Edit shot.../1/Pump=3.3b",
		"--set", "Eco/Eco=46", src)
	if code != 0 {
		t.Fatalf("code %d err %q", code, errs)
	}
	if got := keys(t, stdout); got != "section key a b note" {
		t.Errorf("keys %q", got)
	}
	var rows []string
	for _, r := range jsonRows(t, stdout) {
		rows = append(rows, r["key"].(string))
	}
	if got := strings.Join(rows, " "); got != "0x0087 0x0675 0x0677 0x08AA" {
		t.Errorf("rows %s", got)
	}
	img[0x0675], img[0x0677], img[0x08AA] = 71, 214, 46
	img[0x0087] ^= 71 ^ 214 ^ 45 ^ 46
	if !bytes.Equal(readImage(t, out), img) {
		t.Error("the edited image differs from the source in more than the entries and the checksum")
	}
	if !bytes.Equal(readImage(t, src), func() []byte { _, i := image32(t, t.TempDir(), "x.mcm.hex"); return i }()) {
		t.Error("the source was changed")
	}
}

func TestWriteStandardEOF(t *testing.T) {
	d := t.TempDir()
	src, _ := image32(t, d, "2026-11-10T094200Z_3.2.mcm.hex")
	out := filepath.Join(d, "o.mcm.hex")
	if code, _, errs := run0(t, "write", "--standard-eof", "--out", out, src); code != 0 {
		t.Fatalf("code %d err %q", code, errs)
	}
	got, _ := os.ReadFile(out)
	if !bytes.HasSuffix(got, []byte("\n:00000001FF\n")) {
		t.Errorf("end %q", got[len(got)-20:])
	}
}

func TestWriteRefuses(t *testing.T) {
	d := t.TempDir()
	src, _ := image32(t, d, "2026-11-10T094200Z_3.2.mcm.hex")
	out := filepath.Join(d, "o.mcm.hex")
	absent := func(name string) {
		t.Helper()
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: a file was written", name)
			_ = os.Remove(out)
		}
	}
	for name, c := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no out":         {[]string{src}, 2, "--out"},
		"bad set":        {[]string{"--out", out, "--set", "Eco/Eco", src}, 2, "PATH=VALUE"},
		"unknown path":   {[]string{"--out", out, "--set", "Eco/Nope=1", src}, 1, "no EEPROM entry"},
		"bad value":      {[]string{"--out", out, "--set", "Eco/Eco=x", src}, 1, "not a number"},
		"state block":    {[]string{"--out", out, "--set", "PID 1/Setpoint=90", src}, 1, "no EEPROM entry"},
		"same path":      {[]string{"--out", out, "--set", "Eco/Eco=46", "--set", "Eco/Eco=47", src}, 2, "twice"},
		"out is source":  {[]string{"--out", src, src}, 2, "source"},
		"two backups":    {[]string{"--out", out, src, filepath.Join(d, "other.mcm.hex")}, 2, "exactly one"},
		"no layout":      {[]string{"--firmware", "9.9", "write", "--out", out, "--set", "Eco/Eco=46", src}, 1, "no layout"},
		"mcu only":       {[]string{"--out", out, filepath.Join(d, "only.mcu.txt")}, 1, "no MCm"},
		"damaged source": {[]string{"--out", out, filepath.Join(d, "bad", "bad.mcm.hex")}, 1, "damaged"},
	} {
		if name == "two backups" {
			image32(t, d, "other.mcm.hex")
		}
		write(t, d, "only.mcu.txt", backuptest.ValidMCu())
		_ = os.MkdirAll(filepath.Join(d, "bad"), 0o755)
		write(t, filepath.Join(d, "bad"), "bad.mcm.hex", backuptest.ValidMCm()[:2])
		args := c.args
		if args[0] != "--firmware" {
			args = append([]string{"write"}, args...)
		}
		code, _, errs := run0(t, args...)
		if code != c.code || !strings.Contains(errs, c.want) {
			t.Errorf("%s: code %d (want %d), err %q (want %q)", name, code, c.code, errs, c.want)
		}
		absent(name)
		_ = os.Remove(filepath.Join(d, "other.mcm.hex"))
	}

	// An existing file is kept unless --force.
	if err := os.WriteFile(out, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run0(t, "write", "--out", out, src); code != 1 || !strings.Contains(errs, "exists") {
		t.Errorf("existing: code %d err %q", code, errs)
	}
	if got, _ := os.ReadFile(out); string(got) != "keep" {
		t.Error("existing file was overwritten")
	}
	if code, _, errs := run0(t, "write", "--force", "--out", out, src); code != 0 {
		t.Errorf("--force: code %d err %q", code, errs)
	}
	want, _ := os.ReadFile(src)
	if got, _ := os.ReadFile(out); !bytes.Equal(got, want) {
		t.Error("--force did not write the image")
	}
}
