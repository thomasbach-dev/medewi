package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thomasbach-dev/medewi/fixture"
	"github.com/thomasbach-dev/medewi/leva"
	"github.com/thomasbach-dev/medewi/sim"
)

const syntheticDir = "../testdata/fixtures/synthetic"

func loadSynthetic(t *testing.T) (map[string][]string, []fixture.Event) {
	t.Helper()
	f, err := os.Open(syntheticDir + "/log.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	events, err := fixture.ReadLog(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(syntheticDir + "/responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string][]string
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	return resp, events
}

func TestSyntheticFixturesConform(t *testing.T) {
	resp, events := loadSynthetic(t)
	if len(events) < 3 {
		t.Fatalf("need at least 3 stream events, have %d", len(events))
	}
	spec := synthSpec(t)
	for i, ev := range events {
		if !bytes.HasPrefix(ev.Data, []byte("{")) || !bytes.HasSuffix(ev.Data, []byte("\r\n")) {
			t.Fatalf("event %d: want leading { and trailing CRLF, got %q", i, ev.Data)
		}
		line := bytes.TrimSuffix(ev.Data, []byte("\r\n"))
		payload, err := verifyAndDecode(spec, line)
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		_ = payload
	}

	s := sim.New(sim.Config{Banner: "$id: leva! (3.2)", Responses: resp, Stream: events, Speed: 20})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, s.Addr().String(), StreamState, Env{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Err != nil || r.Skipped {
			t.Errorf("%s: err=%v skipped=%v", r.Name, r.Err, r.Skipped)
		}
	}
}

func TestSyntheticMCmIsValidIntelHex(t *testing.T) {
	resp, _ := loadSynthetic(t)
	recs := resp["MCm"]
	if len(recs) == 0 {
		t.Fatal("no MCm records")
	}
	for i, r := range recs {
		if !strings.HasPrefix(r, ":") || len(r)%2 != 1 {
			t.Fatalf("record %d malformed: %q", i, r)
		}
		var sum byte
		for j := 1; j < len(r); j += 2 {
			v, err := strconv.ParseUint(r[j:j+2], 16, 8)
			if err != nil {
				t.Fatalf("record %d: %v", i, err)
			}
			sum += byte(v)
		}
		if sum != 0 {
			t.Errorf("record %d: checksum does not sum to 0 mod 256: %q", i, r)
		}
	}
}

func verifyAndDecode(spec *leva.RecordSpec, line []byte) ([]byte, error) {
	payload, err := leva.VerifyChecksum(line)
	if err != nil {
		return nil, err
	}
	_, err = spec.Decode(payload)
	return payload, err
}
