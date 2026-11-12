package logview

import (
	"math"
	"strings"
	"testing"
	"time"
)

func num(f float64) Value { return Value{Num: &f} }
func str(s string) Value  { return Value{Str: &s} }

func TestParseAggs(t *testing.T) {
	got, err := ParseAggs("n,min,p90,median")
	if err != nil {
		t.Fatal(err)
	}
	wantN := []string{"n", "min", "p90", "median"}
	wantP := []int{0, 0, 90, 50}
	if len(got) != 4 {
		t.Fatalf("got %v", got)
	}
	for i, a := range got {
		if a.Name != wantN[i] || a.P != wantP[i] {
			t.Errorf("agg %d = %+v, want %s/%d", i, a, wantN[i], wantP[i])
		}
	}
	for _, bad := range []string{"p0", "p100", "avg", ""} {
		if _, err := ParseAggs(bad); err == nil {
			t.Errorf("ParseAggs(%q): want error", bad)
		}
	}
	_, err = ParseAggs("avg")
	if err == nil || !strings.Contains(err.Error(), "tmax") {
		t.Errorf("error %v should name the valid aggregates", err)
	}
}

func TestAggNumeric(t *testing.T) {
	for name, want := range map[string]bool{"n": false, "first": false, "last": false, "min": true, "p90": true, "tmax": true, "srcmin": true} {
		as, err := ParseAggs(name)
		if err != nil {
			t.Fatal(err)
		}
		if as[0].Numeric() != want {
			t.Errorf("%s Numeric = %v", name, !want)
		}
	}
}

func fill(vals ...float64) *Acc {
	var c Acc
	for i, v := range vals {
		c.Add(num(v), time.Duration(i+1)*time.Second, "f:"+string(rune('1'+i)))
	}
	return &c
}

func res(t *testing.T, c *Acc, name string) AggValue {
	t.Helper()
	as, err := ParseAggs(name)
	if err != nil {
		t.Fatal(err)
	}
	return c.Result(as[0])
}

func wantNum(t *testing.T, c *Acc, name string, want float64) {
	t.Helper()
	r := res(t, c, name)
	if r.Num == nil || math.Abs(*r.Num-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, r.Num, want)
	}
}

func TestAccNumbers(t *testing.T) {
	c := fill(5, 1, 3, 2, 4)
	for name, want := range map[string]float64{
		"n": 5, "first": 5, "last": 4, "min": 1, "max": 5, "mean": 3,
		"median": 3, "p90": 5, "p10": 1, "sd": math.Sqrt2,
		"tmin": 2, "tmax": 1,
	} {
		wantNum(t, c, name, want)
	}
	if r := res(t, c, "srcmin"); r.Str == nil || *r.Str != "f:2" {
		t.Errorf("srcmin = %v", r.Str)
	}
	if r := res(t, c, "srcmax"); r.Str == nil || *r.Str != "f:1" {
		t.Errorf("srcmax = %v", r.Str)
	}
}

func TestAccNearestRank(t *testing.T) {
	c := fill(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	wantNum(t, c, "p25", 3)
	wantNum(t, c, "p50", 5)
	wantNum(t, c, "p75", 8)
}

func TestAccMaskedAndBad(t *testing.T) {
	var c Acc
	c.Add(Value{Masked: true}, time.Second, "f:1")
	c.Add(Value{Bad: true}, 2*time.Second, "f:2")
	wantNum(t, &c, "n", 0)
	for _, name := range []string{"first", "last", "min", "max", "mean", "median", "p90", "sd", "tmin", "tmax", "srcmin", "srcmax"} {
		if r := res(t, &c, name); r.Num != nil || r.Str != nil {
			t.Errorf("%s = %+v, want empty", name, r)
		}
	}
}

func TestAccFirstOccurrence(t *testing.T) {
	var c Acc
	for i, v := range []float64{2, 7, 7} {
		c.Add(num(v), time.Duration(i+1)*time.Second, "x")
	}
	wantNum(t, &c, "tmax", 2)
}

func TestAccText(t *testing.T) {
	var c Acc
	c.Add(str("PID"), time.Second, "f:1")
	c.Add(str("ECO"), 2*time.Second, "f:2")
	if r := res(t, &c, "first"); r.Str == nil || *r.Str != "PID" {
		t.Errorf("first = %+v", r)
	}
	if r := res(t, &c, "last"); r.Str == nil || *r.Str != "ECO" {
		t.Errorf("last = %+v", r)
	}
	wantNum(t, &c, "n", 2)
	if r := res(t, &c, "min"); r.Num != nil || r.Str != nil {
		t.Errorf("min of text = %+v", r)
	}
}
