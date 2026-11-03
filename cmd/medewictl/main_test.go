package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
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
	return []string{"-url", srv.URL, "-cert", cert, "-key", keyf, "-ca", ca}
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
	code, _, stderr := exec(t, srv, "backup", "-out", out)
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
	code, stdout, stderr := exec(t, srv, "backup", "-out", "-")
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
	code, _, _ := exec(t, srv, "backup", "-out", out)
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

func TestStatusOutput(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"connected":true,"spec_ok":true,"ready":false,"brew_open":false,` +
			`"power":"eco","firmware":"3.2","temp":91.5,"setpoint":null,"last_brew":null}`))
	}))
	defer srv.Close()
	code, stdout, stderr := exec(t, srv, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"connected: true\n", "spec_ok: true\n", "ready: false\n", "brew_open: false\n",
		"power: eco\n", "firmware: 3.2\n", "temp: 91.5\n", "setpoint: unknown\n", "last_brew: none\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
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
