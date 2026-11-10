package logview

import (
	"bytes"
	"strconv"
)

// The strict shapes below decide what may be shown inside the dump quiet
// period, where a glued dump tail must never pass for a record.

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
// maxNotice bytes without '|' or ':', the bytes that start dump lines.
func isWholeNotice(b []byte) bool {
	return len(b) >= 2 && len(b) <= maxNotice && b[0] == '[' && b[len(b)-1] == ']' &&
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
