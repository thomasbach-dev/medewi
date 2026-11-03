package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/sim"
)

// running is a medewid run() started in a goroutine by start.
type running struct {
	addr   string
	done   chan struct{}
	exit   chan int
	once   sync.Once
	code   int
	stderr syncBuf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func start(t *testing.T, args ...string) *running {
	t.Helper()
	r := &running{done: make(chan struct{}), exit: make(chan int, 1)}
	ready := make(chan string, 1)
	go func() { r.exit <- run(args, &r.stderr, ready, r.done) }()
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

func (r *running) shutdown() int {
	r.once.Do(func() {
		close(r.done)
		r.code = <-r.exit
	})
	return r.code
}

// runExit runs medewid expecting it to exit by itself.
func runExit(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var buf syncBuf
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	c := make(chan int, 1)
	go func() { c <- run(args, &buf, nil, done) }()
	select {
	case code := <-c:
		return code, buf.String()
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit")
		return 0, ""
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// startSim runs a sim answering MC? with XML=synthetic and returns its address
// and the spec path (synthetic.xml, so SpecName is "synthetic").
func startSim(t *testing.T) (addr, xml string) {
	t.Helper()
	s := sim.New(sim.Config{
		Addr:      "127.0.0.1:0",
		Banner:    "$id: leva! (3.2)",
		Responses: map[string][]string{"MC?": {"XML=synthetic"}},
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s.Addr().String(), filepath.Join("..", "..", "testdata", "spec", "synthetic.xml")
}

func baseArgs(t *testing.T, machine, xml string) []string {
	t.Helper()
	return []string{
		"-machine", machine, "-xml", xml,
		"-db", filepath.Join(t.TempDir(), "m.db"),
		"-listen", "127.0.0.1:0",
		"-health-addr", freeAddr(t),
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MEDEWID_MACHINE_ADDR", "MEDEWI_MACHINE_ADDR", "MEDEWID_XML_PATH", "MEDEWI_XML_PATH",
		"MEDEWID_DB", "MEDEWID_LISTEN", "MEDEWID_TLS_CERT", "MEDEWID_TLS_KEY",
		"MEDEWID_TLS_CLIENT_CA", "MEDEWID_HEALTH_ADDR", "MEDEWID_EVENT_EXEC",
	} {
		t.Setenv(k, "")
	}
}

func TestRefusesWithoutTLS(t *testing.T) {
	clearEnv(t)
	code, stderr := runExit(t, baseArgs(t, "127.0.0.1:1", filepath.Join("..", "..", "testdata", "spec", "synthetic.xml"))...)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "refusing to serve without TLS (pass -insecure-http to override)") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestPartialTLSConfig(t *testing.T) {
	clearEnv(t)
	args := append(baseArgs(t, "127.0.0.1:1", filepath.Join("..", "..", "testdata", "spec", "synthetic.xml")), "-tls-cert", "x.pem")
	if code, stderr := runExit(t, args...); code != 2 {
		t.Fatalf("exit = %d, want 2; stderr %s", code, stderr)
	}
}

func TestMissingRequired(t *testing.T) {
	clearEnv(t)
	if code, _ := runExit(t, "-insecure-http"); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestHealthPlaintext(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	args := append(baseArgs(t, machine, xml), "-insecure-http")
	health := args[len(args)-2] // -health-addr value, set by baseArgs
	start(t, args...)

	resp, err := http.Get("http://" + health + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/healthz = %d", resp.StatusCode)
	}
	resp, err = http.Get("http://" + health + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("health listener served /api/status: %d", resp.StatusCode)
	}
}

func TestEnvFallback(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	t.Setenv("MEDEWI_MACHINE_ADDR", machine)
	t.Setenv("MEDEWI_XML_PATH", xml)
	args := []string{
		"-insecure-http", "-listen", "127.0.0.1:0", "-health-addr", freeAddr(t),
		"-db", filepath.Join(t.TempDir(), "m.db"),
	}
	r := start(t, args...)
	waitStatus(t, "http://"+r.addr, http.DefaultClient, func(m map[string]any) bool {
		return m["connected"] == true && m["spec_ok"] == true
	})
}

func waitStatus(t *testing.T, base string, c *http.Client, ok func(map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := c.Get(base + "/api/status")
		if err == nil {
			var m map[string]any
			err = json.NewDecoder(resp.Body).Decode(&m)
			_ = resp.Body.Close()
			if err == nil && ok(m) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("status condition not reached")
}

// pki generates a CA and returns it with a function issuing leaf certs.
type pki struct {
	caPEM []byte
	ca    *x509.Certificate
	key   *ecdsa.PrivateKey
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &pki{caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), ca: ca, key: key}
}

// issue returns PEM cert and key for a leaf valid for 127.0.0.1.
func (p *pki) issue(t *testing.T, serial int64, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMTLSRequiresClientCert(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	p := newPKI(t)
	dir := t.TempDir()
	srvCert, srvKey := p.issue(t, 2, x509.ExtKeyUsageServerAuth)
	cliCert, cliKey := p.issue(t, 3, x509.ExtKeyUsageClientAuth)
	args := append(baseArgs(t, machine, xml),
		"-tls-cert", writeFile(t, dir, "srv.pem", srvCert),
		"-tls-key", writeFile(t, dir, "srv.key", srvKey),
		"-tls-client-ca", writeFile(t, dir, "ca.pem", p.caPEM))
	r := start(t, args...)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(p.caPEM)
	noCert := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}}
	if resp, err := noCert.Get("https://" + r.addr + "/api/status"); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("request without client cert succeeded: %d", resp.StatusCode)
	}

	pair, err := tls.X509KeyPair(cliCert, cliKey)
	if err != nil {
		t.Fatal(err)
	}
	withCert := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}},
	}}
	resp, err := withCert.Get("https://" + r.addr + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("/api/status = %d", resp.StatusCode)
	}
	if code := r.shutdown(); code != 0 {
		t.Fatalf("exit = %d\n%s", code, r.stderr.String())
	}
}
