package logview

import (
	"fmt"
	"strings"

	"github.com/thomasbach-dev/medewi/leva"
)

// Column describes one output field of levalog: a medewi bookkeeping column
// or a value from the XML spec.
type Column struct {
	Key, Name, Unit string
	Factor          float64
	At, Len         int
	// Index is the XML value index, -1 for medewi columns.
	Index int
	// Source is "medewi" or "xml".
	Source string
}

// MedewiColumns are the reserved bookkeeping keys, in output order. Only
// pump maps to a byte of the report line (offset 2).
var MedewiColumns = []Column{
	{Key: "t", At: -1, Len: -1, Index: -1, Source: "medewi"},
	{Key: "wall", At: -1, Len: -1, Index: -1, Source: "medewi"},
	{Key: "src", At: -1, Len: -1, Index: -1, Source: "medewi"},
	{Key: "src_end", At: -1, Len: -1, Index: -1, Source: "medewi"},
	{Key: "dir", At: -1, Len: -1, Index: -1, Source: "medewi"},
	{Key: "event", At: -1, Len: -1, Index: -1, Source: "medewi"},
	{Key: "run", At: -1, Len: -1, Index: -1, Source: "medewi"},
	{Key: "run_t", At: -1, Len: -1, Index: -1, Source: "medewi"},
	{Key: "pump", At: 2, Len: 1, Index: -1, Source: "medewi"},
	{Key: "xml", At: -1, Len: -1, Index: -1, Source: "medewi"},
}

// Key lowercases name and turns every run of characters outside [a-z0-9]
// into a single "_", trimming leading and trailing underscores.
func Key(name string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if sep && b.Len() > 0 {
				b.WriteByte('_')
			}
			sep = false
			b.WriteRune(r)
		} else {
			sep = true
		}
	}
	return b.String()
}

// Columns returns MedewiColumns followed by one column per XML value in spec
// order. Empty names become "mode" (at offset 4) or "field_N". Duplicate keys
// get the unit's key appended (nothing when that is empty); remaining duplicates and clashes with medewi keys
// are errors.
func Columns(spec *leva.RecordSpec) ([]Column, error) {
	cols := append([]Column(nil), MedewiColumns...)
	xml := make([]Column, 0, len(spec.Values))
	for _, v := range spec.Values {
		c := Column{
			Name: v.Name, Unit: v.Unit, Factor: v.Factor,
			At: v.At, Len: v.Len, Index: v.Index, Source: "xml",
		}
		if c.Factor == 0 {
			c.Factor = 1
		}
		switch {
		case v.Name != "":
			c.Key = Key(v.Name)
		case v.At == 4:
			c.Key = "mode"
		default:
			c.Key = fmt.Sprintf("field_%d", v.Index)
		}
		if c.Key == "" {
			c.Key = fmt.Sprintf("field_%d", v.Index)
		}
		xml = append(xml, c)
	}
	count := map[string]int{}
	for _, c := range xml {
		count[c.Key]++
	}
	for i := range xml {
		if u := Key(xml[i].Unit); count[xml[i].Key] > 1 && u != "" {
			xml[i].Key += "_" + u
		}
	}
	reserved := map[string]bool{}
	for _, c := range MedewiColumns {
		reserved[c.Key] = true
	}
	seen := map[string]Column{}
	for _, c := range xml {
		if reserved[c.Key] {
			return nil, fmt.Errorf("xml value %d (%q): key %q clashes with the reserved medewi column %q",
				c.Index, c.Name, c.Key, c.Key)
		}
		if p, ok := seen[c.Key]; ok {
			return nil, fmt.Errorf("xml values %d (%q) and %d (%q) both map to key %q",
				p.Index, p.Name, c.Index, c.Name, c.Key)
		}
		seen[c.Key] = c
	}
	return append(cols, xml...), nil
}
