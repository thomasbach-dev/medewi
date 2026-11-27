package backup

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func TestEncodeMCmRoundTrip(t *testing.T) {
	src := backuptest.ValidMCm()
	m := ParseMCm(src)
	got := EncodeMCm(m.Image, false)
	if want := strings.Join(src, "\n") + "\n"; string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	std := EncodeMCm(m.Image, true)
	if !bytes.HasSuffix(std, []byte("\n:00000001FF\n")) {
		t.Errorf("standard end record: %q", std)
	}
	back := ParseMCm(SplitLines(std))
	if len(back.Problems) != 0 || !bytes.Equal(back.Image, m.Image) {
		t.Errorf("standard form does not read back: %+v", back.Problems)
	}
}

func TestEncodeMCmShortLastRecord(t *testing.T) {
	img := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18}
	back := ParseMCm(SplitLines(EncodeMCm(img, false)))
	if len(back.Problems) != 0 || back.Records != 2 || !bytes.Equal(back.Image, img) {
		t.Errorf("records %d, problems %+v, image %v", back.Records, back.Problems, back.Image)
	}
}

func TestEntryEncode(t *testing.T) {
	tm := Entry{Type: "u16", Scale: 0.1, Sub: 1}
	pump := Entry{Type: "u16", Scale: 0.1, Sub: 181}
	plain := Entry{Type: "u8", Scale: 1}
	for _, c := range []struct {
		e    Entry
		in   string
		want uint32
	}{
		{tm, "7.0", 71},
		{tm, "7", 71},
		{tm, "0", 1},
		{tm, "-", 0},
		{pump, "3.3", 214},
		{pump, "3.3b", 214},
		{pump, " 9.0 ", 271},
		{plain, "255", 255},
		{plain, "0", 0},
	} {
		got, err := c.e.Encode(c.in)
		if err != nil || got != c.want {
			t.Errorf("%+v Encode(%q) = %d, %v; want %d", c.e, c.in, got, err, c.want)
		}
	}
	for _, c := range []struct {
		e  Entry
		in string
	}{
		{plain, "256"}, // does not fit
		{plain, "-1"},  // negative
		{plain, "1.5"}, // not a multiple of the scale
		{tm, "7.05"},   // not a multiple of the scale
		{plain, "-"},   // no "not set" without Sub
		{plain, ""},    // nothing
		{plain, "abc"}, // no number
		{tm, "6553.6"}, // does not fit u16 after Sub
		{Entry{}, "1"}, // unknown type
	} {
		if got, err := c.e.Encode(c.in); err == nil {
			t.Errorf("%+v Encode(%q) = %d, want an error", c.e, c.in, got)
		}
	}
}

// setLayout: checksum at 0x02 over 0x04-0x0B, a block region behind it.
// "Alpha/Edge" has its high byte past the region.
var setLayout = Layout{
	Firmware: "t",
	Entries: []Entry{
		{0x04, "u16", 0.1, 0, "gain", "Alpha/Gain", true, "confirmed", ""},
		{0x06, "u8", 1, 0, "mode", "Alpha/Mode", false, "likely", ""},
		{0x08, "u16", 0.1, 1, "time", "Alpha/Time", true, "likely", ""},
		{0x0A, "u16", 1, 0, "guessed", "Alpha/Guess", true, "guess", ""},
		{0x0B, "u16", 1, 0, "straddles the region's end", "Alpha/Edge", true, "likely", ""},
		{0x0E, "u8", 1, 0, "outside", "Alpha/Outside", true, "confirmed", ""},
		{0x0F, "u8", 1, 0, "no path", "", false, "confirmed", ""},
	},
	Checksum: Checksum{Offset: 0x02, From: 0x04, To: 0x0C},
	State: Block{From: 0x10, To: 0x18, Size: 4, Entries: []Entry{
		{2, "u16", 0.1, 0, "setpoint", "Alpha/Setpoint", true, "likely", ""},
	}},
}

func setImage() []byte {
	img := make([]byte, 32)
	img[0x02] = 0x5a
	img[0x04], img[0x05] = 0x58, 0x00
	copy(img[0x14:], []byte{6, 1, 0x70, 0x03})
	return img
}

func TestLayoutSet(t *testing.T) {
	img := setImage()
	e, err := setLayout.Set(img, "Alpha/Gain", "30.1")
	if err != nil || e.Offset != 0x04 {
		t.Fatalf("Set: %+v, %v", e, err)
	}
	want := setImage()
	want[0x04], want[0x05] = 0x2d, 0x01    // 301
	want[0x02] = 0x5a ^ 0x58 ^ 0x2d ^ 0x01 // old low byte out, new bytes in
	if !reflect.DeepEqual(img, want) {
		t.Errorf("got  %x\nwant %x", img, want)
	}

	// A second edit builds on the first; setting the old value back
	// restores the image.
	if _, err := setLayout.Set(img, "Alpha/Time", "-"); err != nil {
		t.Fatal(err)
	}
	if _, err := setLayout.Set(img, "Alpha/Mode", "3"); err != nil {
		t.Fatal(err)
	}
	for path, v := range map[string]string{"Alpha/Gain": "8.8", "Alpha/Mode": "0"} {
		if _, err := setLayout.Set(img, path, v); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(img, setImage()) {
		t.Errorf("after setting back: %x", img)
	}
}

func TestLayoutSetRefuses(t *testing.T) {
	for _, c := range []struct{ path, value, want string }{
		{"Alpha/Nope", "1", "no EEPROM entry"},
		{"", "1", "no EEPROM entry"},
		{"Alpha/Guess", "1", "guess"},
		{"Alpha/Edge", "256", "checksum"}, // changes the byte past the region
		{"Alpha/Outside", "1", "checksum"},
		{"Alpha/Setpoint", "90", "checksum"},
		{"Alpha/Gain", "x", "Alpha/Gain"},
	} {
		img := setImage()
		_, err := setLayout.Set(img, c.path, c.value)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Set(%q, %q): %v, want an error with %q", c.path, c.value, err, c.want)
		}
		if !reflect.DeepEqual(img, setImage()) {
			t.Errorf("Set(%q, %q) changed the image", c.path, c.value)
		}
	}
	// Only the bytes that change must lie in the region.
	if _, err := setLayout.Set(setImage(), "Alpha/Edge", "7"); err != nil {
		t.Errorf("edit of the last covered byte: %v", err)
	}
	// A layout without a known checksum sets nothing.
	l := setLayout
	l.Checksum = Checksum{}
	if _, err := l.Set(setImage(), "Alpha/Gain", "1"); err == nil {
		t.Error("set without a checksum")
	}
	// An entry past the image.
	if _, err := setLayout.Set(make([]byte, 5), "Alpha/Gain", "1"); err == nil {
		t.Error("set past the image")
	}
}

func TestLayout32Set(t *testing.T) {
	l, _ := LayoutFor("3.2")
	if want := (Checksum{Offset: 0x0087, From: 0x0128, To: 0x08AD}); l.Checksum != want {
		t.Errorf("checksum %+v, want %+v", l.Checksum, want)
	}
	img := make([]byte, 4096)
	if _, err := l.Set(img, "Profiles/Edit/4/Edit shot.../1/Time", "7.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Set(img, "Eco/Standby", "90"); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 4096)
	want[0x0675], want[0x08AC], want[0x0087] = 71, 90, 71^90
	if !bytes.Equal(img, want) {
		t.Error("image differs from the two entries plus the checksum")
	}
}
