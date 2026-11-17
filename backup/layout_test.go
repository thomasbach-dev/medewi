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
		{Offset: 0x0022, Type: "u8", Scale: 1, Confidence: "guess"},
		{Offset: 0x0027, Type: "u24", Scale: 1, Confidence: "likely"},
		{Offset: 0x002F, Type: "u24", Scale: 1, Confidence: "likely"},
		{Offset: 0x0033, Type: "u16", Scale: 1, Confidence: "likely"},
		{Offset: 0x0037, Type: "u8", Scale: 1, Confidence: "guess"},
		{Offset: 0x0173, Type: "u16", Scale: 0.1, MCuPath: "PID 1/Kc", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0175, Type: "u16", Scale: 0.1, MCuPath: "PID 1/Ti", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0177, Type: "u16", Scale: 0.1, MCuPath: "PID 1/Td", Compare: true, Confidence: "confirmed"},
		{Offset: 0x0179, Type: "u16", Scale: 0.1, MCuPath: "PID 1/WU limit", Compare: true, Confidence: "likely"},
		{Offset: 0x04F9, Type: "u8", Scale: 1, MCuPath: "Profiles/Use", Confidence: "likely"},
	}
	if len(l.Entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(l.Entries), len(want))
	}
	end := -1
	for i, e := range l.Entries {
		w := want[i]
		if e.Offset != w.Offset || e.Type != w.Type || e.Scale != w.Scale ||
			e.MCuPath != w.MCuPath || e.Compare != w.Compare || e.Confidence != w.Confidence {
			t.Errorf("entry %d = %+v, want %+v", i, e, w)
		}
		if e.Name == "" || e.Evidence == "" {
			t.Errorf("entry %d lacks name or evidence", i)
		}
		if e.Offset < end {
			t.Errorf("entry %d at %#x overlaps or is out of order", i, e.Offset)
		}
		end = e.Offset + e.Width()
	}
	if _, ok := LayoutFor("unknown"); ok {
		t.Error("unknown firmware has a layout")
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
