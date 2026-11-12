package logview

import (
	"testing"
	"unicode/utf8"

	"github.com/thomasbach-dev/medewi/leva"
)

func field(t leva.ValueType, factor float64, raw string) leva.Field {
	return leva.Field{Spec: leva.ValueSpec{Type: t, Factor: factor}, Raw: raw}
}

func TestParseFieldNumber(t *testing.T) {
	v := ParseField(field(leva.Float, 1, "091.00"))
	if v.Num == nil || *v.Num != 91 || v.Masked || v.Bad {
		t.Fatalf("float: %+v", v)
	}
	v = ParseField(field(leva.Int, 0.0166, "060"))
	if v.Num == nil || FormatNum(*v.Num) != "0.996" {
		t.Fatalf("int: %+v", v)
	}
	v = ParseField(field(leva.Float, 0, "1.5"))
	if v.Num == nil || *v.Num != 1.5 {
		t.Fatalf("zero factor: %+v", v)
	}
	if v.Raw != "1.5" {
		t.Errorf("Raw = %q", v.Raw)
	}
}

func TestParseFieldMasked(t *testing.T) {
	v := ParseField(field(leva.Float, 1, "?????"))
	if !v.Masked || v.Num != nil || v.Bad {
		t.Fatalf("%+v", v)
	}
	v = ParseField(field(leva.String, 0, "???"))
	if !v.Masked || v.Str != nil {
		t.Fatalf("string: %+v", v)
	}
}

func TestParseFieldBad(t *testing.T) {
	for _, raw := range []string{"09?.0", "", "abc"} {
		v := ParseField(field(leva.Float, 1, raw))
		if !v.Bad || v.Masked || v.Num != nil {
			t.Errorf("%q: %+v", raw, v)
		}
	}
}

func TestParseFieldString(t *testing.T) {
	v := ParseField(field(leva.String, 0, ""))
	if v.Bad || v.Str == nil || *v.Str != "" {
		t.Fatalf("empty string: %+v", v)
	}
}

func TestParseFieldLatin1(t *testing.T) {
	v := ParseField(field(leva.String, 0, "SOAK 1.0\xb0"))
	if v.Str == nil || !utf8.ValidString(*v.Str) || *v.Str != "SOAK 1.0°" {
		t.Fatalf("%+v", v)
	}
}

func TestFormatNum(t *testing.T) {
	if got := FormatNum(91); got != "91" {
		t.Errorf("got %q", got)
	}
	if got := FormatNum(0.5); got != "0.5" {
		t.Errorf("got %q", got)
	}
}

func TestParseFieldNegativeZero(t *testing.T) {
	for _, raw := range []string{"-000.0000", "-0.0000001"} {
		v := ParseField(field(leva.Float, 1, raw))
		if v.Num == nil || FormatNum(*v.Num) != "0" {
			t.Errorf("%q: %+v", raw, v)
		}
	}
}
