package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/watch"
)

// Mark refusals; nothing is written.
var (
	ErrNoRawLog    = errors.New("daemon: no raw traffic log (disabled or not connected)")
	ErrInvalidMark = errors.New("daemon: a mark is 1 to 200 bytes on one line")
)

const maxMark = 200

// readyNotice is the machine's own READY announcement.
const readyNotice = "[READY]"

// notice handles an unsolicited bracketed line from the machine.
func (d *Daemon) notice(ctx context.Context, n []byte) {
	if string(n) != readyNotice {
		slog.Info("machine notice", "text", string(n))
		return
	}
	var ev watch.Event
	d.update(func(s *Status) {
		s.MachineReady = true
		ev = watch.Event{Kind: watch.MachineReady}
		if s.Temp != nil {
			ev.Temp = *s.Temp
		}
		if s.Setpoint != nil {
			ev.Setpoint = *s.Setpoint
		}
	})
	d.emit(ctx, ev)
}

// frame records a sampled display frame in the status.
func (d *Daemon) frame(raw []byte) {
	f, err := leva.ParseFrame(raw)
	if err != nil {
		return
	}
	snap := &DisplaySnapshot{Rows: f.Rows, At: time.Now()}
	d.update(func(s *Status) { s.Display = snap })
}

// sampleDisplay switches conn to the LCD mirror for DisplayFor every
// DisplayEvery, on the command worker so that it never interleaves with a
// power change or a dump, until ctx ends. A failed switch is retried at the
// next tick; a dead connection ends the session elsewhere.
func (d *Daemon) sampleDisplay(ctx context.Context, conn *leva.Conn) {
	t := time.NewTicker(d.cfg.DisplayEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		_ = d.do(ctx, func(rctx context.Context, cur *leva.Conn) {
			if cur != conn {
				return // the session this sampler belongs to is over
			}
			if err := cur.StartDisplay(rctx); err != nil {
				slog.Debug("display sample failed", "err", err)
				return
			}
			sleepCtx(rctx, d.cfg.DisplayFor)
			if _, err := cur.StartReports(rctx); err != nil {
				slog.Debug("switching back to reports failed", "err", err)
			}
		})
	}
}

// Mark writes text as a "mark: …" note into the current session's raw
// traffic log, so a moment noticed at the machine can be found again.
// While released it fails with ErrReleased.
func (d *Daemon) Mark(text string) error {
	if text == "" || len(text) > maxMark || strings.ContainsAny(text, "\r\n") {
		return ErrInvalidMark
	}
	d.mu.RLock()
	rs, released, until := d.rs, d.releasedNow(), d.st.ReleasedUntil
	d.mu.RUnlock()
	if released {
		return releasedErr(*until)
	}
	if rs == nil {
		return ErrNoRawLog
	}
	rs.Note("mark: " + text)
	return nil
}
