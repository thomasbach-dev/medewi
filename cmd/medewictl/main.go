// Command medewictl is the command-line client for the medewid HTTP API.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const requestTimeout = 60 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// config holds the connection settings; flags override the environment.
type config struct{ url, cert, key, ca string }

func (c *config) register(fs *flag.FlagSet) {
	fs.StringVar(&c.url, "url", c.url, "medewid base URL (env MEDEWID_URL)")
	fs.StringVar(&c.cert, "cert", c.cert, "client certificate (env MEDEWID_CTL_CERT)")
	fs.StringVar(&c.key, "key", c.key, "client key (env MEDEWID_CTL_KEY)")
	fs.StringVar(&c.ca, "ca", c.ca, "CA file trusted for the server (env MEDEWID_CTL_CA)")
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `usage: medewictl [-url U] [-cert F] [-key F] [-ca F] <subcommand>

  status
  power active|eco|standby
  backup -out DIR|-

Defaults come from $MEDEWID_URL (e.g. https://brewhost:8443),
$MEDEWID_CTL_CERT, $MEDEWID_CTL_KEY and $MEDEWID_CTL_CA.`)
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg := &config{
		url:  os.Getenv("MEDEWID_URL"),
		cert: os.Getenv("MEDEWID_CTL_CERT"),
		key:  os.Getenv("MEDEWID_CTL_KEY"),
		ca:   os.Getenv("MEDEWID_CTL_CA"),
	}
	fs := flag.NewFlagSet("medewictl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr) }
	cfg.register(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		usage(stderr)
		return 2
	}
	sub, rest := rest[0], rest[1:]
	sfs := flag.NewFlagSet(sub, flag.ContinueOnError)
	sfs.SetOutput(stderr)
	cfg.register(sfs)
	var outDir string
	if sub == "backup" {
		sfs.StringVar(&outDir, "out", "", "output directory, or - for stdout")
	}
	switch sub {
	case "status", "power", "backup":
	default:
		_, _ = fmt.Fprintf(stderr, "medewictl: unknown subcommand %q\n", sub)
		usage(stderr)
		return 2
	}
	if err := sfs.Parse(rest); err != nil {
		return 2
	}
	rest = sfs.Args()

	var state string
	switch sub {
	case "status":
		if len(rest) != 0 {
			usage(stderr)
			return 2
		}
	case "power":
		if len(rest) != 1 || (rest[0] != "active" && rest[0] != "eco" && rest[0] != "standby") {
			_, _ = fmt.Fprintln(stderr, "medewictl: power needs one of active, eco, standby")
			return 2
		}
		state = rest[0]
	case "backup":
		if len(rest) != 0 || outDir == "" {
			_, _ = fmt.Fprintln(stderr, "medewictl: backup needs -out DIR (or -out -)")
			return 2
		}
	}

	client, err := cfg.client()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
		return 2
	}
	base := strings.TrimRight(cfg.url, "/")

	switch sub {
	case "status":
		return cmdStatus(client, base, stdout, stderr)
	case "power":
		return cmdPower(client, base, state, stdout, stderr)
	default:
		return cmdBackup(client, base, outDir, stdout, stderr)
	}
}

func (c *config) client() (*http.Client, error) {
	if c.url == "" || c.cert == "" || c.key == "" || c.ca == "" {
		return nil, errors.New("url, cert, key and ca are required (flags or MEDEWID_URL, MEDEWID_CTL_CERT, MEDEWID_CTL_KEY, MEDEWID_CTL_CA)")
	}
	pair, err := tls.LoadX509KeyPair(c.cert, c.key)
	if err != nil {
		return nil, fmt.Errorf("client certificate: %w", err)
	}
	pemBytes, err := os.ReadFile(c.ca)
	if err != nil {
		return nil, fmt.Errorf("ca file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("ca file %s: no certificates found", c.ca)
	}
	return &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{pair},
			RootCAs:      pool,
		}},
	}, nil
}

// do performs a request and returns the status and fully read body.
func do(client *http.Client, req *http.Request) (*http.Response, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

func get(client *http.Client, url string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	return do(client, req)
}

func cmdStatus(client *http.Client, base string, stdout, stderr io.Writer) int {
	resp, body, err := get(client, base+"/api/status")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
		return 1
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "medewictl: status: %s: %s\n", resp.Status, bytes.TrimSpace(body))
		return 1
	}
	var s struct {
		Connected bool     `json:"connected"`
		SpecOK    bool     `json:"spec_ok"`
		Ready     bool     `json:"ready"`
		BrewOpen  bool     `json:"brew_open"`
		Power     string   `json:"power"`
		Firmware  string   `json:"firmware"`
		Temp      *float64 `json:"temp"`
		Setpoint  *float64 `json:"setpoint"`
		LastBrew  *struct {
			ID        int64   `json:"id"`
			StartedAt string  `json:"started_at"`
			Kind      string  `json:"kind"`
			DurationS float64 `json:"duration_s"`
			PeakBar   float64 `json:"peak_bar"`
			VolumeML  float64 `json:"volume_ml"`
		} `json:"last_brew"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: decode status: %v\n", err)
		return 1
	}
	num := func(f *float64) string {
		if f == nil {
			return "unknown"
		}
		return fmt.Sprintf("%g", *f)
	}
	str := func(v string) string {
		if v == "" {
			return "unknown"
		}
		return v
	}
	last := "none"
	if b := s.LastBrew; b != nil {
		last = fmt.Sprintf("id=%d kind=%s started=%s duration_s=%g peak_bar=%g volume_ml=%g",
			b.ID, b.Kind, b.StartedAt, b.DurationS, b.PeakBar, b.VolumeML)
	}
	_, _ = fmt.Fprintf(stdout, "connected: %t\nspec_ok: %t\nready: %t\nbrew_open: %t\npower: %s\nfirmware: %s\ntemp: %s\nsetpoint: %s\nlast_brew: %s\n",
		s.Connected, s.SpecOK, s.Ready, s.BrewOpen, str(s.Power), str(s.Firmware), num(s.Temp), num(s.Setpoint), last)
	return 0
}

func cmdPower(client *http.Client, base, state string, stdout, stderr io.Writer) int {
	req, err := http.NewRequest(http.MethodPost, base+"/api/power", strings.NewReader(fmt.Sprintf(`{"state":%q}`, state)))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, body, err := do(client, req)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
		return 1
	}
	var r struct {
		OK    bool   `json:"ok"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal(body, &r); jerr != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: power: %s: %s\n", resp.Status, bytes.TrimSpace(body))
		return 1
	}
	if resp.StatusCode == http.StatusOK && r.OK {
		_, _ = fmt.Fprintf(stdout, "power: %s\n", r.State)
		return 0
	}
	msg := fmt.Sprintf("medewictl: power %s failed (%s)", state, resp.Status)
	if r.State != "" {
		msg += ", state: " + r.State
	}
	if r.Error != "" {
		msg += ", error: " + r.Error
	}
	_, _ = fmt.Fprintln(stderr, msg)
	return 1
}

// backupKinds lists the dumps in fetch order.
var backupKinds = []struct{ name, endpoint, ext string }{
	{"mcu", "/api/backup/mcu", "mcu.txt"},
	{"mcm", "/api/backup/mcm", "mcm.hex"},
}

func cmdBackup(client *http.Client, base, outDir string, stdout, stderr io.Writer) int {
	for _, k := range backupKinds {
		resp, body, err := get(client, base+k.endpoint)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "medewictl: %s: %v\n", k.name, err)
			return 1
		}
		if resp.StatusCode != http.StatusOK {
			_, _ = fmt.Fprintf(stderr, "medewictl: %s: %s: %s\n", k.name, resp.Status, bytes.TrimSpace(body))
			return 1
		}
		fw := safeName(resp.Header.Get("X-Medewi-Firmware"))
		lines := resp.Header.Get("X-Medewi-Lines")
		name := fmt.Sprintf("%s_%s.%s", time.Now().UTC().Format("2006-01-02T150405Z"), fw, k.ext)
		if outDir == "-" {
			if _, err := fmt.Fprintf(stdout, "==> %s <==\n%s", name, body); err != nil {
				_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
				return 1
			}
			continue
		}
		path := filepath.Join(outDir, name)
		if err := writeAtomic(path, body); err != nil {
			_, _ = fmt.Fprintf(stderr, "medewictl: %s: %v\n", k.name, err)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "%s (%s lines)\n", path, lines)
	}
	return 0
}

// safeName keeps a header value usable as a file-name component.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		}
		return '_'
	}, s)
	if s == "" || strings.Trim(s, ".") == "" {
		return "unknown"
	}
	return s
}

// writeAtomic writes data to path via a temp file in the same directory and a
// rename; the temp file is removed on any failure.
func writeAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".medewictl-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
