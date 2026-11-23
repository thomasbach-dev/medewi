package program

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/thomasbach-dev/medewi/backup"
)

const (
	// warmUpSlack is how far a warm-up pulse may start from its schedule (s).
	warmUpSlack = 5
	soakSlack   = 3   // s
	rinseSlack  = 0.1 // share
)

// BackflushSettings are the settings of the backflush program; Soak and
// Rinse in seconds.
type BackflushSettings struct{ Cycles, Soak, Rinse float64 }

// Settings are the machine's program settings, as far as known.
type Settings struct {
	WarmUp     []float64 // scheduled pulse starts, s from the ramp's start
	Backflush  *BackflushSettings
	DescalePID *float64 // PID 1 of the profile in use, 0 for off
	PurgeML    *float64
}

// SettingsFrom reads the program settings from an MCu dump. missing names
// the sections it could not read.
func SettingsFrom(u *backup.MCu) (s Settings, missing []string) {
	val := map[string]string{}
	for _, c := range u.Settings {
		if !c.Menu && !c.Damaged {
			val[c.Path] = c.Value
		}
	}
	step := regexp.MustCompile(`^Warm-Up/\d+$`)
	for _, c := range u.Settings {
		if !c.Menu || !step.MatchString(c.Path) {
			continue
		}
		at, ok1 := span(val[c.Path+"/Time"])
		dur, ok2 := span(val[c.Path+"/Duration"])
		if ok1 && ok2 && dur > 0 {
			s.WarmUp = append(s.WarmUp, at)
		}
	}
	if s.WarmUp == nil {
		missing = append(missing, "Warm-Up")
	}
	cycles, err := strconv.ParseFloat(val["Backflush/Cycles"], 64)
	soak, ok1 := span(val["Backflush/Soak"])
	rinse, ok2 := span(val["Backflush/Rinse"])
	if err == nil && ok1 && ok2 {
		s.Backflush = &BackflushSettings{cycles, soak, rinse}
	} else {
		missing = append(missing, "Backflush")
	}
	pid, ok := val["Descale/Edit/"+val["Descale/Use"]+"/PID 1"]
	if v, isNum := number(pid); ok && (isNum || strings.HasPrefix(pid, "-")) {
		s.DescalePID = &v
	} else {
		missing = append(missing, "Descale")
	}
	if v, ok := number(val["Descale/Purge"]); ok {
		s.PurgeML = &v
	}
	return s, missing
}

// span parses a menu time, "01m:30s", as seconds.
func span(v string) (float64, bool) {
	var m, s int
	if n, err := fmt.Sscanf(v, "%dm:%ds", &m, &s); err != nil || n != 2 {
		return 0, false
	}
	return float64(60*m + s), true
}

func number(v string) (float64, bool) {
	n, _, ok := backup.SplitNumber(v)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(n, 64)
	return f, err == nil
}

// SetSettings gives the settings the machine had during all samples. They
// refine the runs found, and never add or drop one.
func (d *Detector) SetSettings(s Settings) { d.settings = s }

// profileDescale reports whether the purge-like f ran at the descale
// profile's own temperature: then it is a descale.
func (d *Detector) profileDescale(f *found) bool {
	want := d.settings.DescalePID
	if f.kind != Purge || f.lcd || want == nil || *want == 0 {
		return false
	}
	for _, p := range f.pulses {
		if sp := p.first.Setpoint; p.first.Mode != "PID" || sp == nil || math.Abs(*sp-*want) > 0.05 {
			return false
		}
	}
	return true
}

// compare judges r against the settings.
func (d *Detector) compare(f *found, r *Run) {
	if f.lcd || r.Outcome == Cut {
		return
	}
	if ml := purged(r); ml > 0 && d.settings.PurgeML != nil {
		r.Notes = append(r.Notes, fmt.Sprintf("purge pumped %.0f of %.0f ml", ml, *d.settings.PurgeML))
	}
	match, ok := d.matches(f, r)
	if !ok {
		return
	}
	r.Evidence = append(r.Evidence, "settings")
	if match {
		r.SettingsMatch = "yes"
		if r.Status == Inferred {
			r.Status = Confirmed
		}
		return
	}
	r.SettingsMatch, r.Status = "no", Conflict
	r.Notes = append(r.Notes, "settings may be outdated")
}

// purged is the volume of r's purge pulses, 0 if unknown or none.
func purged(r *Run) float64 {
	sum := 0.0
	for _, p := range r.Pulses {
		if r.Kind != Purge && p.Stage != stagePurge {
			continue
		}
		if p.VolumeML == nil {
			return 0
		}
		sum += *p.VolumeML
	}
	return sum
}

// matches compares f with its settings; ok is false when there are none.
func (d *Detector) matches(f *found, r *Run) (match, ok bool) {
	switch {
	case r.Kind == WarmUp && len(d.settings.WarmUp) > 0:
		return warmUpMatches(d.settings.WarmUp, d.schedule(f)), true
	case r.Kind == Backflush && d.settings.Backflush != nil:
		return backflushMatches(*d.settings.Backflush, f.pulses), true
	case r.Kind == Descale && d.settings.DescalePID != nil:
		for _, p := range f.pulses {
			if sp := p.first.Setpoint; sp == nil || math.Abs(*sp-*d.settings.DescalePID) > 0.05 {
				return false, true
			}
		}
		return true, true
	}
	return false, false
}

// warmUpMatches wants half the scheduled pulses seen. Pulses off the
// schedule are fine: a vent pumps too.
func warmUpMatches(want []float64, got [][3]float64) bool {
	seen := 0
	for _, w := range want {
		for _, g := range got {
			if math.Abs(g[0]-w) <= warmUpSlack {
				seen++
				break
			}
		}
	}
	return 2*seen >= len(want)
}

// backflushMatches wants Cycles pulses Soak apart, then Rinse of pumping.
func backflushMatches(s BackflushSettings, ps []*pulse) bool {
	n := int(s.Cycles)
	if len(ps) <= n {
		return false
	}
	rinse := 0.0
	for k, p := range ps {
		if k >= 1 && k < n && math.Abs(p.first.At.Sub(ps[k-1].last.At).Seconds()-s.Soak) > soakSlack {
			return false
		}
		if k >= n {
			rinse += p.last.At.Sub(p.first.At).Seconds()
		}
	}
	return math.Abs(rinse-s.Rinse) <= rinseSlack*s.Rinse
}
