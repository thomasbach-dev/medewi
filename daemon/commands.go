package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/thomasbach-dev/medewi/leva"
)

// Request refusals and failures. A refusal (ErrReleased, ErrDisconnected,
// ErrBrewOpen, ErrSpecMismatch, ErrInvalidPower) means nothing was sent.
// ErrDisconnected also covers a connection on which no report line has been
// observed yet (just connected, or a spec mismatch, which never starts
// reports): until then nothing is known about a running brew.
var (
	ErrDisconnected   = errors.New("daemon: not connected to the machine")
	ErrBrewOpen       = errors.New("daemon: a brew is in progress")
	ErrSpecMismatch   = errors.New("daemon: machine XML does not match the loaded spec")
	ErrVerifyFailed   = errors.New("daemon: machine did not reach the requested power state")
	ErrIncompleteDump = errors.New("daemon: settings dump is incomplete")
	ErrInvalidPower   = errors.New("daemon: invalid power state")
)

// Timings of the command worker. Variables so tests can shrink them.
var (
	verifyTimeout = 10 * time.Second // bound on the verify-after-write poll
	verifyPoll    = 250 * time.Millisecond
	dumpFirstLine = 5 * time.Second
	dumpIdleGap   = 2 * time.Second // MCm/MCu replies pause mid-stream
)

// powerCmd maps a target state to its power toggle. These and the dump
// commands are the only writes medewid ever sends.
var powerCmd = map[PowerState]string{
	PowerActive:  "MCa",
	PowerEco:     "MCe",
	PowerStandby: "MCE",
}

// hexEOF is the Intel HEX end-of-file record that ends a complete MCm dump.
const hexEOF = ":00000001FF"

// Dump is a settings dump: the reply lines (CR+LF stripped), the firmware
// version from the id banner ("unknown" if none) and the completion time.
type Dump struct {
	Lines    []string
	Firmware string
	Done     time.Time
}

// request is one unit of work for the command worker. run gets the
// connection current when the worker took the request.
type request struct {
	ctx  context.Context
	run  func(ctx context.Context, conn *leva.Conn)
	done chan struct{}
}

// worker serves requests one at a time until ctx is done.
func (d *Daemon) worker(ctx context.Context) {
	defer close(d.workerDone)
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-d.reqs:
			rctx, cancel := context.WithCancel(req.ctx)
			stop := context.AfterFunc(ctx, cancel) // Run's end aborts the request
			d.mu.RLock()
			conn := d.conn
			d.mu.RUnlock()
			req.run(rctx, conn)
			stop()
			cancel()
			close(req.done)
		}
	}
}

// do hands run to the worker and waits for it. Without a running worker it
// waits for ctx; after Run has returned it fails with ErrDisconnected.
func (d *Daemon) do(ctx context.Context, run func(context.Context, *leva.Conn)) error {
	req := request{ctx: ctx, run: run, done: make(chan struct{})}
	select {
	case d.reqs <- req:
	case <-d.workerDone:
		return ErrDisconnected
	case <-ctx.Done():
		return ctx.Err()
	}
	<-req.done // run honours ctx, so this ends promptly once ctx is done
	return nil
}

// refuse runs the checks a request makes before sending anything, in this
// order: released (ErrReleased), disconnected, (power only) spec mismatch, no report observed yet on
// this connection (power unknown), brew open.
func (d *Daemon) refuse(conn *leva.Conn, st Status, power bool) error {
	switch {
	case st.ReleasedUntil != nil:
		return releasedErr(*st.ReleasedUntil)
	case conn == nil || !st.Connected:
		return ErrDisconnected
	case power && !st.SpecOK:
		return ErrSpecMismatch
	case st.Power == PowerUnknown:
		return ErrDisconnected
	case st.BrewOpen:
		return ErrBrewOpen
	}
	return nil
}

// SetPower switches the machine to want (active, eco or standby) and
// verifies the switch from the report stream. It returns the observed state,
// also on failure. It refuses without sending anything on an invalid want
// (ErrInvalidPower), while released (ErrReleased), when disconnected or before the first report line on
// this connection (ErrDisconnected), on a spec mismatch (ErrSpecMismatch) or
// while a brew is open, pumping or coasting (ErrBrewOpen); it succeeds
// without sending anything when the machine is already in want.
// ErrVerifyFailed means the command went out but the state did not follow
// within verifyTimeout.
func (d *Daemon) SetPower(ctx context.Context, want PowerState) (PowerState, error) {
	cmd, ok := powerCmd[want]
	if !ok {
		return d.Status().Power, fmt.Errorf("%w: %q", ErrInvalidPower, want)
	}
	var (
		got  PowerState
		rerr error
	)
	err := d.do(ctx, func(ctx context.Context, conn *leva.Conn) {
		st := d.Status()
		got = st.Power
		if rerr = d.refuse(conn, st, true); rerr != nil {
			return
		}
		if st.Power == want {
			return
		}
		slog.Info("setting power", "from", st.Power, "to", want, "cmd", cmd)
		if _, rerr = conn.Command(ctx, cmd); rerr != nil {
			got = d.Status().Power
			return
		}
		got, rerr = d.verifyPower(ctx, want)
	})
	if err != nil {
		return d.Status().Power, err
	}
	return got, rerr
}

// verifyPower polls Status().Power until it is want or verifyTimeout passes.
func (d *Daemon) verifyPower(ctx context.Context, want PowerState) (PowerState, error) {
	deadline := time.Now().Add(verifyTimeout)
	t := time.NewTicker(verifyPoll)
	defer t.Stop()
	for {
		got := d.Status().Power
		if got == want {
			return got, nil
		}
		if !time.Now().Before(deadline) {
			slog.Warn("power state not verified", "want", want, "got", got)
			return got, ErrVerifyFailed
		}
		select {
		case <-ctx.Done():
			return d.Status().Power, ctx.Err()
		case <-t.C:
		}
	}
}

// DumpMCu returns the machine's user settings (MCu).
func (d *Daemon) DumpMCu(ctx context.Context) (Dump, error) { return d.dump(ctx, "MCu") }

// DumpMCm returns the machine's settings memory (MCm) as Intel HEX. A dump
// whose last non-empty line is not the EOF record gives ErrIncompleteDump.
func (d *Daemon) DumpMCm(ctx context.Context) (Dump, error) { return d.dump(ctx, "MCm") }

// dump runs cmd with the dump timings. It refuses, sending nothing, while
// released with ErrReleased, when disconnected or before the first report line on this connection (so also
// on a spec mismatch, where reports never start) with ErrDisconnected, and
// while a brew is open with ErrBrewOpen. An empty reply is incomplete.
func (d *Daemon) dump(ctx context.Context, cmd string) (Dump, error) {
	var (
		out  Dump
		rerr error
	)
	err := d.do(ctx, func(ctx context.Context, conn *leva.Conn) {
		if rerr = d.refuse(conn, d.Status(), false); rerr != nil {
			return
		}
		lines, err := conn.CommandOpts(ctx, cmd, dumpFirstLine, dumpIdleGap)
		if err != nil {
			rerr = fmt.Errorf("daemon: %s dump: %w", cmd, err)
			return
		}
		if !complete(cmd, lines) {
			rerr = fmt.Errorf("%w: %s, %d lines", ErrIncompleteDump, cmd, len(lines))
			return
		}
		fw := leva.FirmwareVersion(conn.ID())
		if fw == "" {
			fw = "unknown"
		}
		out = Dump{Lines: lines, Firmware: fw, Done: time.Now().UTC()}
	})
	if err != nil {
		return Dump{}, err
	}
	return out, rerr
}

// complete reports whether a dump reply is whole: non-empty, and for MCm
// ending in the Intel HEX EOF record.
func complete(cmd string, lines []string) bool {
	last := ""
	for i := len(lines) - 1; i >= 0 && last == ""; i-- {
		last = strings.TrimSpace(lines[i])
	}
	if last == "" {
		return false
	}
	return cmd != "MCm" || last == hexEOF
}
