package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/sim"
)

func f64(v float64) *float64 { return &v }

// apiDo performs one request against h; a POST carries a JSON Content-Type.
func apiDo(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var hdr map[string]string
	if method == http.MethodPost {
		hdr = map[string]string{"Content-Type": "application/json"}
	}
	return apiDoHeaders(t, h, method, path, body, hdr)
}

// apiDoHeaders performs one request against h with exactly the given headers.
func apiDoHeaders(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// apiSetup runs a daemon on the sim, waits until it is active and returns
// its handler.
func apiSetup(t *testing.T, cfg sim.Config) (*Daemon, http.Handler, *sim.Server) {
	t.Helper()
	s := startSim(t, cfg)
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitActive(t, d)
	return d, NewHandler(d, nil), s
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestAPIStatus(t *testing.T) {
	_, h, _ := apiSetup(t, simConfig(idleLines(20)))
	rec := apiDo(t, h, "GET", "/api/status", "")
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	m := decode[map[string]any](t, rec)
	for _, k := range []string{"connected", "spec_ok", "ready", "brew_open", "power", "firmware", "temp", "setpoint", "last_brew"} {
		if _, ok := m[k]; !ok {
			t.Errorf("status lacks key %q: %v", k, m)
		}
	}
	if len(m) != 9 {
		t.Errorf("status has %d keys, want 9: %v", len(m), m)
	}
	if m["power"] != "active" || m["connected"] != true || m["spec_ok"] != true {
		t.Errorf("status = %v", m)
	}
	if m["temp"] != 93.0 || m["last_brew"] != nil {
		t.Errorf("temp/last_brew = %v / %v", m["temp"], m["last_brew"])
	}
}

func TestAPIStatusUnknownIsNull(t *testing.T) {
	d := New(daemonConfig("127.0.0.1:1", openStore(t)))
	m := decode[map[string]any](t, apiDo(t, NewHandler(d, nil), "GET", "/api/status", ""))
	if m["temp"] != nil || m["setpoint"] != nil || m["connected"] != false {
		t.Errorf("status = %v", m)
	}
}

func TestAPIPower(t *testing.T) {
	_, h, s := apiSetup(t, powerSim(idleLines(20)))
	rec := apiDo(t, h, "POST", "/api/power", `{"state":"eco"}`)
	if rec.Code != 200 {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body)
	}
	if m := decode[map[string]any](t, rec); m["ok"] != true || m["state"] != "eco" {
		t.Errorf("body = %v", m)
	}
	if p := powerSent(s.Received()); len(p) != 1 || p[0] != "MCe" {
		t.Errorf("sent %q", p)
	}
}

// TestAPIPowerCrossOrigin: a browser page on another origin must not be
// able to switch the machine, even with a "simple" (non-preflighted) POST.
func TestAPIPowerCrossOrigin(t *testing.T) {
	_, h, s := apiSetup(t, powerSim(idleLines(20)))
	const body = `{"state":"standby"}`
	for name, hdr := range map[string]map[string]string{
		"sec-fetch-site cross-site": {"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"},
		"origin differs from host":  {"Content-Type": "application/json", "Origin": "https://evil.example"},
	} {
		if rec := apiDoHeaders(t, h, "POST", "/api/power", body, hdr); rec.Code != 403 {
			t.Errorf("%s: code = %d, want 403", name, rec.Code)
		}
	}
	for name, ct := range map[string]string{
		"text/plain": "text/plain", "form": "application/x-www-form-urlencoded", "none": "",
	} {
		hdr := map[string]string{}
		if ct != "" {
			hdr["Content-Type"] = ct
		}
		if rec := apiDoHeaders(t, h, "POST", "/api/power", body, hdr); rec.Code != 415 {
			t.Errorf("%s: code = %d, want 415", name, rec.Code)
		}
	}
	if p := powerSent(s.Received()); len(p) != 0 {
		t.Fatalf("sent %q on refused requests", p)
	}
	// Same origin, JSON with a charset: accepted.
	rec := apiDoHeaders(t, h, "POST", "/api/power", `{"state":"eco"}`, map[string]string{
		"Content-Type": "application/json; charset=utf-8", "Sec-Fetch-Site": "same-origin",
	})
	if rec.Code != 200 {
		t.Fatalf("same-origin: code = %d, body %s", rec.Code, rec.Body)
	}
	if p := powerSent(s.Received()); len(p) != 1 || p[0] != "MCe" {
		t.Errorf("sent %q", p)
	}
}

func TestAPIPowerBadRequests(t *testing.T) {
	_, h, s := apiSetup(t, powerSim(idleLines(20)))
	for name, body := range map[string]string{
		"not json":      `nope`,
		"empty":         ``,
		"unknown state": `{"state":"turbo"}`,
		"missing state": `{}`,
		"too large":     `{"state":"` + strings.Repeat("a", 4096) + `"}`,
	} {
		if rec := apiDo(t, h, "POST", "/api/power", body); rec.Code != 400 {
			t.Errorf("%s: code = %d, want 400", name, rec.Code)
		}
	}
	if p := powerSent(s.Received()); len(p) != 0 {
		t.Errorf("sent %q on bad requests", p)
	}
}

func TestAPIPowerErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func() sim.Config
		prep func(t *testing.T, s *sim.Server, d *Daemon)
		want int
	}{
		{
			"brew open", func() sim.Config { return powerSim(pumpLines(20)) },
			func(t *testing.T, _ *sim.Server, d *Daemon) {
				waitFor(t, "brew open", func() bool { return d.Status().BrewOpen })
			}, 409,
		},
		{"spec mismatch", func() sim.Config {
			cfg := powerSim(idleLines(20))
			cfg.Responses = map[string][]string{"MC?": {"XML=other"}}
			return cfg
		}, func(t *testing.T, s *sim.Server, d *Daemon) {
			waitFor(t, "connected and MC? sent", func() bool {
				return d.Status().Connected && count(s.Received(), "MC?") > 0
			})
		}, 409},
		{"no report yet", noReportSim, func(t *testing.T, _ *sim.Server, d *Daemon) { waitSpecOK(t, d) }, 503},
		{
			"disconnected", func() sim.Config { return powerSim(idleLines(20)) },
			func(t *testing.T, s *sim.Server, d *Daemon) {
				waitActive(t, d)
				_ = s.Stop()
				waitFor(t, "disconnected", func() bool { return !d.Status().Connected })
			}, 503,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startSim(t, tc.cfg())
			d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
			tc.prep(t, s, d)
			h := NewHandler(d, nil)
			if rec := apiDo(t, h, "POST", "/api/power", `{"state":"eco"}`); rec.Code != tc.want {
				t.Errorf("power code = %d, want %d (%s)", rec.Code, tc.want, rec.Body)
			}
			shrinkDumpTimings(t)
			wantBackup := tc.want
			if tc.name == "spec mismatch" {
				wantBackup = 503 // dumps on a mismatched connection are refused as disconnected
			}
			for _, p := range []string{"/api/backup/mcu", "/api/backup/mcm"} {
				if rec := apiDo(t, h, "GET", p, ""); rec.Code != wantBackup {
					t.Errorf("%s code = %d, want %d", p, rec.Code, wantBackup)
				}
			}
		})
	}
}

func TestAPIPowerVerifyFailed(t *testing.T) {
	shrink(t, &verifyTimeout, 300*time.Millisecond)
	_, h, _ := apiSetup(t, simConfig(idleLines(20))) // MCe changes nothing
	rec := apiDo(t, h, "POST", "/api/power", `{"state":"eco"}`)
	if rec.Code != 504 {
		t.Fatalf("code = %d, want 504", rec.Code)
	}
	if m := decode[map[string]any](t, rec); m["ok"] != false || m["state"] != "active" {
		t.Errorf("body = %v", m)
	}
}

func TestAPIMethodRestrictions(t *testing.T) {
	_, h, _ := apiSetup(t, simConfig(idleLines(20)))
	for _, tc := range [][2]string{
		{"GET", "/api/power"},
		{"POST", "/api/status"},
		{"POST", "/api/brews"},
		{"POST", "/api/backup/mcm"},
		{"POST", "/healthz"},
		{"POST", "/"},
	} {
		if rec := apiDo(t, h, tc[0], tc[1], ""); rec.Code != 405 {
			t.Errorf("%s %s = %d, want 405", tc[0], tc[1], rec.Code)
		}
	}
	if rec := apiDo(t, h, "GET", "/nope", ""); rec.Code != 404 {
		t.Errorf("GET /nope = %d, want 404", rec.Code)
	}
}

// seedBrews stores two finished brews; the second has a masked point.
func seedBrews(t *testing.T, d *Daemon) {
	t.Helper()
	ctx := context.Background()
	st := d.Store()
	start := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	for i := range 2 {
		at := start.Add(time.Duration(i) * time.Hour)
		id, err := st.BeginBrew(ctx, at)
		if err != nil {
			t.Fatal(err)
		}
		pts := []brew.Sample{{At: at, Temp: f64(93.5), Setpoint: f64(93), Pressure: f64(8.5), Flow: f64(1.5), Heat: f64(40)}}
		if i == 1 {
			pts = append(pts, brew.Sample{At: at.Add(250 * time.Millisecond), Temp: f64(93.2), Pressure: f64(9)})
		}
		if err := st.AddPoints(ctx, id, at, pts); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishBrew(ctx, id, brew.Summary{
			StartedAt: at, EndedAt: at.Add(28 * time.Second),
			Kind: brew.Shot, Duration: 28 * time.Second, PeakBar: 9.1, VolumeML: 36,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAPIBrews(t *testing.T) {
	d, h, _ := apiSetup(t, simConfig(idleLines(20)))
	seedBrews(t, d)

	rec := apiDo(t, h, "GET", "/api/brews", "")
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	bs := decode[[]map[string]any](t, rec)
	if len(bs) != 2 || bs[0]["id"].(float64) < bs[1]["id"].(float64) {
		t.Fatalf("brews = %v, want 2 newest first", bs)
	}
	b := bs[0]
	for _, k := range []string{"id", "started_at", "ended_at", "kind", "duration_s", "peak_bar", "volume_ml", "notes"} {
		if _, ok := b[k]; !ok {
			t.Errorf("brew lacks key %q", k)
		}
	}
	if len(b) != 8 || b["kind"] != "shot" || b["duration_s"] != 28.0 || b["peak_bar"] != 9.1 || b["volume_ml"] != 36.0 {
		t.Errorf("brew = %v", b)
	}
	if ts, err := time.Parse(time.RFC3339, b["started_at"].(string)); err != nil || ts.Hour() != 9 {
		t.Errorf("started_at = %v (%v)", b["started_at"], err)
	}
	if _, err := time.Parse(time.RFC3339, b["ended_at"].(string)); err != nil {
		t.Errorf("ended_at = %v", b["ended_at"])
	}

	if bs := decode[[]map[string]any](t, apiDo(t, h, "GET", "/api/brews?limit=1", "")); len(bs) != 1 {
		t.Errorf("limit=1 gave %d brews", len(bs))
	}
	if rec := apiDo(t, h, "GET", "/api/brews?limit=500", ""); rec.Code != 200 {
		t.Errorf("limit=500 = %d", rec.Code)
	}
	for _, q := range []string{"0", "x", "-1", "501", ""} {
		want := 400
		if q == "" {
			want = 200 // limit= present but empty: default
		}
		if rec := apiDo(t, h, "GET", "/api/brews?limit="+q, ""); rec.Code != want {
			t.Errorf("limit=%q = %d, want %d", q, rec.Code, want)
		}
	}
}

func TestAPIBrewsEmptyIsArray(t *testing.T) {
	_, h, _ := apiSetup(t, simConfig(idleLines(20)))
	if body := strings.TrimSpace(apiDo(t, h, "GET", "/api/brews", "").Body.String()); body != "[]" {
		t.Errorf("body = %q, want []", body)
	}
}

func TestAPIPoints(t *testing.T) {
	d, h, _ := apiSetup(t, simConfig(idleLines(20)))
	seedBrews(t, d)
	bs, _ := d.Store().Brews(context.Background(), 1)
	path := "/api/brews/" + itoa(bs[0].ID) + "/points"

	rec := apiDo(t, h, "GET", path, "")
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	pts := decode[[]map[string]any](t, rec)
	if len(pts) != 2 || len(pts[0]) != 6 || pts[0]["t_ms"] != 0.0 || pts[0]["heat"] != 40.0 || pts[1]["setpoint"] != nil {
		t.Errorf("points = %v", pts)
	}

	rec = apiDo(t, h, "GET", path+"?format=csv", "")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q", ct)
	}
	want := "t_ms,temp,setpoint,pressure,flow,heat\n0,93.5,93,8.5,1.5,40\n250,93.2,,9,,\n"
	if rec.Body.String() != want {
		t.Errorf("csv = %q, want %q", rec.Body.String(), want)
	}

	for _, p := range []string{"/api/brews/9999/points", "/api/brews/abc/points", "/api/brews/9999/points?format=csv"} {
		if rec := apiDo(t, h, "GET", p, ""); rec.Code != 404 {
			t.Errorf("%s = %d, want 404", p, rec.Code)
		}
	}
	if rec := apiDo(t, h, "GET", path+"?format=xml", ""); rec.Code != 400 {
		t.Errorf("format=xml = %d, want 400", rec.Code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestAPIBackup(t *testing.T) {
	shrinkDumpTimings(t)
	_, h, s := apiSetup(t, dumpSim(idleLines(20), fakeMCm))

	rec := apiDo(t, h, "GET", "/api/backup/mcm", "")
	if rec.Code != 200 {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body)
	}
	if got := rec.Body.String(); got != strings.Join(fakeMCm, "\n") {
		t.Errorf("body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("X-Medewi-Firmware") != "3.2" || rec.Header().Get("X-Medewi-Lines") != "3" {
		t.Errorf("headers = %v", rec.Header())
	}

	rec = apiDo(t, h, "GET", "/api/backup/mcu", "")
	if rec.Code != 200 || rec.Header().Get("X-Medewi-Lines") != "2" || !strings.HasPrefix(rec.Body.String(), "user setting 1 = 42\n") {
		t.Errorf("mcu = %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	assertOnlyAllowed(t, s, "MCm", "MCu")
}

func TestAPIBackupIncomplete(t *testing.T) {
	shrinkDumpTimings(t)
	_, h, _ := apiSetup(t, dumpSim(idleLines(20), fakeMCm[:2]))
	rec := apiDo(t, h, "GET", "/api/backup/mcm", "")
	if rec.Code != 502 {
		t.Errorf("code = %d, want 502", rec.Code)
	}
	if rec.Header().Get("X-Medewi-Lines") != "" || strings.Contains(rec.Body.String(), ":10000000") {
		t.Errorf("incomplete dump leaked: %v %q", rec.Header(), rec.Body)
	}
}

func TestAPIBackupBrewOpen(t *testing.T) {
	shrinkDumpTimings(t)
	s := startSim(t, dumpSim(pumpLines(30), fakeMCm))
	d, _ := runDaemon(t, daemonConfig(s.Addr().String(), openStore(t)))
	waitFor(t, "brew open", func() bool { return d.Status().BrewOpen })
	if rec := apiDo(t, NewHandler(d, nil), "GET", "/api/backup/mcm", ""); rec.Code != 409 {
		t.Errorf("code = %d, want 409", rec.Code)
	}
}

func TestAPIMiscRoutes(t *testing.T) {
	d := New(daemonConfig("127.0.0.1:1", openStore(t)))
	m := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "metrics-body") })
	h := NewHandler(d, m)

	if rec := apiDo(t, h, "GET", "/metrics", ""); rec.Code != 200 || rec.Body.String() != "metrics-body" {
		t.Errorf("/metrics = %d %q", rec.Code, rec.Body)
	}
	if rec := apiDo(t, NewHandler(d, nil), "GET", "/metrics", ""); rec.Code != 404 {
		t.Errorf("/metrics without handler = %d, want 404", rec.Code)
	}
	if rec := apiDo(t, h, "GET", "/healthz", ""); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Errorf("/healthz = %d %q", rec.Code, rec.Body)
	}
	rec := apiDo(t, h, "GET", "/", "")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("/ = %d %v", rec.Code, rec.Header())
	}
	body := rec.Body.String()
	for _, want := range []string{"/api/power", "/api/status", "viewport", "2000", `"Content-Type": "application/json"`} {
		if !strings.Contains(body, want) {
			t.Errorf("panel lacks %q", want)
		}
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Error("panel references an external resource")
	}
}
