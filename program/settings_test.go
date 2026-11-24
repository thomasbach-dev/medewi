package program

import (
	"slices"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/backup"
	"github.com/thomasbach-dev/medewi/backup/backuptest"
)

func dump(lines ...string) *backup.MCu {
	var out []string
	for _, l := range lines {
		d := strings.Count(l, "\t")
		out = append(out, backuptest.Cell(d, strings.TrimLeft(l, "\t")))
	}
	return backup.ParseMCu(out)
}

var menu = []string{
	"PID 1...", "\tWarm-Up...", "\t\t1...", "\t\t\tTime    09m:00s",
	"Warm-Up...", "\t1...", "\t\tTime    00m:10s", "\t\tDuration00m:02s", "\t\tPumpPwr     50\xb0",
	"\t2...", "\t\tTime    00m:00s", "\t\tDuration00m:00s",
	"\t3...", "\t\tTime    01m:10s", "\t\tDuration00m:02s",
	"Descale...", "\tEdit...", "\t\tACID...", "\t\t\tPID 1        -\xb0", "\t\tCITRIC...", "\t\t\tPID 1     50.0\xb0",
	"\tUse      CITRIC", "\tPurge     200ml",
	"Backflush...", "\tCycles        3", "\tSoak    00m:10s", "\tRinse   00m:12s",
	"Cancel",
}

func TestSettingsFrom(t *testing.T) {
	s, missing := SettingsFrom(dump(menu...))
	if len(missing) != 0 {
		t.Errorf("missing %v", missing)
	}
	if !slices.Equal(s.WarmUp, []float64{10, 70}) || s.Backflush == nil || *s.Backflush != (BackflushSettings{3, 10, 12}) ||
		s.DescalePID == nil || *s.DescalePID != 50 || s.PurgeML == nil || *s.PurgeML != 200 {
		t.Errorf("settings %+v", s)
	}
	acid := slices.Clone(menu)
	acid[slices.Index(acid, "\tUse      CITRIC")] = "\tUse        ACID"
	if s, _ = SettingsFrom(dump(acid...)); s.DescalePID == nil || *s.DescalePID != 0 {
		t.Errorf("PID - : %v, want 0", s.DescalePID)
	}
	s, missing = SettingsFrom(dump("Alpha...", "\tGain  1.0%", "Cancel"))
	if s.Backflush != nil || s.DescalePID != nil || s.WarmUp != nil || !slices.Equal(missing, []string{"Warm-Up", "Backflush", "Descale"}) {
		t.Errorf("foreign dump: %+v, missing %v", s, missing)
	}
}

func TestSettingsCompare(t *testing.T) {
	st, _ := SettingsFrom(dump(menu...))
	acid := st
	acid.DescalePID = fp(0)
	descale := func(s *seq) { s.st.Setpoint = fp(0); s.idle(1); s.reset(); s.rhythm(4) }
	// 3 cycles of 6 s, 10 s apart; a rinse of two 6 s pulses.
	flush := func(s *seq) { s.backflush(3); s.idle(30); s.backflush(2) }
	for _, c := range []struct {
		name string
		st   Settings
		f    func(*seq)
		want string
	}{
		{"backflush", st, flush, "yes"},
		{"backflush, quick key press", st, func(s *seq) { s.backflush(5) }, "yes"},
		{"backflush, other cycles", Settings{Backflush: &BackflushSettings{4, 10, 12}}, flush, "no"},
		{"backflush, other soak", Settings{Backflush: &BackflushSettings{3, 20, 12}}, flush, "no"},
		{"backflush, other rinse", Settings{Backflush: &BackflushSettings{3, 10, 60}}, flush, "no"},
		{"backflush, no settings", Settings{}, flush, ""},
		{"descale", acid, descale, "yes"},
		{"descale, other profile", st, descale, "no"},
		{"warm-up", Settings{WarmUp: []float64{10, 70, 130, 190}}, (*seq).warmup, "yes"},
		{"warm-up, other schedule", Settings{WarmUp: []float64{33, 99, 170}}, (*seq).warmup, "no"},
		{"purge", st, func(s *seq) { s.cold(); s.idle(1); s.reset(); s.rhythm(4) }, ""},
	} {
		s := newSeq()
		s.d.SetSettings(c.st)
		s.used()
		s.idle(30)
		c.f(s)
		s.idle(90)
		r := only(t, c.name, s)
		if r.SettingsMatch != c.want {
			t.Errorf("%s: match %q, want %q", c.name, r.SettingsMatch, c.want)
		}
		wantStatus := map[string]Status{"yes": Confirmed, "no": Conflict, "": Inferred}[c.want]
		if r.Status != wantStatus || slices.Contains(r.Evidence, "settings") != (c.want != "") ||
			slices.Contains(r.Notes, "settings may be outdated") != (c.want == "no") {
			t.Errorf("%s: %s %v %v", c.name, r.Status, r.Evidence, r.Notes)
		}
	}
}

// A cut run is not compared: it cannot match.
func TestSettingsCut(t *testing.T) {
	s := newSeq()
	s.d.SetSettings(Settings{Backflush: &BackflushSettings{9, 10, 60}})
	s.used()
	s.idle(5)
	s.backflush(3)
	if r := only(t, "cut", s); r.Outcome != Cut || r.SettingsMatch != "" || r.Status != Inferred {
		t.Errorf("run %+v", r)
	}
}

// Agreeing settings do not hide an LCD conflict.
func TestSettingsLCDConflict(t *testing.T) {
	s := newSeq()
	s.d.SetSettings(Settings{DescalePID: fp(0)})
	s.used()
	s.st.Setpoint = fp(0)
	s.idle(5)
	s.reset()
	s.rhythm(1)
	s.show(flushFrame)
	s.rhythm(2)
	s.idle(90)
	if r := only(t, "descale", s); r.Status != Conflict || r.SettingsMatch != "yes" {
		t.Errorf("run %s %q", r.Status, r.SettingsMatch)
	}
}

func TestDescaleProfile(t *testing.T) {
	warm := func(st Settings) Run {
		s := newSeq()
		s.d.SetSettings(st)
		s.used()
		s.st.Setpoint = fp(50)
		s.idle(5)
		s.reset()
		s.rhythm(4)
		s.idle(90)
		return only(t, "warm descale", s)
	}
	if r := warm(Settings{}); r.Kind != Purge || r.Status != Inferred {
		t.Errorf("without settings: %s %s", r.Kind, r.Status)
	}
	if r := warm(Settings{DescalePID: fp(50)}); r.Kind != Descale || r.Status != Confirmed || r.SettingsMatch != "yes" {
		t.Errorf("at the profile's temperature: %s %s %q", r.Kind, r.Status, r.SettingsMatch)
	}
	if r := warm(Settings{DescalePID: fp(80)}); r.Kind != Purge {
		t.Errorf("at another temperature: %s", r.Kind)
	}
}

func TestPurgeVolumeNote(t *testing.T) {
	s := newSeq()
	s.d.SetSettings(Settings{PurgeML: fp(200)})
	s.cold()
	s.used()
	s.idle(5)
	s.reset()
	s.rhythm(4)
	s.idle(90)
	if r := only(t, "purge", s); !slices.Contains(r.Notes, "purge pumped 160 of 200 ml") || r.Status != Inferred {
		t.Errorf("notes %v, status %s", r.Notes, r.Status)
	}
}
