// Command medewid is the always-on medewi daemon: it owns the connection to
// the machine, records brews, and serves the HTTP API, the ops panel and
// Prometheus metrics over mutual TLS, plus a plaintext /healthz listener.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/thomasbach-dev/medewi/brew"
	"github.com/thomasbach-dev/medewi/daemon"
	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/store"
	"github.com/thomasbach-dev/medewi/watch"
)

const (
	defaultDB     = "/var/lib/medewid/medewi.db"
	defaultListen = ":8443"
	defaultHealth = "127.0.0.1:9114"

	shutdownTimeout = 5 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(os.Args[1:], os.Stderr, nil, ctx.Done())
	stop()
	os.Exit(code)
}

// firstEnv returns the first non-empty environment variable among names.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// envOr returns the value of env name, or def if it is unset or empty.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// run parses args, starts the daemon and its listeners and serves until done
// is closed, then shuts down and returns the exit code (0 ok, 1 runtime
// error, 2 usage or configuration error). ready, if non-nil, receives the
// main listener's bound address once it serves; it must be buffered or read
// promptly. done must be non-nil. Flags override environment variables.
func run(args []string, stderr io.Writer, ready chan<- string, done <-chan struct{}) int {
	fset := flag.NewFlagSet("medewid", flag.ContinueOnError)
	fset.SetOutput(stderr)
	machine := fset.String("machine", firstEnv("MEDEWID_MACHINE_ADDR", "MEDEWI_MACHINE_ADDR"), "machine command address host:port (env MEDEWID_MACHINE_ADDR, MEDEWI_MACHINE_ADDR)")
	xmlPath := fset.String("xml", firstEnv("MEDEWID_XML_PATH", "MEDEWI_XML_PATH"), "vendor status-monitor XML path (env MEDEWID_XML_PATH, MEDEWI_XML_PATH)")
	dbPath := fset.String("db", envOr("MEDEWID_DB", defaultDB), "SQLite database path (env MEDEWID_DB)")
	listen := fset.String("listen", envOr("MEDEWID_LISTEN", defaultListen), "API listen address (env MEDEWID_LISTEN)")
	certFile := fset.String("tls-cert", os.Getenv("MEDEWID_TLS_CERT"), "server certificate PEM (env MEDEWID_TLS_CERT)")
	keyFile := fset.String("tls-key", os.Getenv("MEDEWID_TLS_KEY"), "server key PEM (env MEDEWID_TLS_KEY)")
	caFile := fset.String("tls-client-ca", os.Getenv("MEDEWID_TLS_CLIENT_CA"), "client CA PEM (env MEDEWID_TLS_CLIENT_CA)")
	health := fset.String("health-addr", envOr("MEDEWID_HEALTH_ADDR", defaultHealth), "plaintext /healthz listen address (env MEDEWID_HEALTH_ADDR)")
	eventExec := fset.String("event-exec", os.Getenv("MEDEWID_EVENT_EXEC"), "shell command run per detector event (env MEDEWID_EVENT_EXEC)")
	insecure := fset.Bool("insecure-http", false, "serve the API without TLS")
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	exit := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "medewid: "+format+"\n", a...)
		return code
	}
	if *machine == "" {
		return exit(2, "-machine (or MEDEWID_MACHINE_ADDR / MEDEWI_MACHINE_ADDR) is required")
	}
	if *xmlPath == "" {
		return exit(2, "-xml (or MEDEWID_XML_PATH / MEDEWI_XML_PATH) is required")
	}
	nTLS := 0
	for _, s := range []string{*certFile, *keyFile, *caFile} {
		if s != "" {
			nTLS++
		}
	}
	switch {
	case nTLS != 0 && nTLS != 3:
		return exit(2, "-tls-cert, -tls-key and -tls-client-ca must be set together")
	case nTLS == 0 && !*insecure:
		return exit(2, "refusing to serve without TLS (pass -insecure-http to override)")
	case nTLS == 3 && *insecure:
		return exit(2, "-insecure-http conflicts with the TLS settings")
	}

	var tlsCfg *tls.Config
	if nTLS == 3 {
		var err error
		if tlsCfg, err = loadTLS(*certFile, *keyFile, *caFile); err != nil {
			return exit(1, "%v", err)
		}
	}
	spec, err := leva.LoadSpecFile(*xmlPath)
	if err != nil {
		return exit(1, "load spec: %v", err)
	}
	base := filepath.Base(*xmlPath)
	specName := strings.TrimSuffix(base, filepath.Ext(base))

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	slog.SetDefault(logger)

	st, err := store.Open(*dbPath)
	if err != nil {
		return exit(1, "open store: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			_, _ = fmt.Fprintf(stderr, "medewid: close store: %v\n", err)
		}
	}()

	mainLn, err := net.Listen("tcp", *listen)
	if err != nil {
		return exit(1, "listen: %v", err)
	}
	healthLn, err := net.Listen("tcp", *health)
	if err != nil {
		_ = mainLn.Close()
		return exit(1, "health listen: %v", err)
	}

	reg := prometheus.NewRegistry()
	d := daemon.New(daemon.Config{
		Addr:      *machine,
		Spec:      spec,
		SpecName:  specName,
		Store:     st,
		Brew:      brew.DefaultConfig(),
		Watch:     watch.DefaultConfig(),
		EventExec: *eventExec,
		Registry:  reg,
	})

	mainSrv := &http.Server{
		Handler:           daemon.NewHandler(d, promhttp.HandlerFor(reg, promhttp.HandlerOpts{})),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
	}
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	healthSrv := &http.Server{Handler: healthMux, ReadHeaderTimeout: 10 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	runErr := make(chan error, 1)
	serveErr := make(chan error, 2)
	wg.Go(func() { runErr <- d.Run(ctx) })
	wg.Go(func() {
		var err error
		if tlsCfg != nil {
			err = mainSrv.ServeTLS(mainLn, "", "")
		} else {
			err = mainSrv.Serve(mainLn)
		}
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("serve: %w", err)
		}
	})
	wg.Go(func() {
		if err := healthSrv.Serve(healthLn); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("health serve: %w", err)
		}
	})
	logger.Info("medewid serving", "addr", mainLn.Addr().String(), "health", healthLn.Addr().String(), "tls", tlsCfg != nil, "spec", specName)
	if ready != nil {
		ready <- mainLn.Addr().String()
	}

	code := 0
	runDone := false
	select {
	case <-done:
	case err := <-serveErr:
		code = exit(1, "%v", err)
	case err := <-runErr:
		runDone = true
		code = exit(1, "daemon: %v", err)
	}

	// Drain HTTP first, then stop the daemon; the deferred store close runs
	// last.
	sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer scancel()
	for _, s := range []*http.Server{mainSrv, healthSrv} {
		if err := s.Shutdown(sctx); err != nil {
			_ = s.Close()
		}
	}
	cancel()
	if !runDone {
		if err := <-runErr; err != nil && code == 0 {
			code = exit(1, "daemon: %v", err)
		}
	}
	wg.Wait()
	return code
}

// loadTLS builds the server config: TLS 1.3 and a verified client certificate.
func loadTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("client CA %s: no certificates found", caFile)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
}
