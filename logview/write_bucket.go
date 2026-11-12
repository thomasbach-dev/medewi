package logview

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// BucketWriter writes bucket rows in one format; Close flushes.
type BucketWriter interface {
	Write(BucketRow) error
	Close() error
}

// NewBucketWriter returns a BucketWriter of format f on w. The columns
// are t, wall, then run and run_t when runCols, then reports and cols.
// Text is a table with a lowercase header, wall in loc ("-" without);
// csv has a header row; jsonl one object per row. jsonl and csv carry
// wall in UTC, null or empty without.
func NewBucketWriter(w io.Writer, f Format, cols []BucketCol, runCols bool, loc *time.Location) BucketWriter {
	if loc == nil {
		loc = time.Local
	}
	header := []string{"t", "wall"}
	if runCols {
		header = append(header, "run", "run_t")
	}
	header = append(header, "reports")
	for _, c := range cols {
		header = append(header, c.Name())
	}
	bw := &bucketWriter{f: f, w: w, header: header, runCols: runCols, loc: loc}
	switch f {
	case FormatJSONL:
	case FormatCSV:
		bw.cw = csv.NewWriter(w)
		_ = bw.cw.Write(header)
	default:
		bw.tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(bw.tw, strings.Join(header, "\t"))
	}
	return bw
}

type bucketWriter struct {
	f       Format
	w       io.Writer
	tw      *tabwriter.Writer
	cw      *csv.Writer
	header  []string
	runCols bool
	loc     *time.Location
}

func (b *bucketWriter) Write(r BucketRow) error {
	run, runT := "", ""
	if r.Run != nil {
		run = strconv.Itoa(r.Run.N)
	}
	if r.RunT != nil {
		runT = seconds(*r.RunT)
	}
	reports := strconv.Itoa(r.Reports)
	nv := len(b.header) - len(r.Values)
	switch {
	case b.tw != nil:
		wall := "-"
		if r.HasWall {
			wall = r.Wall.In(b.loc).Format(wallLayout)
		}
		cells := []string{fmt.Sprintf("%.3f", roundMilli(r.Start)), wall}
		if b.runCols {
			if r.RunT != nil {
				runT = fmt.Sprintf("%.3f", roundMilli(*r.RunT))
			}
			cells = append(cells, orDash(run), orDash(runT))
		}
		cells = append(cells, reports)
		for _, v := range r.Values {
			cells = append(cells, aggTextCell(v))
		}
		_, err := fmt.Fprintln(b.tw, strings.Join(cells, "\t"))
		return err
	case b.cw != nil:
		wall := ""
		if r.HasWall {
			wall = utcWall(r.Wall)
		}
		rec := []string{seconds(r.Start), wall}
		if b.runCols {
			rec = append(rec, run, runT)
		}
		rec = append(rec, reports)
		for _, v := range r.Values {
			rec = append(rec, aggText(v))
		}
		return b.cw.Write(rec)
	}
	var o jsonObj
	o.raw("t", seconds(r.Start))
	if r.HasWall {
		o.str("wall", utcWall(r.Wall))
	} else {
		o.raw("wall", "null")
	}
	if b.runCols {
		o.raw("run", orNull(run))
		o.raw("run_t", orNull(runT))
	}
	o.raw("reports", reports)
	for i, v := range r.Values {
		o.agg(b.header[nv+i], v)
	}
	_, err := b.w.Write(o.line())
	return err
}

func (b *bucketWriter) Close() error {
	if b.tw != nil {
		return b.tw.Flush()
	}
	if b.cw != nil {
		b.cw.Flush()
		return b.cw.Error()
	}
	return nil
}
