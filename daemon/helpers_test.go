package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/sim"
	"github.com/thomasbach-dev/medewi/store"
	"github.com/thomasbach-dev/medewi/watch"
)

func TestMain(m *testing.M) {
	// Keep test output pristine; tests that assert on logs install their own.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// testSpec has the real export names at synthetic offsets. The payload
// layout is "{ M MMM " (marker at [2], mode at [4:7]) followed by the fields.
func testSpec() *leva.RecordSpec {
	return &leva.RecordSpec{Values: []leva.ValueSpec{
		{Index: 1, Name: "Temperature PID1", At: 8, Len: 6, Type: leva.Float},
		{Index: 2, Name: "Setpoint PID", At: 15, Len: 6, Type: leva.Float},
		{Index: 3, Name: "Heat PID", At: 22, Len: 5, Type: leva.Int},
		{Index: 4, Name: "Pressure", At: 28, Len: 5, Type: leva.Float},
		{Index: 5, Name: "Pressure SP", At: 34, Len: 5, Type: leva.Float},
		{Index: 30, Name: "Flow rate", At: 40, Len: 5, Type: leva.Int, Factor: 0.0166},
		{Index: 6, Name: "Volume", At: 46, Len: 5, Type: leva.Int},
	}}
}

// specWithout returns testSpec minus the named field.
func specWithout(name string) *leva.RecordSpec {
	s := testSpec()
	var vs []leva.ValueSpec
	for _, v := range s.Values {
		if v.Name != name {
			vs = append(vs, v)
		}
	}
	s.Values = vs
	return s
}

// line renders a report line for testSpec. A negative psp renders as
// "?????"; mode "???" (standby) masks every field, like the real machine.
func line(marker byte, mode string, temp, sp, pressure, psp, flowRaw, volume float64) []byte {
	if mode == "???" {
		return []byte(fmt.Sprintf("{ %c %s %6s %6s %5s %5s %5s %5s %5s", marker, mode,
			"??????", "??????", "?????", "?????", "?????", "?????", "?????"))
	}
	pspStr := "?????"
	if psp >= 0 {
		pspStr = fmt.Sprintf("%5.1f", psp)
	}
	return []byte(fmt.Sprintf("{ %c %s %6.1f %6.1f %5d %5.1f %5s %5.0f %5.0f", marker, mode,
		temp, sp, 0, pressure, pspStr, flowRaw, volume))
}

// stream turns lines into RX events 250 ms apart (25 ms at Speed 10).
func stream(lines ...[]byte) []fixture.Event {
	evs := make([]fixture.Event, len(lines))
	for i, l := range lines {
		evs[i] = fixture.Event{T: int64(i) * 250, Dir: fixture.RX, Data: append(append([]byte(nil), l...), '\r', '\n')}
	}
	return evs
}

// repeat returns n copies of l.
func repeat(n int, l []byte) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = l
	}
	return out
}

// idleLines is a settled, ready machine: PID mode, temp on setpoint.
func idleLines(n int) [][]byte { return repeat(n, line('-', "PID", 93, 93, 0, -1, 0, 0)) }

func simConfig(lines [][]byte) sim.Config {
	return sim.Config{
		BannerInterval: 30 * time.Millisecond,
		Banner:         "$id: leva! (3.2)",
		Responses:      map[string][]string{"MC?": {"XML=test"}},
		Speed:          10,
		Stream:         stream(lines...),
		IdleTimeout:    time.Minute,
	}
}

func startSim(t *testing.T, cfg sim.Config) *sim.Server {
	t.Helper()
	s := sim.New(cfg)
	if err := s.Start(); err != nil {
		t.Fatalf("sim start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "medewi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func daemonConfig(addr string, st *store.Store) Config {
	return Config{
		Addr:     addr,
		Spec:     testSpec(),
		SpecName: "test",
		Store:    st,
		Brew: brew.Config{
			CoastQuiet: 200 * time.Millisecond,
			CoastMax:   300 * time.Millisecond,
			WaterQuiet: 300 * time.Millisecond,
			MinWaterML: 5,
		},
		Watch:      watch.DefaultConfig(),
		Settle:     100 * time.Millisecond,
		BackoffMin: 10 * time.Millisecond,
		BackoffMax: 50 * time.Millisecond,
	}
}

// runDaemon starts Run. The returned stop cancels it and fails the test on a
// hang or an error; it is idempotent and also runs in Cleanup, before the
// sim's and the store's cleanups.
func runDaemon(t *testing.T, cfg Config) (*Daemon, func()) {
	t.Helper()
	d := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return d, stop
}

// waitFor polls cond until it holds or waitLong passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLong)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const waitLong = 10 * time.Second

func brews(t *testing.T, st *store.Store) []store.Brew {
	t.Helper()
	bs, err := st.Brews(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

func count(cmds []string, want string) int {
	n := 0
	for _, c := range cmds {
		if c == want {
			n++
		}
	}
	return n
}

// assertOnlyAllowed fails if the daemon sent anything but the handshake and
// stream commands this task may send.
func assertOnlyAllowed(t *testing.T, s *sim.Server) {
	t.Helper()
	for _, c := range s.Received() {
		switch c {
		case "MC?", "MCr", "MC ":
		default:
			t.Errorf("daemon sent %q", c)
		}
	}
}

// lockedBuffer is an io.Writer safe for concurrent slog use.
type lockedBuffer struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}
