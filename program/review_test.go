package program

import (
	"slices"
	"testing"
)

// A log that starts between two backflush pulses: the pressure trapped by
// the one before shows it.
func TestBackflushOpenStart(t *testing.T) {
	s := newSeq()
	s.used()
	s.st.Pressure = fp(9)
	s.idle(4)
	s.backflush(5)
	s.idle(90)
	if r := only(t, "mid-program", s); r.Outcome != Cut || !r.OpenStart {
		t.Errorf("mid-program: %s, open start %v", r.Outcome, r.OpenStart)
	}
	s = newSeq()
	s.used()
	s.st.Pressure = fp(1.5)
	s.idle(4)
	s.backflush(5)
	s.idle(90)
	if r := only(t, "some pressure left", s); r.OpenStart {
		t.Error("1.5 bar before the first pulse is no unseen start")
	}
}

// A first flush too short to tell a reset by: the second flush's first
// report may show as many ticks.
func TestShortFlushThenFlush(t *testing.T) {
	s := newSeq()
	s.used()
	s.idle(5)
	s.reset()
	s.pump(0.5, 0, nil)
	s.idle(10)
	s.st.Ticks, s.st.Volume = fp(6), fp(1)
	s.pump(20, 0, nil)
	s.idle(90)
	wantKinds(t, "short flush, flush", s)
}

func TestFlushesAtSetpointZero(t *testing.T) {
	s := newSeq()
	s.used()
	s.st.Setpoint = fp(0)
	s.idle(5)
	for range 2 {
		s.reset()
		s.pump(20, 0, nil)
		s.idle(3600)
	}
	wantKinds(t, "two flushes at setpoint 0", s)
}

func TestDescaleMaskedTicks(t *testing.T) {
	s := newSeq()
	s.used()
	s.st.Setpoint = fp(0)
	s.idle(5)
	s.reset()
	s.rhythm(2)
	ticks, vol := s.st.Ticks, s.st.Volume
	s.st.Ticks, s.st.Volume = nil, nil
	s.idle(1)
	s.st.Ticks, s.st.Volume = ticks, vol
	s.pump(10, 0, nil)
	s.idle(90)
	if rs := s.d.Runs(); len(rs) != 1 || len(rs[0].Pulses) != 2 {
		t.Errorf("masked ticks end the group: %+v", rs)
	}
}

// FILL TANK! resets the counter with the pump off.
func TestDescaleResetPumpOff(t *testing.T) {
	s := newSeq()
	s.used()
	s.st.Setpoint = fp(0)
	s.idle(5)
	s.reset()
	s.rhythm(3)
	s.reset()
	s.idle(120)
	s.rhythm(3)
	s.idle(90)
	if r := only(t, "descale", s); len(r.Pulses) != 6 || r.Pulses[2].Stage != stageDescaler || r.Pulses[3].Stage != stagePurge {
		t.Errorf("run %+v", r)
	}
}

func TestVolumeOfOpenPulse(t *testing.T) {
	s := newSeq()
	s.cold()
	s.used()
	s.idle(5)
	s.reset()
	s.rhythm(2)
	s.pump(5, 0, nil)
	s.d.Break()
	s.pump(5, 0, nil)
	s.idle(20)
	s.rhythm(1)
	s.idle(90)
	rs := wantKinds(t, "broken", s, Purge, Purge)
	if len(rs) == 2 && rs[1].Pulses[0].VolumeML != nil {
		t.Errorf("volume %v of a pulse whose start is unseen, want unknown", *rs[1].Pulses[0].VolumeML)
	}
}

func TestLCDOnlyCut(t *testing.T) {
	s := newSeq()
	s.cold()
	s.pump(5, 0, nil)
	s.show(purgeFrame)
	s.pump(5, 0, nil)
	s.idle(90)
	if r := only(t, "open start", s); r.Status != LCD || r.Outcome != Cut {
		t.Errorf("%s %s, want lcd cut", r.Status, r.Outcome)
	}
}

// Frames are sparse: a pulse without one, or with one beside it, belongs to
// the program around it.
func TestLCDOnlyMissingFrame(t *testing.T) {
	rows := [4]string{"WARM-UP", "03m:01s", "", ""}
	s := newSeq()
	s.used()
	s.idle(5)
	for k := range 5 {
		s.reset()
		switch k {
		case 2:
			s.pump(4, 50, nil)
		case 4:
			s.pump(4, 50, nil)
			s.idle(3)
			s.show(rows)
		default:
			s.pump(2, 50, nil)
			s.show(rows)
			s.pump(2, 50, nil)
		}
		s.idle(30)
	}
	s.idle(90)
	if r := only(t, "warm-up without a ramp", s); r.Kind != WarmUp || len(r.Pulses) != 5 {
		t.Errorf("%s with %d pulses, want warm-up with 5", r.Kind, len(r.Pulses))
	}
}

func TestFlushInAutotune(t *testing.T) {
	s := newSeq()
	s.used()
	s.st.Kc, s.st.Ti, s.st.Td = fp(1), fp(20), fp(5)
	s.idle(5)
	s.st.Mode, s.st.Kc, s.st.Ti, s.st.Td = "ATN", nil, nil, nil
	s.idle(60)
	s.reset()
	s.pump(5, 85, nil)
	s.show([4]string{">>Autotune", " PID 1      WAIT", " PID 2", " BACK"})
	s.pump(5, 85, nil)
	s.idle(60)
	s.st.Mode, s.st.Kc, s.st.Ti, s.st.Td = "PID", fp(2), fp(30), fp(7.5)
	s.idle(90)
	wantKinds(t, "flush in autotune", s, Autotune)
}

// The LCD outranks the settings' rename.
func TestProfileDescaleLCD(t *testing.T) {
	s := newSeq()
	s.d.SetSettings(Settings{DescalePID: fp(50)})
	s.used()
	s.st.Setpoint = fp(50)
	s.idle(5)
	s.reset()
	s.rhythm(1)
	s.show(purgeFrame)
	s.rhythm(2)
	s.idle(90)
	if r := only(t, "purge", s); r.Kind != Purge || r.Status != Confirmed {
		t.Errorf("%s %s %v, want a confirmed purge", r.Kind, r.Status, r.Notes)
	}
}

func TestSettingsUnreadableStep(t *testing.T) {
	m := slices.Clone(menu)
	m[slices.Index(m, "\t\tTime    01m:10s")] = "\t\tTime        60s"
	if s, missing := SettingsFrom(dump(m...)); s.WarmUp != nil || !slices.Contains(missing, "Warm-Up") {
		t.Errorf("warm-up %v, missing %v", s.WarmUp, missing)
	}
	m = slices.Clone(menu)
	for k, l := range m {
		if l == "\t\tDuration00m:02s" {
			m[k] = "\t\tDuration00m:00s"
		}
	}
	if s, missing := SettingsFrom(dump(m...)); len(s.WarmUp) != 0 || len(missing) != 0 {
		t.Errorf("no pulses scheduled: %v, missing %v", s.WarmUp, missing)
	}
}

func TestFlushWithReportGap(t *testing.T) {
	s := newSeq()
	s.used()
	s.idle(5)
	s.reset()
	s.pump(20, 0, nil)
	s.skip(25)
	s.pump(20, 0, nil)
	s.idle(90)
	wantKinds(t, "flush with a 25 s gap", s)
}

// Few ticks cannot prove that the counter went on, but the volume must not
// count the running total again.
func TestVolumeOfSmallPulses(t *testing.T) {
	s := newSeq()
	s.used()
	s.idle(5)
	s.reset()
	s.warmup()
	sum := 0.0
	r := only(t, "warm-up", s)
	for _, p := range r.Pulses {
		sum += *p.VolumeML
	}
	if want := float64(8 * len(r.Pulses)); len(r.Pulses) < 3 || sum != want {
		t.Errorf("%d pulses of 8 ml: %v ml, want %v", len(r.Pulses), sum, want)
	}
}
