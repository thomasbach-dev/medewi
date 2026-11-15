package leva

import (
	"errors"
	"strings"
)

// Frame is one decoded display frame of the LCD mirror (MC@).
type Frame struct {
	Backlight bool
	Key       byte // pressed key, '0' for none ('9' seen while the encoder was held)
	Layout    byte // '2' main screen, '1' menu
	// Rows are the four LCD lines, 16 characters (runes, not bytes) each:
	// the LCD's 0xDF is the degree sign, the custom characters 0x01-0x05
	// become ▍ ▋ █ (heat bar), – and ✓, other control bytes become spaces,
	// other bytes are read as Latin-1.
	Rows [4]string
}

// ErrNotFrame means a line is not an intact 69-byte "<BKL…>" frame.
var ErrNotFrame = errors.New("leva: not a display frame")

// frameLen is the length of an intact "<BKL…>" display frame.
const frameLen = 69

func isFrame(line []byte) bool {
	return len(line) == frameLen && line[0] == '<' && line[frameLen-1] == '>'
}

// customGlyphs maps the HD44780 custom-character codes the firmware
// uses to look-alike runes.
var customGlyphs = map[byte]rune{1: '▍', 2: '▋', 3: '█', 4: '–', 5: '✓'}

// ParseFrame decodes an intact display frame (CR+LF stripped).
func ParseFrame(line []byte) (Frame, error) {
	if !isFrame(line) {
		return Frame{}, ErrNotFrame
	}
	f := Frame{Backlight: line[1] == '1', Key: line[2], Layout: line[3]}
	for i := range f.Rows {
		var b strings.Builder
		for _, c := range line[4+16*i : 4+16*(i+1)] {
			switch {
			case c == 0xDF:
				b.WriteRune('°')
			case customGlyphs[c] != 0:
				b.WriteRune(customGlyphs[c])
			case c < 0x20:
				b.WriteByte(' ')
			default:
				b.WriteRune(rune(c))
			}
		}
		f.Rows[i] = b.String()
	}
	return f, nil
}
