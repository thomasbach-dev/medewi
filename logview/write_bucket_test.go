package logview

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteBucket(t *testing.T) {
	cols := []BucketCol{
		{Key: "pressure", Agg: Agg{Name: "max"}},
		{Key: "pressure_sp", Agg: Agg{Name: "max"}},
		{Key: "mode", Agg: Agg{Name: "last"}},
	}
	nine, empty := 9.5, ""
	runT := -time.Second
	run := &Run{N: 3}
	capture := BucketRow{
		Source: SourceInfo{Path: "c/log.jsonl", Notes: "warm-up"}, Run: run, RunT: &runT, Start: time.Second, Reports: 2,
		Values: []AggValue{{Num: &nine}, {}, {Str: &empty}},
	}
	wall := time.Date(2026, 11, 10, 10, 0, 30, 0, time.UTC)
	raw := capture
	raw.Wall, raw.HasWall, raw.Start = wall, true, 30*time.Second
	raw.Source, raw.XML = SourceInfo{Index: 1, Path: "r.jsonl.gz", HasWall: true}, "X"
	for _, tc := range []struct {
		f    Format
		want []string
	}{
		{FormatText, []string{
			"# c/log.jsonl",
			"# notes: warm-up",
			"t wall run run_t reports pressure_max pressure_sp_max mode_last",
			"1.000 - 3 -1.000 2 9.5 ? ?",
			"# r.jsonl.gz",
			"# xml: X",
			"t wall run run_t reports pressure_max pressure_sp_max mode_last",
			"30.000 2026-11-10 10:00:30.000 3 -1.000 2 9.5 ? ?",
			"30.000 2026-11-10 10:00:30.000 3 -1.000 2 9.5 ? ?",
		}},
		{FormatJSONL, []string{
			`{"t":1,"wall":null,"src":"c/log.jsonl","run":3,"run_t":-1,"reports":2,"pressure_max":9.5,"pressure_sp_max":null,"mode_last":""}`,
			`{"t":30,"wall":"2026-11-10T10:00:30.000Z","src":"r.jsonl.gz","run":3,"run_t":-1,"reports":2,"pressure_max":9.5,"pressure_sp_max":null,"mode_last":""}`,
			`{"t":30,"wall":"2026-11-10T10:00:30.000Z","src":"r.jsonl.gz","run":3,"run_t":-1,"reports":2,"pressure_max":9.5,"pressure_sp_max":null,"mode_last":""}`,
		}},
		{FormatCSV, []string{
			"t,wall,src,run,run_t,reports,pressure_max,pressure_sp_max,mode_last",
			"1,,c/log.jsonl,3,-1,2,9.5,,",
			"30,2026-11-10T10:00:30.000Z,r.jsonl.gz,3,-1,2,9.5,,",
			"30,2026-11-10T10:00:30.000Z,r.jsonl.gz,3,-1,2,9.5,,",
		}},
	} {
		var b bytes.Buffer
		w := NewBucketWriter(&b, tc.f, cols, true, time.UTC)
		for _, r := range []BucketRow{capture, raw, raw} {
			if err := w.Write(r); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		got := lines(b.String())
		if tc.f == FormatText {
			for i, l := range got {
				got[i] = strings.Join(strings.Fields(l), " ")
			}
		}
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s:\n%s\nwant\n%s", tc.f, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
		}
	}
}

func TestWriteBucketNoRunCols(t *testing.T) {
	cols := []BucketCol{{Key: "pressure", Agg: Agg{Name: "last"}, Plain: true}}
	for f, want := range map[Format]string{
		FormatText:  "t wall reports pressure",
		FormatCSV:   "t,wall,src,reports,pressure",
		FormatJSONL: "",
	} {
		var b bytes.Buffer
		w := NewBucketWriter(&b, f, cols, false, time.UTC)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(strings.Fields(strings.TrimSpace(b.String())), " "); got != want {
			t.Errorf("%s: %q, want %q", f, got, want)
		}
	}
}

func TestAggTextCell(t *testing.T) {
	x, empty, spaced := 1.5, "", "a b"
	for _, tc := range []struct {
		v    AggValue
		want string
	}{
		{AggValue{Num: &x}, "1.5"},
		{AggValue{}, "?"},
		{AggValue{Str: &empty}, "?"},
		{AggValue{Str: &spaced}, `"a b"`},
	} {
		if got := aggTextCell(tc.v); got != tc.want {
			t.Errorf("aggTextCell(%+v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}
