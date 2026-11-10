package main

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// running is a levasim run() started in a goroutine by start.
type running struct {
	addr   string
	done   chan struct{}
	exit   chan int
	once   sync.Once
	code   int
	stdout bytes.Buffer
	stderr bytes.Buffer
}

// start runs levasim with args, waiting until it reports its listen address.
// Tests must pass --tripwire "" unless they want the default 2323 bound.
func start(t *testing.T, args ...string) *running {
	t.Helper()
	r := &running{done: make(chan struct{}), exit: make(chan int, 1)}
	ready := make(chan string, 1)
	go func() {
		r.exit <- run(args, &r.stdout, &r.stderr, ready, r.done)
	}()
	select {
	case r.addr = <-ready:
	case c := <-r.exit:
		t.Fatalf("exited early with %d\nstderr: %s", c, r.stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for listen address")
	}
	t.Cleanup(func() { r.shutdown() })
	return r
}

// shutdown closes done, waits for run to return and yields its exit code.
// Safe to call more than once.
func (r *running) shutdown() int {
	r.once.Do(func() {
		close(r.done)
		r.code = <-r.exit
	})
	return r.code
}

func dialLines(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return c, bufio.NewReader(c)
}

func readLines(t *testing.T, r *bufio.Reader, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for range n {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read after %d lines: %v", len(out), err)
		}
		out = append(out, strings.TrimRight(line, "\r\n"))
	}
	return out
}

func TestServesOnFlagAddr(t *testing.T) {
	r := start(t, "--addr", "127.0.0.1:0", "--tripwire", "")
	c, rd := dialLines(t, r.addr)

	if got := readLines(t, rd, 1)[0]; got != "$id: leva! (3.2)" {
		t.Fatalf("banner = %q", got)
	}
	if _, err := c.Write([]byte("MCh\r\n")); err != nil {
		t.Fatal(err)
	}
	want := defaultResponses["MCh"]
	got := readLines(t, rd, len(want))
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("MCh response = %q, want %q", got, want)
	}

	if code := r.shutdown(); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, r.stderr.String())
	}
	m := regexp.MustCompile(`(?m)^levasim listening on (\S+) \(tripwire off\)$`).FindStringSubmatch(r.stdout.String())
	if m == nil {
		t.Fatalf("no listen line in stdout: %q", r.stdout.String())
	}
	if m[1] != r.addr {
		t.Errorf("printed addr %q, ready addr %q", m[1], r.addr)
	}
}

func TestFixturesDirServesResponsesAndReplaysLog(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "responses.json"), `{"MCh": ["custom help"]}`)
	logf, err := os.Create(filepath.Join(dir, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.WriteLog(logf, []fixture.Event{
		{T: 0, Dir: fixture.TX, Data: []byte("MCr\r\n")},
		{T: 0, Dir: fixture.RX, Data: []byte("replayed\r\n")},
	}); err != nil {
		t.Fatal(err)
	}
	_ = logf.Close()

	r := start(t, "--addr", "127.0.0.1:0", "--tripwire", "", "--fixtures", dir)
	c, rd := dialLines(t, r.addr)
	readLines(t, rd, 1)

	if _, err := c.Write([]byte("MCh\r\n")); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, rd, 1)[0]; got != "custom help" {
		t.Fatalf("MCh = %q, want custom help", got)
	}
	if _, err := c.Write([]byte("MCr\r\n")); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, rd, 1)[0]; got != "replayed" {
		t.Fatalf("replay = %q, want replayed", got)
	}
}

func TestBadFixturesExitOne(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string) string{
		"malformed responses.json": func(t *testing.T, dir string) string {
			writeFile(t, filepath.Join(dir, "responses.json"), `{not json`)
			return dir
		},
		"malformed log.jsonl": func(t *testing.T, dir string) string {
			writeFile(t, filepath.Join(dir, "log.jsonl"), `{"t":`)
			return dir
		},
		"fixtures path is a file": func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "file")
			writeFile(t, p, "x")
			return p
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := setup(t, t.TempDir())
			done := make(chan struct{})
			close(done) // if run serves anyway, it returns at once
			var out, errb bytes.Buffer
			code := run([]string{"--addr", "127.0.0.1:0", "--tripwire", "", "--fixtures", dir}, &out, &errb, nil, done)
			if code != 1 {
				t.Fatalf("exit %d, want 1; stderr: %s", code, errb.String())
			}
			if errb.Len() == 0 {
				t.Errorf("no error message on stderr")
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSingleDashLongFlagRejected(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-addr", "127.0.0.1:0"}, &out, &errb, nil, make(chan struct{})); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "unknown shorthand flag") {
		t.Errorf("stderr %q", errb.String())
	}
}
