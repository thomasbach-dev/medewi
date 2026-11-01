// Command levacap runs the conformance suite against a leva! target and
// records protocol fixtures from a live machine.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thomasbach-dev/medewi/conformance"
	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

const (
	envAddr = "MEDEWI_MACHINE_ADDR"
	envXML  = "MEDEWI_XML_PATH"

	bannerTimeout = 5 * time.Second
	cmdTimeout    = 10 * time.Second
)

// cmdWait is the pause after each scripted command (shrunk in tests).
var cmdWait = time.Second

// safeCommands is the complete, read-only command whitelist capture may send
// in the "commands" scenario. Do not add anything else.
var safeCommands = []string{"MCh", "MC?", "MCu", "MCm", "MCb", "MCf"}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "conform":
		return runConform(args[1:], stdin, stdout, stderr)
	case "capture":
		return runCapture(args[1:], stdin, stdout, stderr)
	case "-h", "-help", "--help", "help":
		usage(stdout)
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "levacap: unknown subcommand %q\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `usage: levacap <subcommand> [flags]

  conform [-addr host:port] [-max readonly|stream] [-yes]
  capture -scenario commands|stream|shot [-addr host:port] [-out captures/]

Target defaults to $MEDEWI_MACHINE_ADDR; decode spec from $MEDEWI_XML_PATH.`)
}

func newFlagSet(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", os.Getenv(envAddr), "target host:port (default $"+envAddr+")")
	return fs, addr
}

func runConform(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, addr := newFlagSet("conform", stderr)
	maxFlag := fs.String("max", "readonly", "highest check class: readonly|stream")
	yes := fs.Bool("yes", false, "skip the interactive confirmation")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var maxClass conformance.Class
	switch *maxFlag {
	case "readonly":
		maxClass = conformance.ReadOnly
	case "stream":
		maxClass = conformance.StreamState
	default:
		_, _ = fmt.Fprintf(stderr, "levacap: invalid -max %q (want readonly|stream)\n", *maxFlag)
		return 2
	}
	if *addr == "" {
		_, _ = fmt.Fprintf(stderr, "levacap: no target: set $%s or pass -addr\n", envAddr)
		return 2
	}

	var env conformance.Env
	if p := os.Getenv(envXML); p != "" {
		spec, err := leva.LoadSpecFile(p)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "levacap: load spec: %v\n", err)
			return 1
		}
		env.Spec = spec
	} else {
		_, _ = fmt.Fprintf(stdout, "notice: $%s not set; running without report decode assertions\n", envXML)
	}
	br := bufio.NewReader(stdin)
	env.Confirm = func(string) bool {
		if *yes {
			return true
		}
		_, _ = fmt.Fprint(stdout, "machine idle, nobody brewing? [y/N] ")
		line, _ := br.ReadString('\n')
		a := strings.ToLower(strings.TrimSpace(line))
		return a == "y" || a == "yes"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	results, err := conformance.Run(ctx, *addr, maxClass, env)
	failed := false
	for _, r := range results {
		_, _ = fmt.Fprintln(stdout, formatResult(r))
		if r.Err != nil {
			failed = true
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 1
	}
	if failed {
		return 1
	}
	return 0
}

func className(c conformance.Class) string {
	switch c {
	case conformance.ReadOnly:
		return "readonly"
	case conformance.StreamState:
		return "stream"
	default:
		return "interactive"
	}
}

func formatResult(r conformance.Result) string {
	status := "ok"
	switch {
	case r.Skipped:
		status = "skipped"
	case errors.Is(r.Err, conformance.ErrNotRun):
		status = "not-run"
	case r.Err != nil:
		status = "failed"
	}
	line := fmt.Sprintf("%-16s %-12s %s", r.Name, className(r.Class), status)
	if r.Err != nil {
		line += ": " + r.Err.Error()
	}
	return line
}

func runCapture(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, addr := newFlagSet("capture", stderr)
	scenario := fs.String("scenario", "", "scenario: commands|stream|shot (required)")
	out := fs.String("out", "captures", "output directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch *scenario {
	case "commands", "stream", "shot":
	default:
		_, _ = fmt.Fprintf(stderr, "levacap: -scenario must be one of commands, stream, shot (got %q)\n", *scenario)
		return 2
	}
	if *addr == "" {
		_, _ = fmt.Fprintf(stderr, "levacap: no target: set $%s or pass -addr\n", envAddr)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// leva.Dial refuses the bootloader port 2323 before connecting.
	c, err := leva.Dial(ctx, *addr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 1
	}
	defer func() { _ = c.Close() }()

	var (
		mu     sync.Mutex
		events []fixture.Event
		start  = time.Now() // monotonic
	)
	remove := c.Tap(func(dir string, p []byte) {
		d := fixture.RX
		if dir == "tx" {
			d = fixture.TX
		}
		ev := fixture.Event{T: time.Since(start).Milliseconds(), Dir: d, Data: append([]byte(nil), p...)}
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	defer remove()

	if err := waitBanner(ctx, c); err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 1
	}

	xmlName := ""
	capErr := func() error {
		if *scenario == "commands" {
			return captureCommands(ctx, c, &xmlName)
		}
		name, err := queryXMLName(ctx, c)
		if err != nil {
			return err
		}
		xmlName = name
		return captureStream(ctx, c, *scenario, stdin, stdout)
	}()

	// Write whatever was recorded, even after an error: partial captures of
	// a live machine are valuable.
	mu.Lock()
	snapshot := append([]fixture.Event(nil), events...)
	mu.Unlock()
	if err := writeOutput(filepath.Join(*out, *scenario), *scenario, *addr, c.ID(), xmlName, snapshot); err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 1
	}
	if capErr != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: capture incomplete: %v\n", capErr)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %d events to %s\n", len(snapshot), filepath.Join(*out, *scenario))
	return 0
}

// waitBanner polls for the banner; the real machine swallows commands sent
// right after connect.
func waitBanner(ctx context.Context, c *leva.Conn) error {
	deadline := time.Now().Add(bannerTimeout)
	for c.ID() == "" {
		if time.Now().After(deadline) {
			return errors.New("no $id banner within 5s; refusing to send commands")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}

func xmlFromLines(lines []string) string {
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "XML="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func queryXMLName(ctx context.Context, c *leva.Conn) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	lines, err := c.Command(cctx, "MC?")
	if err != nil {
		return "", fmt.Errorf("MC?: %w", err)
	}
	return xmlFromLines(lines), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func captureCommands(ctx context.Context, c *leva.Conn, xmlName *string) error {
	for _, cmd := range safeCommands {
		cctx, cancel := context.WithTimeout(ctx, cmdTimeout)
		lines, err := c.Command(cctx, cmd)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", cmd, err)
		}
		if cmd == "MC?" {
			*xmlName = xmlFromLines(lines)
		}
		if err := sleepCtx(ctx, cmdWait); err != nil {
			return err
		}
	}
	return nil
}

func captureStream(ctx context.Context, c *leva.Conn, scenario string, stdin io.Reader, stdout io.Writer) error {
	reports, err := c.StartReports(ctx)
	if err != nil {
		return fmt.Errorf("start reports: %w", err)
	}
	// Raw traffic is recorded by the tap; just drain the channel.
	go func() {
		for range reports {
		}
	}()
	if scenario == "shot" {
		_, _ = fmt.Fprintln(stdout, "recording: pull the shot now, press Enter when done")
	} else {
		_, _ = fmt.Fprintln(stdout, "recording report stream: press Enter to stop")
	}
	enter := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(stdin).ReadString('\n')
		close(enter)
	}()
	select {
	case <-enter:
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cmdTimeout)
	defer cancel()
	if err := c.StopReports(sctx); err != nil {
		return fmt.Errorf("stop reports: %w", err)
	}
	return nil
}

func writeOutput(dir, scenario, addr, banner, xmlName string, events []fixture.Event) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, "log.jsonl"))
	if err != nil {
		return err
	}
	if err := fixture.WriteLog(f, events); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	m := fixture.Manifest{
		Scenario:        scenario,
		FirmwareVersion: banner,
		XMLName:         xmlName,
		MachineAddr:     addr,
		Recorded:        time.Now(),
	}
	if p := os.Getenv(envXML); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("hash spec: %w", err)
		}
		sum := sha256.Sum256(b)
		m.XMLSHA256 = hex.EncodeToString(sum[:])
	}
	return m.Save(filepath.Join(dir, "manifest.json"))
}
