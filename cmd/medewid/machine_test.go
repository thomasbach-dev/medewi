package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestMachineSmoke runs the daemon against the real machine and asserts it
// connects and the handshake passes. It sends no power command. Gated like
// the conformance machine tests: MEDEWI_MACHINE_ADDR and
// MEDEWI_MACHINE_CONFIRM=1, plus MEDEWI_XML_PATH for the vendor spec.
func TestMachineSmoke(t *testing.T) {
	machine := os.Getenv("MEDEWI_MACHINE_ADDR")
	if machine == "" || os.Getenv("MEDEWI_MACHINE_CONFIRM") != "1" {
		t.Skip("set MEDEWI_MACHINE_ADDR and MEDEWI_MACHINE_CONFIRM=1 to contact the machine")
	}
	xml := os.Getenv("MEDEWI_XML_PATH")
	if xml == "" {
		t.Skip("MEDEWI_XML_PATH not set")
	}
	clearEnv(t)
	r := start(t, "-machine", machine, "-xml", xml,
		"-db", filepath.Join(t.TempDir(), "m.db"),
		"-listen", "127.0.0.1:0", "-health-addr", freeAddr(t), "-insecure-http")
	waitStatus(t, "http://"+r.addr, http.DefaultClient, func(m map[string]any) bool {
		return m["connected"] == true && m["spec_ok"] == true
	})
}
