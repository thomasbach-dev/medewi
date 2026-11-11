// Package brew turns a stream of decoded report samples into pump runs
// (shots and flushes) and wand water draws. It is a pure state machine: no
// I/O, no clocks; time comes from Sample.At.
//
// The Flow field is a decaying rate estimate that lingers for seconds after
// the pump stops, so it never decides anything here. The Volume counter
// growing does.
package brew

import "time"

// Sample is one decoded report line. Nil means the field was masked or
// missing.
type Sample struct {
	At      time.Time
	Pumping bool // payload byte 2 == '*': leva! is driving the pump

	Temp, Setpoint, Pressure, PressureSP, Flow, Heat, Volume *float64 // Flow is factor-scaled (ml/s)
}

// Kind classifies a finished brew.
type Kind string

const (
	// Shot is a run with a pressure setpoint (a profile was active).
	Shot Kind = "shot"
	// Flush is a run without one.
	Flush Kind = "flush"
	// TruncatedLost is a run cut off because the machine connection dropped.
	TruncatedLost Kind = "truncated-lost"
	// TruncatedReleased is a run cut off because the machine was released.
	TruncatedReleased Kind = "truncated-released"
	// TruncatedShutdown is a run cut off because the daemon shut down.
	TruncatedShutdown Kind = "truncated-shutdown"
	// TruncatedCrash is a run left open by a daemon crash, closed at startup.
	TruncatedCrash Kind = "truncated-crash"
)

// Truncated reports whether the run was cut off before it finished.
func (k Kind) Truncated() bool {
	switch k {
	case TruncatedLost, TruncatedReleased, TruncatedShutdown, TruncatedCrash:
		return true
	}
	return false
}

// Config holds the recorder's timing thresholds.
type Config struct {
	CoastQuiet time.Duration // Volume flat this long closes a coasting brew
	CoastMax   time.Duration // a coasting brew closes this long after the last pumping sample
	WaterQuiet time.Duration // a wand draw closes this long after its last increment
	MinWaterML float64       // smaller draws are coast leftovers and are dropped
}

// DefaultConfig returns the thresholds validated against real captures.
func DefaultConfig() Config {
	return Config{
		// ASSUMPTION: after a run the counter adds 1-2 ml within about 1.5 s.
		CoastQuiet: 2 * time.Second,
		// ASSUMPTION: the counter has settled by 3 s after the last pumping sample.
		CoastMax: 3 * time.Second,
		// ASSUMPTION: the captured wand draw grew steadily (144 ml), so a 2 s
		// pause means it is over.
		WaterQuiet: 2 * time.Second,
		// ASSUMPTION: coast leftovers stay far below 5 ml; real draws exceed it.
		MinWaterML: 5,
	}
}

// Summary describes a finished brew.
type Summary struct {
	StartedAt, EndedAt time.Time // first and last pumping sample
	Kind               Kind      // truncated kinds from Abort stay here
	Normal             Kind      // Shot or Flush: what the run looked like
	Duration           time.Duration
	PeakBar, VolumeML  float64
}

// WaterDraw describes a finished wand draw (water that is not a brew).
type WaterDraw struct {
	StartedAt, EndedAt time.Time     // first and last increment
	Duration           time.Duration // last increment minus first
	VolumeML           float64
}

// Result is what one Feed call produced.
type Result struct {
	Opened bool       // a brew opened on this sample
	Points []Sample   // pumping samples belonging to a brew
	Closed *Summary   // a brew closed on this sample
	Water  *WaterDraw // a wand draw closed on this sample
}

// Recorder is the state machine. It is not safe for concurrent use.
type Recorder struct {
	cfg Config

	brew brewState
	draw drawState
	prev *float64 // previous non-nil Volume, for draw accounting
}

type brewState struct {
	open, coasting bool
	start, lastPmp time.Time
	lastGrowth     time.Time
	lastVol        *float64
	peak, maxVol   float64
	shot           bool
}

type drawState struct {
	open        bool
	first, last time.Time
	ml          float64
}

// NewRecorder returns an idle recorder.
func NewRecorder(cfg Config) *Recorder { return &Recorder{cfg: cfg} }

// Open reports whether a brew is open, including one that is coasting.
func (r *Recorder) Open() bool { return r.brew.open }

// Feed advances the state machine by one sample.
func (r *Recorder) Feed(s Sample) Result {
	var res Result
	if s.Pumping {
		r.feedPumping(s, &res)
	} else {
		r.feedIdle(s, &res)
	}
	if s.Volume != nil {
		v := *s.Volume
		r.prev = &v
	}
	return res
}

func (r *Recorder) feedPumping(s Sample, res *Result) {
	res.Water = r.closeDraw()
	b := &r.brew
	// The counter resets at every leva! pump start, so a lower Volume while
	// coasting means a new run.
	if b.open && b.coasting && s.Volume != nil && b.lastVol != nil && *s.Volume < *b.lastVol {
		res.Closed = r.closeBrew(b.normalKind())
	}
	if !b.open {
		*b = brewState{open: true, start: s.At}
		res.Opened = true
	}
	b.coasting = false
	b.lastPmp = s.At
	b.lastGrowth = s.At // the coast's quiet timer starts at the last pumping sample
	if s.PressureSP != nil {
		b.shot = true
	}
	if s.Pressure != nil && *s.Pressure > b.peak {
		b.peak = *s.Pressure
	}
	r.trackVolume(s)
	res.Points = append(res.Points, s)
}

func (r *Recorder) feedIdle(s Sample, res *Result) {
	b := &r.brew
	if b.open {
		// ASSUMPTION: a wand draw started within the coast (e.g. hot water for
		// an Americano right after a shot) adds what it draws until CoastMax to
		// the brew's volume: growth keeps the coast open, so up to 3 s, about
		// 24 ml at about 8 ml/s. Accepted.
		b.coasting = true
		r.trackVolume(s)
		if s.At.Sub(b.lastGrowth) >= r.cfg.CoastQuiet || s.At.Sub(b.lastPmp) >= r.cfg.CoastMax {
			res.Closed = r.closeBrew(b.normalKind())
		}
		return
	}
	r.feedDraw(s, res)
}

// trackVolume folds the sample's Volume into the open brew.
func (r *Recorder) trackVolume(s Sample) {
	b := &r.brew
	if s.Volume == nil {
		return
	}
	v := *s.Volume
	if b.lastVol != nil && v > *b.lastVol {
		b.lastGrowth = s.At
	}
	if v > b.maxVol {
		b.maxVol = v
	}
	b.lastVol = &v
}

// feedDraw does wand-draw accounting while no brew is open.
func (r *Recorder) feedDraw(s Sample, res *Result) {
	d := &r.draw
	if s.Volume != nil && r.prev != nil && *s.Volume > *r.prev {
		if !d.open {
			*d = drawState{open: true, first: s.At}
		}
		d.ml += *s.Volume - *r.prev
		d.last = s.At
	}
	// A decrease rebases without counting (the prev update in Feed does it).
	if d.open && s.At.Sub(d.last) >= r.cfg.WaterQuiet {
		res.Water = r.closeDraw()
	}
}

func (r *Recorder) closeDraw() *WaterDraw {
	d := r.draw
	r.draw = drawState{}
	if !d.open || d.ml < r.cfg.MinWaterML {
		return nil
	}
	return &WaterDraw{StartedAt: d.first, EndedAt: d.last, Duration: d.last.Sub(d.first), VolumeML: d.ml}
}

func (b *brewState) normalKind() Kind {
	if b.shot {
		return Shot
	}
	return Flush
}

func (r *Recorder) closeBrew(k Kind) *Summary {
	b := r.brew
	r.brew = brewState{}
	return &Summary{
		StartedAt: b.start,
		EndedAt:   b.lastPmp,
		Kind:      k,
		Normal:    b.normalKind(),
		Duration:  b.lastPmp.Sub(b.start),
		PeakBar:   b.peak,
		VolumeML:  b.maxVol,
	}
}

// Abort ends the episode after a disconnect. A brew still pumping closes as
// cut (a truncated kind naming the cause), a coasting one with its normal kind; nil if none was open. An
// open wand draw is dropped and the remembered Volume is forgotten, so water
// drawn while disconnected never shows up as a draw.
func (r *Recorder) Abort(cut Kind) *Summary {
	r.draw = drawState{}
	r.prev = nil
	if !r.brew.open {
		return nil
	}
	k := cut
	if r.brew.coasting {
		k = r.brew.normalKind()
	}
	return r.closeBrew(k)
}
