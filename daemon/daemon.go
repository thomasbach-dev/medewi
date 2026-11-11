// Package daemon is medewid's core: it owns the single connection to the
// machine, reconnects with backoff, gates the report stream on a spec-name
// handshake, feeds report lines to the watch detector and the brew recorder,
// stores brews, and publishes a Status snapshot. A single command worker
// runs power changes, settings dumps and display samples on the same
// connection. The machine's "[READY]" notice sets MachineReady. With
// Config.Raw set, each connection's raw traffic goes to a rawlog session
// (dump replies are never logged), where marks are noted too. Release
// gives the machine up until a time or a Reclaim: while released the daemon
// sends nothing and does not dial; the release is persisted in the store and
// survives restarts.
//
// The only strings it sends are the handshake and stream commands "MC?",
// "MCr" and "MC ", the display mirror "MC@", the dumps "MCu" and "MCm", and
// the power toggles "MCa", "MCe" and "MCE".
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/rawlog"
	"github.com/thomasbach-dev/medewi/store"
	"github.com/thomasbach-dev/medewi/watch"
)

const (
	defaultSettle     = 1500 * time.Millisecond
	defaultMinBackoff = time.Second
	defaultMaxBackoff = 3 * time.Minute
	defaultDisplayFor = 500 * time.Millisecond

	readyTimeout = 10 * time.Second       // bound on WaitReady per attempt
	stopTimeout  = 2 * time.Second        // bound on the final "MC "
	storeTimeout = 5 * time.Second        // bound on each store call
	mcqIdleGap   = 300 * time.Millisecond // MC? reply collection gap
	pollConn     = 250 * time.Millisecond // liveness poll while degraded
)

// MC? handshake: the machine swallows commands sent too early, so it is
// retried. Variables so tests can shrink the spacing.
var (
	mcqTries   = 3
	mcqSpacing = 2 * time.Second // start-to-start; also the first-line timeout
)

// reportStallTimeout ends a session whose report stream has gone silent: the
// stream runs ~4/s in every power state, so silence means a dead link, a
// power-cycled machine or another client's "MC " (stream toggles are
// global). A variable so tests can shrink it.
var reportStallTimeout = 10 * time.Second

// sessionEnding runs as a session's end is derived; tests land a release
// there.
var sessionEnding = func() {}

// Config configures a Daemon. Zero durations and zero Brew/Watch configs
// select the defaults.
type Config struct {
	Addr     string           // machine command port, host:port
	Spec     *leva.RecordSpec // parsed vendor XML
	SpecName string           // must equal the machine's MC? "XML=" value
	Store    *store.Store
	Brew     brew.Config
	Watch    watch.Config
	// EventExec, if set, runs via /bin/sh -c for each detector event.
	EventExec string
	// Settle is WaitReady's settle time (default 1.5 s).
	Settle time.Duration
	// BackoffMin and BackoffMax bound the reconnect delay, which doubles
	// per failed attempt (defaults 1 s and 3 min).
	BackoffMin, BackoffMax time.Duration
	// DisplayEvery, if positive, samples the LCD mirror this often for
	// DisplayFor (default 500 ms) between reports; zero disables sampling.
	DisplayEvery, DisplayFor time.Duration
	// Registry receives the Prometheus metrics; nil disables them.
	Registry prometheus.Registerer
	// Raw logs the raw machine traffic of every session; nil disables it.
	Raw *rawlog.Log
}

// Daemon owns the machine connection. Create it with New and start it with Run.
type Daemon struct {
	cfg Config

	mu   sync.RWMutex // guards st, conn, rs and sess
	st   Status
	conn *leva.Conn      // set while st.Connected; the worker's connection
	rs   *rawlog.Session // the current session's raw log, for marks; nil if none
	sess *sessionHandle  // the running session, from before Dial to its end; nil if none

	wake chan struct{} // buffer 1: a release or reclaim nudges the reconnect loop
	// releaseMu serialises each release store access with its state update
	// (Release, Reclaim, Run's startup read), so the two never diverge.
	releaseMu sync.Mutex

	reqs       chan request  // to the command worker
	workerDone chan struct{} // closed when the worker has exited

	// Owned by the Run goroutine.
	det    *watch.Detector
	rec    *brew.Recorder
	brewID int64     // row of the open brew; 0 if none or BeginBrew failed
	brewAt time.Time // its started_at, the base of the points' t_ms

	hooks        sync.WaitGroup // running event hooks
	decodeErrors atomic.Uint64  // report lines skipped (checksum or decode)
	met          *metrics
	attempts     int // Dial calls so far; Run goroutine only
}

// New returns a Daemon for cfg with defaults applied.
func New(cfg Config) *Daemon {
	if cfg.Settle <= 0 {
		cfg.Settle = defaultSettle
	}
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = defaultMinBackoff
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = defaultMaxBackoff
	}
	cfg.BackoffMax = max(cfg.BackoffMax, cfg.BackoffMin)
	if cfg.DisplayFor <= 0 {
		cfg.DisplayFor = defaultDisplayFor
	}
	if cfg.Brew == (brew.Config{}) {
		cfg.Brew = brew.DefaultConfig()
	}
	if cfg.Watch == (watch.Config{}) {
		cfg.Watch = watch.DefaultConfig()
	}
	return &Daemon{
		cfg:        cfg,
		st:         Status{Power: PowerUnknown},
		reqs:       make(chan request),
		wake:       make(chan struct{}, 1),
		workerDone: make(chan struct{}),
		det:        watch.NewDetector(cfg.Watch),
		rec:        brew.NewRecorder(cfg.Brew),
		met:        newMetrics(cfg.Registry),
	}
}

// Run closes brews left open by a crash, reads the persisted release, then
// connects and reconnects until ctx is done, waiting out any release before
// each dial. Before returning it stops the report stream (bounded by 2 s),
// closes the connection, stores any brew Recorder.Abort returns and waits
// for running event hooks. Store errors are logged, never fatal, with one
// exception: Run fails when it cannot read the release (it never dials
// while the release state is unknown), and on a config without Spec or
// Store.
func (d *Daemon) Run(ctx context.Context) error {
	if d.cfg.Spec == nil || d.cfg.Store == nil {
		close(d.workerDone) // requests fail instead of waiting for a worker
		return errors.New("daemon: Config.Spec and Config.Store are required")
	}
	defer d.hooks.Wait()

	sctx, cancel := storeCtx(ctx)
	if n, err := d.cfg.Store.CloseDangling(sctx); err != nil {
		slog.Error("closing dangling brews", "err", err)
	} else if n > 0 {
		slog.Warn("marked brews left open by a crash as truncated-crash", "count", n)
	}
	d.releaseMu.Lock()
	until, released, err := d.cfg.Store.Release(sctx)
	cancel()
	if err != nil {
		d.releaseMu.Unlock()
		close(d.workerDone)
		return fmt.Errorf("daemon: reading the machine release: %w", err)
	}
	if released && until.After(time.Now()) {
		slog.Info("machine released", "until", until.Local().Format(time.DateTime))
		d.update(func(s *Status) { s.ReleasedUntil = &until })
	}
	d.releaseMu.Unlock()
	go d.worker(ctx)
	defer func() { <-d.workerDone }()
	d.refreshLastBrew(ctx)
	d.checkFields()

	backoff := d.cfg.BackoffMin
	for {
		waited, ok := d.waitReleased(ctx)
		if !ok {
			return nil
		}
		if waited {
			backoff = d.cfg.BackoffMin
		}
		if d.session(ctx) {
			backoff = d.cfg.BackoffMin
		}
		if !d.sleepWake(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, d.cfg.BackoffMax)
	}
}

// checkFields warns once for each field name the spec lacks.
func (d *Daemon) checkFields() {
	have := make(map[string]bool, len(d.cfg.Spec.Values))
	for _, v := range d.cfg.Spec.Values {
		have[v.Name] = true
	}
	for _, n := range brew.FieldNames {
		if !have[n] {
			slog.Warn("spec lacks field; its value stays unknown", "field", n)
		}
	}
}

// session runs one connection from Dial to its end. It reports whether
// StartReports succeeded, which resets the reconnect backoff. It does not
// start while released; a Release ends it through sctx's cancel cause
// (errReleased), and then it sends nothing unless reports had started, in
// which case it sends "MC " and checks for the idle banner.
func (d *Daemon) session(ctx context.Context) (started bool) {
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	h := &sessionHandle{cancel: cancel, done: make(chan struct{})}
	d.mu.Lock()
	if d.releasedNow() {
		d.mu.Unlock()
		return false
	}
	d.sess = h
	d.mu.Unlock()
	// Deferred first, so it runs last: Release returns once everything below
	// has finished.
	defer func() {
		d.mu.Lock()
		d.sess = nil
		d.mu.Unlock()
		close(h.done)
	}()
	released := func() bool { return errors.Is(context.Cause(sctx), errReleased) }

	if d.attempts++; d.attempts > 1 {
		d.met.reconnects.Inc()
	}
	conn, err := leva.Dial(sctx, d.cfg.Addr)
	if err != nil {
		if sctx.Err() == nil {
			slog.Info("dial failed", "addr", d.cfg.Addr, "err", err)
		}
		return false
	}
	// Deferred in reverse: disconnected (and its conn.Close) runs first, the
	// tap is removed next, the raw session closes last. The final "MC " is
	// sent in the loop body before any of them runs, so it is logged.
	reporting := false // set once StartReports succeeded
	rs := d.cfg.Raw.Session()
	defer rs.Close()
	if d.cfg.Raw != nil {
		defer conn.Tap(rs.Tap)()
	}
	d.mu.Lock()
	d.rs = rs
	d.mu.Unlock()
	d.met.frags.attach(conn)
	// A brew cut off by the connection ending is lost, unless a release or a
	// clean shutdown ended the session. Derived at the end, so a shutdown
	// racing a lost connection still counts as a shutdown.
	defer func() {
		sessionEnding()
		cut := brew.TruncatedLost
		switch {
		case released():
			cut = brew.TruncatedReleased
			if h.banner == nil { // no banner check ran
				// Before the reports nothing was sent and the banners
				// tell; after them no "MC " went out, so none is claimed.
				b := !reporting && conn.Banners() > 0
				h.banner = &b
			}
		case ctx.Err() != nil:
			cut = brew.TruncatedShutdown
		}
		d.disconnected(ctx, conn, cut)
	}()

	rctx, rcancel := context.WithTimeout(sctx, readyTimeout)
	err = leva.WaitReady(rctx, conn, d.cfg.Settle)
	rcancel()
	switch {
	case sctx.Err() != nil: // released or shut down: nothing sent yet
		return false
	case errors.Is(err, context.DeadlineExceeded):
		// Banners are silent while reporting: the stream may still be on
		// from a daemon killed without "MC ".
		slog.Warn("machine not ready; stopping a possibly running stream and reconnecting")
		d.stopReports(sctx, conn)
		return false
	case err != nil:
		slog.Warn("connection lost before ready", "err", err)
		return false
	}
	fw := leva.FirmwareVersion(conn.ID())
	d.update(func(s *Status) { s.Connected, s.Firmware = true, fw })
	d.mu.Lock()
	d.conn = conn
	d.mu.Unlock()
	slog.Info("connected", "addr", d.cfg.Addr, "firmware", fw)

	name, ok := d.xmlName(sctx, conn)
	if !ok {
		return false
	}
	if name != d.cfg.SpecName {
		slog.Error("machine XML does not match the loaded spec; not starting reports",
			"machine", name, "spec", d.cfg.SpecName)
		d.met.specMismatch.Set(1)
		d.holdUntilEnd(sctx, conn)
		return false
	}
	d.update(func(s *Status) { s.SpecOK = true })

	reports, err := conn.StartReports(sctx)
	if err != nil {
		if sctx.Err() == nil {
			slog.Warn("starting reports failed", "err", err)
		}
		return false
	}
	reporting = true
	slog.Info("reports started")
	samplerDone := make(chan struct{})
	if d.cfg.DisplayEvery > 0 {
		dctx, dcancel := context.WithCancel(sctx)
		defer dcancel()
		go func() {
			defer close(samplerDone)
			d.sampleDisplay(dctx, conn)
		}()
	} else {
		close(samplerDone)
	}
	stall := time.NewTimer(reportStallTimeout)
	defer stall.Stop()
	// Both close with the connection; reports ending ends the session.
	notices, frames := conn.Notices(), conn.DisplayFrames()
	for {
		select {
		case <-sctx.Done():
			// A sample still in flight could send MC@ or MCr after "MC "
			// and leave the machine streaming; the sampler ends with sctx.
			<-samplerDone
			if released() {
				b := d.stopForRelease(ctx, conn)
				h.banner = &b
			} else {
				d.stopReports(sctx, conn)
			}
			return true
		case <-stall.C:
			slog.Warn("report stream silent; reconnecting", "after", reportStallTimeout)
			return true
		case line, ok := <-reports:
			if !ok {
				slog.Warn("connection lost", "err", conn.Err())
				return true
			}
			d.handle(ctx, line) // ctx, not sctx: a release must not kill event hooks
			stall.Reset(reportStallTimeout)
		case n, ok := <-notices:
			if !ok {
				notices = nil
				continue
			}
			d.notice(ctx, n)
		case f, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			d.frame(f)
		}
	}
}

// xmlName asks MC? up to mcqTries times, mcqSpacing apart, for the
// machine's "XML=" name. The machine swallows commands sent shortly after
// connect, so one unanswered MC? is normal.
func (d *Daemon) xmlName(ctx context.Context, conn *leva.Conn) (string, bool) {
	name, err := leva.QueryXMLName(ctx, conn, mcqTries, mcqSpacing, mcqIdleGap)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			slog.Warn("MC? failed", "err", err)
		}
		return "", false
	case name == "":
		slog.Warn("no XML= reply to MC?; reconnecting", "tries", mcqTries)
		return "", false
	}
	return name, true
}

// holdUntilEnd keeps a spec-mismatched connection open, sending nothing,
// until ctx is done or the connection ends.
func (d *Daemon) holdUntilEnd(ctx context.Context, conn *leva.Conn) {
	t := time.NewTicker(pollConn)
	defer t.Stop()
	for conn.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
	slog.Warn("connection lost", "err", conn.Err())
}

// stopReports sends "MC " even when ctx is already done, bounded by stopTimeout.
func (d *Daemon) stopReports(ctx context.Context, conn *leva.Conn) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer cancel()
	if err := conn.StopReports(sctx); err != nil {
		slog.Debug("stopping reports failed", "err", err)
	}
}

// disconnected closes conn, ends any open episode and resets the state.
// A pumping brew is stored with kind cut (why the session ended), a
// coasting one with its own kind.
func (d *Daemon) disconnected(ctx context.Context, conn *leva.Conn, cut brew.Kind) {
	d.mu.Lock()
	d.conn, d.st.Connected = nil, false // new requests are refused from here
	d.rs = nil
	d.met.status(d.st)
	d.mu.Unlock()
	_ = conn.Close() // aborts a request still running on conn
	d.met.frags.detach(conn)
	if sum := d.rec.Abort(cut); sum != nil {
		d.finish(ctx, *sum)
	}
	d.det = watch.NewDetector(d.cfg.Watch)
	d.update(func(s *Status) {
		*s = Status{Power: PowerUnknown, LastBrew: s.LastBrew, ReleasedUntil: s.ReleasedUntil}
	})
}

// handle processes one report line (CR+LF stripped).
func (d *Daemon) handle(ctx context.Context, raw []byte) {
	d.met.lines.Inc()
	payload, err := leva.VerifyChecksum(raw)
	if err != nil {
		d.decodeErrors.Add(1)
		d.met.decodeErrors.Inc()
		slog.Debug("report line skipped", "err", err)
		return
	}
	power := powerFromPayload(payload)
	rec, err := d.cfg.Spec.Decode(payload)
	if err != nil {
		d.decodeErrors.Add(1)
		d.met.decodeErrors.Inc()
		slog.Debug("report line skipped", "err", err)
		d.update(func(s *Status) { s.Power, s.Ready = power, d.det.Ready() && power == PowerActive })
		return
	}
	for _, ev := range d.det.Feed(rec) {
		d.emit(ctx, ev)
	}

	smp := brew.SampleFrom(rec, payload, time.Now())
	res := d.rec.Feed(smp)
	// With both set, Closed is the previous brew and Points the new one's.
	if res.Closed != nil {
		d.finish(ctx, *res.Closed)
	}
	if res.Opened {
		d.begin(ctx, smp.At)
	}
	if len(res.Points) > 0 && d.brewID != 0 {
		sctx, cancel := storeCtx(ctx)
		if err := d.cfg.Store.AddPoints(sctx, d.brewID, d.brewAt, res.Points); err != nil {
			slog.Error("storing brew points", "id", d.brewID, "err", err)
		}
		cancel()
	}
	if res.Water != nil {
		d.water(*res.Water)
	}

	if smp.Heat != nil {
		d.met.heaterPercent.Set(*smp.Heat)
	}
	ready, open := d.det.Ready() && power == PowerActive, d.rec.Open()
	d.update(func(s *Status) {
		s.Power, s.Ready, s.BrewOpen = power, ready, open
		s.Temp, s.Setpoint = smp.Temp, smp.Setpoint
		if power == PowerEco || power == PowerStandby {
			s.MachineReady = false
		}
	})
}

// begin stores a newly opened brew. On error the brew goes unrecorded.
func (d *Daemon) begin(ctx context.Context, at time.Time) {
	sctx, cancel := storeCtx(ctx)
	defer cancel()
	id, err := d.cfg.Store.BeginBrew(sctx, at)
	if err != nil {
		slog.Error("storing new brew; it goes unrecorded", "err", err)
		id = 0
	} else {
		slog.Info("brew opened", "id", id)
	}
	d.brewID, d.brewAt = id, at
}

// finish stores a closed brew's summary and refreshes LastBrew.
func (d *Daemon) finish(ctx context.Context, sum brew.Summary) {
	d.met.brewClosed(sum)
	id := d.brewID
	d.brewID = 0
	if id == 0 {
		slog.Warn("brew closed but not stored", "kind", sum.Kind)
		return
	}
	sctx, cancel := storeCtx(ctx)
	err := d.cfg.Store.FinishBrew(sctx, id, sum)
	cancel()
	if err != nil {
		slog.Error("storing brew summary", "id", id, "err", err)
		return
	}
	slog.Info("brew closed", "id", id, "kind", sum.Kind, "duration", sum.Duration,
		"peak_bar", sum.PeakBar, "volume_ml", sum.VolumeML)
	d.refreshLastBrew(ctx)
}

// water handles a finished wand draw: water that is not a brew.
func (d *Daemon) water(w brew.WaterDraw) {
	d.met.waterDrawn(w)
	slog.Debug("water draw", "volume_ml", w.VolumeML, "duration", w.Duration)
}

func (d *Daemon) refreshLastBrew(ctx context.Context) {
	sctx, cancel := storeCtx(ctx)
	defer cancel()
	bs, err := d.cfg.Store.Brews(sctx, 1)
	if err != nil {
		slog.Error("loading last brew", "err", err)
		return
	}
	if len(bs) == 1 {
		d.update(func(s *Status) { s.LastBrew = &bs[0] })
	}
}

// storeCtx bounds a store call. It survives ctx's cancellation, so a brew
// closed during shutdown is still written.
func storeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
}

// sleepCtx waits d (nothing if d <= 0); false means ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
