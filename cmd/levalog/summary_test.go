package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummarySessions(t *testing.T) {
	noEnv(t)
	inCET(t)
	dir := threeSessionDir(t)
	args := []string{"summary", "--dir", dir, "-s", "2026-11-14 08:58:35", "-u", "2026-11-14 08:59:00"}
	code, out, _ := runCmd(t, args...)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, w := range []string{
		"sessions:\n",
		"  starts before 2026-11-14 07:05:52.646 +01:00 (first file not in input); firmware unknown; xml " + xmlName + "; covers [2026-11-14 08:58:35.000, 2026-11-14 08:58:40.000)\n",
		"  2026-11-14 08:58:40.000 +01:00; first file 20261114T075840.000Z.jsonl.gz; firmware $id: leva! (3.3); xml unknown; covers [2026-11-14 08:58:40.000, 2026-11-14 08:58:50.000)\n",
		"  2026-11-14 08:58:50.000 +01:00; first file 20261114T075850.000Z.jsonl.gz; firmware $id: leva! (3.4); xml unknown; covers [2026-11-14 08:58:50.000, 2026-11-14 08:59:00.000)\n",
		"firmware (in this file):",
		"xml (in this file):",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "\nfirmware:") || strings.Contains(out, "\nxml:") {
		t.Errorf("old per-file labels remain:\n%s", out)
	}

	code, out, _ = runCmd(t, append(args, "--format", "jsonl")...)
	if code != 0 {
		t.Fatalf("jsonl exit %d", code)
	}
	var o struct {
		Sessions []map[string]any `json:"sessions"`
	}
	line, _, _ := strings.Cut(out, "\n")
	if err := json.Unmarshal([]byte(line), &o); err != nil || len(o.Sessions) != 3 {
		t.Fatalf("jsonl sessions: %v\n%s", err, out)
	}
	if s := o.Sessions[0]; s["start"] != nil || s["first_file_known"] != false ||
		s["firmware"] != nil || s["xml"] != xmlName || s["since"] != "2026-11-14T07:58:35.000Z" || s["until"] != "2026-11-14T07:58:40.000Z" {
		t.Errorf("session 1 = %v", s)
	}
}
