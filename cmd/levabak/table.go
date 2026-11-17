package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// table is the output of a subcommand: named columns and string rows. An
// empty cell is null in jsonl, empty in csv and "-" in text; columns in num
// print as JSON numbers.
type table struct {
	cols []string
	num  map[string]bool
	rows [][]string
}

// write prints t as text (aligned, with a header; nothing when there are
// no rows), jsonl (one object per row, keys in column order) or csv
// (header and rows).
func (t table) write(w io.Writer, format string) error {
	switch format {
	case "jsonl":
		for _, r := range t.rows {
			var b bytes.Buffer
			b.WriteByte('{')
			for i, c := range t.cols {
				if i > 0 {
					b.WriteByte(',')
				}
				k, _ := json.Marshal(c)
				b.Write(k)
				b.WriteByte(':')
				b.Write(t.jsonValue(c, r[i]))
			}
			b.WriteString("}\n")
			if _, err := w.Write(b.Bytes()); err != nil {
				return err
			}
		}
		return nil
	case "csv":
		cw := csv.NewWriter(w)
		_ = cw.Write(t.cols)
		for _, r := range t.rows {
			_ = cw.Write(r)
		}
		cw.Flush()
		return cw.Error()
	}
	if len(t.rows) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	row := func(cells []string) {
		for i, c := range cells {
			if c == "" {
				c = "-"
			}
			if i > 0 {
				_, _ = fmt.Fprint(tw, "\t")
			}
			_, _ = fmt.Fprint(tw, c)
		}
		_, _ = fmt.Fprintln(tw)
	}
	row(t.cols)
	for _, r := range t.rows {
		row(r)
	}
	return tw.Flush()
}

func (t table) jsonValue(col, cell string) []byte {
	if cell == "" {
		return []byte("null")
	}
	if t.num[col] {
		var n json.Number
		if json.Unmarshal([]byte(cell), &n) == nil {
			return []byte(cell)
		}
	}
	b, _ := json.Marshal(cell)
	return b
}
