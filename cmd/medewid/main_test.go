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
		"-raw-dir", filepath.Join(t.TempDir(), "raw"),
		"-health-addr", freeAddr(t),
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

// metricsText fetches /metrics from a running insecure daemon.
func metricsText(t *testing.T, r *running) string {
	t.Helper()
	resp, err := http.Get("http://" + r.addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// blockedDir returns a path below a regular file, which no MkdirAll can create.
func blockedDir(t *testing.T) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(f, "raw")
}

func TestRawLogFlags(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	args := append(baseArgs(t, machine, xml), "-insecure-http")
	r := start(t, args...)
	waitStatus(t, "http://"+r.addr, http.DefaultClient, func(m map[string]any) bool {
		return m["connected"] == true && m["spec_ok"] == true
	})
	m := metricsText(t, r)
	for _, name := range []string{"medewi_rawlog_dropped_total", "medewi_rawlog_write_errors_total", "medewi_rawlog_redacted_lines_total"} {
		if !strings.Contains(m, name) {
			t.Errorf("metrics lack %s", name)
		}
	}
	if code := r.shutdown(); code != 0 {
		t.Fatalf("exit %d\n%s", code, r.stderr.String())
	}
	files, err := filepath.Glob(filepath.Join(argVal(t, args, "-raw-dir"), "*.jsonl.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no raw log file (err %v)\n%s", err, r.stderr.String())
	}
}

func TestRawLogEnv(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	dir := filepath.Join(t.TempDir(), "envraw")
	t.Setenv("MEDEWID_RAW_DIR", dir)
	t.Setenv("MEDEWID_RAW_RETENTION", "24h")
	t.Setenv("MEDEWID_RAW_MAX_MB", "1")
	args := []string{
		"-machine", machine, "-xml", xml, "-insecure-http", "-listen", "127.0.0.1:0",
		"-health-addr", freeAddr(t), "-db", filepath.Join(t.TempDir(), "m.db"),
	}
	r := start(t, args...)
	if code := r.shutdown(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEDEWID_RAW_RETENTION", "bogus")
	if code, _ := runExit(t, args...); code != 2 {
		t.Fatalf("bad env retention: exit %d, want 2", code)
	}
}

func TestRawLogDisabled(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	args := append(baseArgs(t, machine, xml), "-insecure-http", "-raw-retention", "0")
	r := start(t, args...)
	waitStatus(t, "http://"+r.addr, http.DefaultClient, func(m map[string]any) bool {
		return m["connected"] == true
	})
	if m := metricsText(t, r); strings.Contains(m, "medewi_rawlog_") {
		t.Error("rawlog metrics present while disabled")
	}
	r.shutdown()
	if es, _ := os.ReadDir(argVal(t, args, "-raw-dir")); len(es) != 0 {
		t.Errorf("raw dir not empty: %v", es)
	}
}

func TestRawLogExplicitDirUnusable(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	args := append(baseArgs(t, machine, xml), "-insecure-http", "-raw-dir", blockedDir(t))
	if code, se := runExit(t, args...); code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, se)
	}
}

func TestRawLogDefaultDirUnusableWarns(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	old := rawDefault
	rawDefault = blockedDir(t)
	t.Cleanup(func() { rawDefault = old })
	var args []string
	base := baseArgs(t, machine, xml)
	for i := 0; i < len(base); i++ {
		if base[i] == "-raw-dir" {
			i++ // skip its value
			continue
		}
		args = append(args, base[i])
	}
	args = append(args, "-insecure-http")
	r := start(t, args...)
	if code := r.shutdown(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(r.stderr.String(), "raw traffic log disabled") {
		t.Errorf("no warning in stderr:\n%s", r.stderr.String())
	}
}

func TestRawLogBadFlags(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	for _, extra := range [][]string{{"-raw-retention", "-1h"}, {"-raw-max-mb", "0"}, {"-raw-max-mb", "1048577"}, {"-raw-max-mb", "9223372036854775807"}, {"-raw-retention", "x"}} {
		args := append(append(baseArgs(t, machine, xml), "-insecure-http"), extra...)
		if code, se := runExit(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2\n%s", extra, code, se)
		}
	}
}

func TestDisplaySampling(t *testing.T) {
	clearEnv(t)
	s := sim.New(sim.Config{
		Addr:   "127.0.0.1:0",
		Banner: "$id: leva! (3.2)",
		Responses: map[string][]string{
			"MC?": {"XML=synthetic"},
			"MC@": {"<102SOAK 1.0b       1: 91.0\xdf   READY\x02             7% 0.1b\x05  0s   0ml>"},
		},
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	xml := filepath.Join("..", "..", "testdata", "spec", "synthetic.xml")
	r := start(t, append(baseArgs(t, s.Addr().String(), xml), "-insecure-http", "-display-every", "100ms", "-display-for", "20ms")...)
	waitStatus(t, "http://"+r.addr, http.DefaultClient, func(m map[string]any) bool {
		return m["display"] != nil
	})
	if code := r.shutdown(); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestDisplayFlagsValidated(t *testing.T) {
	clearEnv(t)
	machine, xml := startSim(t)
	for _, extra := range [][]string{
		{"-display-every", "-1s"},
		{"-display-for", "0s"},
		{"-display-every", "1s", "-display-for", "1s"},
		{"-display-every", "x"},
		{"-display-every", "30s", "-display-for", "3s"}, // a long sample stalls the report stream
	} {
		args := append(append(baseArgs(t, machine, xml), "-insecure-http"), extra...)
		if code, se := runExit(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2\n%s", extra, code, se)
		}
	}
	t.Setenv("MEDEWID_DISPLAY_EVERY", "bogus")
	if code, _ := runExit(t, append(baseArgs(t, machine, xml), "-insecure-http")...); code != 2 {
		t.Errorf("bad env: exit %d, want 2", code)
	}
	// 0 disables sampling, whatever -display-for says.
	t.Setenv("MEDEWID_DISPLAY_EVERY", "0")
	r := start(t, append(baseArgs(t, machine, xml), "-insecure-http")...)
	if code := r.shutdown(); code != 0 {
		t.Fatalf("display disabled: exit %d\n%s", code, r.stderr.String())
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"MEDEWID_MACHINE_ADDR", "MEDEWI_MACHINE_ADDR", "MEDEWID_XML_PATH", "MEDEWI_XML_PATH",
		"MEDEWID_DB", "MEDEWID_LISTEN", "MEDEWID_TLS_CERT", "MEDEWID_TLS_KEY",
		"MEDEWID_TLS_CLIENT_CA", "MEDEWID_TLS_METRICS_CA", "MEDEWID_HEALTH_ADDR", "MEDEWID_EVENT_EXEC",
		"MEDEWID_RAW_DIR", "MEDEWID_RAW_RETENTION", "MEDEWID_RAW_MAX_MB",
		"MEDEWID_DISPLAY_EVERY", "MEDEWID_DISPLAY_FOR",
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

// mtlsClient returns a client trusting serverCA that presents certPEM/keyPEM.
func mtlsClient(t *testing.T, serverCA, certPEM, keyPEM []byte) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(serverCA)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}},
	}}
}

func statusOf(t *testing.T, c *http.Client, method, url string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(`{"state":"eco"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// startMetricsCA starts medewid with the server certificate from admin and
// clientCA and metricsCA as -tls-client-ca and -tls-metrics-ca.
func startMetricsCA(t *testing.T, admin *pki, clientCA, metricsCA []byte) *running {
	t.Helper()
	machine, xml := startSim(t)
	dir := t.TempDir()
	srvCert, srvKey := admin.issue(t, 2, x509.ExtKeyUsageServerAuth)
	return start(t, append(baseArgs(t, machine, xml),
		"-tls-cert", writeFile(t, dir, "srv.pem", srvCert),
		"-tls-key", writeFile(t, dir, "srv.key", srvKey),
		"-tls-client-ca", writeFile(t, dir, "ca.pem", clientCA),
		"-tls-metrics-ca", writeFile(t, dir, "metrics-ca.pem", metricsCA))...)
}

func TestMetricsCAOnlyScrapes(t *testing.T) {
	clearEnv(t)
	admin, mon := newPKI(t), newPKI(t)
	r := startMetricsCA(t, admin, admin.caPEM, mon.caPEM)
	base := "https://" + r.addr

	scrapeCert, scrapeKey := mon.issue(t, 3, x509.ExtKeyUsageClientAuth)
	scraper := mtlsClient(t, admin.caPEM, scrapeCert, scrapeKey)
	if got := statusOf(t, scraper, "GET", base+"/metrics"); got != 200 {
		t.Fatalf("metrics cert: /metrics = %d, want 200", got)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/status"},
		{"POST", "/api/power"},
		{"POST", "/api/mark"},
		{"GET", "/api/brews"},
		{"GET", "/api/backup/mcu"},
		{"GET", "/api/backup/mcm"},
		{"GET", "/"},
		{"GET", "/no-such-path"},
	} {
		if got := statusOf(t, scraper, c.method, base+c.path); got != http.StatusForbidden {
			t.Errorf("metrics cert: %s %s = %d, want 403", c.method, c.path, got)
		}
	}

	adminCert, adminKey := admin.issue(t, 4, x509.ExtKeyUsageClientAuth)
	ctl := mtlsClient(t, admin.caPEM, adminCert, adminKey)
	for _, path := range []string{"/api/status", "/metrics"} {
		if got := statusOf(t, ctl, "GET", base+path); got != 200 {
			t.Errorf("admin cert: %s = %d, want 200", path, got)
		}
	}
	if code := r.shutdown(); code != 0 {
		t.Fatalf("exit = %d\n%s", code, r.stderr.String())
	}
}

// The same CA in both options keeps every certificate an admin.
func TestMetricsCASameAsClientCA(t *testing.T) {
	clearEnv(t)
	admin := newPKI(t)
	r := startMetricsCA(t, admin, admin.caPEM, admin.caPEM)
	cert, key := admin.issue(t, 3, x509.ExtKeyUsageClientAuth)
	if got := statusOf(t, mtlsClient(t, admin.caPEM, cert, key), "GET", "https://"+r.addr+"/api/status"); got != 200 {
		t.Fatalf("/api/status = %d, want 200", got)
	}
	if code := r.shutdown(); code != 0 {
		t.Fatalf("exit = %d\n%s", code, r.stderr.String())
	}
}

func TestMetricsCANeedsTLS(t *testing.T) {
	clearEnv(t)
	args := append(baseArgs(t, "127.0.0.1:1", filepath.Join("..", "..", "testdata", "spec", "synthetic.xml")),
		"-insecure-http", "-tls-metrics-ca", "x.pem")
	code, stderr := runExit(t, args...)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "-tls-metrics-ca needs the TLS settings") {
		t.Fatalf("stderr = %q", stderr)
	}
}
