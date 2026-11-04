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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thomasbach-dev/medewi/conformance"
	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
)

const (
	envAddr = "MEDEWI_MACHINE_ADDR"
	envXML  = "MEDEWI_XML_PATH"

	bannerTimeout = 5 * time.Second
)

// bannerSettle is how long waitBanner waits after a lone first banner for a
// second one (shrunk in tests).
var bannerSettle = 1500 * time.Millisecond

// cmdWait is the pause after each scripted command (shrunk in tests).
var cmdWait = time.Second

// cmdTimeout bounds each command round trip (shrunk in tests).
var cmdTimeout = 10 * time.Second

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
  capture -scenario commands|stream|shot|display [-addr host:port] [-out captures/]

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
		env.XMLName = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
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
	scenario := fs.String("scenario", "", "scenario: commands|stream|shot|display (required)")
	out := fs.String("out", "captures", "output directory")
	force := fs.Bool("force", false, "overwrite an existing complete capture")
	sample := displaySample{}
	fs.DurationVar(&sample.every, "display-every", 10*time.Second, "display scenario: switch to the LCD mirror this often")
	fs.DurationVar(&sample.dur, "display-for", 500*time.Millisecond, "display scenario: stay on the LCD mirror this long per sample")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch *scenario {
	case "commands", "stream", "shot", "display":
	default:
		_, _ = fmt.Fprintf(stderr, "levacap: -scenario must be one of commands, stream, shot, display (got %q)\n", *scenario)
		return 2
	}
	if sample.every <= 0 || sample.dur <= 0 || sample.dur >= sample.every {
		_, _ = fmt.Fprintln(stderr, "levacap: need 0 < -display-for < -display-every")
		return 2
	}
	if *addr == "" {
		_, _ = fmt.Fprintf(stderr, "levacap: no target: set $%s or pass -addr\n", envAddr)
		return 2
	}

	outDir := filepath.Join(*out, *scenario)
	if !*force {
		for _, n := range []string{"log.jsonl", "manifest.json"} {
			// Any stat result other than "does not exist" counts as exists.
			if _, err := os.Stat(filepath.Join(outDir, n)); !errors.Is(err, os.ErrNotExist) {
				_, _ = fmt.Fprintf(stderr, "levacap: %s already exists; refusing to overwrite (use -force)\n", filepath.Join(outDir, n))
				return 1
			}
		}
	}

	// Validate and hash the spec before touching the machine.
	xmlSHA := ""
	if p := os.Getenv(envXML); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "levacap: read $%s: %v\n", envXML, err)
			return 1
		}
		sum := sha256.Sum256(b)
		xmlSHA = hex.EncodeToString(sum[:])
	}

	// SIGHUP too: a closed terminal must still stop the stream with "MC ".
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
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
	var marks []int64
	capErr := func() error {
		if *scenario == "commands" {
			return captureCommands(ctx, c, &xmlName)
		}
		name, err := queryXMLName(ctx, c)
		if err != nil {
			return err
		}
		xmlName = name
		marks, err = captureStream(ctx, c, *scenario, sample, start, stdin, stdout)
		return err
	}()

	var notes []string
	if *scenario == "display" {
		notes = append(notes, fmt.Sprintf("display sampled %v every %v", sample.dur, sample.every))
	}
	if len(marks) > 0 {
		ms := make([]string, len(marks))
		for i, m := range marks {
			ms[i] = strconv.FormatInt(m, 10)
		}
		notes = append(notes, "marks (ms): "+strings.Join(ms, ", "))
	}
	if xmlName == "" {
		_, _ = fmt.Fprintln(stderr, "levacap: warning: MC? returned no XML= line")
		notes = append(notes, "MC? returned no XML= line")
	}
	logName, manName := "log.jsonl", "manifest.json"
	if capErr != nil {
		// Never mix incomplete data into the names fixtures are loaded by.
		logName, manName = "log.partial.jsonl", "manifest.partial.json"
		notes = append(notes, "capture failed: "+capErr.Error())
	}
	mu.Lock()
	snapshot := append([]fixture.Event(nil), events...)
	mu.Unlock()
	if err := writeOutput(outDir, logName, manName, *scenario, *addr, c.ID(), xmlName, xmlSHA, strings.Join(notes, "; "), snapshot); err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 1
	}
	if capErr != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: capture incomplete (wrote %s): %v\n", logName, capErr)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "wrote %d events to %s\n", len(snapshot), outDir)
	return 0
}

// waitBanner waits until the machine is ready (second banner, or first
// banner plus settle); the real machine swallows commands sent right after
// connect.
func waitBanner(ctx context.Context, c *leva.Conn) error {
	wctx, cancel := context.WithTimeout(ctx, bannerTimeout+bannerSettle)
	defer cancel()
	if err := leva.WaitReady(wctx, c, bannerSettle); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("no $id banner within 5s; refusing to send commands")
		}
		return err
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

// displaySample is how the "display" scenario interleaves the LCD mirror
// with the report stream: the machine sends only one of the two at a time.
type displaySample struct {
	every, dur time.Duration
}

// sampleDisplay switches to the LCD mirror for s.dur every s.every and back
// to reports, until ctx ends. It returns once nothing more will be sent.
func sampleDisplay(ctx context.Context, c *leva.Conn, s displaySample) error {
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if err := c.StartDisplay(ctx); err != nil {
			return ignoreCanceled(ctx, fmt.Errorf("start display: %w", err))
		}
		// A stop mid-sample skips the switch back: the caller's "MC "
		// ends both streams.
		if sleepCtx(ctx, s.dur) != nil {
			return nil
		}
		if _, err := c.StartReports(ctx); err != nil {
			return ignoreCanceled(ctx, fmt.Errorf("resume reports: %w", err))
		}
	}
}

func ignoreCanceled(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// captureStream records the report stream (for "display" interleaved with
// LCD mirror samples) until an empty line or EOF on stdin. A line "m"
// records a mark: milliseconds since start, e.g. when the READY beeps sound.
func captureStream(ctx context.Context, c *leva.Conn, scenario string, sample displaySample, start time.Time, stdin io.Reader, stdout io.Writer) ([]int64, error) {
	reports, err := c.StartReports(ctx)
	if err != nil {
		return nil, fmt.Errorf("start reports: %w", err)
	}
	// Raw traffic is recorded by the tap; just drain the channel. The read
	// loop closes it when the connection dies.
	lost := make(chan struct{})
	go func() {
		defer close(lost)
		for range reports {
		}
	}()
	var sampled chan struct{} // closed when the display sampler gives up; nil: no sampler
	stop := func() error {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cmdTimeout)
		defer cancel()
		if err := c.StopReports(sctx); err != nil {
			return fmt.Errorf("stop reports: %w", err)
		}
		return nil
	}
	switch scenario {
	case "shot":
		_, _ = fmt.Fprintln(stdout, "recording: pull the shot now, press Enter when done")
	case "display":
		sctx, cancel := context.WithCancel(ctx)
		sampled = make(chan struct{})
		var sampleErr error
		go func() {
			defer close(sampled)
			sampleErr = sampleDisplay(sctx, c, sample)
		}()
		// Wrap stop so no sample command can follow "MC ".
		stopReports := stop
		stop = func() error {
			cancel()
			<-sampled
			return errors.Join(sampleErr, stopReports())
		}
		_, _ = fmt.Fprintf(stdout, "recording reports, display every %v: type m + Enter to mark a moment, Enter to stop\n", sample.every)
	default:
		_, _ = fmt.Fprintln(stdout, "recording report stream: press Enter to stop")
	}
	var (
		mu    sync.Mutex
		marks []int64
	)
	enter := make(chan struct{})
	go func() {
		defer close(enter)
		sc := bufio.NewScanner(stdin)
		for sc.Scan() {
			switch strings.TrimSpace(sc.Text()) {
			case "":
				return
			case "m":
			default:
				_, _ = fmt.Fprintln(stdout, "type m + Enter to mark, an empty line to stop")
				continue
			}
			ms := time.Since(start).Milliseconds()
			mu.Lock()
			marks = append(marks, ms)
			n := len(marks)
			mu.Unlock()
			_, _ = fmt.Fprintf(stdout, "mark %d at %s\n", n, (time.Duration(ms) * time.Millisecond).Round(time.Second))
		}
	}()
	// Without the last two cases a dead connection keeps "recording" until
	// the operator presses Enter.
	select {
	case <-enter:
	case <-ctx.Done():
	case <-lost:
	case <-sampled:
	}
	mu.Lock()
	defer mu.Unlock()
	return marks, stop()
}

// writeOutput writes both files under temp names and renames them into place
// only after both succeeded, so a failure never leaves a half-written capture
// under the final names or damages existing files.
func writeOutput(dir, logName, manName, scenario, addr, banner, xmlName, xmlSHA, notes string, events []fixture.Event) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	logTmp := filepath.Join(dir, "."+logName+".tmp")
	manTmp := filepath.Join(dir, "."+manName+".tmp")
	f, err := os.Create(logTmp)
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
		XMLSHA256:       xmlSHA,
		MachineAddr:     addr,
		Notes:           notes,
		Recorded:        time.Now(),
	}
	if err := m.Save(manTmp); err != nil {
		return err
	}
	if err := os.Rename(logTmp, filepath.Join(dir, logName)); err != nil {
		return err
	}
	return os.Rename(manTmp, filepath.Join(dir, manName))
}
