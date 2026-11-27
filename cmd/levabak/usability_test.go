package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

// damagedMCu has one damaged region: lines 5-6, starting in section Beta.
func damagedMCu() []string {
	c := backuptest.Cell
	short := c(1, "Mid2   5")
	return []string{
		c(0, "Alpha..."),   // 1
		c(1, "Gain   1.0"), // 2
		c(0, "Beta..."),    // 3
		c(1, "Mid    5"),   // 4
		short[:len(short)-3],
		c(3, "Deep   1"), // 6
		c(0, "Gamma..."), // 7
		c(1, "Zed    1"), // 8
		c(0, "Cancel"),   // 9
	}
}

func TestShortSrc(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(d)
	cwd, _ := os.Getwd()
	for in, want := range map[string]string{
		"sub/x.mcu.txt":                        "sub/x.mcu.txt",
		"x.mcu.txt":                            "x.mcu.txt",
		filepath.Join(cwd, "sub", "x.mcu.txt"): "sub/x.mcu.txt",
		"../other/x.mcu.txt":                   "x.mcu.txt",
		"/nonexistent/dir/x.mcu.txt":           "x.mcu.txt",
	} {
		if got := shortSrc(in); got != want {
			t.Errorf("shortSrc(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortSrcInOutput(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", backuptest.ValidMCu())
	_, out, errs := run0(t, "show", p)
	if strings.Contains(out, d) || strings.Contains(errs, d) {
		t.Errorf("long src in text: %q %q", out, errs)
	}
	_, out, _ = run0(t, "show", "--format", "jsonl", p)
	if !strings.Contains(out, d) {
		t.Errorf("jsonl lost the full src: %q", out)
	}
	t.Chdir(d)
	_, out, _ = run0(t, "show", filepath.Join(d, "2026-11-10T094200Z_3.2.mcu.txt"))
	if !strings.HasPrefix(out[strings.Index(out, "\n")+1:], "2026-11-10T094200Z_3.2.mcu.txt") {
		t.Errorf("not relative: %q", out)
	}
}

func TestTextEmptyCell(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", backuptest.ValidMCu())
	_, out, _ := run0(t, "show", p)
	if !strings.Contains(out, "·") {
		t.Errorf("no · marker: %q", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, " - ") || strings.HasSuffix(l, " -") {
			if !strings.Contains(l, "Gamma") { // the real value "-"
				t.Errorf("stray - in %q", l)
			}
		}
	}
	_, csvOut, _ := run0(t, "show", "--format", "csv", p)
	if strings.Contains(csvOut, "·") {
		t.Errorf("csv has ·: %q", csvOut)
	}
}

func TestCheckRegionRows(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", damagedMCu())
	code, out, _ := run0(t, "check", "--format", "jsonl", p)
	if code != 1 {
		t.Fatal(code)
	}
	rows := jsonRows(t, out)
	last := rows[len(rows)-1]
	want := "damaged region: lines 5-6, starting in section Beta; lines may be lost here and the settings in it may sit under wrong paths"
	if last["kind"] != "mcu" || last["line"] != float64(5) || last["problem"] != want {
		t.Errorf("last row %v", last)
	}
	nprob := 0
	for _, r := range rows {
		if !strings.HasPrefix(r["problem"].(string), "damaged region") {
			nprob++
		}
	}
	_, out, _ = run0(t, "check", p)
	wantV := filepath.Base(p) + ": damaged, " + string(rune('0'+nprob)) + " problems, 1 damaged region (lines 5-6)"
	if !strings.Contains(out, wantV) {
		t.Errorf("verdict: want %q in\n%s", wantV, out)
	}
}

func TestCheckRegionNoSection(t *testing.T) {
	d := t.TempDir()
	lines := damagedMCu()
	lines[0] = lines[0][:10] // line 1 broken before any section is known
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", lines)
	_, out, _ := run0(t, "check", "--format", "jsonl", p)
	if !strings.Contains(out, "damaged region: lines 1-") {
		t.Fatalf("no region row: %s", out)
	}
}

func TestShowNumberUnitFlag(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", damagedMCu())
	code, out, errs := run0(t, "show", "--format", "jsonl", p)
	if code != 0 {
		t.Fatal(code)
	}
	if got := keys(t, out); got != "src note line path label value number unit flag raw" {
		t.Errorf("keys %q", got)
	}
	flagged := map[float64]any{}
	for _, r := range jsonRows(t, out) {
		flagged[r["line"].(float64)] = r["flag"]
		if r["path"] == "Alpha/Gain" && (r["number"] != 1.0 || r["unit"] != nil) {
			t.Errorf("Gain row %v", r)
		}
	}
	if flagged[2] != nil || flagged[5] != "damaged" || flagged[6] != "damaged" {
		t.Errorf("flags %v", flagged)
	}
	wantW := filepath.Base(p) + ": damaged (see levabak check): settings in lines 5-6 may sit under wrong paths; flagged damaged"
	if !strings.Contains(errs, "levabak: "+wantW) {
		t.Errorf("warning: want %q in %q", wantW, errs)
	}
	// text: number and unit are omitted, flag shows.
	_, out, _ = run0(t, "show", p)
	hdr := strings.Fields(firstLine(out))
	if strings.Join(hdr, " ") != "src note line path label value flag raw" {
		t.Errorf("text header %v", hdr)
	}
	// A clean backup: no flag column in text, no warning.
	q := write(t, d, "2026-11-11T094200Z_3.2.mcu.txt", backuptest.ValidMCu())
	_, out, errs = run0(t, "show", q)
	hdr = strings.Fields(firstLine(out))
	if strings.Join(hdr, " ") != "src note line path label value raw" || strings.Contains(errs, "damaged") {
		t.Errorf("clean: %v %q", hdr, errs)
	}
	// Not a number: empty number and unit.
	_, out, _ = run0(t, "show", "--format", "jsonl", q)
	for _, r := range jsonRows(t, out) {
		if r["path"] == "Alpha/Duration" && (r["number"] != nil || r["unit"] != nil) {
			t.Errorf("duration row %v", r)
		}
	}
}

func TestShortNote(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	cwd, _ := os.Getwd()
	long := filepath.Join(cwd, "sub", "x.mcu.txt")
	for in, want := range map[string]string{
		"unpaired mcu file " + long:                        "unpaired mcu file sub/x.mcu.txt",
		"unpaired mcm file /elsewhere/y.mcm.hex":           "unpaired mcm file y.mcm.hex",
		"skipped /elsewhere/z.txt: not a backup file name": "skipped z.txt: not a backup file name",
		"something else /elsewhere/z.txt":                  "something else /elsewhere/z.txt",
	} {
		if got := shortNote(in); got != want {
			t.Errorf("shortNote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHelpLineLength(t *testing.T) {
	var b strings.Builder
	usage(&b)
	texts := []string{b.String()}
	for _, sub := range []string{"check", "show", "diff", "layout"} {
		_, _, errs := run0(t, sub, "--help")
		texts = append(texts, errs)
	}
	for _, text := range texts {
		for _, l := range strings.Split(text, "\n") {
			if strings.HasPrefix(l, "  levabak ") { // example lines carry comments
				continue
			}
			if n := len([]rune(l)); n > 80 {
				t.Errorf("help line of %d columns: %q", n, l)
			}
		}
	}
}

func TestCheckVerdictPlural(t *testing.T) {
	d := t.TempDir()
	u := backuptest.ValidMCu()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", u[:len(u)-1]) // one problem, one region
	_, out, _ := run0(t, "check", p)
	if !strings.Contains(out, ": damaged, 1 problem, 1 damaged region (lines ") {
		t.Errorf("singular: %q", out)
	}
	q := write(t, d, "2026-11-11T094200Z_3.2.mcu.txt", damagedMCu())
	_, out, _ = run0(t, "check", q)
	if !strings.Contains(out, " problems, 1 damaged region (lines 5-6)") {
		t.Errorf("plural problems: %q", out)
	}
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func TestShowPath(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", backuptest.ValidMCu())
	_, out, errs := run0(t, "show", "--format", "jsonl", "--path", "Alpha/", p)
	rows := jsonRows(t, out)
	if len(rows) != 5 || strings.Contains(errs, "no rows") {
		t.Errorf("Alpha/: %d rows, err %q", len(rows), errs)
	}
	_, out, _ = run0(t, "show", "--format", "jsonl", "--path", "alpha", p)
	if len(jsonRows(t, out)) != 0 {
		t.Errorf("case-insensitive match: %s", out)
	}
	code, out, errs := run0(t, "show", "--path", "Nope/", p)
	if code != 0 || out != "" || !strings.Contains(errs, `levabak: no rows match --path "Nope/"`) {
		t.Errorf("no match: %d %q %q", code, out, errs)
	}
}

func TestJSONNumber(t *testing.T) {
	for in, want := range map[string]string{"88.0": "88.0", "+5": "5", "-5": "-5", ".5": "0.5", "5.": "5", "007": "7", "0": "0", "0.5": "0.5"} {
		if got := jsonNumber(in); got != want {
			t.Errorf("jsonNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShowEEPROMPath(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcm.hex", eeprom(88))
	_, out, _ := run0(t, "show", "--eeprom", "--format", "jsonl", "--path", "PID 1/", p)
	rows := jsonRows(t, out)
	if len(rows) != 11 {
		t.Fatalf("want the 11 PID 1 entries, got %d: %s", len(rows), out)
	}
	_, out, _ = run0(t, "show", "--eeprom", "--format", "jsonl", "--path", "PID 1/Kc", p)
	if len(jsonRows(t, out)) != 1 {
		t.Errorf("Kc only: %s", out)
	}
}

func TestDiffPath(t *testing.T) {
	d := t.TempDir()
	a := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", mcu("1.0"))
	b := write(t, d, "2026-11-11T094200Z_3.2.mcu.txt", mcu("2.0"))
	write(t, d, "2026-11-10T094200Z_3.2.mcm.hex", eeprom(88))
	write(t, d, "2026-11-11T094200Z_3.2.mcm.hex", eeprom(90))
	_, out, _ := run0(t, "diff", "--format", "jsonl", "--path", "Alpha/G", d)
	rows := jsonRows(t, out)
	if len(rows) != 1 || rows[0]["key"] != "Alpha/Gain" {
		t.Errorf("setting prefix: %s", out)
	}
	_, out, _ = run0(t, "diff", "--format", "jsonl", "--path", "PID 1/", d)
	rows = jsonRows(t, out)
	if len(rows) != 1 || rows[0]["section"] != "eeprom" {
		t.Errorf("eeprom by MCuPath: %s", out)
	}
	code, out, errs := run0(t, "diff", "--path", "Zzz", a, b)
	if code != 0 || out != "" || !strings.Contains(errs, `no rows match --path "Zzz"`) {
		t.Errorf("no match: %d %q %q", code, out, errs)
	}
}

func TestDiffPathSectionRow(t *testing.T) {
	d := t.TempDir()
	a := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", backuptest.ValidMCu())
	u := append([]string{backuptest.Cell(0, "Delta..."), backuptest.Cell(1, "Foo    1")}, backuptest.ValidMCu()...)
	b := write(t, d, "2026-11-11T094200Z_3.2.mcu.txt", u)
	_, out, _ := run0(t, "diff", "--format", "jsonl", a, b)
	rows := jsonRows(t, out)
	if len(rows) != 1 || rows[0]["key"] != "Delta/" || rows[0]["note"] != "section only in B" {
		t.Fatalf("section row: %s", out)
	}
	for _, pre := range []string{"Delta/", "Delta/Foo", "D", "Delta/"} {
		_, out, _ = run0(t, "diff", "--format", "jsonl", "--path", pre, a, b)
		if len(jsonRows(t, out)) != 1 {
			t.Errorf("--path %q dropped the section row: %q", pre, out)
		}
	}
	_, out, _ = run0(t, "diff", "--format", "jsonl", "--path", "Alpha/", a, b)
	if len(jsonRows(t, out)) != 0 {
		t.Errorf("--path Alpha/ kept it: %q", out)
	}
}
