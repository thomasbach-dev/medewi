package program

import (
	"slices"
	"testing"
	"time"
)

func TestFrameKind(t *testing.T) {
	run := func(r2, r3, r4 string) [4]string { return [4]string{">Run            ", r2, r3, r4} }
	for _, c := range []struct {
		rows  [4]string
		kind  Kind
		stage string
	}{
		{[4]string{"WARM-UP         ", "03m:01s", "  0%", ""}, WarmUp, ""},
		{[4]string{"FLUSH           ", "00m:07s", " 86%", ""}, flushKind, ""},
		{run(" Flush          ", " DESCALER  126ml", " Purge          "), Descale, stageDescaler},
		{run(" Flush          ", " PURGE    1996ml", " Purge          "), Descale, stagePurge},
		{run(" Flush          ", " PAUSE   00m:19s", " Purge          "), Descale, ""},
		{run(" Flush          ", " EMPTY TRAY!    ", " Purge          "), Descale, ""},
		{run(" Flush          ", " FILL TANK!     ", " Purge          "), Descale, ""},
		{run(" Descale        ", " PURGE    1989ml", " Backflush      "), Purge, ""},
		{run(" Descale        ", " PAUSE   00m:19s", " Backflush      "), Purge, ""},
		{run(" Descale        ", " EMPTY TRAY!    ", " Backflush      "), Purge, ""},
		{run(" Purge          ", " PUMP ███▋      ", " Autotune...    "), Backflush, ""},
		{run(" Purge          ", " SOAK    00m:01s", " Autotune...    "), Backflush, ""},
		{run(" Purge          ", " WAIT ████      ", " Autotune...    "), Backflush, ""},
		{run(" Purge          ", " EMPTY          ", " Autotune...    "), Backflush, ""},
		{run(" Test flow     -", " WAIT ██    2.9b", " BACK           "), PressureTest, ""},
		{run(" Test valve     ", " WAIT ██        ", " Test pressure ✓"), FlowTest, ""},
		{[4]string{">>Autotune      ", " PID 1      WAIT", " PID 2", " BACK"}, Autotune, ""},
	} {
		k, stage, ok := frameKind(c.rows)
		if !ok || k != c.kind || stage != c.stage {
			t.Errorf("%q: %s %s %v, want %s %s", c.rows, k, stage, ok, c.kind, c.stage)
		}
	}
	for _, rows := range [][4]string{
		run(" Descale        ", "–Purge          ", " Backflush      "),
		run(" Purge          ", "–Backflush      ", " Autotune...    "),
		run(" Warm-Up        ", "–Flush          ", " Descale        "),
		run(" Test valve     ", "–Test flow     ✓", " Test pressure ✓"),
		{"MENU            ", " [1]           -", "–[2]       FLUSH", " [3]        COLD"},
		{">>Pump          ", " Press max 12.0b", "–Phase OPV   50°", " Phase off 100°"},
		{">Setup          ", " Warm-Up...     ", "–Flush...       ", " Descale...     "},
		{">>Autotune      ", " PID 1          ", " PID 2", " BACK"},
		{"MY PROFILE      ", "1: 90.0° – 90.0°", "  4%", " 9.0b✓ 20s  40ml"},
		{"FLUSHY PROFILE  ", "1: 90.0° – 90.0°", "  4%", ""},
	} {
		if k, _, ok := frameKind(rows); ok {
			t.Errorf("%q: %s, want none", rows, k)
		}
	}
}

// show feeds the frame rows at the present time.
func (s *seq) show(rows [4]string) { s.d.Frame(s.at, rows) }

var (
	purgeFrame = [4]string{">Run", " Descale        ", " PURGE     250ml", " Backflush      "}
	flushFrame = [4]string{"FLUSH           ", "00m:07s", "", ""}
)

func TestStatus(t *testing.T) {
	purge := func(rows ...[4]string) Run {
		s := newSeq()
		s.cold()
		s.used()
		s.idle(5)
		s.reset()
		s.rhythm(1)
		for _, r := range rows {
			s.show(r)
		}
		s.rhythm(1)
		s.idle(90)
		return only(t, "purge", s)
	}
	r := purge()
	if r.Status != Inferred || !slices.Equal(r.Evidence, []string{"report"}) {
		t.Errorf("no frames: %s %v", r.Status, r.Evidence)
	}
	r = purge(purgeFrame)
	if r.Status != Confirmed || !slices.Equal(r.Evidence, []string{"report", "lcd"}) {
		t.Errorf("agreeing: %s %v", r.Status, r.Evidence)
	}
	r = purge(purgeFrame, flushFrame)
	if r.Status != Conflict || r.Kind != Purge || len(r.Notes) != 1 || r.Notes[0] != "LCD shows flush, reports say purge" {
		t.Errorf("conflict: %+v", r)
	}
	if r = purge([4]string{">>Autotune", " PID 1", " PID 2", " BACK"}); r.Status != Inferred {
		t.Errorf("autotune menu: %s, want inferred", r.Status)
	}
	if r = purge([4]string{">Run", " Test valve", "–Test flow", " Test pressure"}); r.Status != Inferred {
		t.Errorf("menu frame: %s, want inferred", r.Status)
	}
}

func TestLCDOnly(t *testing.T) {
	s := newSeq()
	s.cold()
	s.used()
	s.idle(5)
	s.reset()
	s.pump(5, 0, nil)
	s.show(purgeFrame)
	s.pump(5, 0, nil)
	s.idle(90)
	r := only(t, "single purge pulse", s)
	if r.Kind != Purge || r.Status != LCD || !slices.Equal(r.Evidence, []string{"lcd"}) || len(r.Pulses) != 1 {
		t.Errorf("run %+v", r)
	}
	s.reset()
	s.pump(5, 85, nil)
	s.show(flushFrame)
	s.pump(5, 85, nil)
	s.idle(90)
	if rs := s.d.Runs(); len(rs) != 1 {
		t.Errorf("a flush on the LCD became a program: %v", kinds(rs))
	}
}

func TestFlowTestFinished(t *testing.T) {
	menu := func(mark string) [4]string {
		return [4]string{">Run", " Test valve     ", "–Test flow     " + mark, " Test pressure –"}
	}
	for _, c := range []struct {
		before, after string
		want          Outcome
	}{{"-", "✓", Finished}, {"✓", "✓", Unknown}, {"-", "-", Unknown}} {
		s := newSeq()
		s.cold()
		s.idle(5)
		s.show(menu(c.before))
		s.sweep(0, 110, 5, flat(0.1))
		s.at = s.at.Add(time.Second)
		s.show(menu(c.after))
		s.idle(90)
		if r := only(t, "flow test", s); r.Outcome != c.want {
			t.Errorf("%s -> %s: %s, want %s", c.before, c.after, r.Outcome, c.want)
		}
	}
}

// A kind that only the LCD names has no ramp, sweep or mode to judge by.
func TestLCDOnlyEveryKind(t *testing.T) {
	for _, rows := range [][4]string{
		{"WARM-UP", "03m:01s", "", ""},
		{">>Autotune", " PID 1      WAIT", " PID 2", " BACK"},
		{">Run", " Test flow     -", " WAIT ██", " BACK"},
		{">Run", " Test valve", " WAIT ██", " Test pressure –"},
		{">Run", " Purge", " PUMP ██", " Autotune..."},
		{">Run", " Flush", " DESCALER 1ml", " Purge"},
	} {
		s := newSeq()
		s.used()
		s.idle(5)
		s.flush(0.5, 0, nil)
		s.reset()
		s.pump(2, 0, nil)
		s.show(rows)
		s.pump(2, 0, nil)
		s.idle(90)
		if r := only(t, rows[2], s); r.Status != LCD || r.Outcome != Unknown || r.Figures != nil {
			t.Errorf("%q: %+v", rows, r)
		}
	}
}
