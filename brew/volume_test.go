package brew

import (
	"testing"
	"time"
)

// The Volume field is 3 characters wide and reads ? above 999 ml; the tick
// counter keeps counting (issue #67). These tests use 2 ticks per ml.

// vt is a sample with Volume (nil for masked) and Ticks.
func vt(pumping bool, vol *float64, ticks float64) Sample {
	return Sample{Pumping: pumping, Volume: vol, Ticks: f(ticks)}
}

// A run that crosses 999 ml goes on counting from the ticks, and a wand
// draw while Volume reads ? is still recorded.
func TestVolumeFromTicksWhileMasked(t *testing.T) {
	h := newHarness(t)
	h.repeat(8, vt(false, f(900), 1800)) // ratio learned while both are valid
	for ticks := 1800.0; ticks <= 2400; ticks += 20 {
		var vol *float64
		if ticks/2 <= 999 {
			vol = f(ticks / 2)
		}
		h.feed(vt(true, vol, ticks))
	}
	h.repeat(16, vt(false, nil, 2400))
	if len(h.closed) != 1 {
		t.Fatalf("closed = %+v, want one run", h.closed)
	}
	if c := h.closed[0]; c.VolumeML != 1200 || c.VolumeUnknown {
		t.Errorf("run = %+v, want 1200 ml, known", c)
	}
	if last := h.points[len(h.points)-1]; last.Volume == nil || *last.Volume != 1200 {
		t.Errorf("last point volume = %v, want 1200 from the ticks", last.Volume)
	}

	// A wand draw of 200 ticks (100 ml) with Volume still masked.
	for ticks := 2420.0; ticks <= 2600; ticks += 20 {
		h.feed(vt(false, nil, ticks))
	}
	h.repeat(12, vt(false, nil, 2600))
	if len(h.water) != 1 || h.water[0].VolumeML != 100 {
		t.Fatalf("water = %+v, want one 100 ml draw", h.water)
	}
}

// Without a learned ratio a masked Volume stays unknown: the run says so
// instead of reporting a volume it never saw, and no draw is invented.
func TestMaskedVolumeWithoutRatioIsUnknown(t *testing.T) {
	h := newHarness(t)
	for ticks := 2000.0; ticks <= 2400; ticks += 20 {
		h.feed(vt(true, nil, ticks))
	}
	h.repeat(16, vt(false, nil, 2400))
	if len(h.closed) != 1 || !h.closed[0].VolumeUnknown || h.closed[0].VolumeML != 0 {
		t.Fatalf("closed = %+v, want one run with unknown volume", h.closed)
	}
	for ticks := 2420.0; ticks <= 2600; ticks += 20 {
		h.feed(vt(false, nil, ticks))
	}
	h.repeat(12, vt(false, nil, 2600))
	h.wantNoWater()
}

// A run seen partly, below the volume a ratio is learned from, then masked:
// its maximum would be a lower bound, so the volume is unknown.
func TestPartlyMaskedRunIsUnknown(t *testing.T) {
	h := newHarness(t)
	for i := range 20 {
		h.feed(vt(true, f(float64(5*i)), float64(10*i)))
	}
	h.repeat(8, vt(true, nil, 300))
	h.repeat(16, vt(false, nil, 300))
	if len(h.closed) != 1 || !h.closed[0].VolumeUnknown {
		t.Fatalf("closed = %+v, want unknown volume", h.closed)
	}
}

// Small volumes quantise badly (the field counts whole ml): no ratio is
// learned from them.
func TestRatioNeedsMinVolume(t *testing.T) {
	h := newHarness(t)
	h.repeat(8, vt(false, f(10), 21))
	h.repeat(8, vt(true, nil, 2000))
	h.repeat(16, vt(false, nil, 2000))
	if len(h.closed) != 1 || !h.closed[0].VolumeUnknown {
		t.Fatalf("closed = %+v, want unknown volume", h.closed)
	}
}

// The ratio is a machine setting (Impulses/l): it survives a disconnect.
func TestRatioSurvivesAbort(t *testing.T) {
	h := newHarness(t)
	h.repeat(8, vt(false, f(900), 1800))
	h.r.Abort(TruncatedLost)
	h.repeat(8, vt(true, nil, 2400))
	h.repeat(16, vt(false, nil, 2400))
	if len(h.closed) != 1 || h.closed[0].VolumeML != 1200 || h.closed[0].VolumeUnknown {
		t.Fatalf("closed = %+v, want 1200 ml after the reconnect", h.closed)
	}
}

// A run with a readable Volume is known, as before.
func TestReadableVolumeIsKnown(t *testing.T) {
	h := newHarness(t)
	h.repeat(8, Sample{Pumping: true, Volume: f(40)})
	h.idle(4*time.Second, f(40))
	if len(h.closed) != 1 || h.closed[0].VolumeUnknown || h.closed[0].VolumeML != 40 {
		t.Fatalf("closed = %+v, want 40 ml, known", h.closed)
	}
}
