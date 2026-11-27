package backup

import (
	"reflect"
	"strings"
	"testing"
)

// blockLayout has one fixed entry and a 4-byte block that moves within
// 0x04-0x10.
var blockLayout = Layout{
	Firmware: "t",
	Entries: []Entry{
		{0x0000, "u16", 0.1, 0, "Kc", "", false, "confirmed", ""},
	},
	State: Block{From: 0x04, To: 0x10, Size: 4, Entries: []Entry{
		{1, "u8", 1, 0, "saves", "", false, "likely", ""},
		{2, "u16", 0.1, 0, "setpoint", "Alpha/Gain", true, "likely", ""},
	}},
}

// blockImage returns a 32-byte image with the state block at base.
func blockImage(base int, saves byte, setpoint uint16) []byte {
	img := make([]byte, 32)
	if base >= 0 {
		copy(img[base:], []byte{6, saves, byte(setpoint), byte(setpoint >> 8)})
	}
	return img
}

func TestBlockFind(t *testing.T) {
	b := blockLayout.State
	for _, base := range []int{0x04, 0x08, 0x0c} {
		if got, ok := b.Find(blockImage(base, 1, 880)); !ok || got != base {
			t.Errorf("block at %#x: got %#x, %v", base, got, ok)
		}
	}
	if _, ok := b.Find(blockImage(-1, 0, 0)); ok {
		t.Error("found a block in an empty region")
	}
	two := blockImage(0x04, 1, 880)
	two[0x0c] = 6
	if _, ok := b.Find(two); ok {
		t.Error("found a block although the region holds more than one")
	}
	if _, ok := b.Find(blockImage(0x0e, 1, 880)); ok {
		t.Error("found a block that runs past the region")
	}
	if _, ok := b.Find(make([]byte, 8)); ok {
		t.Error("found a block in an image shorter than the region")
	}
	if _, ok := (Block{}).Find(blockImage(0x04, 1, 880)); ok {
		t.Error("a layout without a block found one")
	}
}

func TestLayoutAt(t *testing.T) {
	got := blockLayout.At(blockImage(0x08, 7, 880))
	var offs []int
	for _, e := range got {
		offs = append(offs, e.Offset)
	}
	if !reflect.DeepEqual(offs, []int{0x00, 0x09, 0x0a}) {
		t.Errorf("offsets %#x", offs)
	}
	if raw, _, _ := got[1].Decode(blockImage(0x08, 7, 880)); raw != 7 {
		t.Errorf("saves = %d", raw)
	}
	// Without a block only the fixed entries are left.
	if got := blockLayout.At(blockImage(-1, 0, 0)); len(got) != 1 {
		t.Errorf("no block: %+v", got)
	}
}

func TestCrossCheckBlock(t *testing.T) {
	u := mcuBackup(gainLines("88.0")).MCu
	if p := CrossCheck(u, &MCm{Image: blockImage(0x0c, 1, 880)}, blockLayout); len(p) != 0 {
		t.Errorf("match: %+v", p)
	}
	p := CrossCheck(u, &MCm{Image: blockImage(0x0c, 1, 920)}, blockLayout)
	if len(p) != 1 || !strings.Contains(p[0].Text, "EEPROM 0x000e says 92.0") {
		t.Errorf("mismatch: %+v", p)
	}
}

func TestDiffBlockSamePlace(t *testing.T) {
	got := Diff(mcmBackup(blockImage(0x08, 1, 880)), mcmBackup(blockImage(0x08, 2, 880)), blockLayout, true)
	want := []Change{{"eeprom", "0x0009", "1", "2", "saves (likely)"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

// A moved block is compared by meaning: one row for the move, then only
// the entries and unknown block bytes that differ.
func TestDiffBlockMoved(t *testing.T) {
	a, b := blockImage(0x04, 255, 880), blockImage(0x08, 0, 880)
	b[0x08] = 7 // an unknown byte of the block
	got := Diff(mcmBackup(a), mcmBackup(b), blockLayout, true)
	want := []Change{
		{"eeprom", "0x0008", "0x0004", "0x0008", "state block moved"},
		{"eeprom", "0x0009", "255", "0", "saves (likely); at 0x0005 in A"},
		{"eeprom", "0x0008", "06", "07", "in the state block; at 0x0004 in A"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	// A is not changed by the comparison.
	if !reflect.DeepEqual(a, blockImage(0x04, 255, 880)) {
		t.Error("Diff changed image A")
	}
}

// With the block missing on one side its bytes diff as unknown bytes.
func TestDiffBlockOneSide(t *testing.T) {
	got := Diff(mcmBackup(blockImage(-1, 0, 0)), mcmBackup(blockImage(0x08, 1, 0)), blockLayout, true)
	want := []Change{{"eeprom", "0x0008", "0000", "0601", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestLayout32State(t *testing.T) {
	l, _ := LayoutFor("3.2")
	img := make([]byte, 4096)
	block := []byte{0x06, 0x39, 0x70, 0x03, 0x98, 0x03, 0x78, 0x1c, 0x0b}
	for _, base := range []int{0x0021, 0x003F, 0x005D} {
		clear(img)
		copy(img[base:], block)
		img[0x0083] = 0xf1 // first byte after the region
		got, ok := l.State.Find(img)
		if !ok || got != base {
			t.Fatalf("block at %#x: got %#x, %v", base, got, ok)
		}
		vals := map[string]string{}
		for _, e := range l.At(img) {
			if e.Offset >= base && e.Offset < base+l.State.Size {
				_, v, _ := e.Decode(img)
				vals[e.Name] = e.Format(v)
			}
		}
		for name, want := range map[string]string{
			"PID 1 setpoint (°)":          "88.0",
			"PID 2 setpoint (°)?":         "92.0",
			"flow-meter ticks, lifetime?": "728184",
			"run counter?":                "57",
		} {
			if vals[name] != want {
				t.Errorf("block at %#x: %s = %q, want %q (all: %v)", base, name, vals[name], want, vals)
			}
		}
	}
	if got, want := len(l.State.Entries), 7; got != want {
		t.Errorf("got %d state entries, want %d", got, want)
	}
}
