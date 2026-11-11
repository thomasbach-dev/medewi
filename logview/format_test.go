package logview

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

func TestFormatLine(t *testing.T) {
	loc := time.FixedZone("X", 3600)
	wall := time.Date(2026, 11, 10, 11, 0, 5, 7_000_000, time.UTC) // 12:00:05.007 in loc
	rec := &leva.Record{Fields: []leva.Field{
		{Spec: leva.ValueSpec{Name: "Heat PID", Index: 3}, Raw: "91.0"},
		{Spec: leva.ValueSpec{Name: "", Index: 7}, Raw: "?????"},
	}}
	tests := []struct {
		name string
		e    Entry
		want string
	}{
		{
			"report",
			Entry{Offset: 592123 * time.Millisecond, Wall: wall, HasWall: true, Dir: fixture.RX, Kind: KindReport, Record: rec},
			"   592.123 2026-11-10 12:00:05.007 rx   report   Heat PID=91.0; #7=?????",
		},
		{
			"frame no wall",
			Entry{
				Offset: 1500 * time.Millisecond, Dir: fixture.RX, Kind: KindFrame,
				Frame: &leva.Frame{Backlight: true, Key: 0, Layout: 2, Rows: [4]string{"a", "b °C", "", "d"}},
			},
			"     1.500 - rx   frame    bkl=1 key=0 layout=2 |a|b °C||d|",
		},
		{
			"redacted",
			Entry{Dir: fixture.RX, Kind: KindRedacted, Count: 12},
			"     0.000 - rx   redacted dump redacted (12 lines)",
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
		if err := FormatLine(&b, tc.e, loc); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := b.String(); got != tc.want+"\n" {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want+"\n")
		}
	}
}
