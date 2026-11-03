// Package watch derives machine-state events (ready, steam window, eco) from
// decoded report records. The Detector is a pure state machine: no clock, no
// I/O.
package watch

import (
	"math"

	"github.com/thomasbach-dev/medewi/leva"
)

// Kind is the type of a detected event.
type Kind int

const (
	// Ready means temperature has settled on a brew setpoint.
	Ready Kind = iota
	// SteamWindow means temperature is climbing through the steam range.
	SteamWindow
	// Eco means the setpoint dropped from a brew setpoint to an eco setpoint.
	Eco
)

func (k Kind) String() string {
	switch k {
	case Ready:
		return "READY"
	case SteamWindow:
		return "STEAM_WINDOW"
	case Eco:
		return "ECO"
	}
	return "UNKNOWN"
}

// Event is one detection, with the temperature and setpoint that triggered it.
type Event struct {
	Kind           Kind
	Temp, Setpoint float64
}

// Config holds the detector thresholds.
type Config struct {
	ReadyBand       float64 // |temp-setpoint| at most this counts as in band
	ReadyCount      int     // consecutive in-band records needed for Ready
	SteamLow        float64 // steam window opens at or above this temperature
	SteamHigh       float64 // upper end of the steam range (informational)
	BrewSetpointMin float64 // setpoints below this are eco, not brew
}

// DefaultConfig returns the thresholds measured on the real machine.
func DefaultConfig() Config {
	return Config{ReadyBand: 0.5, ReadyCount: 10, SteamLow: 113, SteamHigh: 119, BrewSetpointMin: 85}
}

const (
	readyRearmBand = 2.0 // |temp-setpoint| beyond this re-arms Ready
	steamMargin    = 5.0 // steam needs temp above setpoint by more than this
	steamRearmDrop = 3.0 // steam re-arms this far below SteamLow
)

// Detector turns a stream of records into events.
type Detector struct {
	cfg Config

	havePrev     bool
	prevTemp     float64
	prevSetpoint float64

	inBand     int  // consecutive in-band records
	readyFired bool // Ready fired in this episode (the readiness level)
	steamFired bool
}

// NewDetector returns a Detector using cfg.
func NewDetector(cfg Config) *Detector { return &Detector{cfg: cfg} }

// Ready reports the current readiness level: true from the Ready event until
// the episode re-arms. Masked records do not change it.
func (d *Detector) Ready() bool { return d.readyFired }

// Feed processes one record. Records with a missing or masked temperature or
// setpoint are ignored and leave all state untouched.
func (d *Detector) Feed(rec leva.Record) []Event {
	temp, ok1 := rec.Float("Temperature PID1")
	sp, ok2 := rec.Float("Setpoint PID")
	if !ok1 || !ok2 {
		return nil
	}
	var events []Event
	emit := func(k Kind) { events = append(events, Event{Kind: k, Temp: temp, Setpoint: sp}) }

	brew := sp >= d.cfg.BrewSetpointMin
	dev := math.Abs(temp - sp)

	// Eco: setpoint falls out of the brew range.
	if d.havePrev && d.prevSetpoint >= d.cfg.BrewSetpointMin && !brew {
		emit(Eco)
	}

	// Ready.
	if !brew || dev > readyRearmBand {
		d.readyFired = false
	}
	if brew && dev <= d.cfg.ReadyBand {
		d.inBand++
	} else {
		d.inBand = 0
	}
	if !d.readyFired && d.inBand >= d.cfg.ReadyCount {
		d.readyFired = true
		emit(Ready)
	}

	// Steam window.
	if temp < d.cfg.SteamLow-steamRearmDrop {
		d.steamFired = false
	}
	if !d.steamFired && d.havePrev && temp >= d.cfg.SteamLow && temp > d.prevTemp && temp > sp+steamMargin {
		d.steamFired = true
		emit(SteamWindow)
	}

	d.havePrev = true
	d.prevTemp = temp
	d.prevSetpoint = sp
	return events
}
