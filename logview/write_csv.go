package logview

import (
	"encoding/csv"
	"fmt"
	"slices"
	"strconv"
)

// idColumns are the columns that lead every csv and jsonl row of events,
// whatever --fields says: where the line is and which run holds it.
var idColumns = []string{"t", "wall", "src", "run", "run_t"}

// csvMedewi are the medewi columns CSV prints before the report fields.
var csvMedewi = append(slices.Clone(idColumns), "src_end", "pump")

// csvHeader is the header for fields: idColumns, then fields without them.
func csvHeader(fields []string) []string {
	h := slices.Clone(idColumns)
	for _, k := range fields {
		if !slices.Contains(h, k) {
			h = append(h, k)
		}
	}
	return h
}

type csvWriter struct {
	base
	cw     *csv.Writer
	header []string
	set    string // key set of the header's spec, without Fields
}

func (c *csvWriter) Write(r Row) error {
	e := r.Entry
	if e.Kind != KindReport {
		return nil
	}
	if e.Record == nil || e.Spec == nil {
		if e.Err != nil {
			c.skipErr++
		} else {
			c.skipRaw++
		}
		return nil
	}
	sc, err := c.specCols(e.Spec)
	if err != nil {
		return err
	}
	if c.header == nil {
		c.header = csvHeader(c.opt.Fields)
		if len(c.opt.Fields) == 0 {
			c.header = append(slices.Clone(csvMedewi), sc.keys...)
			c.set = sc.set
		}
		if err := c.cw.Write(c.header); err != nil {
			return err
		}
	} else if len(c.opt.Fields) == 0 && sc.set != c.set {
		return fmt.Errorf("report at %s: XML %s has other fields than the first report's; use --fields",
			srcRef(e.Path, e.Src), orDash(e.XML))
	}
	rec := make([]string, len(c.header))
	for i, k := range c.header {
		rec[i] = c.cell(r, sc, k)
	}
	return c.cw.Write(rec)
}

func (c *csvWriter) cell(r Row, sc *specCols, k string) string {
	e := r.Entry
	switch k {
	case "t":
		return seconds(e.Offset)
	case "wall":
		if e.HasWall {
			return utcWall(e.Wall)
		}
		return ""
	case "src":
		return srcRef(e.Path, e.Src)
	case "src_end":
		if e.SrcEnd != e.Src {
			return srcRef(e.Path, e.SrcEnd)
		}
		return ""
	case "dir":
		return string(e.Dir)
	case "event":
		return string(e.Kind)
	case "run":
		if r.Run != nil {
			return strconv.Itoa(r.Run.N)
		}
		return ""
	case "run_t":
		if r.RunT != nil {
			return seconds(*r.RunT)
		}
		return ""
	case "pump":
		switch pumpByte(e) {
		case '*':
			return "1"
		case '-':
			return "0"
		}
		return ""
	case "xml":
		return e.XML
	}
	v, ok := c.field(e, sc, k)
	if !ok || v.masked || v.bad {
		return ""
	}
	return v.s
}

func (c *csvWriter) Close() error {
	if c.header == nil {
		h := csvHeader(c.opt.Fields)
		if len(c.opt.Fields) == 0 {
			h = csvMedewi
		}
		if err := c.cw.Write(h); err != nil {
			return err
		}
	}
	c.cw.Flush()
	return c.cw.Error()
}
