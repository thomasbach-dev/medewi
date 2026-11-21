package rawlog

import (
	"bytes"
	"strconv"

	"github.com/thomasbach-dev/medewi/leva"
)

// The strict shapes below decide what may be kept (or, in logview, shown)
// inside the dump quiet period, where a glued dump tail must never pass for
// a record: a stream switch can glue a cut-off report or frame onto the
// next line, and right after an MCu/MCm that line may be a dump reply.

// IsWholeLine reports whether every piece of line (CRLF stripped, split by
// leva.SplitLine) is a whole record in its strict shape: an intact display
// frame, a report whose trailing "~ HH}" checksum verifies, an exact
// "$id: leva! (N.N)" banner, or a "[…]" notice of at most 32 bytes without
// '|' or ':'.
func IsWholeLine(line []byte) bool {
	if len(line) == 0 {
		return false
	}
	for _, p := range leva.SplitLine(line) {
		d := p.Data
		switch {
		case p.Fragment || len(d) == 0:
			return false
		case d[0] == '{':
			if !reportSumOK(d) {
				return false
			}
		case d[0] == '$':
			if !isWholeBanner(d) {
				return false
			}
		case d[0] == '[':
			if !isWholeNotice(d) {
				return false
			}
		default:
			if _, err := leva.ParseFrame(d); err != nil {
				return false
			}
		}
	}
	return true
}

// isWholeBanner reports whether b is exactly "$id: leva! (N.N)": only
// digits and dots inside the parentheses and nothing after them.
func isWholeBanner(b []byte) bool {
	v, ok := bytes.CutPrefix(b, []byte("$id: leva! ("))
	if !ok {
		return false
	}
	v, ok = bytes.CutSuffix(v, []byte(")"))
	if !ok || len(v) == 0 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}

// isWholeNotice reports whether b is a short "[…]" notice of at most
// maxNoticeLine bytes without '|' or ':', the bytes that start dump lines.
func isWholeNotice(b []byte) bool {
	return len(b) >= 2 && len(b) <= maxNoticeLine && b[0] == '[' && b[len(b)-1] == ']' &&
		!bytes.ContainsAny(b, "|:")
}

// reportSumOK reports whether b is a "{…}" report whose trailing "~ HH}"
// checksum verifies: HH (uppercase hex) is the two's complement of the
// 8-bit sum of all bytes before the '~' (bytes 0–283 of a 289-byte report).
func reportSumOK(b []byte) bool {
	n := len(b)
	if n < 6 || b[0] != '{' || b[n-1] != '}' || b[n-5] != '~' || b[n-4] != ' ' {
		return false
	}
	hh := b[n-3 : n-1]
	for _, c := range hh {
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') {
			return false
		}
	}
	want, err := strconv.ParseUint(string(hh), 16, 8)
	if err != nil {
		return false
	}
	var sum byte
	for _, c := range b[:n-5] {
		sum += c
	}
	return -sum == byte(want)
}
