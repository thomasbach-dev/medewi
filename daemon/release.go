package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// Release refusals and failures. ErrReleased wraps every request refused
// because the machine is released; its message names the end time.
var (
	ErrReleased       = errors.New("daemon: machine released")
	ErrInvalidRelease = errors.New("daemon: release end must be in the future")
	ErrReleaseStore   = errors.New("daemon: release state could not be saved")
)

// releaseBannerTimeout bounds the wait for the idle banner after the
// release's "MC " (banners come about once a second). A variable so tests
// can shrink it.
var releaseBannerTimeout = 3 * time.Second

// errReleased is the cancel cause of a session ended by Release.
var errReleased = errors.New("daemon: session ended by a release")

// sessionHandle lets Release end the running session and wait for it.
type sessionHandle struct {
	cancel context.CancelCauseFunc
	done   chan struct{} // closed once the session has fully ended
	// banner is the session's own result for ReleaseResult.Banner; written
	// by the session goroutine before done closes.
	banner *bool
}

// ReleaseResult is a successful release: its end and whether the idle
// banner was seen after the session ended (nil: no session was up).
type ReleaseResult struct {
	Until  time.Time
	Banner *bool
}

// releasedErr is the refusal for a request while released until until.
func releasedErr(until time.Time) error {
	return fmt.Errorf("%w until %s", ErrReleased, until.Local().Format(time.DateTime))
}

// Release gives up the machine until until, which must lie in the future
// (ErrInvalidRelease); a release while released replaces the end. The end
// is persisted first: if that fails, nothing changes and the error wraps
// ErrReleaseStore. A running session then stops the report stream, checks
// for the idle banner (bounded by releaseBannerTimeout) and closes, and only
// then does Release return. If ctx ends first it returns ctx's error; the
// release stays in effect.
func (d *Daemon) Release(ctx context.Context, until time.Time) (ReleaseResult, error) {
	if !until.After(time.Now()) {
		return ReleaseResult{}, ErrInvalidRelease
	}
	until = until.UTC()
	if err := d.storeRelease(ctx, func(ctx context.Context) error {
		return d.cfg.Store.SetRelease(ctx, until)
	}); err != nil {
		return ReleaseResult{}, err
	}
	// Setting the state and taking d.sess under one lock: a session either
	// registered before (and is cancelled here) or sees the release and
	// refuses to start.
	d.mu.Lock()
	u := until
	d.st.ReleasedUntil = &u
	d.met.status(d.st)
	h := d.sess
	if h != nil {
		h.cancel(errReleased)
	}
	d.mu.Unlock()
	d.signalWake()
	slog.Info("machine released", "until", until.Local().Format(time.DateTime), "session", h != nil)
	res := ReleaseResult{Until: until}
	if h == nil {
		return res, nil
	}
	select {
	case <-h.done:
	case <-ctx.Done():
		return res, ctx.Err()
	}
	res.Banner = h.banner
	return res, nil
}

// Reclaim ends a release early and wakes the reconnect loop; it does not
// wait for the reconnect. It is no error when nothing is released. If the
// persisted release cannot be cleared the daemon stays released and the
// error wraps ErrReleaseStore.
func (d *Daemon) Reclaim(ctx context.Context) error {
	if err := d.storeRelease(ctx, d.cfg.Store.ClearRelease); err != nil {
		return err
	}
	var was *time.Time
	d.update(func(s *Status) { was, s.ReleasedUntil = s.ReleasedUntil, nil })
	d.signalWake()
	if was != nil {
		slog.Info("machine reclaimed")
	}
	return nil
}

// storeRelease runs a release store write bounded by storeTimeout.
func (d *Daemon) storeRelease(ctx context.Context, write func(context.Context) error) error {
	if d.cfg.Store == nil {
		return fmt.Errorf("%w: no store", ErrReleaseStore)
	}
	sctx, cancel := storeCtx(ctx)
	defer cancel()
	if err := write(sctx); err != nil {
		slog.Error("storing the machine release", "err", err)
		return fmt.Errorf("%w: %w", ErrReleaseStore, err)
	}
	return nil
}

// signalWake nudges the reconnect loop without blocking; one pending
// wake-up is enough.
func (d *Daemon) signalWake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// releasedNow reports whether the state holds a release that has not yet
// run out. Call with d.mu held.
func (d *Daemon) releasedNow() bool {
	r := d.st.ReleasedUntil
	return r != nil && r.After(time.Now())
}

// waitReleased returns once no release is in effect: at once if none is,
// else at its end or a reclaim. An expired release is cleared from the
// status (not from the store: a past end has no effect). waited says
// whether it had to wait; ok is false when ctx ended first.
func (d *Daemon) waitReleased(ctx context.Context) (waited, ok bool) {
	for {
		d.mu.Lock()
		r := d.st.ReleasedUntil
		if r == nil || !r.After(time.Now()) {
			if r != nil {
				d.st.ReleasedUntil = nil
				d.met.status(d.st)
				slog.Info("machine release ended")
			}
			d.mu.Unlock()
			return waited, true
		}
		until := *r
		d.mu.Unlock()
		waited = true
		t := time.NewTimer(time.Until(until))
		select {
		case <-ctx.Done():
			t.Stop()
			return waited, false
		case <-t.C:
		case <-d.wake:
			t.Stop()
		}
	}
}

// sleepWake is sleepCtx that also ends early on a wake-up (a reclaim must
// not wait out a long backoff); false means ctx is done.
func (d *Daemon) sleepWake(ctx context.Context, dur time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if dur <= 0 {
		return true
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-d.wake:
	}
	return true
}

// stopForRelease sends "MC " and waits up to releaseBannerTimeout for a new
// idle banner, the sign that the machine is back to idle and free for the
// next client. It reports whether the banner came.
func (d *Daemon) stopForRelease(ctx context.Context, conn *leva.Conn) bool {
	n := conn.Banners()
	d.stopReports(ctx, conn)
	deadline := time.NewTimer(releaseBannerTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
wait:
	for conn.Banners() <= n && conn.Err() == nil {
		select {
		case <-tick.C:
		case <-deadline.C:
			break wait
		case <-ctx.Done(): // shutdown: no point waiting
			break wait
		}
	}
	if conn.Banners() > n {
		return true
	}
	slog.Warn("idle banner did not return after MC ; releasing anyway")
	return false
}
