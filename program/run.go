package program

import "time"

// RuleVersion counts changes of the rules, so stored results can be told
// apart.
const RuleVersion = 1

// Kind is a program.
type Kind string

// The programs.
const (
	WarmUp       Kind = "warm-up"
	Autotune     Kind = "autotune"
	PressureTest Kind = "pressure-test"
	FlowTest     Kind = "flow-test"
	Backflush    Kind = "backflush"
	Descale      Kind = "descale"
	Purge        Kind = "purge"
)

// Outcome says how a run ended.
type Outcome string

// Status says how sure a run is.
type Status string

// Stages of a descale.
const (
	stageDescaler = "descaler"
	stagePurge    = "purge"
)

// Pulse is one pump pulse of a Run.
type Pulse struct {
	Start, End time.Time
	VolumeML   *float64 // the pulse's own volume; nil if unknown
	PeakBar    float64
	Phase      *float64 // the most common one
	Stage      string   // of a descale: descaler, purge
}

// Run is one program run.
type Run struct {
	Kind        Kind
	Start, End  time.Time
	First, Last any // Ref of the first and last sample
	Pulses      []Pulse
	// OpenStart, OpenEnd: the run may have begun before, or gone on
	// after, what the Detector saw of it.
	OpenStart, OpenEnd bool
	Outcome            Outcome
	Status             Status
	Evidence, Notes    []string
	SettingsMatch      string // yes, no, or empty: not compared
	Figures            map[string]any
}
