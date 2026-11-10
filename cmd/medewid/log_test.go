package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSystemdHandler(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(newLogHandler("systemd", &buf, slog.LevelDebug)).With("addr", "m:23")
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")
	l.Log(t.Context(), slog.LevelError+4, "beyond error")
	l.WithGroup("g").Info("grouped", "k", 1)

	want := []string{
		`<7>msg=d addr=m:23`,
		`<6>msg=i addr=m:23`,
		`<4>msg=w addr=m:23`,
		`<3>msg=e addr=m:23`,
		`<3>msg="beyond error" addr=m:23`,
		`<6>msg=grouped addr=m:23 g.k=1`,
	}
	got := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSystemdHandlerLevel(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newLogHandler("systemd", &buf, slog.LevelInfo)).Debug("hidden")
	if buf.Len() != 0 {
		t.Errorf("debug line logged at info level: %q", buf.String())
	}
}

func TestTextHandlerHasTime(t *testing.T) {
	var buf bytes.Buffer
	slog.New(newLogHandler("text", &buf, slog.LevelInfo)).Info("x")
	if !strings.HasPrefix(buf.String(), "time=") {
		t.Errorf("text flavour lost its timestamp: %q", buf.String())
	}
}

func TestLogFlavourFlag(t *testing.T) {
	addr, xml := startSim(t)
	r := start(t, append(baseArgs(t, addr, xml), "--insecure-http", "--log-flavour", "systemd")...)
	r.shutdown()
	if out := r.stderr.String(); !strings.Contains(out, "<6>msg=\"medewid serving\"") || strings.Contains(out, "time=") || strings.Contains(out, "level=") {
		t.Errorf("stderr not in systemd flavour:\n%s", out)
	}
}

func TestLogFlavourEnv(t *testing.T) {
	t.Setenv("MEDEWID_LOG_FLAVOUR", "systemd")
	addr, xml := startSim(t)
	r := start(t, append(baseArgs(t, addr, xml), "--insecure-http")...)
	r.shutdown()
	if out := r.stderr.String(); !strings.Contains(out, "<6>msg=") {
		t.Errorf("MEDEWID_LOG_FLAVOUR ignored:\n%s", out)
	}
}

func TestLogFlavourInvalid(t *testing.T) {
	addr, xml := startSim(t)
	code, stderr := runExit(t, append(baseArgs(t, addr, xml), "--insecure-http", "--log-flavour", "json")...)
	if code != 2 || !strings.Contains(stderr, "--log-flavour") {
		t.Errorf("exit %d, stderr %q; want 2 naming --log-flavour", code, stderr)
	}
}
