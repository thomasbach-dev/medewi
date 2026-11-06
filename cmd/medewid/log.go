package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
)

// logFlavours are the accepted -log-flavour values: "text" is slog's text
// format with timestamps; "systemd" drops the timestamp (the journal adds
// one) and prefixes each line with its syslog priority, "<N>", which
// journald reads as the line's level.
var logFlavours = map[string]bool{"text": true, "systemd": true}

// newLogHandler returns the handler for flavour, writing to w.
func newLogHandler(flavour string, w io.Writer, level slog.Leveler) slog.Handler {
	if flavour != "systemd" {
		return slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	}
	h := &systemdHandler{}
	mu := &sync.Mutex{}
	for i, prio := range systemdPriorities {
		h.hs[i] = slog.NewTextHandler(prefixWriter{mu, w, []byte(prio)}, &slog.HandlerOptions{
			Level: level,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if len(groups) == 0 && a.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return a
			},
		})
	}
	return h
}

// systemdPriorities are the sd-daemon(3) prefixes for debug, info, warn and
// error records.
var systemdPriorities = [4]string{"<7>", "<6>", "<4>", "<3>"}

// systemdHandler routes each record to the text handler whose writer
// prefixes the record's priority.
type systemdHandler struct {
	hs [4]slog.Handler
}

func (h *systemdHandler) pick(l slog.Level) slog.Handler {
	switch {
	case l < slog.LevelInfo:
		return h.hs[0]
	case l < slog.LevelWarn:
		return h.hs[1]
	case l < slog.LevelError:
		return h.hs[2]
	default:
		return h.hs[3]
	}
}

func (h *systemdHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.pick(l).Enabled(ctx, l)
}

func (h *systemdHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.pick(r.Level).Handle(ctx, r)
}

func (h *systemdHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return h.with(func(x slog.Handler) slog.Handler { return x.WithAttrs(as) })
}

func (h *systemdHandler) WithGroup(name string) slog.Handler {
	return h.with(func(x slog.Handler) slog.Handler { return x.WithGroup(name) })
}

func (h *systemdHandler) with(f func(slog.Handler) slog.Handler) slog.Handler {
	n := &systemdHandler{}
	for i, x := range h.hs {
		n.hs[i] = f(x)
	}
	return n
}

// prefixWriter writes prefix before each Write; slog's text handler writes
// one whole record per call. The four handlers share mu, so their lines
// never interleave.
type prefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix []byte
}

func (p prefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.w.Write(append(p.prefix[:len(p.prefix):len(p.prefix)], b...)); err != nil {
		return 0, err
	}
	return len(b), nil
}
