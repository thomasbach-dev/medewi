package systime

import (
	"fmt"
	"strings"
	"time"
)

// ParseTimestamp parses the systemd.time(7) timestamp forms medewi accepts:
//
//	now, today, yesterday, tomorrow
//	YYYY-MM-DD HH:MM:SS, YYYY-MM-DD HH:MM, YYYY-MM-DD
//	HH:MM:SS, HH:MM            (today)
//	+SPAN, -SPAN, SPAN ago     (relative to now)
//
// A trailing " UTC" interprets the time in UTC instead of loc. Wall-clock
// times are resolved in loc like journalctl does; for a time that occurs
// twice at a DST change the choice is time.Date's (currently the later one).
func ParseTimestamp(s string, now time.Time, loc *time.Location) (time.Time, error) {
	orig := s
	s = strings.TrimSpace(s)
	if cut, ok := strings.CutSuffix(s, " UTC"); ok {
		s, loc = strings.TrimSpace(cut), time.UTC
	}
	if strings.Contains(s, ",") {
		// Go accepts a comma fraction separator; systemd does not.
		return time.Time{}, fmt.Errorf("invalid timestamp %q", orig)
	}
	local := now.In(loc)
	midnight := func(offsetDays int) time.Time {
		y, m, d := local.Date()
		return time.Date(y, m, d+offsetDays, 0, 0, 0, 0, loc)
	}
	switch s {
	case "now":
		return now, nil
	case "today":
		return midnight(0), nil
	case "yesterday":
		return midnight(-1), nil
	case "tomorrow":
		return midnight(1), nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			y, m, d := local.Date()
			return time.Date(y, m, d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc), nil
		}
	}
	var span string
	sign := time.Duration(1)
	switch {
	case strings.HasPrefix(s, "+"):
		span = s[1:]
	case strings.HasPrefix(s, "-"):
		span, sign = s[1:], -1
	case strings.HasSuffix(s, " ago"):
		span, sign = strings.TrimSuffix(s, " ago"), -1
	default:
		return time.Time{}, fmt.Errorf("invalid timestamp %q", orig)
	}
	d, err := ParseSpan(span)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q: %w", orig, err)
	}
	return now.Add(sign * d), nil
}
