package daemon

import (
	"time"

	"github.com/thomasbach-dev/medewi/store"
)

// PowerState is the machine's power mode as read from report lines.
type PowerState string

// Power states. Unknown covers "not connected" and unrecognised mode fields.
const (
	PowerActive  PowerState = "active"
	PowerEco     PowerState = "eco"
	PowerStandby PowerState = "standby"
	// PowerCold is mode CLD: PID1 control off (Setup › PID1 › Control),
	// heating off, temperatures live, setpoint and PID fields masked.
	PowerCold    PowerState = "cold"
	PowerUnknown PowerState = "unknown"
)

// powerFromPayload reads the mode from payload bytes [4:7] (the payload
// includes the leading '{'; the field has no export name in the XML).
func powerFromPayload(p []byte) PowerState {
	if len(p) < 7 {
		return PowerUnknown
	}
	switch string(p[4:7]) {
	case "PID":
		return PowerActive
	case "ECO":
		return PowerEco
	case "???":
		return PowerStandby
	case "CLD":
		return PowerCold
	}
	return PowerUnknown
}

// Status is a snapshot of the daemon's view of the machine. Nil pointers
// mean unknown (not connected, masked or missing field, no brew yet).
type Status struct {
	Connected bool // a connection passed WaitReady
	SpecOK    bool // MC? named the loaded spec; reports run only then
	Ready     bool // the boiler settled on the setpoint and the machine is active
	// MachineReady is the machine's own whole-machine READY ("[READY]"),
	// which comes minutes after Ready; cleared by eco, standby, cold and
	// disconnects.
	MachineReady bool
	BrewOpen     bool // a brew is pumping or coasting
	// Reporting: a checksum-valid report line has been seen on this
	// connection, so a running brew would be known. Power can still be
	// unknown (unrecognised mode field).
	Reporting bool
	Power     PowerState
	Firmware  string // from the id banner, e.g. "3.2"
	Temp      *float64
	Setpoint  *float64
	LastBrew  *store.Brew      // most recent stored brew
	Display   *DisplaySnapshot // latest sampled LCD; nil without sampling
	// ReleasedUntil is the end of a release (see Daemon.Release); nil when
	// not released or the release has run out.
	ReleasedUntil *time.Time
}

// DisplaySnapshot is the LCD content of the latest sampled frame.
type DisplaySnapshot struct {
	Rows [4]string // see leva.Frame
	At   time.Time
}

// Status returns a copy of the current state; it shares no memory with the
// daemon.
func (d *Daemon) Status() Status {
	d.mu.RLock()
	defer d.mu.RUnlock()
	s := d.st
	s.Temp, s.Setpoint = clonef(s.Temp), clonef(s.Setpoint)
	if s.LastBrew != nil {
		b := *s.LastBrew
		if b.EndedAt != nil {
			e := *b.EndedAt
			b.EndedAt = &e
		}
		s.LastBrew = &b
	}
	if s.Display != nil {
		ds := *s.Display
		s.Display = &ds
	}
	if r := s.ReleasedUntil; r != nil {
		s.ReleasedUntil = nil
		if r.After(time.Now()) { // run out: the reconnect loop clears it soon
			u := *r
			s.ReleasedUntil = &u
		}
	}
	return s
}

// update applies fn to the state under the write lock.
func (d *Daemon) update(fn func(*Status)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(&d.st)
	d.met.status(d.st)
}

func clonef(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
