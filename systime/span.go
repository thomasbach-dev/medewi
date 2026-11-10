// Package systime parses and formats the subset of systemd.time(7) that
// medewi's command-line tools accept: time spans ("1h 30min") and timestamps
// ("10:42", "yesterday", "-10min"). Everything accepted here is valid in
// systemd with the same meaning; anything else is rejected.
package systime

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"
)

// units maps the systemd.time(7) unit names we support to their length.
// Months and years are deliberately absent: systemd defines them as 30.44d
// and 365.25d, which we do not need.
var units = map[string]time.Duration{
	"usec": time.Microsecond, "us": time.Microsecond, "µs": time.Microsecond, "μs": time.Microsecond,
	"msec": time.Millisecond, "ms": time.Millisecond,
	"seconds": time.Second, "second": time.Second, "sec": time.Second, "s": time.Second,
	"minutes": time.Minute, "minute": time.Minute, "min": time.Minute, "m": time.Minute,
	"hours": time.Hour, "hour": time.Hour, "hr": time.Hour, "h": time.Hour,
	"days": 24 * time.Hour, "day": 24 * time.Hour, "d": 24 * time.Hour,
	"weeks": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "w": 7 * 24 * time.Hour,
}

// ParseSpan parses a systemd.time(7) time span: whitespace-separated or
// adjacent groups of "number[.fraction] unit". A lone number without a unit
// means seconds. Negative spans, months and years are errors.
func ParseSpan(s string) (time.Duration, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty time span")
	}
	total := new(big.Int)
	rest := s
	for rest != "" {
		// digits [. digits]
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("invalid time span %q: expected a number at %q", orig, rest)
		}
		intDigits := rest[:i]
		fracDigits := ""
		if i < len(rest) && rest[i] == '.' {
			j := i + 1
			for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
				j++
			}
			if j == i+1 {
				return 0, fmt.Errorf("invalid time span %q: no digits after '.'", orig)
			}
			fracDigits = rest[i+1 : j]
			i = j
		}
		rest = strings.TrimLeft(rest[i:], " \t")
		// unit: a run of letters
		k := 0
		for _, r := range rest {
			if !unicode.IsLetter(r) {
				break
			}
			k += len(string(r))
		}
		unitName := rest[:k]
		rest = strings.TrimLeft(rest[k:], " \t")
		unit := time.Second
		if unitName == "" {
			// Only a lone number is allowed without a unit.
			if rest != "" || total.Sign() != 0 || s != intDigits+dotted(fracDigits) {
				return 0, fmt.Errorf("invalid time span %q: missing unit", orig)
			}
		} else {
			var ok bool
			unit, ok = units[unitName]
			if !ok {
				return 0, fmt.Errorf("invalid time span %q: unknown unit %q", orig, unitName)
			}
		}
		// (intDigits.fracDigits) * unit, truncated to whole nanoseconds.
		num, _ := new(big.Int).SetString(intDigits+fracDigits, 10)
		num.Mul(num, big.NewInt(int64(unit)))
		den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fracDigits))), nil)
		num.Quo(num, den)
		total.Add(total, num)
		if !total.IsInt64() {
			return 0, fmt.Errorf("invalid time span %q: out of range", orig)
		}
	}
	return time.Duration(total.Int64()), nil
}

func dotted(frac string) string {
	if frac == "" {
		return ""
	}
	return "." + frac
}

// FormatSpan renders d in the systemd style ("1h 30min"), using the units
// d, h, min, s, ms and us. The result parses back to d with ParseSpan.
// Sub-microsecond remainders become a fraction of a microsecond.
func FormatSpan(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	var parts []string
	sign := ""
	u := uint64(d)
	if d < 0 {
		sign = "-"
		u = -u
	}
	for _, f := range []struct {
		name string
		len  uint64
	}{
		{"d", uint64(24 * time.Hour)},
		{"h", uint64(time.Hour)},
		{"min", uint64(time.Minute)},
		{"s", uint64(time.Second)},
		{"ms", uint64(time.Millisecond)},
		{"us", uint64(time.Microsecond)},
	} {
		n := u / f.len
		u %= f.len
		switch {
		case f.name == "us" && u != 0:
			frac := strings.TrimRight(fmt.Sprintf("%03d", u), "0")
			parts = append(parts, fmt.Sprintf("%d.%sus", n, frac))
		case n != 0:
			parts = append(parts, fmt.Sprintf("%d%s", n, f.name))
		}
	}
	return sign + strings.Join(parts, " ")
}
