package logview

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

// TestTextEntries pins the text line of each non-report entry kind.
func TestTextEntries(t *testing.T) {
	loc := time.FixedZone("X", 3600)
	wall := time.Date(2026, 11, 10, 11, 0, 5, 7_000_000, time.UTC) // 12:00:05.007 in loc
	tests := []struct {
		name string
		e    Entry
		want string
	}{
		{
			"frame no wall",
			Entry{
				Offset: 1500 * time.Millisecond, Dir: fixture.RX, Kind: KindFrame,
				Frame: &leva.Frame{Backlight: true, Key: '0', Layout: '2', Rows: [4]string{"a", "b °C", "", "d"}},
			},
			"     1.500 - rx   frame    bkl=1 key=0 layout=2 |a|b °C||d|",
		},
		{
			"redacted",
			Entry{Wall: wall, HasWall: true, Dir: fixture.RX, Kind: KindRedacted, Count: 12},
			"     0.000 2026-11-10 12:00:05.007 rx   redacted dump redacted (12 lines)",
		},
		{
			"error",
			Entry{Dir: fixture.RX, Kind: KindReport, Raw: []byte("{bad}"), Err: errors.New("short line")},
			`     0.000 - rx   report   "{bad}"  # error: short line`,
		},
		{
			"raw report",
			Entry{Dir: fixture.RX, Kind: KindReport, Raw: []byte("{x}")},
			"     0.000 - rx   report   {x}",
		},
		{
			"glued fragment",
			Entry{Dir: fixture.RX, Kind: KindFragment, Glued: true, Raw: []byte("{cut")},
			"     0.000 - rx   fragment+ {cut",
		},
		{
			"glued banner pad",
			Entry{Dir: fixture.RX, Kind: KindBanner, Glued: true, Raw: []byte("$id: leva! (3.2)")},
			"     0.000 - rx   banner+  $id: leva! (3.2)",
		},
		{
			"tx multi",
			Entry{Dir: fixture.TX, Kind: KindCmd, Raw: []byte("MCh\r\nMC?")},
			`     0.000 - tx   cmd      "MCh\r\nMC?"`,
		},
		{
			"note",
			Entry{Dir: fixture.Note, Kind: KindNote, Raw: []byte("hello")},
			"     0.000 - note note     hello",
		},
		{
			"latin1",
			Entry{Dir: fixture.RX, Kind: KindReply, Raw: []byte("T\xb0C")},
			"     0.000 - rx   reply    T°C",
		},
		{
			"control",
			Entry{Dir: fixture.RX, Kind: KindPartial, Raw: []byte("a\x01")},
			`     0.000 - rx   partial  "a\x01"`,
		},
	}
	for _, tc := range tests {
		var b bytes.Buffer
		w := NewWriter(&b, FormatText, WriterOptions{Loc: loc})
		if err := w.Write(Row{Entry: tc.e}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, _ := strings.CutPrefix(b.String(), "# \n") // the PATH header of an unnamed source
		if got != tc.want+"\n" {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want+"\n")
		}
	}
}
