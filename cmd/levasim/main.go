// Command levasim runs a fake leva! controller (package sim) for development
// and CI, optionally replaying recorded traffic from a fixture directory.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/sim"
)

const (
	banner = "$id: leva! (3.2)"

	logFile       = "log.jsonl"
	responsesFile = "responses.json"
)

// defaultResponses are served when -fixtures does not supply responses.json.
var defaultResponses = map[string][]string{
	"MCh": {"MCh  this help", "MCu  complete setup dump", "MCr  start reports", "MC<space>  stop reports"},
	"MC?": {"XML=synthetic"},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(os.Args[1:], os.Stdout, os.Stderr, nil, ctx.Done())
	stop()
	os.Exit(code)
}

// run parses args, starts the simulator and serves until done is closed, then
// stops it cleanly and returns the exit code (0 ok, 1 runtime error, 2 usage).
//
// Once listening, run prints "levasim listening on <addr> (tripwire <addr|off>)"
// to stdout and sends the main listen address on ready, if ready is non-nil.
// ready must be buffered or read promptly. done must be non-nil; close it to
// request shutdown. Tests drive run with a ready channel and their own done
// channel; main passes nil and a signal-driven done.
func run(args []string, stdout, stderr io.Writer, ready chan<- string, done <-chan struct{}) int {
	fset := pflag.NewFlagSet("levasim", pflag.ContinueOnError)
	fset.SetOutput(stderr)
	addr := fset.String("addr", "127.0.0.1:2300", "main listen address")
	tripwire := fset.String("tripwire", "127.0.0.1:2323", `tripwire listen address ("" disables it)`)
	speed := fset.Float64("speed", 1.0, "stream replay speed factor")
	fixtures := fset.String("fixtures", "", "fixture directory with "+logFile+" and/or "+responsesFile)
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "levasim: %v\n", err)
		return 2
	}
	if *speed <= 0 {
		_, _ = fmt.Fprintf(stderr, "levasim: --speed must be > 0, got %v\n", *speed)
		return 2
	}

	responses, stream, err := loadFixtures(*fixtures)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "levasim: %v\n", err)
		return 1
	}
	s := sim.New(sim.Config{
		Addr:         *addr,
		TripwireAddr: *tripwire,
		Speed:        *speed,
		Banner:       banner,
		Responses:    responses,
		Stream:       stream,
	})
	if err := s.Start(); err != nil {
		_, _ = fmt.Fprintf(stderr, "levasim: %v\n", err)
		return 1
	}
	trip := "off"
	if a := s.TripwireAddr(); a != nil {
		trip = a.String()
	}
	_, _ = fmt.Fprintf(stdout, "levasim listening on %s (tripwire %s)\n", s.Addr(), trip)
	if ready != nil {
		ready <- s.Addr().String()
	}

	<-done
	if err := s.Stop(); err != nil {
		_, _ = fmt.Fprintf(stderr, "levasim: stop: %v\n", err)
		return 1
	}
	return 0
}

// loadFixtures returns the canned responses and replay stream for dir. An empty
// dir selects the built-in defaults. Within a dir, a missing file falls back to
// its default (responses) or to no stream (log); a malformed file is an error.
func loadFixtures(dir string) (map[string][]string, []fixture.Event, error) {
	if dir == "" {
		return defaultResponses, nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("fixtures: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("fixtures: %s is not a directory", dir)
	}

	var stream []fixture.Event
	logPath := filepath.Join(dir, logFile)
	if f, err := os.Open(logPath); err == nil {
		stream, err = fixture.ReadLog(f)
		_ = f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", logPath, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("open %s: %w", logPath, err)
	}

	responses := defaultResponses
	respPath := filepath.Join(dir, responsesFile)
	data, err := os.ReadFile(respPath)
	switch {
	case err == nil:
		responses = nil
		if err := json.Unmarshal(data, &responses); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", respPath, err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, nil, fmt.Errorf("read %s: %w", respPath, err)
	}
	return responses, stream, nil
}
