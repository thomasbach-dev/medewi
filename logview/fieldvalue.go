package logview

import (
	"sync"

	"github.com/thomasbach-dev/medewi/leva"
)

// fieldPos caches, per spec, the position in Record.Fields of each XML key.
var fieldPos sync.Map // *leva.RecordSpec -> map[string]int

func keyPositions(spec *leva.RecordSpec) map[string]int {
	if m, ok := fieldPos.Load(spec); ok {
		return m.(map[string]int)
	}
	pos := map[string]int{}
	cols, _ := Columns(spec) // a clash leaves no keys; writers report it
	for _, c := range cols {
		if c.Source != "xml" {
			continue
		}
		for p, v := range spec.Values {
			if v.Index == c.Index {
				pos[c.Key] = p
				break
			}
		}
	}
	m, _ := fieldPos.LoadOrStore(spec, pos)
	return m.(map[string]int)
}

// FieldOf returns the value of key in report e: pump is report byte 2 as
// text (masked in standby), other keys come from e's spec. It is false
// when e has no Record or its spec lacks key.
func FieldOf(e *Entry, key string) (Value, bool) {
	if e.Record == nil {
		return Value{}, false
	}
	if key == "pump" {
		if len(e.Raw) < 3 {
			return Value{}, false
		}
		s := string(e.Raw[2:3])
		if s == "?" {
			return Value{Raw: s, Masked: true}, true
		}
		return Value{Raw: s, Str: &s}, true
	}
	if e.Spec == nil {
		return Value{}, false
	}
	p, ok := keyPositions(e.Spec)[key]
	if !ok || p >= len(e.Record.Fields) {
		return Value{}, false
	}
	return ParseField(e.Record.Fields[p]), true
}

// IsTextKey reports whether key is a text field: pump, or a String field
// of spec.
func IsTextKey(spec *leva.RecordSpec, key string) bool {
	if key == "pump" {
		return true
	}
	if spec == nil {
		return false
	}
	p, ok := keyPositions(spec)[key]
	return ok && spec.Values[p].Type == leva.String
}
