package daemon

import (
	"log/slog"
	"time"
)

// outage tracks a run of failed dials, so a switched-off machine, dialled
// every BackoffMax, keeps the journal quiet: the first failure is logged at
// info, a reminder at most every `every`, the rest at debug. Run goroutine
// only.
type outage struct {
	every        time.Duration
	fails        int       // failed dials since the last successful one
	since, noted time.Time // first failure; last failure logged at info
}

// fail records a failed dial at now. It returns the level to log it at and
// whether it is the periodic "still unreachable" reminder.
func (o *outage) fail(now time.Time) (lvl slog.Level, reminder bool) {
	o.fails++
	switch {
	case o.fails == 1:
		o.since, o.noted = now, now
		return slog.LevelInfo, false
	case now.Sub(o.noted) >= o.every:
		o.noted = now
		return slog.LevelInfo, true
	}
	return slog.LevelDebug, false
}

// unreachableFor is the outage's length at now.
func (o *outage) unreachableFor(now time.Time) time.Duration { return now.Sub(o.since) }

// end closes the outage at a successful dial: it returns the failed dials
// and the time since the first one, and ok false if there were none.
func (o *outage) end(now time.Time) (fails int, d time.Duration, ok bool) {
	if o.fails == 0 {
		return 0, 0, false
	}
	fails, d = o.fails, o.unreachableFor(now)
	*o = outage{every: o.every}
	return fails, d, true
}
