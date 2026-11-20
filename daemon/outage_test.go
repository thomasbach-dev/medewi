package daemon

import (
	"log/slog"
	"testing"
	"time"
)

// A switched-off machine fails a dial every 10 s: the first failure and an
// hourly reminder go to info, the rest to debug.
func TestOutageLevels(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC)
	o := outage{every: time.Hour}
	type step struct {
		at       time.Duration
		lvl      slog.Level
		reminder bool
	}
	for i, s := range []step{
		{0, slog.LevelInfo, false},
		{10 * time.Second, slog.LevelDebug, false},
		{59*time.Minute + 59*time.Second, slog.LevelDebug, false},
		{time.Hour, slog.LevelInfo, true},
		{time.Hour + 10*time.Second, slog.LevelDebug, false},
		{2 * time.Hour, slog.LevelInfo, true},
	} {
		lvl, reminder := o.fail(t0.Add(s.at))
		if lvl != s.lvl || reminder != s.reminder {
			t.Errorf("step %d at %v: got (%v, %v), want (%v, %v)", i, s.at, lvl, reminder, s.lvl, s.reminder)
		}
	}
	if n, d := o.fails, o.unreachableFor(t0.Add(2*time.Hour)); n != 6 || d != 2*time.Hour {
		t.Errorf("during the outage: %d failures over %v, want 6 over 2h", n, d)
	}
	n, d, ok := o.end(t0.Add(2*time.Hour + 5*time.Second))
	if !ok || n != 6 || d != 2*time.Hour+5*time.Second {
		t.Errorf("end = (%d, %v, %v), want (6, 2h0m5s, true)", n, d, ok)
	}
	// Ended: the next failure starts a new outage, logged at info again.
	if _, _, ok := o.end(t0.Add(3 * time.Hour)); ok {
		t.Error("second end reports an outage")
	}
	if lvl, reminder := o.fail(t0.Add(3 * time.Hour)); lvl != slog.LevelInfo || reminder {
		t.Errorf("new outage: got (%v, %v), want (INFO, false)", lvl, reminder)
	}
}

// Without failures there is no outage to end.
func TestOutageEndWithoutFailure(t *testing.T) {
	t.Parallel()
	o := outage{every: time.Hour}
	if n, d, ok := o.end(time.Now()); ok || n != 0 || d != 0 {
		t.Errorf("end = (%d, %v, %v), want (0, 0, false)", n, d, ok)
	}
}
