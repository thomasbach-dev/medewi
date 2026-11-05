package sim

import (
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

const bannerLine = "$id: leva!\r\n"

func TestHeartbeatWhileIdle(t *testing.T) {
	s := start(t, Config{BannerInterval: 20 * time.Millisecond})
	c, r := dial(t, s)
	for i := range 4 { // connect banner + at least 3 heartbeats
		if got := readLine(t, r, c); got != bannerLine {
			t.Fatalf("line %d = %q, want banner", i, got)
		}
	}
}

func TestNoHeartbeatByDefault(t *testing.T) {
	s := start(t, Config{})
	c, r := dial(t, s)
	readLine(t, r, c)
	_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if line, err := r.ReadString('\n'); err == nil {
		t.Fatalf("unexpected line %q", line)
	}
}

// Like the real machine (2026-11-04: a phone app had left MCr on), a client
// connecting while reporting is on gets reports and no banner: the banner
// is the idle heartbeat, and it is silent while reporting.
func TestNoConnectBannerWhileReporting(t *testing.T) {
	cfg := Config{
		BannerInterval: 20 * time.Millisecond,
		IdleTimeout:    time10s,
		Stream:         []fixture.Event{rx(0, "r1\r\n"), rx(20, "r2\r\n")},
	}
	s := start(t, cfg)
	c1, r1 := dial(t, s)
	send(t, c1, "MCr\r\n")
	for line := ""; line != "r1\r\n"; { // skip banners queued before MCr
		line = readLine(t, r1, c1)
	}
	c2, r2 := dial(t, s)
	for i := range 5 {
		if got := readLine(t, r2, c2); got == bannerLine {
			t.Fatalf("line %d is a banner while reporting is on", i)
		}
	}
}

func TestHeartbeatPausesDuringReporting(t *testing.T) {
	cfg := Config{
		BannerInterval: 20 * time.Millisecond,
		IdleTimeout:    time10s,
		Stream:         []fixture.Event{rx(0, "r1\r\n"), rx(300, "r2\r\n")},
	}
	s := start(t, cfg)
	c, r := dial(t, s)
	send(t, c, "MCr\r\n")
	// Banners queued before MCr was processed are fine; skip to the first report.
	for line := readLine(t, r, c); line != "r1\r\n"; line = readLine(t, r, c) {
		if line != bannerLine {
			t.Fatalf("unexpected line %q", line)
		}
	}
	// 300 ms gap is 15 intervals: any banner here would show up before r2.
	expect(t, r, c, "r2\r\n")
	expect(t, r, c, "r1\r\n")
	send(t, c, "MC \r\n")
	// Drain in-flight report lines, then banners must resume.
	for n := 0; n < 3; {
		line := readLine(t, r, c)
		switch {
		case line == bannerLine:
			n++
		case n > 0:
			t.Fatalf("report line %q after banner resumed", line)
		case !strings.HasPrefix(line, "r"):
			t.Fatalf("unexpected line %q", line)
		}
	}
}
