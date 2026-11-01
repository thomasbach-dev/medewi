package conformance

import (
	"context"
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
	s := sim.New(sim.Config{
		Banner: "$id: leva! 3.2",
		Responses: map[string][]string{
			"MCh": {"MCh help", "MCu setup dump", "MCr reports"},
			"MC?": {"XML=leva!-65FCB201"},
		},
		Stream: stream,
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
	s := sim.New(sim.Config{Banner: "$id: other"})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, s.Addr().String(), ReadOnly, Env{})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Name != "banner" || res[0].Err == nil {
		t.Fatalf("banner should fail: %+v", res[0])
	}
	for _, r := range res[1:] {
		if r.Err == nil && !r.Skipped {
			t.Errorf("%s ran despite failed banner", r.Name)
		}
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
