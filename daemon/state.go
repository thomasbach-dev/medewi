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
	// which comes minutes after Ready; cleared by eco, standby and
	// disconnects.
	MachineReady bool
	BrewOpen     bool // a brew is pumping or coasting
	Power        PowerState
	Firmware     string // from the id banner, e.g. "3.2"
	Temp         *float64
	Setpoint     *float64
	LastBrew     *store.Brew      // most recent stored brew
	Display      *DisplaySnapshot // latest sampled LCD; nil without sampling
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
