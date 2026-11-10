package rawlog

import (
	"bytes"
	"strings"
	"time"
)

const (
	// DefaultReplyWindow is how long after a command its reply lines are kept.
	DefaultReplyWindow = 5 * time.Second
	// DefaultDumpQuiet is how long after an MCu/MCm dump no reply window opens.
	DefaultDumpQuiet = 60 * time.Second
)

// FileStart returns the UTC start time encoded in a log file's base name
// (20060102T150405.000Z.jsonl.gz), false for any other name.
func FileStart(name string) (time.Time, bool) {
	base, ok := strings.CutSuffix(name, nameSuffix)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(nameLayout, base)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// IsDumpCommand reports whether cmd is MCu or MCm with any suffix: the
// commands whose replies carry the license key.
func IsDumpCommand(cmd []byte) bool {
	return bytes.HasPrefix(cmd, []byte("MCu")) || bytes.HasPrefix(cmd, []byte("MCm"))
}

// IsStreamToggle reports whether cmd is a stream control command (MCr,
// "MC " or MC@); these get no reply.
func IsStreamToggle(cmd []byte) bool {
	return bytes.Equal(cmd, []byte("MCr")) || bytes.Equal(cmd, []byte("MC ")) || bytes.Equal(cmd, []byte("MC@"))
}

// IsDumpLine reports whether line (an rx line start) belongs to an MCu/MCm
// dump: it starts with | or :.
func IsDumpLine(line []byte) bool {
	return len(line) > 0 && (line[0] == '|' || line[0] == ':')
}

// IsStreamLine reports whether line, CRLF already stripped, is a record that
// is always kept: a report ({), display frame (<), banner ($id:) or a short
// "[…]" notice of at most 32 bytes.
func IsStreamLine(line []byte) bool {
	if len(line) > 0 && (line[0] == '{' || line[0] == '<') || bytes.HasPrefix(line, []byte("$id:")) {
		return true
	}
	return len(line) > 0 && line[0] == '[' && len(line) <= maxNoticeLine
}
