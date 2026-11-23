package program

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The statuses.
const (
	Confirmed Status = "confirmed" // the rules, and the LCD or the settings agree
	Inferred  Status = "inferred"  // the rules only
	Conflict  Status = "conflict"  // the LCD or the settings contradict the rules
	LCD       Status = "lcd"       // the LCD names it, the rules missed it
)

// flushKind is the grouphead flush, which is no program here; the LCD names
// it.
const flushKind Kind = "flush"

// flowMark is how long after a flow test its menu mark may show up.
const flowMark = 60 * time.Second

// lcdFrame is a frame that says something.
type lcdFrame struct {
	at   time.Time
	brk  int
	kind Kind // "" for a menu frame with a flow test mark
	mark rune // the flow test's mark in the run menu, 0 for none
}

// Frame feeds the next LCD frame.
func (d *Detector) Frame(at time.Time, rows [4]string) {
	kind, _, _ := frameKind(rows)
	mark := flowTestMark(rows)
	if kind != "" || mark != 0 {
		d.frames = append(d.frames, lcdFrame{at: at, brk: d.brk, kind: kind, mark: mark})
	}
}

// descaleRow is the third row of a running descale or purge: its volume
// or what it waits for.
var descaleRow = regexp.MustCompile(`^(DESCALER +\d+ml|PURGE +\d+ml|PAUSE +\d+m:\d+s|EMPTY TRAY!|FILL TANK!)$`)

// backflushRow is the third row of a running backflush.
var backflushRow = regexp.MustCompile(`^(PUMP[ ▍▋█]*|WAIT[ ▍▋█]*|SOAK +\d+m:\d+s|EMPTY)$`)

// frameKind reads the running program off a frame. In the run menu the
// program's own row shows its state instead of its name, so the rows around
// it tell which program it is.
func frameKind(rows [4]string) (kind Kind, stage string, ok bool) {
	var r [4]string
	for k := range rows {
		r[k] = strings.TrimSpace(rows[k])
	}
	switch r[0] {
	case "WARM-UP":
		return WarmUp, "", true
	case "FLUSH":
		return flushKind, "", true
	case ">>Autotune":
		for _, row := range r[1:] {
			if strings.HasPrefix(row, "PID") && strings.HasSuffix(row, "WAIT") {
				return Autotune, "", true
			}
		}
		return "", "", false
	case ">Run":
	default:
		return "", "", false
	}
	switch {
	case r[1] == "Flush" && r[3] == "Purge" && descaleRow.MatchString(r[2]):
		switch {
		case strings.HasPrefix(r[2], "DESCALER"):
			stage = stageDescaler
		case strings.HasPrefix(r[2], "PURGE"):
			stage = stagePurge
		}
		return Descale, stage, true
	case r[1] == "Descale" && r[3] == "Backflush" && descaleRow.MatchString(r[2]):
		return Purge, "", true
	case r[1] == "Purge" && strings.HasPrefix(r[3], "Autotune") && backflushRow.MatchString(r[2]):
		return Backflush, "", true
	case strings.HasPrefix(r[1], "Test flow") && r[3] == "BACK" && strings.HasPrefix(r[2], "WAIT"):
		return PressureTest, "", true
	case r[1] == "Test valve" && strings.HasPrefix(r[3], "Test pressure") && strings.HasPrefix(r[2], "WAIT"):
		return FlowTest, "", true
	}
	return "", "", false
}

// flowTestMark returns the mark behind Test flow in the run menu: ✓ with
// stored data, - without; 0 when the row is not shown.
func flowTestMark(rows [4]string) rune {
	for _, row := range rows[1:] {
		_, rest, ok := strings.Cut(row, "Test flow")
		if rest = strings.TrimSpace(rest); ok && (rest == "✓" || rest == "-") {
			return []rune(rest)[0]
		}
	}
	return 0
}

// lcdKinds returns the distinct programs the LCD showed from..to.
func (d *Detector) lcdKinds(from, to time.Time) []Kind {
	var out []Kind
	for _, f := range d.frames {
		if f.kind != "" && !f.at.Before(from) && !f.at.After(to) && !slices.Contains(out, f.kind) {
			out = append(out, f.kind)
		}
	}
	return out
}

// judge sets the status, evidence and notes of r from the LCD.
func (d *Detector) judge(r *Run) {
	r.Evidence = []string{"report"}
	r.Status = Inferred
	ks := d.lcdKinds(r.Start, r.End)
	if len(ks) == 0 {
		return
	}
	r.Evidence = append(r.Evidence, "lcd")
	r.Status = Confirmed
	for _, k := range ks {
		if k != r.Kind {
			r.Status = Conflict
			r.Notes = append(r.Notes, fmt.Sprintf("LCD shows %s, reports say %s", k, r.Kind))
		}
	}
}

// lcdOnly finds the pulses no rule took whose frames name a program.
func (d *Detector) lcdOnly(ps []*pulse, taken []bool) []*found {
	var out []*found
	var last *pulse
	for i, p := range ps {
		if taken[i] || p.shot {
			last = nil
			continue
		}
		ks := d.lcdKinds(p.first.At, p.last.At)
		if len(ks) != 1 || ks[0] == flushKind {
			last = nil
			continue
		}
		if n := len(out) - 1; last != nil && out[n].kind == ks[0] && last.brk == p.brk && p.first.At.Sub(last.last.At) <= backflushGap {
			f := d.group(ks[0], append(slices.Clone(out[n].pulses), p), nil)
			f.lcd = true
			out[n] = f
		} else {
			f := d.group(ks[0], []*pulse{p}, nil)
			f.lcd = true
			out = append(out, f)
		}
		last = p
	}
	return out
}

// flowTestDone reports whether the run menu's flow test mark appeared with f.
func (d *Detector) flowTestDone(f *found) bool {
	before, after := rune(0), rune(0)
	for _, fr := range d.frames {
		switch {
		case fr.mark == 0 || fr.brk != f.pulses[0].brk:
		case !fr.at.After(f.first.At):
			before = fr.mark
		case fr.at.After(f.last.At) && fr.at.Sub(f.last.At) <= flowMark:
			after = fr.mark
		}
	}
	return before == '-' && after == '✓'
}
