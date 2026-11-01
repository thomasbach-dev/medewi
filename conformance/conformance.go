// Package conformance is the executable protocol model for leva! controllers.
// The same checks run against the simulator (in CI) and against the real
// machine (on demand, gated), which keeps the simulator honest and serves as
// the firmware-flash gate.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

// Class says how invasive a check is; Run executes checks up to a maximum.
type Class int

const (
	// ReadOnly checks only issue harmless query commands.
	ReadOnly Class = iota
	// StreamState checks toggle the report stream (MCr / MC<space>).
	StreamState
	// Interactive checks need an operator at the machine.
	Interactive
)

// Env carries optional collaborators for checks and Run.
type Env struct {
	// Spec decodes report lines; reports-decode only decodes when non-nil.
	Spec *leva.RecordSpec
	// Confirm asks the operator a yes/no question. Run requires it (and a
	// yes) before touching any non-loopback target.
	Confirm func(prompt string) bool
	// Record, if non-nil, receives all raw traffic as fixture events.
	// Nil means not recording.
	Record func(fixture.Event)
}

// Check is one protocol assertion.
type Check struct {
	Name  string
	Class Class
	Run   func(ctx context.Context, c *leva.Conn, env Env) error
}

// Result is the outcome of one Check.
type Result struct {
	Name    string
	Class   Class
	Err     error
	Skipped bool
}

// bootloaderPort is the raw XMODEM bootloader port; never dialled.
const bootloaderPort = 2323

// ErrNotRun marks a check that was not executed because the banner readiness
// gate failed.
var ErrNotRun = errors.New("conformance: check not run")

const (
	bannerWait   = 5 * time.Second
	reportWait   = 5 * time.Second
	reportsToGet = 3
)

// All returns the v0 checks in execution order. banner must stay first: it
// doubles as the readiness gate, because the real machine swallows a command
// sent immediately after connect.
func All() []Check {
	return []Check{
		{Name: "banner", Class: ReadOnly, Run: checkBanner},
		{Name: "help", Class: ReadOnly, Run: checkHelp},
		{Name: "xml-name", Class: ReadOnly, Run: checkXMLName},
		{Name: "reports-decode", Class: StreamState, Run: checkReportsDecode},
	}
}

func checkBanner(ctx context.Context, c *leva.Conn, _ Env) error {
	deadline := time.Now().Add(bannerWait)
	for {
		if id := c.ID(); id != "" {
			if !strings.Contains(id, "leva!") {
				return fmt.Errorf("banner %q does not contain %q", id, "leva!")
			}
			return nil
		}
		if err := c.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no $id banner within %s", bannerWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func checkHelp(ctx context.Context, c *leva.Conn, _ Env) error {
	lines, err := c.Command(ctx, "MCh")
	if err != nil {
		return err
	}
	out := strings.Join(lines, "\n")
	for _, want := range []string{"MCu", "MCr"} {
		if !strings.Contains(out, want) {
			return fmt.Errorf("MCh output does not mention %s", want)
		}
	}
	return nil
}

func checkXMLName(ctx context.Context, c *leva.Conn, _ Env) error {
	lines, err := c.Command(ctx, "MC?")
	if err != nil {
		return err
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "XML=") {
			return nil
		}
	}
	return fmt.Errorf("MC? output has no XML= line: %q", lines)
}

func checkReportsDecode(ctx context.Context, c *leva.Conn, env Env) (err error) {
	// The MCr may have been delivered even if StartReports fails (e.g. a
	// timeout), so always try to stop the stream, even when the check failed.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		err = errors.Join(err, c.StopReports(stopCtx))
	}()
	ch, err := c.StartReports(ctx)
	if err != nil {
		return err
	}
	timeout := time.NewTimer(reportWait)
	defer timeout.Stop()
	for i := 0; i < reportsToGet; i++ {
		select {
		case line, ok := <-ch:
			if !ok {
				return fmt.Errorf("report channel closed after %d reports: %w", i, errors.Join(c.Err(), leva.ErrClosed))
			}
			payload, verr := leva.VerifyChecksum(line)
			if verr != nil {
				return fmt.Errorf("report %d: %w", i+1, verr)
			}
			if env.Spec != nil {
				if _, derr := env.Spec.Decode(payload); derr != nil {
					return fmt.Errorf("report %d: %w", i+1, derr)
				}
			}
		case <-timeout.C:
			return fmt.Errorf("only %d of %d reports within %s", i, reportsToGet, reportWait)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// isLoopback reports whether addr's host is a loopback address or localhost.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Run executes the checks of class <= maxClass against addr and returns one Result
// per check, in All() order. Checks above max are Skipped.
//
// Gating: any non-loopback target (e.g. the real machine) requires
// env.Confirm to be set and to return true, asked exactly once before
// connecting; a nil Confirm refuses. Loopback targets (the sim) never prompt.
//
// banner runs first and is the readiness gate; if it fails, the remaining
// checks are not run and carry an error naming the banner failure. A
// connection error aborts the run with a non-nil error.
func Run(ctx context.Context, addr string, maxClass Class, env Env) ([]Result, error) {
	// Defense in depth: reject the bootloader port before any prompt or
	// connection (leva.Dial also guards it).
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		if port, err := net.LookupPort("tcp", portStr); err == nil && port == bootloaderPort {
			return nil, leva.ErrBootloaderPort
		}
	}
	if !isLoopback(addr) {
		if env.Confirm == nil {
			return nil, fmt.Errorf("conformance: refusing non-loopback target %s without Confirm", addr)
		}
		if !env.Confirm(fmt.Sprintf("Run conformance checks (up to class %d) against %s?", maxClass, addr)) {
			return nil, fmt.Errorf("conformance: run against %s not confirmed", addr)
		}
	}
	c, err := leva.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()

	if env.Record != nil {
		start := time.Now()
		var mu sync.Mutex // taps fire from the reader and the caller
		remove := c.Tap(func(dir string, p []byte) {
			mu.Lock()
			defer mu.Unlock()
			env.Record(fixture.Event{
				T:    time.Since(start).Milliseconds(),
				Dir:  fixture.Dir(dir),
				Data: append([]byte(nil), p...),
			})
		})
		defer remove()
	}

	checks := All()
	results := make([]Result, 0, len(checks))
	var gateErr error
	for _, ck := range checks {
		r := Result{Name: ck.Name, Class: ck.Class}
		switch {
		case ck.Class > maxClass:
			r.Skipped = true
		case gateErr != nil:
			r.Err = gateErr
		default:
			r.Err = ck.Run(ctx, c, env)
			if ck.Name == "banner" && r.Err != nil {
				gateErr = fmt.Errorf("%w: banner check failed: %w", ErrNotRun, r.Err)
			}
		}
		results = append(results, r)
		if r.Err != nil && c.Err() != nil {
			return results, fmt.Errorf("conformance: connection lost during %s: %w", ck.Name, c.Err())
		}
	}
	return results, nil
}
