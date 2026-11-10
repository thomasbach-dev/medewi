package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/thomasbach-dev/medewi/leva"
)

// runSessionReset stops a report or display stream that another client left
// on, so the next capture starts from an idle machine. It sends "MC " only
// when the machine streams without a banner, and then verifies that the
// banner heartbeat comes back. It cannot see idle connections of other
// devices: the protocol has no way to list clients.
func runSessionReset(args []string, stdout, stderr io.Writer) int {
	fs, addr := newFlagSet("session-reset", stderr)
	if err := fs.Parse(args); err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 2
	}
	if *addr == "" {
		_, _ = fmt.Fprintf(stderr, "levacap: no target: set $%s or pass --addr\n", envAddr)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	// leva.Dial refuses the bootloader port 2323 before connecting.
	c, err := leva.Dial(ctx, *addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 1
	}
	defer func() { _ = c.Close() }()

	received, remove := watchRX(c)
	defer remove()
	err = waitBanner(ctx, c, received)
	switch {
	case err == nil:
		_, _ = fmt.Fprintln(stdout, "banner seen, no stream on: the machine is idle; nothing sent")
		return 0
	case !errors.Is(err, errStreamOn):
		if !received() {
			_, _ = fmt.Fprintf(stderr, "levacap: %v (nothing received from the machine; nothing sent)\n", err)
		} else {
			_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		}
		return 1
	}

	sctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	err = c.StopReports(sctx)
	cancel()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: stop stream: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, `stream was on without a banner; sent "MC "`)

	// Verify after write: no banner was seen yet, so WaitReady now waits for
	// the heartbeat to resume.
	wctx, cancel := context.WithTimeout(ctx, bannerTimeout+bannerSettle)
	err = leva.WaitReady(wctx, c, bannerSettle)
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			_, _ = fmt.Fprintf(stderr, "levacap: still no $id banner %v after \"MC \": "+
				"another device is probably connected and keeps its stream on; disconnect it and try again\n", bannerTimeout)
		} else {
			_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		}
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "the banner is back: the machine is idle")
	return 0
}
