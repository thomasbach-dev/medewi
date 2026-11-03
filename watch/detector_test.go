package watch

import (
	"strconv"
	"testing"

	"github.com/thomasbach-dev/medewi/leva"
)

// rec builds a record with the two fields the detector reads.
func rec(temp, setpoint float64) leva.Record {
	return leva.Record{Fields: []leva.Field{
		{Spec: leva.ValueSpec{Name: "Temperature PID1"}, Raw: strconv.FormatFloat(temp, 'f', 1, 64)},
		{Spec: leva.ValueSpec{Name: "Setpoint PID"}, Raw: strconv.FormatFloat(setpoint, 'f', 1, 64)},
	}}
}

// masked is a line whose fields are '?'-masked, as in standby.
func masked() leva.Record {
	return leva.Record{Fields: []leva.Field{
		{Spec: leva.ValueSpec{Name: "Temperature PID1"}, Raw: "?"},
		{Spec: leva.ValueSpec{Name: "Setpoint PID"}, Raw: "?"},
	}}
}

// feedAll feeds each (temp, setpoint) pair and counts events by kind.
func feedAll(d *Detector, recs ...leva.Record) map[Kind]int {
	got := map[Kind]int{}
	for _, r := range recs {
		for _, e := range d.Feed(r) {
			got[e.Kind]++
		}
	}
	return got
}

func repeat(n int, temp, sp float64) []leva.Record {
	out := make([]leva.Record, n)
	for i := range out {
		out[i] = rec(temp, sp)
	}
	return out
}

func ramp(from, to float64) []leva.Record {
	var out []leva.Record
	step := 1.0
	if to < from {
		step = -1.0
	}
	for t := from; (step > 0 && t <= to) || (step < 0 && t >= to); t += step {
		out = append(out, rec(t, 91))
	}
	return out
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{Ready: "READY", SteamWindow: "STEAM_WINDOW", Eco: "ECO"} {
		if k.String() != want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(k), k.String(), want)
		}
	}
}

func TestReadyFiresOnceAndRearms(t *testing.T) {
	d := NewDetector(DefaultConfig())
	recs := ramp(79, 91)
	recs = append(recs, repeat(20, 91, 91)...)
	if got := feedAll(d, recs...); got[Ready] != 1 {
		t.Fatalf("Ready events = %d, want 1", got[Ready])
	}
	// Dip to 85 (outside the 2.0 re-arm band), then return.
	recs = append(ramp(90, 85), repeat(15, 91, 91)...)
	if got := feedAll(d, recs...); got[Ready] != 1 {
		t.Fatalf("Ready events after dip = %d, want 1", got[Ready])
	}
}

func TestReadyNeedsConsecutive(t *testing.T) {
	d := NewDetector(DefaultConfig())
	recs := repeat(9, 91, 91)
	recs = append(recs, rec(90, 91)) // out of ReadyBand, inside the re-arm band
	recs = append(recs, repeat(9, 91, 91)...)
	if got := feedAll(d, recs...); got[Ready] != 0 {
		t.Fatalf("Ready events = %d, want 0", got[Ready])
	}
}

func TestReadyLevel(t *testing.T) {
	d := NewDetector(DefaultConfig())
	if d.Ready() {
		t.Fatal("Ready() true before any record")
	}
	for i := 0; i < 9; i++ {
		d.Feed(rec(91, 91))
	}
	if d.Ready() {
		t.Fatal("Ready() true before ReadyCount")
	}
	if ev := d.Feed(rec(91, 91)); len(ev) != 1 || ev[0].Kind != Ready {
		t.Fatalf("events = %v, want one Ready", ev)
	}
	if !d.Ready() {
		t.Fatal("Ready() false after Ready event")
	}
	d.Feed(rec(90.4, 91)) // in band
	d.Feed(rec(89.5, 91)) // out of band but within re-arm threshold
	if !d.Ready() {
		t.Fatal("Ready() dropped before the episode re-armed")
	}
	d.Feed(masked())
	if !d.Ready() {
		t.Fatal("Ready() dropped on a masked line")
	}
	d.Feed(rec(85, 91))
	if d.Ready() {
		t.Fatal("Ready() still true after the episode re-armed")
	}
}

func TestReadyRequiresBrewSetpoint(t *testing.T) {
	d := NewDetector(DefaultConfig())
	if got := feedAll(d, repeat(30, 79, 79)...); got[Ready] != 0 {
		t.Fatalf("Ready events at eco setpoint = %d, want 0", got[Ready])
	}
}

func TestSteamWindowOnRisingEdgeOnly(t *testing.T) {
	d := NewDetector(DefaultConfig())
	if got := feedAll(d, ramp(91, 115)...); got[SteamWindow] != 1 {
		t.Fatalf("SteamWindow events on climb = %d, want 1", got[SteamWindow])
	}
	if got := feedAll(d, ramp(116, 119)...); got[SteamWindow] != 0 {
		t.Fatalf("SteamWindow events on continued climb = %d, want 0", got[SteamWindow])
	}
	// Falling above SteamLow must not fire either.
	if got := feedAll(d, ramp(118, 100)...); got[SteamWindow] != 0 {
		t.Fatalf("SteamWindow events on fall = %d, want 0", got[SteamWindow])
	}
	if got := feedAll(d, ramp(101, 115)...); got[SteamWindow] != 1 {
		t.Fatalf("SteamWindow events on re-climb = %d, want 1", got[SteamWindow])
	}
}

func TestSteamEventCarriesValues(t *testing.T) {
	d := NewDetector(DefaultConfig())
	var ev []Event
	for _, r := range ramp(100, 113) {
		ev = append(ev, d.Feed(r)...)
	}
	if len(ev) != 1 || ev[0].Kind != SteamWindow || ev[0].Temp != 113 || ev[0].Setpoint != 91 {
		t.Fatalf("events = %+v, want one SteamWindow{113, 91}", ev)
	}
}

func TestSteamNotDuringBrewOvershoot(t *testing.T) {
	d := NewDetector(DefaultConfig())
	// Brew-temperature overshoot: far below SteamLow.
	recs := append(ramp(88, 93), repeat(20, 93, 91)...)
	// Above SteamLow and rising, but not more than setpoint+5 above.
	recs = append(recs, rec(112, 110), rec(113, 110), rec(114, 110))
	recs = append(recs, repeat(30, 114, 110)...)
	if got := feedAll(d, recs...); got[SteamWindow] != 0 {
		t.Fatalf("SteamWindow events = %d, want 0", got[SteamWindow])
	}
}

func TestEcoOnSetpointDrop(t *testing.T) {
	d := NewDetector(DefaultConfig())
	if got := feedAll(d, rec(91, 91), rec(91, 91), rec(88, 79)); got[Eco] != 1 {
		t.Fatalf("Eco events on drop = %d, want 1", got[Eco])
	}
	if got := feedAll(d, rec(87, 79), rec(86, 79), rec(85, 79)); got[Eco] != 0 {
		t.Fatalf("Eco events while staying in eco = %d, want 0", got[Eco])
	}
	// Eco again after returning to brew setpoint.
	if got := feedAll(d, rec(85, 91), rec(86, 91), rec(86, 79)); got[Eco] != 1 {
		t.Fatalf("Eco events on second drop = %d, want 1", got[Eco])
	}
}

func TestMaskedLinesIgnored(t *testing.T) {
	d := NewDetector(DefaultConfig())
	short := leva.Record{} // short line: fields missing entirely
	var recs []leva.Record
	for i := 0; i < 10; i++ {
		recs = append(recs, rec(91, 91))
		if i < 9 {
			recs = append(recs, masked(), short)
		}
	}
	// Masked lines between in-band records must neither reset nor advance
	// the consecutive count: the 10th real record completes it.
	if got := feedAll(d, recs...); got[Ready] != 1 {
		t.Fatalf("Ready events = %d, want 1", got[Ready])
	}
	if ev := d.Feed(masked()); ev != nil {
		t.Fatalf("masked Feed returned %v, want nil", ev)
	}
	// A masked line must not count as a setpoint drop either.
	if got := feedAll(d, masked(), rec(91, 91)); got[Eco] != 0 {
		t.Fatalf("Eco events around masked line = %d, want 0", got[Eco])
	}
}
