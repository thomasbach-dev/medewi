package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/thomasbach-dev/medewi/watch"
)

// hookWaitDelay bounds how long a killed hook may hold its output pipes
// (e.g. via a grandchild) before Wait gives up.
const hookWaitDelay = 2 * time.Second

// emit logs a detector event and, if configured, runs the event hook in its
// own goroutine. The hook is killed when ctx is done; Run waits for it.
func (d *Daemon) emit(ctx context.Context, ev watch.Event) {
	kind := ev.Kind.String()
	slog.Info("event", "kind", kind, "temp", ev.Temp, "setpoint", ev.Setpoint)
	if d.cfg.EventExec == "" {
		return
	}
	d.hooks.Add(1)
	go func() {
		defer d.hooks.Done()
		// Absolute path: NixOS services have no shell on PATH.
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", d.cfg.EventExec)
		cmd.Env = append(os.Environ(),
			"MEDEWI_EVENT="+kind,
			fmt.Sprintf("MEDEWI_TEMP=%.1f", ev.Temp),
			fmt.Sprintf("MEDEWI_SETPOINT=%.1f", ev.Setpoint))
		cmd.WaitDelay = hookWaitDelay
		if out, err := cmd.CombinedOutput(); err != nil {
			slog.Warn("event hook failed", "kind", kind, "err", err, "output", string(out))
		}
	}()
}
