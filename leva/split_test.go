package leva

import (
	"reflect"
	"testing"
)

func TestSplitLine(t *testing.T) {
	report := "{ -1PID091.20091.0008-001.70009.20000.00000~ 6D}"
	frame := "<102WARM-UP         01m:34s                       0% 0.0b\x05  4s 239ml>"
	cut := frame[:40]
	type piece struct {
		data string
		frag bool
	}
	for _, tc := range []struct {
		name string
		line string
		want []piece
	}{
		{"intact frame with <100 in its text", "<102MENU   <100ms  " + frame[19:], []piece{{"<102MENU   <100ms  " + frame[19:], false}}},
		{"frame cut, report", cut + report, []piece{{cut, true}, {report, false}}},
		{"report, frame glued on", report + frame, []piece{{report, false}, {frame, false}}},
		{"cut-off frame alone", cut, []piece{{cut, true}}},
		{"reply with <100", "P <100", []piece{{"P <100", false}}},
		{"fragment, banner", cut + "$id: leva! (3.2)", []piece{{cut, true}, {"$id: leva! (3.2)", false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []piece
			for _, p := range SplitLine([]byte(tc.line)) {
				got = append(got, piece{string(p.Data), p.Fragment})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitLine = %v, want %v", got, tc.want)
			}
		})
	}
}
