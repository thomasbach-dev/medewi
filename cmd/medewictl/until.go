package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var (
	untilGroup = regexp.MustCompile(`^(\d+)(h|min|s)`)
	untilUnits = map[string]time.Duration{"h": time.Hour, "min": time.Minute, "s": time.Second}
)

// parseUntil turns "+1h30min" style text (systemd units h, min, s; groups add
// up, any order, no whitespace) into the absolute time now plus that duration.
func parseUntil(s string, now time.Time) (time.Time, error) {
	if len(s) == 0 || s[0] != '+' {
		return time.Time{}, fmt.Errorf("bad -until %q: want + followed by e.g. 30min, 1h30min, 90s", s)
	}
	rest := s[1:]
	var total time.Duration
	for rest != "" {
		m := untilGroup.FindStringSubmatch(rest)
		if m == nil {
			return time.Time{}, fmt.Errorf("bad -until %q: cannot parse %q (units: h, min, s)", s, rest)
		}
		n, err := strconv.ParseInt(m[1], 10, 32)
		if err != nil {
			return time.Time{}, fmt.Errorf("bad -until %q: number too large", s)
		}
		total += time.Duration(n) * untilUnits[m[2]]
		rest = rest[len(m[0]):]
	}
	if total == 0 {
		return time.Time{}, errors.New("bad -until " + strconv.Quote(s) + ": must be longer than zero")
	}
	return now.Add(total), nil
}
