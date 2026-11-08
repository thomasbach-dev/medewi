package brew

import (
	"testing"
	"time"
)

const step = 250 * time.Millisecond

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

// harness feeds samples and accumulates everything the recorder reports.
type harness struct {
	t      *testing.T
	r      *Recorder
	now    time.Time
	opened int
	points []Sample
	closed []Summary
	water  []WaterDraw
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, r: NewRecorder(DefaultConfig()), now: t0}
}

// feed sends one sample at the current time, then advances by step.
func (h *harness) feed(s Sample) {
	h.t.Helper()
	s.At = h.now
	h.now = h.now.Add(step)
	res := h.r.Feed(s)
	if res.Opened {
		h.opened++
	}
	h.points = append(h.points, res.Points...)
	if res.Closed != nil {
		h.closed = append(h.closed, *res.Closed)
	}
	if res.Water != nil {
		h.water = append(h.water, *res.Water)
	}
}

func (h *harness) repeat(n int, s Sample) {
	h.t.Helper()
	for range n {
		h.feed(s)
	}
}

// idle feeds quiet non-pumping samples for d.
func (h *harness) idle(d time.Duration, vol *float64) {
	h.t.Helper()
	h.repeat(int(d/step), Sample{Volume: vol})
}

func (h *harness) wantNoWater() {
	h.t.Helper()
	if len(h.water) != 0 {
		h.t.Fatalf("water = %+v, want none", h.water)
	}
}

func TestShotRun(t *testing.T) {
	h := newHarness(t)
	h.repeat(120, Sample{Pumping: true, Pressure: f(9), PressureSP: f(9)})
	h.idle(5*time.Second, nil)
	if h.opened != 1 || len(h.closed) != 1 {
		t.Fatalf("opened=%d closed=%d, want 1/1", h.opened, len(h.closed))
	}
	c := h.closed[0]
	if c.Kind != Shot || c.PeakBar != 9 || c.Duration != 30*time.Second-step {
		t.Errorf("summary = %+v", c)
	}
	if len(h.points) != 120 {
		t.Errorf("points = %d, want 120", len(h.points))
	}
	if h.r.Open() {
		t.Error("recorder still open")
	}
}

func TestFlushRun(t *testing.T) {
	h := newHarness(t)
	h.repeat(80, Sample{Pumping: true, Pressure: f(0.05)})
	h.idle(5*time.Second, nil)
	if len(h.closed) != 1 || h.closed[0].Kind != Flush {
		t.Fatalf("closed = %+v, want one flush", h.closed)
	}
}

func TestStaticPressureAfterRun(t *testing.T) {
	h := newHarness(t)
	h.repeat(40, Sample{Pumping: true, Pressure: f(8.5), PressureSP: f(9)})
	npts := len(h.points)
	for i := range 40 {
		h.feed(Sample{Pressure: f(4 + 5.9*float64(i)/39), Flow: f(2)})
	}
	if len(h.closed) != 1 || h.closed[0].PeakBar != 8.5 {
		t.Fatalf("closed = %+v, want peak 8.5", h.closed)
	}
	if len(h.points) != npts {
		t.Errorf("points grew from %d to %d during static pressure", npts, len(h.points))
	}
}

func TestStaticPressureNoRun(t *testing.T) {
	h := newHarness(t)
	h.repeat(240, Sample{Pressure: f(10), Flow: f(0)})
	if h.opened != 0 || len(h.closed) != 0 || len(h.points) != 0 {
		t.Errorf("opened=%d closed=%v points=%d, want nothing", h.opened, h.closed, len(h.points))
	}
	h.wantNoWater()
}

func TestWandDrawNotABrew(t *testing.T) {
	h := newHarness(t)
	h.feed(Sample{Volume: f(72)})
	for i := range 80 {
		h.feed(Sample{Volume: f(72 + 144*float64(i+1)/80), Flow: f(7)})
	}
	h.idle(3*time.Second, f(216))
	if h.opened != 0 || len(h.closed) != 0 {
		t.Fatalf("opened=%d closed=%v, want no brew", h.opened, h.closed)
	}
	if len(h.water) != 1 || h.water[0].VolumeML != 144 {
		t.Fatalf("water = %+v, want one draw of 144 ml", h.water)
	}
	if want := 79 * step; h.water[0].Duration != want {
		t.Errorf("duration = %v, want %v", h.water[0].Duration, want)
	}
}

func TestStaleFlowNoDraw(t *testing.T) {
	h := newHarness(t)
	h.repeat(40, Sample{Pumping: true, Volume: f(50)})
	h.idle(10*time.Second, f(50))
	h.repeat(20, Sample{Volume: f(50), Flow: f(2)})
	h.wantNoWater()
}

func TestCoastVolume(t *testing.T) {
	h := newHarness(t)
	h.repeat(40, Sample{Pumping: true, Volume: f(100), PressureSP: f(9)})
	h.idle(time.Second, f(100))
	h.idle(5*time.Second, f(101))
	if len(h.closed) != 1 || h.closed[0].VolumeML != 101 {
		t.Fatalf("closed = %+v, want volume 101", h.closed)
	}
	h.wantNoWater()
}

func TestCounterDecreaseRebases(t *testing.T) {
	h := newHarness(t)
	for _, v := range []float64{72, 80, 3, 10} {
		h.feed(Sample{Volume: f(v)})
	}
	h.idle(3*time.Second, f(10))
	if len(h.water) != 1 || h.water[0].VolumeML != 15 {
		t.Fatalf("water = %+v, want 15 ml", h.water)
	}
}

func TestSmallGrowthIsNoDraw(t *testing.T) {
	h := newHarness(t)
	h.feed(Sample{Volume: f(10)})
	h.feed(Sample{Volume: f(12)})
	h.idle(3*time.Second, f(12))
	h.wantNoWater()
}

func TestVolumeIsRunMax(t *testing.T) {
	h := newHarness(t)
	for i := range 101 {
		h.feed(Sample{Pumping: true, Volume: f(float64(i))})
	}
	h.repeat(10, Sample{Pumping: true, Volume: f(101)})
	h.idle(4*time.Second, f(101))
	if len(h.closed) != 1 || h.closed[0].VolumeML != 101 {
		t.Fatalf("closed = %+v, want volume 101", h.closed)
	}
}

func TestMaskedVolumeAndPressure(t *testing.T) {
	h := newHarness(t)
	h.repeat(8, Sample{Pumping: true})
	h.idle(4*time.Second, nil)
	if len(h.closed) != 1 || h.closed[0].Kind != Flush || h.closed[0].PeakBar != 0 || h.closed[0].VolumeML != 0 {
		t.Fatalf("closed = %+v", h.closed)
	}
}

func TestAbort(t *testing.T) {
	t.Run("pumping", func(t *testing.T) {
		h := newHarness(t)
		h.repeat(20, Sample{Pumping: true, PressureSP: f(9), Pressure: f(9)})
		s := h.r.Abort(TruncatedReleased)
		if s == nil || s.Kind != TruncatedReleased || s.PeakBar != 9 {
			t.Fatalf("Abort = %+v, want truncated-released", s)
		}
		if h.r.Open() {
			t.Error("still open after Abort")
		}
	})
	t.Run("coasting", func(t *testing.T) {
		h := newHarness(t)
		h.repeat(20, Sample{Pumping: true, PressureSP: f(9)})
		h.repeat(2, Sample{})
		if !h.r.Open() {
			t.Error("coasting brew should count as open")
		}
		if s := h.r.Abort(TruncatedLost); s == nil || s.Kind != Shot {
			t.Fatalf("Abort = %+v, want shot", s)
		}
	})
	t.Run("idle", func(t *testing.T) {
		h := newHarness(t)
		h.repeat(4, Sample{})
		if s := h.r.Abort(TruncatedLost); s != nil {
			t.Fatalf("Abort = %+v, want nil", s)
		}
	})
	t.Run("forgets volume and draw", func(t *testing.T) {
		h := newHarness(t)
		h.feed(Sample{Volume: f(10)})
		h.feed(Sample{Volume: f(30)}) // open draw, dropped by Abort
		h.r.Abort(TruncatedLost)
		h.feed(Sample{Volume: f(200)})
		h.idle(4*time.Second, f(200))
		h.wantNoWater()
	})
}

func TestBackToBackRuns(t *testing.T) {
	h := newHarness(t)
	h.repeat(20, Sample{Pumping: true, Volume: f(60), PressureSP: f(9)})
	h.repeat(4, Sample{Volume: f(60)})
	h.feed(Sample{Pumping: true, Volume: f(0)})
	h.repeat(10, Sample{Pumping: true, Volume: f(30)})
	h.idle(4*time.Second, f(30))
	if h.opened != 2 || len(h.closed) != 2 {
		t.Fatalf("opened=%d closed=%d, want 2/2", h.opened, len(h.closed))
	}
	if h.closed[0].Kind != Shot || h.closed[0].VolumeML != 60 || h.closed[1].Kind != Flush || h.closed[1].VolumeML != 30 {
		t.Errorf("closed = %+v", h.closed)
	}
}

func TestPumpResumesInCoast(t *testing.T) {
	h := newHarness(t)
	h.repeat(20, Sample{Pumping: true, Volume: f(60)})
	h.repeat(4, Sample{Volume: f(60)})
	h.repeat(20, Sample{Pumping: true, Volume: f(70)})
	h.idle(4*time.Second, f(70))
	if h.opened != 1 || len(h.closed) != 1 || h.closed[0].VolumeML != 70 {
		t.Fatalf("opened=%d closed=%+v, want one brew of 70 ml", h.opened, h.closed)
	}
}

func TestPumpingEndsDraw(t *testing.T) {
	h := newHarness(t)
	h.feed(Sample{Volume: f(0)})
	for i := range 8 {
		h.feed(Sample{Volume: f(float64(i+1) * 2)})
	}
	got := h.r.Feed(Sample{At: h.now, Pumping: true, Volume: f(0)})
	if !got.Opened || got.Water == nil || got.Water.VolumeML != 16 {
		t.Fatalf("result = %+v, want opened plus a 16 ml draw", got)
	}
}

// TestShotThenFlush plays the end of a real shot followed by a real flush, as
// transcribed from a coldstart capture. gap is the time from the shot's
// last pumping sample to the flush's first.
func TestShotThenFlush(t *testing.T) {
	for _, tc := range []struct {
		name string
		gap  time.Duration
	}{
		{"captured gap", 9300 * time.Millisecond},
		{"flush inside coast", 2500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			shot := Sample{Pumping: true, PressureSP: f(9), Pressure: f(9)}
			for _, v := range []float64{82, 83, 83} {
				s := shot
				s.Volume = f(v)
				h.feed(s)
			}
			lastPump := h.now.Add(-step)
			for i := 1; time.Duration(i)*step < tc.gap; i++ {
				v := 84.0
				if i >= 6 {
					v = 85
				}
				h.feed(Sample{Volume: f(v)})
			}
			h.now = lastPump.Add(tc.gap)
			h.feed(Sample{Pumping: true, Volume: f(0)})
			for v := 1; v <= 73; v++ {
				h.feed(Sample{Pumping: true, Volume: f(float64(v))})
			}
			h.idle(5*time.Second, f(73))
			if len(h.closed) != 2 {
				t.Fatalf("closed = %+v, want 2 brews", h.closed)
			}
			if c := h.closed[0]; c.Kind != Shot || c.VolumeML != 85 {
				t.Errorf("first = %+v, want shot 85 ml", c)
			}
			if c := h.closed[1]; c.Kind != Flush || c.VolumeML != 73 {
				t.Errorf("second = %+v, want flush 73 ml", c)
			}
			h.wantNoWater()
		})
	}
}

func TestKindTruncated(t *testing.T) {
	for k, want := range map[Kind]bool{
		TruncatedLost: true, TruncatedReleased: true, TruncatedShutdown: true, TruncatedCrash: true,
		Shot: false, Flush: false, Kind(""): false, Kind("truncated"): false,
	} {
		if got := k.Truncated(); got != want {
			t.Errorf("Kind(%q).Truncated() = %v, want %v", k, got, want)
		}
	}
}
