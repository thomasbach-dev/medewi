package leva

import (
	"context"
	"strings"
	"time"
)

// XMLName returns the vendor XML name from an MC? reply ("XML=…").
func XMLName(lines []string) (string, bool) {
	for _, l := range lines {
		if name, ok := strings.CutPrefix(l, "XML="); ok {
			return strings.TrimSpace(name), true
		}
	}
	return "", false
}

// QueryXMLName asks MC? up to tries times, spacing apart (start to start;
// spacing is also the first-line timeout), collecting each reply until
// idleGap of silence. The machine can swallow a command sent soon after
// connect even after WaitReady, so an unanswered MC? is retried. It returns
// "" and no error when no try got an "XML=" line, and the error of a failed
// command otherwise.
func QueryXMLName(ctx context.Context, c *Conn, tries int, spacing, idleGap time.Duration) (string, error) {
	for i := range tries {
		start := time.Now()
		lines, err := c.CommandOpts(ctx, "MC?", spacing, idleGap)
		if err != nil {
			return "", err
		}
		if name, ok := XMLName(lines); ok {
			return name, nil
		}
		if i < tries-1 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(spacing - time.Since(start)):
			}
		}
	}
	return "", nil
}
