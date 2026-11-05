package conformance

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/sim"
)

func synthLine(temp string) []byte {
	line := []byte(strings.Repeat(" ", 54))
	line[0] = '{'
	line[2] = '-'
	copy(line[4:], "1:%")
	copy(line[7:], temp)
	copy(line[18:], " 42")
	copy(line[36:], " 9.10")
	copy(line[50:], "Out ")
	return append(line, '\r', '\n')
}

func startSim(t *testing.T) string {
	t.Helper()
	var stream []fixture.Event
	for i, temp := range []string{" 93.50", " 93.60", " 93.70", " 93.80"} {
		stream = append(stream, fixture.Event{T: int64(i * 10), Dir: fixture.RX, Data: synthLine(temp)})
	}
	return startSimStream(t, stream)
}

func startSimStream(t *testing.T, stream []fixture.Event) string {
	t.Helper()
	s := sim.New(sim.Config{
		Banner: "$id: leva! 3.2",
		Responses: map[string][]string{
			"MCh": {"MCh help", "MCu setup dump", "MCr reports"},
			"MC?": {"XML=leva!-65FCB201"},
		},
		Stream: stream,
		// Idle heartbeat: the banner check takes the second-banner fast path.
		BannerInterval: 30 * time.Millisecond,
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s.Addr().String()
}

func synthSpec(t *testing.T) *leva.RecordSpec {
	t.Helper()
	spec, err := leva.LoadSpecFile("../testdata/spec/synthetic.xml")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestAllChecksPassAgainstSim(t *testing.T) {
	addr := startSim(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, addr, StreamState, Env{Spec: synthSpec(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(All()) {
		t.Fatalf("got %d results, want %d", len(res), len(All()))
	}
	for _, r := range res {
		if r.Skipped || r.Err != nil {
			t.Errorf("%s: skipped=%v err=%v", r.Name, r.Skipped, r.Err)
		}
	}
}

func TestBadPumpMarkerFailsReportsDecode(t *testing.T) {
	var stream []fixture.Event
	for i := range 4 {
		line := synthLine(" 93.50")
		line[2] = 'x'
		stream = append(stream, fixture.Event{T: int64(i * 10), Dir: fixture.RX, Data: line})
	}
	addr := startSimStream(t, stream)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, addr, StreamState, Env{Spec: synthSpec(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Name != "reports-decode" {
			continue
		}
		if r.Err == nil || !strings.Contains(r.Err.Error(), "pump marker") {
			t.Errorf("reports-decode err=%v, want pump marker error", r.Err)
		}
		return
	}
	t.Fatal("no reports-decode result")
}

func TestMaxClassSkips(t *testing.T) {
	addr := startSim(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, addr, ReadOnly, Env{Spec: synthSpec(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		want := r.Name == "reports-decode"
		if r.Skipped != want || r.Err != nil {
			t.Errorf("%s: skipped=%v err=%v, want skipped=%v", r.Name, r.Skipped, r.Err, want)
		}
	}
}

func TestBannerFailureAbortsRest(t *testing.T) {
	// Valid responses, but a non-banner greeting: later checks would pass if
	// they ran, so any error on them proves the gate.
	s := sim.New(sim.Config{
		Banner: "noise",
		Responses: map[string][]string{
			"MCh": {"MCu", "MCr"},
			"MC?": {"XML=leva!-65FCB201"},
		},
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var tx []string
	res, err := Run(ctx, s.Addr().String(), StreamState, Env{Record: func(ev fixture.Event) {
		if ev.Dir == fixture.TX {
			tx = append(tx, string(ev.Data))
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Name != "banner" || res[0].Err == nil || errors.Is(res[0].Err, ErrNotRun) {
		t.Fatalf("banner should fail on its own: %+v", res[0])
	}
	for _, r := range res[1:] {
		if !errors.Is(r.Err, ErrNotRun) {
			t.Errorf("%s: err=%v, want ErrNotRun", r.Name, r.Err)
		}
	}
	if len(tx) != 0 {
		t.Errorf("commands sent after failed banner: %q", tx)
	}
}

func TestBootloaderPortRejectedEarly(t *testing.T) {
	_, err := Run(context.Background(), "127.0.0.1:2323", ReadOnly, Env{
		Confirm: func(string) bool { t.Fatal("Confirm called"); return true },
	})
	if !errors.Is(err, leva.ErrBootloaderPort) {
		t.Errorf("err=%v, want ErrBootloaderPort", err)
	}
	_, err = Run(context.Background(), "192.0.2.1:2323", ReadOnly, Env{
		Confirm: func(string) bool { t.Fatal("Confirm called"); return true },
	})
	if !errors.Is(err, leva.ErrBootloaderPort) {
		t.Errorf("non-loopback: err=%v, want ErrBootloaderPort", err)
	}
}

func TestRecordReceivesEvents(t *testing.T) {
	addr := startSim(t)
	var n int
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := Run(ctx, addr, ReadOnly, Env{Record: func(fixture.Event) { n++ }}); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("Record never called")
	}
}

func TestNonLoopbackNeedsConfirm(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 192.0.2.1 (TEST-NET-1): must be refused before any connection attempt.
	if _, err := Run(ctx, "192.0.2.1:23", ReadOnly, Env{}); err == nil {
		t.Error("expected refusal without Confirm")
	}
	asked := 0
	_, err := Run(ctx, "192.0.2.1:23", ReadOnly, Env{Confirm: func(string) bool { asked++; return false }})
	if err == nil || asked != 1 {
		t.Errorf("declined confirm: err=%v asked=%d", err, asked)
	}
}

func TestMachineConformance(t *testing.T) {
	addr := os.Getenv("MEDEWI_MACHINE_ADDR")
	if addr == "" || os.Getenv("MEDEWI_MACHINE_CONFIRM") != "1" {
		t.Skip("set MEDEWI_MACHINE_ADDR and MEDEWI_MACHINE_CONFIRM=1 to run against the real machine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := Run(ctx, addr, ReadOnly, Env{Confirm: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Name, r.Err)
		}
	}
}

func xmlNameResult(t *testing.T, res []Result) Result {
	t.Helper()
	for _, r := range res {
		if r.Name == "xml-name" {
			return r
		}
	}
	t.Fatal("no xml-name result")
	return Result{}
}

func TestXMLNameMismatchFails(t *testing.T) {
	addr := startSim(t) // cans MC? -> XML=leva!-65FCB201
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, addr, ReadOnly, Env{XMLName: "synthetic"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := xmlNameResult(t, res)
	if r.Err == nil {
		t.Fatal("xml-name passed despite spec/machine XML name mismatch")
	}
	for _, want := range []string{"synthetic", "leva!-65FCB201", "MEDEWI_XML_PATH"} {
		if !strings.Contains(r.Err.Error(), want) {
			t.Errorf("mismatch error %q does not mention %q", r.Err, want)
		}
	}
}

func TestXMLNameMatchPasses(t *testing.T) {
	addr := startSim(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, addr, ReadOnly, Env{XMLName: "leva!-65FCB201"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r := xmlNameResult(t, res); r.Err != nil {
		t.Fatalf("xml-name failed despite matching name: %v", r.Err)
	}
}

func init() { bannerSettle = 50 * time.Millisecond }
