package rawlog

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func openLog(t *testing.T, cfg Config) (*Log, string) {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = filepath.Join(t.TempDir(), "raw")
	}
	l, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, cfg.Dir
}

func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

func readDir(t *testing.T, dir string) [][]fixture.Event {
	t.Helper()
	var out [][]fixture.Event
	for _, n := range fileNames(t, dir) {
		evs, err := ReadFile(n)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", n, err)
		}
		out = append(out, evs)
	}
	return out
}

func TestSessionWritesLinesAndTX(t *testing.T) {
	clk := newClock()
	l, dir := openLog(t, Config{Now: clk.Now})
	s := l.Session()
	s.Tap("rx", []byte("$id: leva"))
	clk.Advance(10 * time.Millisecond)
	s.Tap("rx", []byte("! (3.2)\r\n{a}\r\n{b"))
	clk.Advance(10 * time.Millisecond)
	s.Tap("tx", []byte("MCr\r\n"))
	s.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	files := readDir(t, dir)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	want := []struct {
		dir  fixture.Dir
		data string
	}{
		{fixture.RX, "$id: leva! (3.2)\r\n"},
		{fixture.RX, "{a}\r\n"},
		{fixture.TX, "MCr\r\n"},
		{fixture.RX, "{b"},
	}
	evs := files[0]
	if len(evs) != len(want) {
		t.Fatalf("events = %+v", evs)
	}
	var prev int64
	for i, w := range want {
		if evs[i].Dir != w.dir || string(evs[i].Data) != w.data {
			t.Errorf("event %d = %s %q, want %s %q", i, evs[i].Dir, evs[i].Data, w.dir, w.data)
		}
		// The partial line "{b" is stamped with its first byte (10 ms) but
		// emitted at Close, after the tx at 20 ms: the one allowed step back.
		if i < 3 && evs[i].T < prev {
			t.Errorf("event %d: t went backwards", i)
		}
		prev = evs[i].T
	}
	if evs[3].T != 10 {
		t.Errorf("partial line t = %d, want 10 (its first byte)", evs[3].T)
	}
	if evs[0].T != 0 {
		t.Errorf("first line t = %d, want 0 (first chunk)", evs[0].T)
	}

	name := fileNames(t, dir)[0]
	if !regexp.MustCompile(`^\d{8}T\d{6}\.\d{3}Z\.jsonl\.gz$`).MatchString(filepath.Base(name)) {
		t.Errorf("name %q", name)
	}
	fi, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
}

func TestLongPartialLineEmitted(t *testing.T) {
	l, dir := openLog(t, Config{FlushEvery: 20 * time.Millisecond})
	s := l.Session()
	s.Tap("rx", []byte("{"+strings.Repeat("x", 4999)))
	deadline := time.Now().Add(time.Second)
	for {
		names := fileNames(t, dir)
		if len(names) == 1 {
			if evs, err := ReadFile(names[0]); err == nil && len(evs) == 1 {
				if evs[0].Dir != fixture.RX || len(evs[0].Data) != 4096 {
					t.Fatalf("event = %s len %d", evs[0].Dir, len(evs[0].Data))
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no 4096-byte event within 1s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewFilePerSession(t *testing.T) {
	clk := newClock()
	l, dir := openLog(t, Config{Now: clk.Now})
	for range 2 {
		s := l.Session()
		s.Tap("rx", []byte("{a}\r\n"))
		s.Close()
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	names := fileNames(t, dir)
	if len(names) != 2 {
		t.Fatalf("files = %v", names)
	}
	if !strings.HasSuffix(names[0], "120000.000Z.jsonl.gz") || !strings.HasSuffix(names[1], "120000.001Z.jsonl.gz") {
		t.Errorf("names = %v", names)
	}
}

func TestEmptySessionCreatesNoFile(t *testing.T) {
	l, dir := openLog(t, Config{})
	l.Session().Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if n := fileNames(t, dir); len(n) != 0 {
		t.Fatalf("files = %v", n)
	}
}

func TestRotateEvery(t *testing.T) {
	clk := newClock()
	l, dir := openLog(t, Config{Now: clk.Now, RotateEvery: time.Minute})
	s := l.Session()
	s.Tap("rx", []byte("{a}\r\n"))
	clk.Advance(61 * time.Second)
	s.Tap("rx", []byte("{b}\r\n"))
	s.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	files := readDir(t, dir)
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	if len(files[1]) != 1 || files[1][0].T != 0 || string(files[1][0].Data) != "{b}\r\n" {
		t.Errorf("second file = %+v", files[1])
	}
}

func TestTapAfterClose(t *testing.T) {
	l, dir := openLog(t, Config{})
	s := l.Session()
	s.Tap("rx", []byte("{a}\r\n"))
	s.Close()
	s.Tap("rx", []byte("{late}\r\n"))
	s.Tap("tx", []byte("MC?\r\n"))
	s2 := l.Session()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	s2.Tap("rx", []byte("{later}\r\n"))
	s2.Close()
	_ = l.Close() // idempotent
	if n := len(l.items); n != 0 {
		t.Errorf("queue holds %d items after late taps", n)
	}

	files := readDir(t, dir)
	if len(files) != 1 || len(files[0]) != 1 {
		t.Fatalf("files = %+v", files)
	}
}

func TestReadFileTruncated(t *testing.T) {
	l, dir := openLog(t, Config{FlushEvery: 10 * time.Millisecond})
	s := l.Session()
	s.Tap("rx", []byte("{a}\r\n"))
	cp := filepath.Join(t.TempDir(), "copy.jsonl.gz")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if names := fileNames(t, dir); len(names) == 1 {
			if b, err := os.ReadFile(names[0]); err == nil {
				if err := os.WriteFile(cp, b, 0o600); err != nil {
					t.Fatal(err)
				}
				evs, err := ReadFile(cp)
				if err != nil {
					t.Fatalf("ReadFile truncated: %v", err)
				}
				if len(evs) == 1 {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("flushed event never readable")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNilSafe(t *testing.T) {
	var l *Log
	s := l.Session()
	s.Tap("rx", nil)
	s.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenChmodsExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, _ := openLog(t, Config{Dir: dir})
	_ = l.Close()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode().Perm())
	}
}

func TestCloseReleasesBlockedWriteHook(t *testing.T) {
	entered := make(chan struct{}, 1)
	l, _ := openLog(t, Config{writeHook: func(stop <-chan struct{}) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-stop
	}})
	s := l.Session()
	s.Tap("rx", []byte("{a}\r\n"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hook never called")
	}
	done := make(chan struct{})
	go func() { _ = l.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on the write hook")
	}
}

func TestNoNegativeTAfterRotation(t *testing.T) {
	clk := newClock()
	l, dir := openLog(t, Config{Now: clk.Now, RotateEvery: time.Minute})
	s := l.Session()
	s.Tap("rx", []byte("{a}\r\n")) // opens the first file
	clk.Advance(30 * time.Second)
	s.Tap("rx", []byte("{b")) // partial, stamped +30 s
	clk.Advance(31 * time.Second)
	s.Tap("tx", []byte("MC?\r\n")) // rotates
	s.Tap("rx", []byte("}\r\n"))   // completes the line stamped before the rotation
	s.Close()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	files := readDir(t, dir)
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	for _, evs := range files {
		for _, ev := range evs {
			if ev.T < 0 {
				t.Errorf("negative t: %+v", ev)
			}
		}
	}
}

func TestConcurrentTapsRacingClose(t *testing.T) {
	l, _ := openLog(t, Config{Buffer: 16})
	s := l.Session()
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir := "rx"
			if i%2 == 0 {
				dir = "tx"
			}
			for range 500 {
				s.Tap(dir, []byte("{x}\r\n"))
			}
		}()
	}
	_ = l.Close()
	s.Close()
	wg.Wait()
}

func writeFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestPruneByAge(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw")
	writeFile(t, dir, "20261020T000000.000Z.jsonl.gz", 10)
	writeFile(t, dir, "20261103T000000.000Z.jsonl.gz", 10)
	writeFile(t, dir, "notes.txt", 10)
	clk := newClock()
	openLog(t, Config{Dir: dir, Now: clk.Now, Retention: 7 * 24 * time.Hour})
	if exists(dir, "20261020T000000.000Z.jsonl.gz") {
		t.Error("old file survived")
	}
	for _, n := range []string{"20261103T000000.000Z.jsonl.gz", "notes.txt"} {
		if !exists(dir, n) {
			t.Errorf("%s was deleted", n)
		}
	}
}

func TestPruneBySize(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "raw")
	writeFile(t, dir, "20261101T000000.000Z.jsonl.gz", 1000)
	writeFile(t, dir, "20261102T000000.000Z.jsonl.gz", 1000)
	writeFile(t, dir, "20261103T000000.000Z.jsonl.gz", 1000)
	writeFile(t, dir, "other.bin", 5000) // not a log: not counted
	openLog(t, Config{Dir: dir, Now: newClock().Now, MaxBytes: 2500})
	if exists(dir, "20261101T000000.000Z.jsonl.gz") {
		t.Error("oldest file survived")
	}
	for _, n := range []string{"20261102T000000.000Z.jsonl.gz", "20261103T000000.000Z.jsonl.gz", "other.bin"} {
		if !exists(dir, n) {
			t.Errorf("%s was deleted", n)
		}
	}
}

func TestPruneSkipsOpenFile(t *testing.T) {
	clk := newClock()
	l, dir := openLog(t, Config{Now: clk.Now, MaxBytes: 1, FlushEvery: 10 * time.Millisecond})
	s := l.Session()
	s.Tap("rx", []byte("{a}\r\n"))
	s.Close()
	clk.Advance(time.Second)
	s = l.Session()
	s.Tap("rx", []byte("{b}\r\n"))
	deadline := time.Now().Add(2 * time.Second)
	for {
		names := fileNames(t, dir)
		if len(names) == 1 {
			evs, err := ReadFile(names[0])
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) == 1 && string(evs[0].Data) == "{b}\r\n" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("want only the open file with its event, got %v", names)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDropsWhenFull(t *testing.T) {
	l, _ := openLog(t, Config{Buffer: 1, writeHook: func(stop <-chan struct{}) { <-stop }})
	s := l.Session()
	start := time.Now()
	for range 100 {
		s.Tap("tx", []byte("MCh\r\n"))
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("100 taps took %v", d)
	}
	if l.Dropped() == 0 {
		t.Error("nothing dropped")
	}
	done := make(chan struct{})
	go func() { _ = l.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close blocked")
	}
}

func TestWriteErrorKeepsRunning(t *testing.T) {
	l, dir := openLog(t, Config{})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := l.Session()
	s.Tap("rx", []byte("{a}\r\n"))
	deadline := time.Now().Add(time.Second)
	for l.WriteErrors() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no write error counted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Tap("rx", []byte("{b}\r\n")) // still accepted
	done := make(chan struct{})
	go func() { _ = l.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close hung")
	}
}

func TestAccessorsNilSafe(t *testing.T) {
	var l *Log
	if l.Dropped() != 0 || l.WriteErrors() != 0 {
		t.Error("nil log counts")
	}
}
