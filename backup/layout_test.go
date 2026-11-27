package backup

import (
	"strings"
	"testing"
)

func TestLayout32(t *testing.T) {
	l, ok := LayoutFor("3.2")
	if !ok {
		t.Fatal("no 3.2 layout")
	}
	want := []Entry{
		{Offset: 0x0138, Type: "u16", Scale: 0.1, MCuPath: "PID 1/SP User 1", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0173, Type: "u16", Scale: 0.1, MCuPath: "PID 1/Kc", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0175, Type: "u16", Scale: 0.1, MCuPath: "PID 1/Ti", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0177, Type: "u16", Scale: 0.1, MCuPath: "PID 1/Td", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0179, Type: "u16", Scale: 0.1, MCuPath: "PID 1/WU limit", Compare: true, Confidence: "likely"},
		{Offset: 0x04F9, Type: "u8", Scale: 1, MCuPath: "Profiles/Use", Confidence: "likely"},
		// Profile slot 1 starts at 0x04FF, each slot is 0x6B bytes.
		{Offset: 0x050C, Type: "u16", Scale: 0.1, Sub: 1, MCuPath: "Profiles/Edit/1/Edit PI.../1/Time", Compare: true, Confidence: "likely"},
		{Offset: 0x0534, Type: "u16", Scale: 0.1, Sub: 1, MCuPath: "Profiles/Edit/1/Edit shot.../1/Time", Compare: true, Confidence: "likely"},
		{Offset: 0x064D, Type: "u16", Scale: 0.1, Sub: 1, MCuPath: "Profiles/Edit/4/Edit PI.../1/Time", Compare: true, Confidence: "confirmed"},
		{Offset: 0x064F, Type: "u16", Scale: 0.1, Sub: 181, MCuPath: "Profiles/Edit/4/Edit PI.../1/Pump", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0675, Type: "u16", Scale: 0.1, Sub: 1, MCuPath: "Profiles/Edit/4/Edit shot.../1/Time", Compare: true, Confidence: "confirmed"},
		{Offset: 0x067F, Type: "u16", Scale: 0.1, Sub: 181, MCuPath: "Profiles/Edit/4/Edit shot.../3/Pump", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0697, Type: "u16", Scale: 0.1, Sub: 181, MCuPath: "Profiles/Edit/4/Edit shot.../9/Pump", Compare: true, Confidence: "likely"},
		{Offset: 0x069D, Type: "u8", Scale: 1, MCuPath: "Profiles/Edit/4/Edit PI.../Repeat", Compare: true, Confidence: "confirmed"},
		{Offset: 0x069E, Type: "u8", Scale: 0.1, MCuPath: "Profiles/Edit/4/Edit PI.../Wait", Compare: true, Confidence: "confirmed"},
		{Offset: 0x06A0, Type: "u8", Scale: 1, MCuPath: "Profiles/Edit/4/End", Confidence: "likely"},
		{Offset: 0x06A7, Type: "u16", Scale: 0.1, MCuPath: "Profiles/Edit/4/PID 1", Compare: true, Confidence: "confirmed"},
		// Dose 1 starts at 0x079E, each dose is 0x11 bytes.
		{Offset: 0x07A2, Type: "u16", Scale: 0.1, MCuPath: "Doses/Ristretto/In", Compare: true, Confidence: "likely"},
		{Offset: 0x07A4, Type: "u16", Scale: 0.1, MCuPath: "Doses/Ristretto/Out", Compare: true, Confidence: "likely"},
		{Offset: 0x07A6, Type: "u16", Scale: 0.1, MCuPath: "Doses/Ristretto/Ratio 1 :", Compare: true, Confidence: "likely"},
		{Offset: 0x07C2, Type: "u16", Scale: 1, MCuPath: "Doses/Lungo/Volume", Compare: true, Confidence: "confirmed"},
		{Offset: 0x07E4, Type: "u16", Scale: 1, MCuPath: "Doses/Dbl Espresso/Volume", Compare: true, Confidence: "confirmed"},
		{Offset: 0x084A, Type: "u16", Scale: 1, MCuPath: "Doses/Americano XL/Volume", Compare: true, Confidence: "likely"},
		{Offset: 0x08AA, Type: "u16", Scale: 1, MCuPath: "Eco/Eco", Compare: true, Confidence: "confirmed"},
		{Offset: 0x08AC, Type: "u16", Scale: 1, MCuPath: "Eco/Standby", Compare: true, Confidence: "confirmed"},
	}
	byOffset := map[int]Entry{}
	end := -1
	for i, e := range l.Entries {
		byOffset[e.Offset] = e
		if e.Name == "" || e.Evidence == "" {
			t.Errorf("entry %d at %#x lacks name or evidence", i, e.Offset)
		}
		if e.Width() == 0 {
			t.Errorf("entry %d at %#x has unknown type %q", i, e.Offset, e.Type)
		}
		if e.Offset < end {
			t.Errorf("entry %d at %#x overlaps or is out of order", i, e.Offset)
		}
		end = e.Offset + e.Width()
	}
	for _, w := range want {
		e, ok := byOffset[w.Offset]
		if !ok {
			t.Errorf("no entry at %#x (%s)", w.Offset, w.MCuPath)
			continue
		}
		if e.Type != w.Type || e.Scale != w.Scale || e.Sub != w.Sub ||
			e.MCuPath != w.MCuPath || e.Compare != w.Compare || e.Confidence != w.Confidence {
			t.Errorf("entry at %#x = %+v, want %+v", w.Offset, e, w)
		}
	}
	// 6 fixed entries, 4 profile slots of 41 entries, 11 doses of 4,
	// 6 more in the PID 1 block, 2 eco timers. The counters are in the
	// state block (TestLayout32State).
	if got, want := len(l.Entries), 6+4*41+11*4+6+2; got != want {
		t.Errorf("got %d entries, want %d", got, want)
	}
	if _, ok := LayoutFor("unknown"); ok {
		t.Error("unknown firmware has a layout")
	}
}

func TestEntrySub(t *testing.T) {
	tm := Entry{Offset: 0, Type: "u16", Scale: 0.1, Sub: 1}
	raw, v, ok := tm.Decode([]byte{0x47, 0x00})
	if !ok || raw != 71 || tm.Format(v) != "7.0" {
		t.Errorf("time = %d %v %v", raw, v, ok)
	}
	pump := Entry{Offset: 0, Type: "u16", Scale: 0.1, Sub: 181}
	if _, v, _ := pump.Decode([]byte{0xd6, 0x00}); pump.Format(v) != "3.3" {
		t.Errorf("pump = %q", pump.Format(v))
	}
	// Raw 0 is "not set"; a raw value below Sub is another encoding.
	for _, b := range [][]byte{{0, 0}, {90, 0}} {
		raw, v, ok := pump.Decode(b)
		if !ok || raw != uint32(b[0]) || pump.Format(v) != "" {
			t.Errorf("raw %d = %v %q %v", b[0], v, pump.Format(v), ok)
		}
	}
}

func TestCrossCheckSub(t *testing.T) {
	l := Layout{Firmware: "test", Entries: []Entry{
		{Offset: 0, Type: "u16", Scale: 0.1, Sub: 181, MCuPath: "Alpha/Pump", Compare: true},
	}}
	mcu := func(v string) *MCu {
		return &MCu{Settings: []Setting{{Path: "Alpha/Pump", Value: v}}}
	}
	img := func(b ...byte) *MCm { return &MCm{Image: b} }
	if p := CrossCheck(mcu("3.3b"), img(0xd6, 0), l); len(p) != 0 {
		t.Errorf("match gave %v", p)
	}
	if p := CrossCheck(mcu("-"), img(0, 0), l); len(p) != 0 {
		t.Errorf("unset on both sides gave %v", p)
	}
	if p := CrossCheck(mcu("3.3b"), img(0, 0), l); len(p) != 1 {
		t.Errorf("unset in the EEPROM only gave %v", p)
	}
	// Below Sub the raw value is not a pressure; nothing to compare.
	if p := CrossCheck(mcu("85°"), img(90, 0), l); len(p) != 0 {
		t.Errorf("other encoding gave %v", p)
	}
}

func TestEntryDecode(t *testing.T) {
	e := Entry{Offset: 1, Type: "u16", Scale: 0.1}
	raw, v, ok := e.Decode([]byte{0, 0x59, 0x00})
	if !ok || raw != 89 || v < 8.89 || v > 8.91 {
		t.Errorf("u16 = %d %v %v", raw, v, ok)
	}
	e = Entry{Offset: 0, Type: "u24", Scale: 1}
	raw, v, ok = e.Decode([]byte{0xa1, 0x6d, 0x0a})
	if !ok || raw != 683425 || v != 683425 {
		t.Errorf("u24 = %d %v %v", raw, v, ok)
	}
	if _, _, ok := (Entry{Offset: 2, Type: "u24", Scale: 1}).Decode([]byte{1, 2, 3}); ok {
		t.Error("past-image entry decoded")
	}
}

func TestCrossCheckCompareFalse(t *testing.T) {
	l := Layout{Firmware: "test", Entries: []Entry{
		{Offset: 0, Type: "u8", Scale: 1, MCuPath: "Alpha/Use", Compare: false},
	}}
	u := &MCu{Settings: []Setting{{Path: "Alpha/Use", Value: "9BAR"}}}
	if p := CrossCheck(u, &MCm{Image: []byte{2}}, l); len(p) != 0 {
		t.Errorf("Compare=false gave %v", p)
	}
}

func TestEntryFormat(t *testing.T) {
	e := Entry{Offset: 0, Type: "u16", Scale: 0.1}
	_, v, _ := e.Decode([]byte{0xd3, 0x02})
	if got := e.Format(v); got != "72.3" {
		t.Errorf("Format = %q", got)
	}
	e = Entry{Offset: 0, Type: "u24", Scale: 1}
	_, v, _ = e.Decode([]byte{0xa1, 0x6d, 0x0a})
	if got := e.Format(v); got != "683425" {
		t.Errorf("Format = %q", got)
	}
}

func TestCrossCheck(t *testing.T) {
	l := Layout{Firmware: "test", Entries: []Entry{
		{Offset: 0, Type: "u16", Scale: 0.1, MCuPath: "Alpha/Gain", Compare: true},
		{Offset: 2, Type: "u8", Scale: 1, MCuPath: "Alpha/Missing", Compare: true},
		{Offset: 3, Type: "u8", Scale: 1},
	}}
	mcu := func(v string) *MCu {
		return &MCu{Settings: []Setting{{Path: "Alpha/Gain", Value: v}}}
	}
	img := func(b ...byte) *MCm { return &MCm{Image: b} }

	if p := CrossCheck(mcu("8.8%/°"), img(0x58, 0, 0, 0), l); len(p) != 0 {
		t.Errorf("match gave %v", p)
	}
	p := CrossCheck(mcu("8.8%/°"), img(0x59, 0, 0, 0), l)
	if len(p) != 1 || p[0].Kind != KindPair {
		t.Fatalf("mismatch gave %v", p)
	}
	for _, s := range []string{"Alpha/Gain", "8.8", "8.9"} {
		if !strings.Contains(p[0].Text, s) {
			t.Errorf("problem %q lacks %q", p[0].Text, s)
		}
	}
	if p := CrossCheck(mcu("DARK"), img(0x59, 0, 0, 0), l); len(p) != 0 {
		t.Errorf("non-numeric gave %v", p)
	}
}
