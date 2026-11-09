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
type config struct {
	url, cert, key, ca string
	insecure           bool
}

func (c *config) register(fs *flag.FlagSet) {
	fs.StringVar(&c.url, "url", c.url, "medewid base URL (env MEDEWID_URL)")
	fs.StringVar(&c.cert, "cert", c.cert, "client certificate (env MEDEWID_CTL_CERT)")
	fs.StringVar(&c.key, "key", c.key, "client key (env MEDEWID_CTL_KEY)")
	fs.StringVar(&c.ca, "ca", c.ca, "CA file trusted for the server (env MEDEWID_CTL_CA)")
	fs.BoolVar(&c.insecure, "insecure", c.insecure, "plain HTTP without certificates, for a medewid run with -insecure-http (needs an http:// URL)")
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, `usage: medewictl [-url U] [-cert F] [-key F] [-ca F] [-insecure] <subcommand>

  status
  power active|eco|standby
  mark TEXT...              note TEXT in medewid's raw traffic log
  backup -out DIR|-
  release [-until +DURATION]   free the machine (default +1h; units h, min, s)
  reclaim                    take it back now

Defaults come from $MEDEWID_URL (e.g. https://brewhost:8443),
$MEDEWID_CTL_CERT, $MEDEWID_CTL_KEY and $MEDEWID_CTL_CA; unset cert,
key and ca fall back to client.crt, client.key and ca.crt in
$XDG_CONFIG_HOME/medewi (~/.config/medewi).
-insecure talks plain HTTP to a medewid started with -insecure-http
(e.g. -url http://127.0.0.1:8080); it never skips certificate checks.`)
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
	untilArg := "+1h"
	if sub == "release" {
		sfs.StringVar(&untilArg, "until", untilArg, "release for this long, +DURATION (units h, min, s)")
	}
	switch sub {
	case "status", "power", "mark", "backup", "release", "reclaim":
	default:
		_, _ = fmt.Fprintf(stderr, "medewictl: unknown subcommand %q\n", sub)
		usage(stderr)
		return 2
	}
	if err := sfs.Parse(rest); err != nil {
		return 2
	}
	rest = sfs.Args()

	var state, text string
	var until time.Time
	switch sub {
	case "release":
		if len(rest) != 0 {
			usage(stderr)
			return 2
		}
		var err error
		if until, err = parseUntil(untilArg, time.Now()); err != nil {
			_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
			return 2
		}
	case "status", "reclaim":
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
	case "mark":
		if text = strings.Join(rest, " "); text == "" {
			_, _ = fmt.Fprintln(stderr, "medewictl: mark needs a text")
			return 2
		}
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
	case "mark":
		return cmdMark(client, base, text, stderr)
	case "release":
		return cmdRelease(client, base, until, stdout, stderr)
	case "reclaim":
		return cmdReclaim(client, base, stdout, stderr)
	default:
		return cmdBackup(client, base, outDir, stdout, stderr)
	}
}

func (c *config) client() (*http.Client, error) {
	plain := strings.HasPrefix(c.url, "http://")
	switch {
	case c.insecure && !plain:
		return nil, errors.New("-insecure needs an http:// URL")
	case c.insecure:
		return &http.Client{Timeout: requestTimeout}, nil
	case plain:
		return nil, errors.New("an http:// URL needs -insecure (medewid serves mTLS unless run with -insecure-http)")
	}
	if c.url == "" {
		return nil, errors.New("url is required (-url or MEDEWID_URL)")
	}
	if err := c.defaultFiles(); err != nil {
		return nil, err
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

// defaultFiles fills an unset cert, key or ca from the user's config
// directory: client.crt, client.key and ca.crt in $XDG_CONFIG_HOME/medewi
// (~/.config/medewi).
func (c *config) defaultFiles() error {
	if c.cert != "" && c.key != "" && c.ca != "" {
		return nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("cert, key and ca unset and no default directory: %w", err)
	}
	dir := filepath.Join(base, "medewi")
	for _, f := range []struct {
		v    *string
		name string
	}{{&c.cert, "client.crt"}, {&c.key, "client.key"}, {&c.ca, "ca.crt"}} {
		if *f.v == "" {
			*f.v = filepath.Join(dir, f.name)
		}
	}
	return nil
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

// postJSON posts v as JSON.
func postJSON(client *http.Client, url string, v any) (*http.Response, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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
		Connected     bool       `json:"connected"`
		SpecOK        bool       `json:"spec_ok"`
		Ready         bool       `json:"ready"`
		MachineReady  bool       `json:"machine_ready"`
		BrewOpen      bool       `json:"brew_open"`
		ReleasedUntil *time.Time `json:"released_until"`
		Power         string     `json:"power"`
		Firmware      string     `json:"firmware"`
		Temp          *float64   `json:"temp"`
		Setpoint      *float64   `json:"setpoint"`
		Display       *struct {
			Rows []string `json:"rows"`
		} `json:"display"`
		LastBrew *struct {
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
	released := "none"
	if s.ReleasedUntil != nil {
		released = s.ReleasedUntil.Local().Format(time.DateTime)
	}
	last := "none"
	if b := s.LastBrew; b != nil {
		last = fmt.Sprintf("id=%d kind=%s started=%s duration_s=%g peak_bar=%g volume_ml=%g",
			b.ID, b.Kind, b.StartedAt, b.DurationS, b.PeakBar, b.VolumeML)
	}
	_, _ = fmt.Fprintf(stdout, "connected: %t\nspec_ok: %t\nready: %t\nmachine_ready: %t\nbrew_open: %t\npower: %s\nfirmware: %s\ntemp: %s\nsetpoint: %s\nreleased_until: %s\nlast_brew: %s\n",
		s.Connected, s.SpecOK, s.Ready, s.MachineReady, s.BrewOpen, str(s.Power), str(s.Firmware), num(s.Temp), num(s.Setpoint), released, last)
	if s.Display == nil {
		_, _ = fmt.Fprintln(stdout, "display: none")
	} else {
		for _, r := range s.Display.Rows {
			_, _ = fmt.Fprintf(stdout, "display: |%s|\n", r)
		}
	}
	return 0
}

func cmdMark(client *http.Client, base, text string, stderr io.Writer) int {
	resp, body, err := postJSON(client, base+"/api/mark", map[string]string{"text": text})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
		return 1
	}
	if resp.StatusCode != http.StatusOK {
		var r struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &r) != nil || r.Error == "" {
			r.Error = string(bytes.TrimSpace(body))
		}
		_, _ = fmt.Fprintf(stderr, "medewictl: mark failed (%s): %s\n", resp.Status, r.Error)
		return 1
	}
	return 0
}

func cmdPower(client *http.Client, base, state string, stdout, stderr io.Writer) int {
	resp, body, err := postJSON(client, base+"/api/power", map[string]string{"state": state})
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

func cmdRelease(client *http.Client, base string, until time.Time, stdout, stderr io.Writer) int {
	resp, body, err := postJSON(client, base+"/api/release", map[string]string{"until": until.UTC().Format(time.RFC3339)})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
		return 1
	}
	var r struct {
		OK     bool       `json:"ok"`
		Until  *time.Time `json:"until"`
		Banner *bool      `json:"banner"`
		Error  string     `json:"error"`
	}
	if jerr := json.Unmarshal(body, &r); jerr != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: release: %s: %s\n", resp.Status, bytes.TrimSpace(body))
		return 1
	}
	if resp.StatusCode != http.StatusOK || !r.OK || r.Until == nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: release failed (%s): %s\n", resp.Status, r.Error)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "released until %s (local time)\n", r.Until.Local().Format(time.DateTime))
	if r.Banner != nil && !*r.Banner {
		_, _ = fmt.Fprintln(stderr, "medewictl: warning: the idle banner did not return before the port was freed; the machine is released anyway")
	}
	return 0
}

func cmdReclaim(client *http.Client, base string, stdout, stderr io.Writer) int {
	resp, body, err := postJSON(client, base+"/api/reclaim", struct{}{})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: %v\n", err)
		return 1
	}
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal(body, &r); jerr != nil {
		_, _ = fmt.Fprintf(stderr, "medewictl: reclaim: %s: %s\n", resp.Status, bytes.TrimSpace(body))
		return 1
	}
	if resp.StatusCode != http.StatusOK || !r.OK {
		_, _ = fmt.Fprintf(stderr, "medewictl: reclaim failed (%s): %s\n", resp.Status, r.Error)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "reclaimed")
	return 0
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
