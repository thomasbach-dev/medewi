package leva

import "bytes"

// Piece is one record SplitLine found in a line.
type Piece struct {
	Data     []byte // aliases the line passed to SplitLine
	Fragment bool   // a report or frame cut off by a stream switch
}

// SplitLine undoes what a stream switch (MCr/MC@) does on the real machine:
// it can cut a report or display frame off without its CRLF and glue the
// next record onto it. A report or frame line is split at the next record
// start; the part before it is a whole piece if it is a whole frame or
// report and a Fragment otherwise. Other lines (command replies) are never
// split: help text may contain "<100" and the like. line has its CRLF
// stripped.
func SplitLine(line []byte) []Piece {
	if isFrame(line) {
		return []Piece{{Data: line}} // intact; its text may contain "<100" and the like
	}
	var out []Piece
	for len(line) > 0 && (line[0] == '{' || line[0] == '<') {
		i := nextRecord(line)
		if i < 0 {
			break
		}
		head := line[:i]
		whole := isFrame(head) || (head[0] == '{' && head[i-1] == '}')
		out = append(out, Piece{Data: head, Fragment: !whole})
		line = line[i:]
	}
	if len(line) > 0 && line[0] == '<' && !isFrame(line) {
		// a frame cut off by "MC " or MCr, nothing glued on
		return append(out, Piece{Data: line, Fragment: true})
	}
	return append(out, Piece{Data: line})
}

// LastRecordStart returns the offset of the last record that starts in b,
// a piece of received traffic that may begin or end anywhere (a TCP read),
// or -1 if none does. Offset 0 counts if b begins with '<', '{', '[' or '$';
// later offsets count where a report, frame or banner glued on by a stream
// switch starts (as SplitLine splits). An intact frame is one record: its
// text may contain "<100" and the like. A trailing CRLF is ignored.
func LastRecordStart(b []byte) int {
	b = bytes.TrimSuffix(b, []byte("\r\n"))
	if isFrame(b) {
		return 0
	}
	last := -1
	if len(b) > 0 && bytes.IndexByte([]byte("<{[$"), b[0]) >= 0 {
		last = 0
	}
	for i := 0; ; {
		j := nextRecord(b[i:])
		if j < 0 {
			return last
		}
		i += j
		last = i
	}
}
