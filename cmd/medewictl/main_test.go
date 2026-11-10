package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// tlsArgs writes a CA file trusting srv plus a throwaway client keypair and
// returns the global flags pointing at them.
func tlsArgs(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ca := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "medewictl-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert := write("cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyf := write("key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}))
	return []string{"--url", srv.URL, "--cert", cert, "--key", keyf, "--ca", ca}
}

func exec(t *testing.T, srv *httptest.Server, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append(tlsArgs(t, srv), args...), &out, &errb)
	return code, out.String(), errb.String()
}

const (
	mcuBody = "MCu line 1\nMCu line 2\n"
	mcmBody = ":10000000AA\n:00000001FF\n"
)

func backupServer(mcmStatus int) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/backup/mcu":
			w.Header().Set("X-Medewi-Firmware", "3.2")
			w.Header().Set("X-Medewi-Lines", "2")
			_, _ = w.Write([]byte(mcuBody))
		case "/api/backup/mcm":
			if mcmStatus != http.StatusOK {
				http.Error(w, `{"ok":false,"error":"truncated"}`, mcmStatus)
				return
			}
			w.Header().Set("X-Medewi-Firmware", "3.2")
			w.Header().Set("X-Medewi-Lines", "2")
			_, _ = w.Write([]byte(mcmBody))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestBackupFileNames(t *testing.T) {
	srv := backupServer(http.StatusOK)
	defer srv.Close()
	out := t.TempDir()
	code, _, stderr := exec(t, srv, "backup", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	ents, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	mcu := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{6}Z_3\.2\.mcu\.txt$`)
	mcm := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{6}Z_3\.2\.mcm\.hex$`)
	var sawU, sawM bool
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(out, e.Name()))
		if info, err := e.Info(); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v (err %v), want 0600", e.Name(), info.Mode().Perm(), err)
		}
		switch {
		case mcu.MatchString(e.Name()):
			sawU = true
			if string(b) != mcuBody {
				t.Errorf("mcu content %q", b)
			}
			if !strings.Contains(stderr, filepath.Join(out, e.Name())) {
				t.Errorf("stderr lacks mcu path: %s", stderr)
			}
		case mcm.MatchString(e.Name()):
			sawM = true
			if string(b) != mcmBody {
				t.Errorf("mcm content %q", b)
			}
		default:
			t.Errorf("unexpected file %q", e.Name())
		}
	}
	if !sawU || !sawM {
		t.Fatalf("files missing: %v", ents)
	}
	if !strings.Contains(stderr, "2 lines") {
		t.Errorf("stderr lacks line count: %s", stderr)
	}
}

func TestBackupStdout(t *testing.T) {
	srv := backupServer(http.StatusOK)
	defer srv.Close()
	code, stdout, stderr := exec(t, srv, "backup", "--out", "-")
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	re := regexp.MustCompile(`(?s)^==> \d{4}-\d{2}-\d{2}T\d{6}Z_3\.2\.mcu\.txt <==\n` +
		regexp.QuoteMeta(mcuBody) + `==> \d{4}-\d{2}-\d{2}T\d{6}Z_3\.2\.mcm\.hex <==\n` +
		regexp.QuoteMeta(mcmBody) + `$`)
	if !re.MatchString(stdout) {
		t.Fatalf("stdout:\n%s", stdout)
	}
}

func TestBackupMCmFailureWritesNothing(t *testing.T) {
	srv := backupServer(http.StatusBadGateway)
	defer srv.Close()
	out := t.TempDir()
	code, _, _ := exec(t, srv, "backup", "--out", out)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".mcu.txt") {
			t.Errorf("leftover %q", e.Name())
		}
	}
}

func TestPowerFailureExit1(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("bad request %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		if b.String() != `{"state":"eco"}` {
			t.Errorf("body %q", b)
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"state":"active","error":"brew open"}`))
	}))
	defer srv.Close()
	code, stdout, stderr := exec(t, srv, "power", "eco")
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	if got := stdout + stderr; !strings.Contains(got, "brew open") || !strings.Contains(got, "active") {
		t.Errorf("output lacks error/state: %q", got)
	}
}

func TestPowerOK(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"state":"standby"}`))
	}))
	defer srv.Close()
	code, stdout, _ := exec(t, srv, "power", "standby")
	if code != 0 || !strings.Contains(stdout, "standby") {
		t.Fatalf("exit %d out %q", code, stdout)
	}
}

func TestPowerRejectsBadState(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"power", "turbo"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestMark(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		if r.URL.Path != "/api/mark" || r.Header.Get("Content-Type") != "application/json" || b.String() != `{"text":"pre-infusion over"}` {
			t.Errorf("bad request %s %q %q", r.URL.Path, r.Header.Get("Content-Type"), b)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	if code, _, stderr := exec(t, srv, "mark", "pre-infusion", "over"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestMarkFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"error":"no raw traffic log"}`))
	}))
	defer srv.Close()
	code, _, stderr := exec(t, srv, "mark", "x")
	if code != 1 || !strings.Contains(stderr, "no raw traffic log") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"mark"}, &out, &errb); code != 2 {
		t.Errorf("mark without text: exit %d, want 2", code)
	}
}

func TestStatusOutput(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connected":true,"spec_ok":true,"ready":false,"machine_ready":true,"brew_open":false,` +
			`"power":"eco","firmware":"3.2","temp":91.5,"setpoint":null,"last_brew":null,` +
			`"display":{"rows":["SOAK 1.0b       ","1: 91.0°   READY","              7%","0.1b  0s   0ml  "],"at":"2026-11-05T10:00:00Z"}}`))
	}))
	defer srv.Close()
	code, stdout, stderr := exec(t, srv, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"connected: true\n", "spec_ok: true\n", "ready: false\n", "machine_ready: true\n", "brew_open: false\n",
		"power: eco\n", "firmware: 3.2\n", "temp: 91.5\n", "setpoint: unknown\n", "last_brew: none\n",
		"display: |1: 91.0°   READY|\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
}

func TestInsecurePlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connected":true,"spec_ok":true,"ready":true,"brew_open":false,` +
			`"power":"active","firmware":"3.2","temp":91,"setpoint":91,"last_brew":null}`))
	}))
	defer srv.Close()
	t.Setenv("MEDEWID_CTL_CERT", "")
	t.Setenv("MEDEWID_CTL_KEY", "")
	t.Setenv("MEDEWID_CTL_CA", "")
	for _, args := range [][]string{
		{"--insecure", "--url", srv.URL, "status"},
		{"status", "--insecure", "--url", srv.URL},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 0 || !strings.Contains(out.String(), "power: active\n") {
			t.Errorf("%v: exit %d out %q err %q", args, code, out.String(), errb.String())
		}
	}
}

func TestInsecureNeedsHTTPURL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	// --insecure never means "skip certificate checks over https".
	var out, errb bytes.Buffer
	if code := run([]string{"--insecure", "--url", srv.URL, "status"}, &out, &errb); code != 2 {
		t.Fatalf("--insecure with https: exit %d, want 2", code)
	}
	// An http:// URL without --insecure must not silently send plaintext.
	plain := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("plaintext request reached the server")
	}))
	defer plain.Close()
	args := append(tlsArgs(t, srv), "--url", plain.URL, "status")
	if code := run(args, &out, &errb); code != 2 {
		t.Fatalf("http URL without --insecure: exit %d, want 2", code)
	}
}

func TestEnvAndMissingConfig(t *testing.T) {
	t.Setenv("MEDEWID_URL", "")
	t.Setenv("MEDEWID_CTL_CERT", "")
	t.Setenv("MEDEWID_CTL_KEY", "")
	t.Setenv("MEDEWID_CTL_CA", "")
	var out, errb bytes.Buffer
	if code := run([]string{"status"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

// clearEnv unsets the connection variables so only flags and defaults count.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"MEDEWID_URL", "MEDEWID_CTL_CERT", "MEDEWID_CTL_KEY", "MEDEWID_CTL_CA"} {
		t.Setenv(k, "")
	}
}

func TestDefaultCredentialFiles(t *testing.T) {
	clearEnv(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connected":true}`))
	}))
	defer srv.Close()
	args := tlsArgs(t, srv)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	dir := filepath.Join(cfgHome, "medewi")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for flag, name := range map[string]string{"--cert": "client.crt", "--key": "client.key", "--ca": "ca.crt"} {
		if err := os.Rename(argVal(t, args, flag), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--url", srv.URL, "status"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "connected: true") {
		t.Fatalf("stdout %q", out.String())
	}
}

func TestMissingDefaultCredentialNamesPath(t *testing.T) {
	clearEnv(t)
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	var out, errb bytes.Buffer
	if code := run([]string{"--url", "https://127.0.0.1:1", "status"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2; stderr %q", code, errb.String())
	}
	if want := filepath.Join(cfgHome, "medewi", "client.crt"); !strings.Contains(errb.String(), want) {
		t.Fatalf("stderr %q does not name %s", errb.String(), want)
	}
}

// argVal returns the value following flag in args.
func argVal(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	t.Fatalf("flag %s not in args", flag)
	return ""
}

func TestRelease(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want time.Duration
	}{
		{"explicit", []string{"release", "--until", "+30min"}, 30 * time.Minute},
		{"equals form", []string{"release", "--until=+30min"}, 30 * time.Minute},
		{"default", []string{"release"}, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotUntil time.Time
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Until string `json:"until"`
				}
				if r.URL.Path != "/api/release" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("bad request %s %s", r.Method, r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				var err error
				if gotUntil, err = time.Parse(time.RFC3339, req.Until); err != nil || !strings.HasSuffix(req.Until, "Z") {
					t.Errorf("until %q: want UTC RFC 3339 (%v)", req.Until, err)
				}
				_, _ = w.Write([]byte(`{"ok":true,"until":"` + req.Until + `","banner":true}`))
			}))
			defer srv.Close()
			before := time.Now()
			code, stdout, stderr := exec(t, srv, tc.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			if d := gotUntil.Sub(before.Add(tc.want)); d < -time.Second || d > 2*time.Second {
				t.Errorf("until %v, want about now+%v", gotUntil, tc.want)
			}
			if want := "released until " + gotUntil.Local().Format(time.DateTime) + " (local time)\n"; stdout != want {
				t.Errorf("stdout %q, want %q", stdout, want)
			}
			if strings.Contains(stderr, "warning") {
				t.Errorf("unexpected warning: %q", stderr)
			}
		})
	}
}

func TestReleaseBannerWarning(t *testing.T) {
	for banner, wantWarn := range map[string]bool{"false": true, "null": false} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true,"until":"2030-01-01T00:00:00Z","banner":` + banner + `}`))
		}))
		code, _, stderr := exec(t, srv, "release")
		srv.Close()
		if code != 0 || strings.Contains(stderr, "warning") != wantWarn {
			t.Errorf("banner %s: exit %d stderr %q, want warning=%t", banner, code, stderr, wantWarn)
		}
	}
}

func TestReleaseFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error":"store write failed"}`))
	}))
	defer srv.Close()
	code, _, stderr := exec(t, srv, "release")
	if code != 1 || !strings.Contains(stderr, "store write failed") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestReleaseBadUntilSendsNothing(t *testing.T) {
	called := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	for _, a := range []string{"30min", "+0s", "-5min", "+6000000h"} {
		if code, _, _ := exec(t, srv, "release", "--until", a); code != 2 {
			t.Errorf("--until %s: exit %d, want 2", a, code)
		}
	}
	if code, _, _ := exec(t, srv, "release", "extra"); code != 2 {
		t.Errorf("extra arg: exit %d, want 2", code)
	}
	if called {
		t.Error("request sent despite usage error")
	}
}

func TestReclaim(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		if r.URL.Path != "/api/reclaim" || r.Header.Get("Content-Type") != "application/json" || b.String() != `{}` {
			t.Errorf("bad request %s %q %q", r.URL.Path, r.Header.Get("Content-Type"), b)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	code, stdout, stderr := exec(t, srv, "reclaim")
	if code != 0 || stdout != "reclaimed\n" {
		t.Fatalf("exit %d out %q err %q", code, stdout, stderr)
	}
	if code, _, _ := exec(t, srv, "reclaim", "now"); code != 2 {
		t.Errorf("extra arg: exit %d, want 2", code)
	}
}

func TestReclaimFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error":"store write failed"}`))
	}))
	defer srv.Close()
	code, _, stderr := exec(t, srv, "reclaim")
	if code != 1 || !strings.Contains(stderr, "store write failed") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestStatusReleasedUntil(t *testing.T) {
	for _, tc := range []struct{ json, want string }{
		{`"2030-01-02T03:04:05Z"`, "released_until: " + time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC).Local().Format(time.DateTime) + "\n"},
		{`null`, "released_until: none\n"},
	} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"connected":false,"released_until":` + tc.json + `}`))
		}))
		code, stdout, stderr := exec(t, srv, "status")
		srv.Close()
		if code != 0 || !strings.Contains(stdout, tc.want) {
			t.Errorf("%s: exit %d out %q err %q", tc.json, code, stdout, stderr)
		}
	}
}

func TestSingleDashLongFlagRejected(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-ca", "x", "status"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "unknown shorthand flag") {
		t.Errorf("stderr %q does not say unknown shorthand flag", errb.String())
	}
}

func TestReleaseUntil(t *testing.T) {
	now := time.Date(2026, 11, 10, 14, 0, 0, 0, time.Local)
	ok := []struct {
		in   string
		want time.Time
	}{
		{"+1h", now.Add(time.Hour)},
		{"+1h30min", now.Add(90 * time.Minute)},
		{"2026-11-10 15:00", time.Date(2026, 11, 10, 15, 0, 0, 0, time.Local)},
	}
	for _, c := range ok {
		got, err := releaseUntil(c.in, now)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("releaseUntil(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"1h30min", "+1h30min"},
		{"", "--until"},
		{"-1h", "--until must be in the future"},
		{"2026-11-10 14:00", "--until must be in the future"},
		{"2026-11-10 13:00", "--until must be in the future"},
		{"bogus", "--until"},
	} {
		got, err := releaseUntil(c.in, now)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), " -until") {
			t.Errorf("releaseUntil(%q) = %v, %v; want error containing %q", c.in, got, err, c.want)
		}
	}
}
