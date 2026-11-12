package logview

import (
	"fmt"
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
		"median": 3, "p90": 5, "p10": 1, "sd": 1.414214, // sqrt 2, rounded to 6 decimals
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

func TestAccResultRoundedCopies(t *testing.T) {
	var c Acc
	x := 3.0
	c.Add(Value{Num: &x}, 1234567*time.Microsecond, "f:1")
	wantNum(t, &c, "tmax", 1.235)
	wantNum(t, &c, "tmin", 1.235)
	r := res(t, &c, "first")
	*r.Num = 99
	s := res(t, &c, "srcmax")
	*s.Str = "changed"
	if x != 3 {
		t.Error("first points into the caller's Value")
	}
	wantNum(t, &c, "last", 3)
	if s := res(t, &c, "srcmax"); *s.Str != "f:1" {
		t.Errorf("srcmax points into the accumulator: %q", *s.Str)
	}
	wantNum(t, fill(1, 2, 2), "mean", 1.666667)
}

func TestParseAggsDedup(t *testing.T) {
	got, err := ParseAggs("max,min,max,p90,p90")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if strings.Join(names, ",") != "max,min,p90" {
		t.Errorf("got %v, want max,min,p90", names)
	}
}

// An Acc told its aggregates keeps the values only for a percentile, and
// gives the same results either way.
func TestAccKeepsValuesOnlyForPercentiles(t *testing.T) {
	all, err := ParseAggs("n,first,last,min,max,mean,median,p90,sd,tmin,tmax,srcmin,srcmax")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := ParseAggs("n,first,last,min,max,mean,sd,tmin,tmax,srcmin,srcmax")
	if err != nil {
		t.Fatal(err)
	}
	full, lean := NewAcc(all), NewAcc(plain)
	vals := []float64{5, 1, 3, 2, 4, 1e6 + 0.1, 1e6 + 0.2, -7.25}
	for i, v := range vals {
		at, src := time.Duration(i+1)*time.Second, "f:"+string(rune('1'+i))
		full.Add(num(v), at, src)
		lean.Add(num(v), at, src)
	}
	if lean.nums != nil {
		t.Errorf("lean Acc keeps %d values", len(lean.nums))
	}
	if len(full.nums) != len(vals) {
		t.Errorf("full Acc keeps %d values, want %d", len(full.nums), len(vals))
	}
	ref := fill(vals...)
	for _, a := range plain {
		w, g := ref.Result(a), lean.Result(a)
		if fmt.Sprint(deref(w)) != fmt.Sprint(deref(g)) {
			t.Errorf("%s = %v, want %v", a.Name, deref(g), deref(w))
		}
	}
	wantNum(t, &full, "p90", 1e6+0.2)
}

func deref(v AggValue) any {
	switch {
	case v.Num != nil:
		return *v.Num
	case v.Str != nil:
		return *v.Str
	}
	return nil
}

// tmin and tmax are marked as times, so text prints them like t.
func TestAccTimesMarked(t *testing.T) {
	c := fill(1, 2)
	if r := res(t, c, "tmin"); !r.Seconds {
		t.Error("tmin not marked as seconds")
	}
	if r := res(t, c, "max"); r.Seconds {
		t.Error("max marked as seconds")
	}
	if got := aggTextCell(res(t, c, "tmax")); got != "2.000" {
		t.Errorf("text tmax = %q, want 2.000", got)
	}
	if got := aggText(res(t, c, "tmax")); got != "2" {
		t.Errorf("csv tmax = %q, want 2", got)
	}
}

// On ties, tmin/tmax and srcmin/srcmax name the first occurrence.
func TestAccTiesFirst(t *testing.T) {
	c := fill(2, 1, 3, 1, 3)
	wantNum(t, c, "tmin", 2)
	wantNum(t, c, "tmax", 3)
	if s := res(t, c, "srcmin").Str; s == nil || *s != "f:2" {
		t.Errorf("srcmin = %v, want f:2", s)
	}
	if s := res(t, c, "srcmax").Str; s == nil || *s != "f:3" {
		t.Errorf("srcmax = %v, want f:3", s)
	}
}
