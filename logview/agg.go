package logview

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const validAggs = "n, first, last, min, max, mean, median, pNN (1-99), sd, tmin, tmax, srcmin, srcmax"

// Agg is one aggregate. Name is as written ("p90", "median"); P is the
// percentile for pNN and median (50), 0 otherwise.
type Agg struct {
	Name string
	P    int
}

// ParseAggs parses a comma-separated aggregate list.
func ParseAggs(s string) ([]Agg, error) {
	var out []Agg
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		a := Agg{Name: name}
		switch name {
		case "n", "first", "last", "min", "max", "mean", "sd", "tmin", "tmax", "srcmin", "srcmax":
		case "median":
			a.P = 50
		default:
			p, err := strconv.Atoi(strings.TrimPrefix(name, "p"))
			if !strings.HasPrefix(name, "p") || err != nil || p < 1 || p > 99 ||
				strconv.Itoa(p) != name[1:] {
				return nil, fmt.Errorf("unknown aggregate %q (valid: %s)", name, validAggs)
			}
			a.P = p
		}
		out = append(out, a)
	}
	return out, nil
}

// Numeric reports whether the aggregate only applies to numbers; n, first
// and last also work on text.
func (a Agg) Numeric() bool {
	switch a.Name {
	case "n", "first", "last":
		return false
	}
	return true
}

// AggValue is an aggregate result; both nil means empty (masked output).
type AggValue struct {
	Num *float64
	Str *string
}

// Acc accumulates the values of one field. The zero value is usable.
type Acc struct {
	n           int
	nums        []float64
	first, last Value
	min, max    float64
	minT, maxT  time.Duration
	minS, maxS  string
}

// Add counts v; masked and bad values are ignored. t and src locate the
// value for tmin/tmax/srcmin/srcmax; the first occurrence of a tie wins.
func (c *Acc) Add(v Value, t time.Duration, src string) {
	if v.Masked || v.Bad || (v.Num == nil && v.Str == nil) {
		return
	}
	if c.n == 0 {
		c.first = v
	}
	c.last = v
	c.n++
	if v.Num == nil {
		return
	}
	x := *v.Num
	if len(c.nums) == 0 || x < c.min {
		c.min, c.minT, c.minS = x, t, src
	}
	if len(c.nums) == 0 || x > c.max {
		c.max, c.maxT, c.maxS = x, t, src
	}
	c.nums = append(c.nums, x)
}

// Result evaluates a; an aggregate without data is empty.
func (c *Acc) Result(a Agg) AggValue {
	switch a.Name {
	case "n":
		return numVal(float64(c.n))
	case "first", "last":
		v := c.first
		if a.Name == "last" {
			v = c.last
		}
		var out AggValue
		if v.Num != nil {
			x := *v.Num
			out.Num = &x
		}
		if v.Str != nil {
			s := *v.Str
			out.Str = &s
		}
		return out
	}
	if len(c.nums) == 0 {
		return AggValue{}
	}
	switch a.Name {
	case "min":
		return numVal(c.min)
	case "max":
		return numVal(c.max)
	case "tmin":
		return numVal(roundMilli(c.minT))
	case "tmax":
		return numVal(roundMilli(c.maxT))
	case "srcmin":
		s := c.minS
		return AggValue{Str: &s}
	case "srcmax":
		s := c.maxS
		return AggValue{Str: &s}
	case "mean":
		return numVal(round6(c.mean()))
	case "sd":
		m, ss := c.mean(), 0.0
		for _, x := range c.nums {
			ss += (x - m) * (x - m)
		}
		return numVal(round6(math.Sqrt(ss / float64(len(c.nums)))))
	}
	// pNN and median: nearest rank, ceil(P/100*n).
	sorted := slices.Sorted(slices.Values(c.nums))
	rank := (a.P*len(sorted) + 99) / 100
	return numVal(sorted[rank-1])
}

func (c *Acc) mean() float64 {
	sum := 0.0
	for _, x := range c.nums {
		sum += x
	}
	return sum / float64(len(c.nums))
}

func numVal(f float64) AggValue { return AggValue{Num: &f} }

// round6 rounds to 6 decimals, as ParseField does, never -0.
func round6(f float64) float64 {
	f = math.Round(f*1e6) / 1e6
	if f == 0 {
		f = 0
	}
	return f
}
