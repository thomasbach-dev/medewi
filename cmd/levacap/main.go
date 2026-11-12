// Command levacap runs the conformance suite against a leva! target and
// records protocol fixtures from a live machine.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/thomasbach-dev/medewi/conformance"
	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/systime"
)

const (
	envAddr = "MEDEWI_MACHINE_ADDR"
	envXML  = "MEDEWI_XML_PATH"
)

// bannerTimeout is how long waitBanner waits for the first banner (shrunk
// in tests).
var bannerTimeout = 5 * time.Second

// bannerSettle is how long waitBanner waits after a lone first banner for a
// second one (shrunk in tests).
var bannerSettle = 1500 * time.Millisecond

// cmdWait is the pause after each scripted command (shrunk in tests).
var cmdWait = time.Second

// cmdTimeout bounds each command round trip (shrunk in tests).
var cmdTimeout = 10 * time.Second

// safeCommands is the complete, read-only command whitelist capture may send
// by capture --commands. Do not add anything else.
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
	case "session-reset":
		return runSessionReset(args[1:], stdout, stderr)
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

  conform [--addr host:port] [--max readonly|stream] [--yes]
  capture --out DIR [--notes TEXT] [--display-every SPAN [--display-for SPAN]] [--addr host:port]
                                     record the report stream into DIR until Enter
  capture --commands --out DIR [--addr host:port]
                                     each whitelist command once (license key inside: private)
  session-reset [--addr host:port]   stop a stream another client left on

Target defaults to $MEDEWI_MACHINE_ADDR; decode spec from $MEDEWI_XML_PATH.`)
}

func newFlagSet(name string, stderr io.Writer) (*pflag.FlagSet, *string) {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", os.Getenv(envAddr), "target host:port (default $"+envAddr+")")
	return fs, addr
}

// parseFlags parses args into fs. When it returns false the caller exits
// with code: 0 after --help (pflag printed the flags), 2 on a usage error.
func parseFlags(fs *pflag.FlagSet, args []string, stderr io.Writer) (code int, ok bool) {
	err := fs.Parse(args)
	switch {
	case err == nil:
		return 0, true
	case errors.Is(err, pflag.ErrHelp):
		return 0, false
	default:
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 2, false
	}
}

func runConform(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, addr := newFlagSet("conform", stderr)
	maxFlag := fs.String("max", "readonly", "highest check class: readonly|stream")
	yes := fs.Bool("yes", false, "skip the interactive confirmation")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	var maxClass conformance.Class
	switch *maxFlag {
	case "readonly":
		maxClass = conformance.ReadOnly
	case "stream":
		maxClass = conformance.StreamState
	default:
		_, _ = fmt.Fprintf(stderr, "levacap: invalid --max %q (want readonly|stream)\n", *maxFlag)
		return 2
	}
	if *addr == "" {
		_, _ = fmt.Fprintf(stderr, "levacap: no target: set $%s or pass --addr\n", envAddr)
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
	outDir := ""
	fs.StringVar(&outDir, "out", "", "capture directory, e.g. captures/shot-2026-11-12 (required); log.jsonl and manifest.json go straight into it")
	commands := fs.Bool("commands", false, "send each whitelist command once (MCh MC? MCu MCm MCb MCf) instead of recording the report stream; the output holds the license key: private storage only")
	userNotes := fs.String("notes", "", "free text for the manifest: what was recorded")
	force := fs.Bool("force", false, "overwrite an existing complete capture")
	sample := displaySample{dur: 500 * time.Millisecond}
	fs.Var(systime.NewSpan(&sample.every), "display-every", "also sample the LCD mirror (MC@) this often; 0 = off (default); costs report samples meanwhile")
	fs.Var(systime.NewSpan(&sample.dur), "display-for", "with --display-every: stay on the LCD mirror this long per sample")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	if outDir == "" {
		_, _ = fmt.Fprintln(stderr, "levacap: --out is required: the capture directory, e.g. captures/shot-2026-11-12")
		return 2
	}
	if *commands && sample.every > 0 {
		_, _ = fmt.Fprintln(stderr, "levacap: --commands records no stream; --display-every needs the stream")
		return 2
	}
	if sample.every > 0 && (sample.dur <= 0 || sample.dur >= sample.every) {
		_, _ = fmt.Fprintln(stderr, "levacap: need 0 < --display-for < --display-every")
		return 2
	}
	if *addr == "" {
		_, _ = fmt.Fprintf(stderr, "levacap: no target: set $%s or pass --addr\n", envAddr)
		return 2
	}
	scenario := "stream"
	if *commands {
		scenario = "commands"
	}

	if !*force {
		for _, n := range []string{"log.jsonl", "manifest.json"} {
			// Any stat result other than "does not exist" counts as exists.
			if _, err := os.Stat(filepath.Join(outDir, n)); !errors.Is(err, os.ErrNotExist) {
				_, _ = fmt.Fprintf(stderr, "levacap: %s already exists; refusing to overwrite (use --force)\n", filepath.Join(outDir, n))
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

	received, removeRX := watchRX(c)
	defer removeRX()
	if err := waitBanner(ctx, c, received); err != nil {
		_, _ = fmt.Fprintf(stderr, "levacap: %v\n", err)
		return 1
	}

	xmlName := ""
	var marks []int64
	capErr := func() error {
		if *commands {
			return captureCommands(ctx, c, &xmlName)
		}
		name, err := queryXMLName(ctx, c)
		if err != nil {
			return err
		}
		xmlName = name
		marks, err = captureStream(ctx, c, sample, start, stdin, stdout)
		return err
	}()

	var notes []string
	if *userNotes != "" {
		notes = append(notes, *userNotes)
	}
	if sample.every > 0 {
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
	if err := writeOutput(outDir, logName, manName, scenario, *addr, c.ID(), xmlName, xmlSHA, strings.Join(notes, "; "), snapshot); err != nil {
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

// errStreamOn means the machine sent data but no banner: the banner is the
// idle heartbeat, silent while a report or display stream is on, and stream
// toggles are global machine state.
var errStreamOn = errors.New("data received but no $id banner: a stream is on. " +
	"Another device may hold a connection (a phone app, medewid), or a previous client left its stream on. " +
	"Ensure that all other devices are disconnected and/or run 'levacap session-reset'")

// watchRX reports whether c has received any bytes so far.
func watchRX(c *leva.Conn) (received func() bool, remove func()) {
	var got atomic.Bool
	remove = c.Tap(func(dir string, p []byte) {
		if dir == "rx" && len(p) > 0 {
			got.Store(true)
		}
	})
	return got.Load, remove
}

// waitBanner waits until the machine is ready (second banner, or first
// banner plus settle); the real machine swallows commands sent right after
// connect. Without a banner it returns errStreamOn if received reports data.
func waitBanner(ctx context.Context, c *leva.Conn, received func() bool) error {
	wctx, cancel := context.WithTimeout(ctx, bannerTimeout+bannerSettle)
	defer cancel()
	if err := leva.WaitReady(wctx, c, bannerSettle); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			if received() {
				return fmt.Errorf("refusing to send commands: %w", errStreamOn)
			}
			return fmt.Errorf("no $id banner within %v; refusing to send commands", bannerTimeout)
		}
		return err
	}
	return nil
}

// MC? is tried mcqTries times, mcqSpacing apart, like the daemon's
// handshake: the machine can swallow the first command even after the
// banner wait (seen 2026-11-05). mcqSpacing is shrunk in tests.
var mcqSpacing = 2 * time.Second

const (
	mcqTries   = 3
	mcqIdleGap = 300 * time.Millisecond
)

func queryXMLName(ctx context.Context, c *leva.Conn) (string, error) {
	name, err := leva.QueryXMLName(ctx, c, mcqTries, mcqSpacing, mcqIdleGap)
	if err != nil {
		return "", fmt.Errorf("MC?: %w", err)
	}
	return name, nil
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
			*xmlName, _ = leva.XMLName(lines)
		}
		if err := sleepCtx(ctx, cmdWait); err != nil {
			return err
		}
	}
	return nil
}

// displaySample is how a capture with --display-every interleaves the LCD mirror
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
func captureStream(ctx context.Context, c *leva.Conn, sample displaySample, start time.Time, stdin io.Reader, stdout io.Writer) ([]int64, error) {
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
	// Frames and notices too, so Conn.Dropped stays meaningful.
	for _, ch := range []<-chan []byte{c.DisplayFrames(), c.Notices()} {
		go func() {
			for range ch {
			}
		}()
	}
	var sampled chan struct{} // closed when the display sampler gives up; nil: no sampler
	stop := func() error {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cmdTimeout)
		defer cancel()
		if err := c.StopReports(sctx); err != nil {
			return fmt.Errorf("stop reports: %w", err)
		}
		return nil
	}
	if sample.every > 0 {
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
	} else {
		_, _ = fmt.Fprintln(stdout, "recording reports: type m + Enter to mark a moment, Enter to stop")
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
