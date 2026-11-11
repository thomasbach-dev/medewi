package logview

import (
	"bytes"
	"fmt"
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

func hasRedacted(es []Entry) bool {
	for _, e := range es {
		if e.Kind == KindRedacted {
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
	es := Decode(gluedSource("{ * 93.5", 10), Options{Catalog: testCatalog(t)}).Entries
	noSecret(t, es)
	if !hasRedacted(es) {
		t.Errorf("kinds = %v", kinds(es))
	}
}

func TestDecodeGluedDumpTailFrame(t *testing.T) {
	es := Decode(gluedSource("<112"+strings.Repeat("x", 20), 10), Options{Catalog: testCatalog(t)}).Entries
	noSecret(t, es)
	if !hasRedacted(es) {
		t.Errorf("kinds = %v", kinds(es))
	}
}

func TestDecodeGluedLineOutsideQuiet(t *testing.T) {
	es := Decode(gluedSource("{ * 93.5", 70_000), Options{}).Entries
	wantKinds(t, es, KindCmd, KindCmd, KindReport)
}

func TestDecodeWholeRecordsInQuietShown(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.TX, "MCu\r\n"),
		ev(10, fixture.RX, string(sumReport())+string(testFrame())+"\r\n"),
	}}
	es := Decode(src, Options{Catalog: testCatalog(t)}).Entries
	wantKinds(t, es, KindCmd, KindReport, KindFrame)
}

// sumReport is testReport with the machine's trailing "~ HH}" checksum:
// HH is the two's complement of the sum of bytes 0–283.
func sumReport() []byte {
	b := testReport()
	var s byte
	for _, c := range b[:284] {
		s += c
	}
	copy(b[284:], fmt.Sprintf("~ %02X}", -s))
	return b
}

func TestDecodeQuietProbes(t *testing.T) {
	for name, line := range map[string]string{
		"banner prefix":          "$id: leva! (3.2)|0001 secret",
		"notice shape":           "[READY]|secret",
		"report then banner+":    string(sumReport()) + "$id: |0001 secret",
		"glued tail ending in }": "{ * 93.5|0001 secret}",
	} {
		t.Run(name, func(t *testing.T) {
			src := Source{Events: []fixture.Event{
				ev(0, fixture.TX, "MCu\r\n"),
				ev(5, fixture.RX, line+"\r\n"),
			}}
			es := Decode(src, Options{Catalog: testCatalog(t)}).Entries
			noSecret(t, es)
			if !hasRedacted(es) {
				t.Errorf("kinds = %v", kinds(es))
			}
		})
	}
}

func TestDecodeQuietPartialTail(t *testing.T) {
	for _, tail := range []string{"{ * 93.5|0001 secret", "$id: leva! (3.2)|0001 secret"} {
		src := Source{Events: []fixture.Event{
			ev(0, fixture.TX, "MCu\r\n"),
			ev(5, fixture.RX, tail),
		}}
		es := Decode(src, Options{Catalog: testCatalog(t)}).Entries
		noSecret(t, es)
		if !hasRedacted(es) {
			t.Errorf("%q: kinds = %v", tail, kinds(es))
		}
	}
}

func TestDecodeQuietWholeReportAndBannerShown(t *testing.T) {
	src := Source{Events: []fixture.Event{
		ev(0, fixture.TX, "MCu\r\n"),
		ev(5, fixture.RX, string(sumReport())+"$id: leva! (3.2)\r\n"),
		ev(9, fixture.RX, "$id: leva! (3.2)\r\n"),
	}}
	es := Decode(src, Options{Catalog: testCatalog(t)}).Entries
	wantKinds(t, es, KindCmd, KindReport, KindBanner, KindBanner)
}
