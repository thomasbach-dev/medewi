package main

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/backup/backuptest"
	"github.com/thomasbach-dev/medewi/leva"
)

// dumpMachine is a fake machine for capture --commands. It sends idle
// banners until the first command, then answers the whitelist; the MCu
// reply stops after half its lines for pause, as the real dump did on
// 2026-11-21 (524 ms after ~560 lines). mcu and mcm are the dump replies.
type dumpMachine struct {
	addr string

	mu       sync.Mutex
	got      []string // command lines received, in order
	collided []string // commands received while a reply was still being sent
}

func (m *dumpMachine) received() (got, collided []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.got), slices.Clone(m.collided)
}

func startDumpMachine(t *testing.T, pause time.Duration, mcu, mcm []string) *dumpMachine {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := &dumpMachine{addr: ln.Addr().String()}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var busy sync.Mutex // held while a reply is being written
		replying := false
		lines := make(chan string)
		go func() {
			defer close(lines)
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				l := strings.TrimRight(sc.Text(), "\r")
				m.mu.Lock()
				m.got = append(m.got, l)
				busy.Lock()
				if replying {
					m.collided = append(m.collided, l)
				}
				busy.Unlock()
				m.mu.Unlock()
				lines <- l
			}
		}()
		write := func(ls []string) {
			for _, l := range ls {
				_, _ = c.Write([]byte(l + "\r\n"))
			}
		}
		reply := func(parts ...func()) {
			busy.Lock()
			replying = true
			busy.Unlock()
			for _, p := range parts {
				p()
			}
			busy.Lock()
			replying = false
			busy.Unlock()
		}
		tick := time.NewTicker(30 * time.Millisecond)
		defer tick.Stop()
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
				tick.Stop() // no banners once commands flow
				switch l {
				case "MC?":
					write([]string{"XML=synthetic"})
				case "MCu":
					half := len(mcu) / 2
					reply(
						func() { write(mcu[:half]) },
						func() {
							select {
							case <-done:
							case <-time.After(pause):
							}
						},
						func() { write(mcu[half:]) },
					)
				case "MCm":
					write(mcm)
				default:
					write([]string{l + " reply"})
				}
			}
		}
	}()
	return m
}

func TestCaptureWaitsForPausingMCuDump(t *testing.T) {
	old := cmdWait
	cmdWait = 10 * time.Millisecond
	oldGap := dumpIdleGap
	dumpIdleGap = leva.DumpIdleGap
	t.Cleanup(func() { cmdWait, dumpIdleGap = old, oldGap })
	// Longer than leva's default idle gap (300 ms), shorter than the dump
	// idle gap.
	m := startDumpMachine(t, 700*time.Millisecond, backuptest.ValidMCu(), backuptest.ValidMCm())
	var out, errb bytes.Buffer
	code := run([]string{"capture", "--commands", "--addr", m.addr, "--out", t.TempDir()}, strings.NewReader(""), &out, &errb)
	got, collided := m.received()
	if len(collided) > 0 {
		t.Errorf("sent %q while the MCu dump was still running", collided)
	}
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if want := safeCommands; !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
}

func TestCaptureStopsAfterIncompleteDump(t *testing.T) {
	old := cmdWait
	cmdWait = 10 * time.Millisecond
	t.Cleanup(func() { cmdWait = old })
	mcu, mcm := backuptest.ValidMCu(), backuptest.ValidMCm()
	for name, tc := range map[string]struct {
		mcu, mcm []string
		want     []string // commands the machine must receive, in order
	}{
		"MCu without Cancel":   {mcu[:len(mcu)-1], mcm, []string{"MCh", "MC?", "MCu"}},
		"MCm without EOF":      {mcu, mcm[:len(mcm)-1], []string{"MCh", "MC?", "MCu", "MCm"}},
		"MCm missing a record": {mcu, []string{mcm[1], mcm[2]}, []string{"MCh", "MC?", "MCu", "MCm"}},
	} {
		t.Run(name, func(t *testing.T) {
			m := startDumpMachine(t, 0, tc.mcu, tc.mcm)
			dir := t.TempDir()
			var out, errb bytes.Buffer
			code := run([]string{"capture", "--commands", "--addr", m.addr, "--out", dir}, strings.NewReader(""), &out, &errb)
			if code != 1 {
				t.Fatalf("exit %d, want 1; stderr %s", code, errb.String())
			}
			last := tc.want[len(tc.want)-1]
			if !strings.Contains(errb.String(), last+": incomplete dump") {
				t.Errorf("stderr %q does not name the incomplete %s dump", errb.String(), last)
			}
			// The partial log is kept, under its partial name.
			if _, err := os.Stat(filepath.Join(dir, "log.partial.jsonl")); err != nil {
				t.Error(err)
			}
			got, _ := m.received()
			if !slices.Equal(got, tc.want) {
				t.Errorf("commands = %q, want %q (nothing after the incomplete dump)", got, tc.want)
			}
		})
	}
}
