package logview

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// medewiNames and medewiUnits describe the medewi columns in WriteColumns.
var (
	medewiNames = map[string]string{
		"t": "(offset)", "wall": "(wall clock)", "src": "(source line)", "src_end": "(source end)",
		"dir": "(direction)", "event": "(event kind)", "run": "(run number)", "run_t": "(run time)",
		"pump": "(pump marker)", "xml": "(xml name)",
	}
	medewiUnits = map[string]string{"t": "s", "run_t": "s"}
)

// WriteColumns lists cols of the XML named xml: KEY NAME UNIT FACTOR AT LEN
// SOURCE (text, after a "# xml: NAME" line), or the same lowercase keys plus
// xml per object or row (jsonl, csv).
func WriteColumns(w io.Writer, f Format, xml string, cols []Column) error {
	type colRow struct{ key, name, unit, factor, at, len, source string }
	rows := make([]colRow, 0, len(cols))
	for _, c := range cols {
		r := colRow{key: c.Key, name: c.Name, unit: c.Unit, source: c.Source}
		if c.Source == "medewi" {
			r.name, r.unit = medewiNames[c.Key], medewiUnits[c.Key]
		}
		factor := c.Factor
		if factor == 0 {
			factor = 1
		}
		r.factor = FormatNum(factor)
		if c.At >= 0 {
			r.at = strconv.Itoa(c.At)
		}
		if c.Len >= 0 {
			r.len = strconv.Itoa(c.Len)
		}
		rows = append(rows, r)
	}
	switch f {
	case FormatJSONL:
		for _, r := range rows {
			var o jsonObj
			o.str("key", r.key)
			o.strOrNull("name", r.name)
			o.strOrNull("unit", r.unit)
			o.raw("factor", r.factor)
			o.raw("at", orNull(r.at))
			o.raw("len", orNull(r.len))
			o.str("source", r.source)
			o.strOrNull("xml", xml)
			if _, err := w.Write(o.line()); err != nil {
				return err
			}
		}
		return nil
	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"key", "name", "unit", "factor", "at", "len", "source", "xml"})
		for _, r := range rows {
			_ = cw.Write([]string{r.key, r.name, r.unit, r.factor, r.at, r.len, r.source, xml})
		}
		cw.Flush()
		return cw.Error()
	}
	if xml != "" {
		if _, err := fmt.Fprintf(w, "# xml: %s\n", xml); err != nil {
			return err
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KEY\tNAME\tUNIT\tFACTOR\tAT\tLEN\tSOURCE")
	for _, r := range rows {
		factor := r.factor
		if factor == "1" {
			factor = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.key, orDash(r.name), orDash(r.unit), factor, orDash(r.at), orDash(r.len), r.source)
	}
	return tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// RunsOptions controls WriteRuns.
type RunsOptions struct {
	// Loc is the zone of START in text; nil means time.Local.
	Loc *time.Location
	// Stats, when set, appends its columns after SRC.
	Stats *RunStats
}

// WriteRuns lists runs: RUN KIND START T DURATION PEAK_BAR VOLUME_ML
// TRUNCATED XML SRC, all of the run's first sample, then the Stats
// columns (lowercase also in text). START is the wall time in Loc (text)
// or UTC (jsonl, csv), absent for captures.
func WriteRuns(w io.Writer, f Format, runs []Run, opt RunsOptions) error {
	loc := opt.Loc
	if loc == nil {
		loc = time.Local
	}
	header := []string{"run", "kind", "start", "t", "duration", "peak_bar", "volume_ml", "truncated", "xml", "src"}
	var statCols []string
	if opt.Stats != nil {
		statCols = opt.Stats.Header()
	}
	var tw *tabwriter.Writer
	var cw *csv.Writer
	switch f {
	case FormatJSONL:
	case FormatCSV:
		cw = csv.NewWriter(w)
		_ = cw.Write(append(header, statCols...))
	default:
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, strings.Join(append([]string{strings.ToUpper(strings.Join(header, "\t"))}, statCols...), "\t"))
	}
	for _, r := range runs {
		e := r.First
		start, peak := "", ""
		if e.HasWall {
			start = utcWall(e.Wall)
			if tw != nil {
				start = e.Wall.In(loc).Format(wallLayout)
			}
		}
		if r.PeakBar != nil {
			peak = FormatNum(*r.PeakBar)
		}
		n, vol := strconv.Itoa(r.N), FormatNum(math.Round(r.VolumeML*1e6)/1e6)
		src := srcRef(e.Path, e.Src)
		var stats []AggValue
		if opt.Stats != nil {
			stats = opt.Stats.Values(r.N)
		}
		switch {
		case tw != nil:
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%.3f\t%s\t%s\t%s\t%s\t%s\t%s",
				n, r.Kind, orDash(start), e.Offset.Seconds(), seconds(r.Duration), orDash(peak), vol,
				yesNo(r.Truncated), orDash(r.XML), src)
			for _, v := range stats {
				_, _ = fmt.Fprintf(tw, "\t%s", aggTextCell(v))
			}
			_, _ = fmt.Fprintln(tw)
		case cw != nil:
			rec := []string{
				n, string(r.Kind), start, seconds(e.Offset), seconds(r.Duration), peak, vol,
				strconv.FormatBool(r.Truncated), r.XML, src,
			}
			for _, v := range stats {
				rec = append(rec, aggText(v))
			}
			_ = cw.Write(rec)
		default:
			var o jsonObj
			o.raw("run", n)
			o.str("kind", string(r.Kind))
			o.strOrNull("start", start)
			o.raw("t", seconds(e.Offset))
			o.raw("duration", seconds(r.Duration))
			o.raw("peak_bar", orNull(peak))
			o.raw("volume_ml", vol)
			o.raw("truncated", strconv.FormatBool(r.Truncated))
			o.strOrNull("xml", r.XML)
			o.str("src", src)
			for i, v := range stats {
				o.agg(statCols[i], v)
			}
			if _, err := w.Write(o.line()); err != nil {
				return err
			}
		}
	}
	if tw != nil {
		return tw.Flush()
	}
	if cw != nil {
		cw.Flush()
		return cw.Error()
	}
	return nil
}

// aggText is an aggregate as text, empty when it is empty.
func aggText(v AggValue) string {
	switch {
	case v.Num != nil:
		return FormatNum(*v.Num)
	case v.Str != nil:
		return *v.Str
	}
	return ""
}

// aggTextCell is an aggregate as one text-table token: ? when empty or
// an empty string, a string quoted when it holds blanks, a time with three
// decimals like t.
func aggTextCell(v AggValue) string {
	s := aggText(v)
	switch {
	case s == "":
		return "?"
	case v.Str != nil:
		return textQuote(s)
	case v.Seconds:
		return fmt.Sprintf("%.3f", *v.Num)
	}
	return s
}

// agg adds v: a number, a string, or null when empty.
func (o *jsonObj) agg(k string, v AggValue) {
	switch {
	case v.Num != nil:
		o.raw(k, FormatNum(*v.Num))
	case v.Str != nil:
		o.str(k, *v.Str)
	default:
		o.raw(k, "null")
	}
}
