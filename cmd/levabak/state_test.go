package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

// eepromState returns an MCm dump with the 3.2 state block at base: PID 1
// setpoint sp (tenths) and the lifetime tick counter.
func eepromState(base int, sp uint16, ticks uint32) []string {
	img := make([]byte, 0x180)
	copy(img[base:], []byte{
		6, 1, byte(sp), byte(sp >> 8), 0x98, 0x03,
		byte(ticks), byte(ticks >> 8), byte(ticks >> 16),
	})
	var lines []string
	for a := 0; a < len(img); a += 16 {
		lines = append(lines, backuptest.Rec(a, img[a:a+16]))
	}
	return append(lines, ":00000001F")
}

// The counters sit in a block that moves: show reads them where the
// block is.
func TestShowEEPROMState(t *testing.T) {
	d := t.TempDir()
	for base, want := range map[int]string{0x0021: "0x0027", 0x003F: "0x0045"} {
		p := write(t, d, "2026-11-10T094200Z_3.2.mcm.hex", eepromState(base, 880, 728184))
		_, out, _ := run0(t, "show", "--eeprom", "--format", "jsonl", p)
		found := false
		for _, r := range jsonRows(t, out) {
			if r["name"] == "flow-meter ticks, lifetime?" {
				found = true
				if r["offset"] != want || r["raw"] != float64(728184) {
					t.Errorf("block at %#x: row %v", base, r)
				}
			}
		}
		if !found {
			t.Errorf("block at %#x: no tick counter row: %s", base, out)
		}
		_, out, _ = run0(t, "show", "--eeprom", "--format", "jsonl", "--path", "PID 1/Setpoint", p)
		if r := jsonRows(t, out); len(r) != 1 || r[0]["value"] != float64(88) {
			t.Errorf("block at %#x: setpoint by path: %s", base, out)
		}
	}
}

func TestDiffStateMoved(t *testing.T) {
	d := t.TempDir()
	write(t, d, "2026-11-10T094200Z_3.2.mcm.hex", eepromState(0x0021, 880, 1000))
	write(t, d, "2026-11-11T094200Z_3.2.mcm.hex", eepromState(0x003F, 890, 1006))
	_, out, _ := run0(t, "diff", "--format", "jsonl", d)
	var got []string
	for _, r := range jsonRows(t, out) {
		got = append(got, fmt.Sprint(r["key"], " ", r["a"], " ", r["b"], " ", r["note"]))
	}
	want := []string{
		"0x003F 0x0021 0x003F state block moved",
		"0x0041 88.0 89.0 PID 1 setpoint (°) (likely); at 0x0023 in A",
		"0x0045 1000 1006 flow-meter ticks, lifetime? (likely); at 0x0027 in A",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	_, out, _ = run0(t, "diff", "--format", "jsonl", "--path", "PID 1/Setpoint", d)
	if r := jsonRows(t, out); len(r) != 1 || r[0]["key"] != "0x0041" {
		t.Errorf("by path: %s", out)
	}
}

func TestLayoutCmdState(t *testing.T) {
	_, out, _ := run0(t, "layout", "--format", "jsonl")
	for _, r := range jsonRows(t, out) {
		if r["name"] == "flow-meter ticks, lifetime?" {
			if r["offset"] != "state+0x06" {
				t.Errorf("row %v", r)
			}
			return
		}
	}
	t.Error("no tick counter row")
}
