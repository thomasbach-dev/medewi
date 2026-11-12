package logview

import (
	"math"
	"strconv"
	"strings"

	"github.com/thomasbach-dev/medewi/leva"
)

// Value is one report field in parsed form. Exactly one of Num, Str, Masked
// and Bad describes it.
type Value struct {
	Raw    string
	Num    *float64 // Float/Int fields, factor applied, rounded to 6 decimals, never -0
	Str    *string  // String fields, Latin-1 converted to UTF-8
	Masked bool     // Raw is non-empty and all '?'
	Bad    bool     // not parseable as the field's type
}

// ParseField parses f according to its spec type. An empty String field is a
// valid empty string; an empty number is Bad.
func ParseField(f leva.Field) Value {
	v := Value{Raw: f.Raw}
	if f.Raw != "" && strings.Trim(f.Raw, "?") == "" {
		v.Masked = true
		return v
	}
	if f.Spec.Type == leva.String {
		s := latin1([]byte(f.Raw))
		v.Str = &s
		return v
	}
	n, err := strconv.ParseFloat(f.Raw, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		v.Bad = true
		return v
	}
	if f.Spec.Factor != 0 {
		n *= f.Spec.Factor
	}
	n = math.Round(n*1e6) / 1e6
	if n == 0 {
		n = 0 // no negative zero
	}
	v.Num = &n
	return v
}

// FormatNum prints v in its shortest form.
func FormatNum(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
