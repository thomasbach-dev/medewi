package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

// run0 runs levabak and returns the exit code, stdout and stderr.
func run0(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func write(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// eeprom returns an MCm dump whose image holds kc (u16, tenths) at 0x0173.
func eeprom(kc uint16) []string {
	img := make([]byte, 0x180)
	img[0x173], img[0x174] = byte(kc), byte(kc>>8)
	var lines []string
	for a := 0; a < len(img); a += 16 {
		lines = append(lines, backuptest.Rec(a, img[a:a+16]))
	}
	return append(lines, ":00000001F")
}

// mcu returns ValidMCu with its Gain value replaced.
func mcu(gain string) []string {
	l := backuptest.ValidMCu()
	l[1] = backuptest.Cell(1, "Gain     "+gain+"%\xb0")
	return l
}

func jsonRows(t *testing.T, s string) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad jsonl %q: %v", l, err)
		}
		rows = append(rows, m)
	}
	return rows
}

// keys returns the key order of the first JSON object in s.
func keys(t *testing.T, s string) string {
	t.Helper()
	_, body, _ := strings.Cut(s, "{")
	dec := json.NewDecoder(strings.NewReader("{" + body))
	var order []string
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	for dec.More() {
		k, _ := dec.Token()
		order = append(order, k.(string))
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatal(err)
		}
	}
	return strings.Join(order, " ")
}

func TestHelp(t *testing.T) {
	code, _, errs := run0(t, "--help")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	for _, w := range []string{"check", "show", "diff", "layout", "license key", "Exit codes", "problems are rows on stdout", "no header", "past the end of the image", "uses A's", "--path", "·", "pid1.csv", "damaged region", "section only in", "silences"} {
		if !strings.Contains(errs, w) {
			t.Errorf("help lacks %q", w)
		}
	}
	code, _, errs = run0(t, "check", "--help")
	if code != 0 || !strings.Contains(errs, "usage: levabak") || !strings.Contains(errs, "--format") ||
		!strings.Contains(errs, "levabak --help") {
		t.Errorf("check --help: %d\n%s", code, errs)
	}
	if code, _, _ := run0(t, "bogus"); code != 2 {
		t.Errorf("unknown subcommand: %d", code)
	}
	if code, _, _ := run0(t); code != 2 {
		t.Errorf("no subcommand: %d", code)
	}
}

func TestCheckGood(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", backuptest.ValidMCu())
	code, out, _ := run0(t, "check", p)
	if code != 0 || strings.TrimSpace(out) != filepath.Base(p)+": ok" {
		t.Errorf("code %d out %q", code, out)
	}
}

func TestCheckDamaged(t *testing.T) {
	d := t.TempDir()
	u := backuptest.ValidMCu()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", u[:len(u)-1]) // no Cancel
	code, out, _ := run0(t, "check", p)
	if code != 1 || !strings.Contains(out, filepath.Base(p)+": damaged, 1 problem,") {
		t.Errorf("code %d out %q", code, out)
	}
	code, out, _ = run0(t, "check", "--format", "jsonl", p)
	if code != 1 {
		t.Fatal(code)
	}
	if got := keys(t, out); got != "src note line kind problem" {
		t.Errorf("keys %q", got)
	}
	rows := jsonRows(t, out)
	if _, ok := rows[0]["line"].(float64); !ok || rows[0]["kind"] != "mcu" {
		t.Errorf("row %v", rows[0])
	}
}

func TestCheckOneKindWarns(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", backuptest.ValidMCu())
	code, _, errs := run0(t, "check", p)
	if code != 0 || !strings.HasPrefix(errs, "levabak: ") || !strings.Contains(errs, "no MCm") {
		t.Errorf("code %d err %q", code, errs)
	}
}

func TestCheckCrossCheck(t *testing.T) {
	d := t.TempDir()
	u := append([]string{backuptest.Cell(0, "PID 1..."), backuptest.Cell(1, "Kc    9.9 %/°")},
		backuptest.ValidMCu()...)
	write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", u)
	write(t, d, "2026-11-10T094200Z_3.2.mcm.hex", eeprom(88))
	code, out, _ := run0(t, "check", "--format", "jsonl", d)
	if code != 1 || !strings.Contains(out, `"kind":"pair"`) {
		t.Errorf("code %d out %q", code, out)
	}
}

func TestShow(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2_morning.mcu.txt", backuptest.ValidMCu())
	code, out, _ := run0(t, "show", "--format", "csv", p)
	if code != 0 {
		t.Fatal(code)
	}
	recs, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(recs[0], " ") != "src note line path label value number unit flag raw" {
		t.Errorf("header %v", recs[0])
	}
	if !utf8.ValidString(out) || !strings.Contains(out, "°") {
		t.Errorf("not UTF-8 with degree sign: %q", out)
	}
	found := false
	for _, r := range recs[1:] {
		if r[3] == "Alpha/Gain" && r[1] == "morning" && r[2] == "2" && r[6] == "1.0" && r[7] == "%°" && r[8] == "" {
			found = true
		}
	}
	if !found {
		t.Errorf("no Alpha/Gain row:\n%s", out)
	}
	_, out, _ = run0(t, "show", "--format", "jsonl", p)
	if r := jsonRows(t, out); r[0]["line"] != float64(2) {
		t.Errorf("line not a number: %v", r[0])
	}
}

func TestShowEEPROM(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcm.hex", eeprom(88))
	code, out, _ := run0(t, "show", "--eeprom", "--format", "jsonl", p)
	if code != 0 {
		t.Fatal(code)
	}
	if got := keys(t, out); got != "src note offset type raw value name confidence" {
		t.Errorf("keys %q", got)
	}
	for _, r := range jsonRows(t, out) {
		if r["offset"] == "0x0173" {
			if r["raw"] != float64(88) || r["value"] != 8.8 || r["note"] != nil {
				t.Errorf("row %v", r)
			}
			return
		}
	}
	t.Errorf("no 0x0173 row: %s", out)
}

func TestDiff(t *testing.T) {
	d := t.TempDir()
	a := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", mcu("1.0"))
	b := write(t, d, "2026-11-11T094200Z_3.2.mcu.txt", mcu("2.0"))
	write(t, d, "2026-11-10T094200Z_3.2.mcm.hex", eeprom(88))
	write(t, d, "2026-11-11T094200Z_3.2.mcm.hex", eeprom(90))
	code, out, _ := run0(t, "diff", "--format", "jsonl", d)
	if code != 0 {
		t.Fatal(code)
	}
	if got := keys(t, out); got != "section key a b note" {
		t.Errorf("keys %q", got)
	}
	var secs []string
	for _, r := range jsonRows(t, out) {
		secs = append(secs, r["section"].(string)+":"+r["key"].(string))
	}
	if strings.Join(secs, ",") != "setting:Alpha/Gain,eeprom:0x0173" {
		t.Errorf("rows %v", secs)
	}
	if _, out, _ := run0(t, "diff", a, b); !strings.Contains(out, "Alpha/Gain") || strings.Contains(out, "0x0173") {
		t.Errorf("two MCu files: %q", out)
	}
	code, out, errs := run0(t, "diff", a, a)
	if code != 0 || !strings.Contains(errs, "no differences") || strings.Count(out, "\n") > 1 {
		t.Errorf("equal: %d %q %q", code, out, errs)
	}
	if code, _, errs := run0(t, "diff", a); code != 2 || !strings.Contains(errs, "found 1") {
		t.Errorf("one: %d %q", code, errs)
	}
	write(t, d, "2026-11-12T094200Z_3.2.mcu.txt", mcu("3.0"))
	if code, _, errs := run0(t, "diff", d); code != 2 || !strings.Contains(errs, "found 3") {
		t.Errorf("three: %d %q", code, errs)
	}
}

func TestDiffDamaged(t *testing.T) {
	d := t.TempDir()
	u := backuptest.ValidMCu()
	a := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", u[:len(u)-1])
	b := write(t, d, "2026-11-11T094200Z_3.2.mcu.txt", mcu("2.0"))
	code, out, errs := run0(t, "diff", a, b)
	if code != 0 || !strings.Contains(errs, "damaged, 1 problem (see levabak check)") || out == "" {
		t.Errorf("code %d out %q err %q", code, out, errs)
	}
}

func TestLayoutCmd(t *testing.T) {
	code, out, _ := run0(t, "layout", "--format", "jsonl")
	if code != 0 {
		t.Fatal(code)
	}
	if got := keys(t, out); got != "firmware offset type scale name confidence evidence" {
		t.Errorf("keys %q", got)
	}
	r := jsonRows(t, out)
	if r[0]["firmware"] != "3.2" || r[0]["scale"] != float64(1) {
		t.Errorf("row %v", r[0])
	}
	if !strings.Contains(out, `"0x0173"`) {
		t.Error("no 0x0173")
	}
	code, _, errs := run0(t, "--firmware", "unknown", "layout")
	if code != 1 || !strings.Contains(errs, "levabak: no layout for firmware unknown") {
		t.Errorf("unknown: %d %q", code, errs)
	}
}

func TestDiffUnknownFirmware(t *testing.T) {
	d := t.TempDir()
	write(t, d, "2026-11-10T094200Z_9.9.mcm.hex", eeprom(88))
	write(t, d, "2026-11-11T094200Z_9.9.mcm.hex", eeprom(90))
	code, out, errs := run0(t, "diff", "--format", "jsonl", d)
	if code != 0 || strings.Count(errs, "no layout for firmware 9.9") != 1 {
		t.Errorf("code %d err %q", code, errs)
	}
	r := jsonRows(t, out)
	if len(r) != 1 || r[0]["section"] != "eeprom" || r[0]["note"] != nil {
		t.Errorf("rows %v", r)
	}
}

func TestFirmwareOverride(t *testing.T) {
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_unknown.mcm.hex", eeprom(88))
	_, out, errs := run0(t, "show", "--eeprom", p)
	if strings.Contains(out, "0x0173") || !strings.Contains(errs, "no layout") {
		t.Errorf("without override: %q %q", out, errs)
	}
	code, out, _ := run0(t, "--firmware", "3.2", "show", "--eeprom", p)
	if code != 0 || !strings.Contains(out, "0x0173") {
		t.Errorf("override: %d %q", code, out)
	}
}

func TestErrors(t *testing.T) {
	if code, _, errs := run0(t, "check", "/nonexistent/x.mcu.txt"); code != 1 || !strings.HasPrefix(errs, "levabak: ") {
		t.Errorf("unreadable: %d %q", code, errs)
	}
	d := t.TempDir()
	p := write(t, d, "2026-11-10T094200Z_3.2.mcu.txt", backuptest.ValidMCu())
	if code, _, _ := run0(t, "check", "--format", "xml", p); code != 2 {
		t.Errorf("xml: %d", code)
	}
	if code, _, _ := run0(t, "check"); code != 2 {
		t.Errorf("no backup: %d", code)
	}
}
