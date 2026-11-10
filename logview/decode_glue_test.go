package logview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thomasbach-dev/medewi/fixture"
)

func noSecret(t *testing.T, es []Entry) {
	t.Helper()
	for _, e := range es {
		if bytes.Contains(e.Raw, []byte("secret")) || (e.Err != nil && strings.Contains(e.Err.Error(), "secret")) {
			t.Errorf("dump bytes leaked: %+v", e)
		}
	}
}

func hasKind(es []Entry, k Kind) bool {
	for _, e := range es {
		if e.Kind == k {
			return true
		}
	}
	return false
}

func gluedSource(prefix string, at int64) Source {
	return Source{Events: []fixture.Event{
		ev(0, fixture.TX, "MC "),
		ev(5, fixture.TX, "MCu\r\n"),
		ev(at, fixture.RX, prefix+"|0001 secret\r\n"),
	}}
}

func TestDecodeGluedDumpTailReport(t *testing.T) {
	es := Decode(gluedSource("{ * 93.5", 10), Options{Spec: testSpec(t)})
	noSecret(t, es)
	if !hasKind(es, KindRedacted) {
		t.Errorf("kinds = %v", kinds(es))
	}
}

func TestDecodeGluedDumpTailFrame(t *testing.T) {
	es := Decode(gluedSource("<112"+strings.Repeat("x", 20), 10), Options{Spec: testSpec(t)})
	noSecret(t, es)
	if !hasKind(es, KindRedacted) {
		t.Errorf("kinds = %v", kinds(es))
	}
}

func TestDecodeGluedLineOutsideQuiet(t *testing.T) {
	es := Decode(gluedSource("{ * 93.5", 70_000), Options{})
	wantKinds(t, es, KindCmd, KindCmd, KindReport)
}

func TestDecodeWholeRecordsInQuietShown(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.TX, "MCu\r\n"),
		ev(10, fixture.RX, string(testReport())+string(testFrame())+"\r\n"),
	}}
	es := Decode(src, Options{Spec: testSpec(t)})
	wantKinds(t, es, KindCmd, KindReport, KindFrame)
}
