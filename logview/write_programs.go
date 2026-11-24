package logview

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/thomasbach-dev/medewi/program"
)

var programHeader = []string{
	"program", "kind", "start", "t", "duration", "outcome", "status", "evidence", "pulses", "pump_s",
	"volume_ml", "peak_bar", "settings_match", "notes", "xml", "src",
}

// WritePrograms lists ps. START is Program.Began in loc (text; nil means
// time.Local) or UTC (jsonl, csv), absent when unknown; the rest of a row is
// of the program's first sample. VOLUME_ML is absent when a pulse's volume
// is unknown. jsonl adds pulses_detail, figures and rule_version, csv ends
// with rule_version.
func WritePrograms(w io.Writer, f Format, ps []Program, loc *time.Location) error {
	if loc == nil {
		loc = time.Local
	}
	version := strconv.Itoa(program.RuleVersion)
	switch f {
	case FormatJSONL:
		for _, p := range ps {
			if err := writeProgramJSON(w, p, version); err != nil {
				return err
			}
		}
		return nil
	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write(append(append([]string{}, programHeader...), "rule_version"))
		for _, p := range ps {
			start := ""
			if !p.Began.IsZero() {
				start = utcWall(p.Began)
			}
			c := programCells(p)
			_ = cw.Write([]string{
				c.n, string(p.Kind), start, seconds(p.FirstPt.Offset), c.duration, string(p.Outcome), string(p.Status),
				c.evidence, c.pulses, c.pump, c.volume, c.peak, p.SettingsMatch, c.notes, p.XML, c.src, version,
			})
		}
		cw.Flush()
		return cw.Error()
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.ToUpper(strings.Join(programHeader, "\t")))
	for _, p := range ps {
		start := "-"
		if !p.Began.IsZero() {
			start = p.Began.In(loc).Format(wallLayout)
		}
		c := programCells(p)
		notes := "-"
		if c.notes != "" {
			notes = textQuote(c.notes)
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%.3f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.n, p.Kind, start, p.FirstPt.Offset.Seconds(), c.duration, p.Outcome, p.Status, c.evidence, c.pulses,
			c.pump, orDash(c.volume), orDash(c.peak), orDash(p.SettingsMatch), notes, orDash(p.XML), c.src)
	}
	return tw.Flush()
}

// programCell holds the formatted numbers of a row, "" for unknown.
type programCell struct {
	n, duration, evidence, pulses, pump, volume, peak, notes, src string
}

func programCells(p Program) programCell {
	c := programCell{
		n: strconv.Itoa(p.N), duration: seconds(p.End.Sub(p.Start)), evidence: strings.Join(p.Evidence, "+"),
		pulses: strconv.Itoa(len(p.Pulses)), notes: strings.Join(p.Notes, "; "), src: srcRef(p.FirstPt.Path, p.FirstPt.Src),
	}
	var pump time.Duration
	vol, peak, known := 0.0, 0.0, len(p.Pulses) > 0
	for _, pl := range p.Pulses {
		pump += pl.End.Sub(pl.Start)
		peak = max(peak, pl.PeakBar)
		if pl.VolumeML == nil {
			known = false
			continue
		}
		vol += *pl.VolumeML
	}
	c.pump = seconds(pump)
	if known {
		c.volume = num1(vol)
	}
	if len(p.Pulses) > 0 {
		c.peak = FormatNum(peak)
	}
	return c
}

// num1 formats v to a tenth.
func num1(v float64) string { return FormatNum(math.Round(v*10) / 10) }

func writeProgramJSON(w io.Writer, p Program, version string) error {
	c := programCells(p)
	start := ""
	if !p.Began.IsZero() {
		start = utcWall(p.Began)
	}
	strs := func(v []string) string {
		if len(v) == 0 {
			return "[]"
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	var o jsonObj
	o.raw("program", c.n)
	o.str("kind", string(p.Kind))
	o.strOrNull("start", start)
	o.raw("t", seconds(p.FirstPt.Offset))
	o.raw("duration", c.duration)
	o.str("outcome", string(p.Outcome))
	o.str("status", string(p.Status))
	o.raw("evidence", strs(p.Evidence))
	o.raw("pulses", c.pulses)
	o.raw("pump_s", c.pump)
	o.raw("volume_ml", orNull(c.volume))
	o.raw("peak_bar", orNull(c.peak))
	o.strOrNull("settings_match", p.SettingsMatch)
	o.raw("notes", strs(p.Notes))
	o.strOrNull("xml", p.XML)
	o.str("src", c.src)
	parts := make([]string, 0, len(p.Pulses))
	for _, pl := range p.Pulses {
		var q jsonObj
		q.raw("t", seconds(pl.Start.Sub(p.Start)))
		q.raw("duration", seconds(pl.End.Sub(pl.Start)))
		vol, phase := "", ""
		if pl.VolumeML != nil {
			vol = num1(*pl.VolumeML)
		}
		if pl.Phase != nil {
			phase = FormatNum(*pl.Phase)
		}
		q.raw("volume_ml", orNull(vol))
		q.raw("peak_bar", FormatNum(pl.PeakBar))
		q.raw("phase", orNull(phase))
		q.strOrNull("stage", pl.Stage)
		parts = append(parts, strings.TrimSpace(string(q.line())))
	}
	o.raw("pulses_detail", "["+strings.Join(parts, ",")+"]")
	fig := "null"
	if p.Figures != nil {
		b, err := json.Marshal(p.Figures)
		if err != nil {
			return err
		}
		fig = string(b)
	}
	o.raw("figures", fig)
	o.raw("rule_version", version)
	_, err := w.Write(o.line())
	return err
}
