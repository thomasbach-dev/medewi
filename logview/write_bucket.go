package logview

import (
	"encoding/csv"
	"fmt"
	"io"
	"slices"
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
// are t, wall, then run and run_t when runCols, then reports and cols;
// jsonl and csv have src (the row's source path) after wall. Text is a
// table with a lowercase header, wall in loc ("-" without); a "# PATH"
// line (with "# notes: ..." for a capture with notes) when the source
// changes and "# xml: NAME" when the XML does start a new table, header
// repeated. csv has a header row; jsonl one object per row. jsonl and csv
// carry wall in UTC, null or empty without.
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
		_ = bw.cw.Write(slices.Insert(slices.Clone(header), 2, "src"))
	default:
		bw.tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
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
	started bool   // text: a table is open
	path    string // text: the source and XML announced last
	xml     string
}

// textHead starts a new text table when r's source or XML differs from
// the last row's: the comment lines, then the header.
func (b *bucketWriter) textHead(r BucketRow) error {
	if b.started && r.Source.Path == b.path && r.XML == b.xml {
		return nil
	}
	if err := b.tw.Flush(); err != nil { // the comments end a table
		return err
	}
	var s strings.Builder
	if !b.started || r.Source.Path != b.path {
		fmt.Fprintf(&s, "# %s\n", r.Source.Path)
		if r.Source.Notes != "" {
			fmt.Fprintf(&s, "# notes: %s\n", r.Source.Notes)
		}
	}
	if r.XML != b.xml {
		fmt.Fprintf(&s, "# xml: %s\n", orDash(r.XML))
	}
	b.started, b.path, b.xml = true, r.Source.Path, r.XML
	if _, err := io.WriteString(b.w, s.String()); err != nil {
		return err
	}
	_, err := fmt.Fprintln(b.tw, strings.Join(b.header, "\t"))
	return err
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
		if err := b.textHead(r); err != nil {
			return err
		}
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
		rec := []string{seconds(r.Start), wall, r.Source.Path}
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
	o.str("src", r.Source.Path)
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
		if !b.started { // no row: the header alone
			_, _ = fmt.Fprintln(b.tw, strings.Join(b.header, "\t"))
		}
		return b.tw.Flush()
	}
	if b.cw != nil {
		b.cw.Flush()
		return b.cw.Error()
	}
	return nil
}
