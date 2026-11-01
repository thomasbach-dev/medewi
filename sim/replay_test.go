package sim

import (
	"bufio"
	"net"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

const time10s = 10 * time.Second

func rx(t int64, data string) fixture.Event {
	return fixture.Event{T: t, Dir: fixture.RX, Data: []byte(data)}
}

func replayCfg() Config {
	return Config{
		Speed: 10000,
		Stream: []fixture.Event{
			rx(0, "a1\r\n"),
			{T: 5, Dir: fixture.TX, Data: []byte("ignored\r\n")},
			rx(10, "a2\r\n"),
		},
	}
}

func send(t *testing.T, c net.Conn, cmd string) {
	t.Helper()
	if _, err := c.Write([]byte(cmd)); err != nil {
		t.Fatal(err)
	}
}

func expect(t *testing.T, r *bufio.Reader, c net.Conn, want string) {
	t.Helper()
	if got := readLine(t, r, c); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMCrStartsReplayForAllClients(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	s := start(t, cfg)
	c1, r1 := dial(t, s)
	c2, r2 := dial(t, s)
	readLine(t, r1, c1)
	readLine(t, r2, c2)
	send(t, c1, "MCr\r\n")
	for _, p := range []struct {
		r *bufio.Reader
		c net.Conn
	}{{r1, c1}, {r2, c2}} {
		expect(t, p.r, p.c, "a1\r\n") // TX event skipped
		expect(t, p.r, p.c, "a2\r\n")
	}
}

func TestReplayLoops(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	for i := 0; i < 3; i++ {
		expect(t, r, c, "a1\r\n")
		expect(t, r, c, "a2\r\n")
	}
}

func TestMCSpaceStopsGlobally(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	cfg.Responses = map[string][]string{"MCh": {"pong"}}
	s := start(t, cfg)
	cA, rA := dial(t, s)
	cB, rB := dial(t, s)
	readLine(t, rA, cA)
	readLine(t, rB, cB)
	send(t, cB, "MCr\r\n") // B starts
	expect(t, rA, cA, "a1\r\n")
	send(t, cA, "MC \r\n") // A stops
	// Drain until the stream goes quiet: a sentinel response marks the end.
	send(t, cA, "MCh\r\n")
	for line := readLine(t, rA, cA); line != "pong\r\n"; line = readLine(t, rA, cA) {
		_ = line // drain in-flight replay items
	}
	// After the sentinel nothing but our next sentinel may arrive.
	send(t, cA, "MCh\r\n")
	expect(t, rA, cA, "pong\r\n")
	s.mu.Lock()
	running := s.stopReplay != nil
	s.mu.Unlock()
	if running {
		t.Fatal("replayer still registered after MC space")
	}
}

func TestMCRResendsLast(t *testing.T) {
	cfg := Config{Speed: 1000, IdleTimeout: time10s, Stream: []fixture.Event{rx(0, "only\r\n")}}
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	expect(t, r, c, "only\r\n")
	send(t, c, "MC \r\n")
	send(t, c, "MCr\r\n")
	send(t, c, "MC \r\n")
	// Stopped now: MCR must be ignored.
	send(t, c, "MCR\r\n")
	// Restart to be reporting, then MCR must resend the last item.
	send(t, c, "MCr\r\n")
	send(t, c, "MCR\r\n")
	expect(t, r, c, "only\r\n")
}

func TestMCRWhenIdleSendsNothing(t *testing.T) {
	cfg := Config{Speed: 1000, IdleTimeout: time10s, Responses: map[string][]string{"MCh": {"pong"}}}
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCR\r\n")
	send(t, c, "MCh\r\n")
	expect(t, r, c, "pong\r\n")
}

func TestEmptyStreamReportsNothing(t *testing.T) {
	cfg := Config{Speed: 1000, IdleTimeout: time10s, Responses: map[string][]string{"MCh": {"pong"}}}
	s := start(t, cfg)
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	send(t, c, "MCR\r\n")
	send(t, c, "MCh\r\n")
	expect(t, r, c, "pong\r\n")
}

func TestReportingSurvivesDisconnectAndNoDoubleReplayer(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	s := start(t, cfg)
	c1, r1 := dial(t, s)
	readLine(t, r1, c1)
	send(t, c1, "MCr\r\n")
	send(t, c1, "MCr\r\n") // second start must not spawn a second replayer
	expect(t, r1, c1, "a1\r\n")
	_ = c1.Close()
	c2, r2 := dial(t, s)
	readLine(t, r2, c2)
	// The stream continues for a late joiner, strictly alternating a1/a2.
	for i := 0; i < 5; i++ {
		first := readLine(t, r2, c2)
		if first == "a2\r\n" {
			expect(t, r2, c2, "a1\r\n")
			continue
		}
		expect(t, r2, c2, "a2\r\n")
	}
}

func TestStopTerminatesReplayer(t *testing.T) {
	cfg := replayCfg()
	cfg.IdleTimeout = time10s
	s := New(cfg)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	c, r := dial(t, s)
	readLine(t, r, c)
	send(t, c, "MCr\r\n")
	expect(t, r, c, "a1\r\n")
	done := make(chan struct{})
	go func() { _ = s.Stop(); close(done) }()
	<-done // Stop joins the replayer via wg; hangs (test timeout) on a leak
}
