package main

import (
	"bufio"
	"bytes"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
)

// swallowingMachine sends idle banners and answers MC? only from the second
// try on, like the real machine on 2026-11-05: the first command after the
// banner wait went unanswered.
func swallowingMachine(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		lines := make(chan string)
		go func() {
			defer close(lines)
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				lines <- strings.TrimRight(sc.Text(), "\r")
			}
		}()
		tick := time.NewTicker(30 * time.Millisecond)
		defer tick.Stop()
		tries := 0
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				_, _ = c.Write([]byte("$id: leva! 3.2\r\n"))
			case l, ok := <-lines:
				if !ok {
					return
				}
				if l == "MC?" {
					if tries++; tries > 1 {
						_, _ = c.Write([]byte("XML=synthetic\r\n"))
					}
				}
			}
		}
	}()
	return ln.Addr().String()
}

func TestCaptureRetriesSwallowedMCq(t *testing.T) {
	old := mcqSpacing
	mcqSpacing = 200 * time.Millisecond
	t.Cleanup(func() { mcqSpacing = old })
	addr := swallowingMachine(t)
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"capture", "--addr", addr, "--out", dir},
		strings.NewReader("\n"), &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	m, err := fixture.LoadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m.XMLName != "synthetic" {
		t.Errorf("xml_name = %q, want the reply to the retried MC?", m.XMLName)
	}
}
