package yaml

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// blocks finds the line of the | that starts a literal block scalar, from
// the file's bytes. yaml.v3 gives a scalar the line and column where its
// event starts, which is its anchor or its tag when it has one, and those
// may stand on a line above the |. The walker converts nodes in the file's
// order, so the lines it asks about only grow, and a cursor that keeps the
// start of the line it reached last reads the file once in all.
//
// Every loop here moves forward by at least one byte before it tests
// anything, and ends at the end of the file, so a wrong test gives a wrong
// line or 0, never a loop that does not end.
type blocks struct {
	data  []byte
	line  int // the line the cursor is at, counted from 1, as yaml.v3 counts
	start int // the offset where that line starts
	off   bool
}

// newBlocks is a cursor at the start of data, past a UTF-8 byte order mark,
// for which yaml.v3 counts no column. Of a UTF-16 file, which yaml.v3 reads
// as the UTF-8 it decodes it to, it finds no line, so its block scalars are
// reported as any other string is.
func newBlocks(data []byte) *blocks {
	b := &blocks{data: data, line: 1}
	b.off = bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff})
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		b.start = 3
	}
	return b
}

// lineOf returns the line of the | of y, a literal block scalar that holds
// no LS or PS, as core.Node's Block says, or 0 for any other node, for one
// whose | it does not find where yaml.v3's line and column point, and for
// one asked about after a node on a later line, which the walker never does.
// A 0 reports the block's values at the line where the string starts.
func (b *blocks) lineOf(y *yaml.Node) int32 {
	if y.Style&yaml.LiteralStyle == 0 || strings.ContainsAny(y.Value, "\u2028\u2029") || b.off {
		return 0
	}
	if y.Line < b.line {
		return 0
	}
	for b.line != y.Line {
		if b.start == len(b.data) {
			return 0
		}
		n := breakAt(b.data[b.start:])
		b.start += max(n, 1)
		if n != 0 {
			b.line++
		}
	}
	i := b.start
	for col := 1; col != y.Column; col++ {
		rest := b.data[i:]
		if len(rest) == 0 || breakAt(rest) != 0 {
			return 0
		}
		_, n := utf8.DecodeRune(rest)
		i += n
	}
	return indicatorLine(b.data[i:], y.Line)
}

// What indicatorLine is reading: blanks between the parts before the |, an
// anchor or a tag, or a comment.
const (
	between = iota
	property
	comment
)

// indicatorLine reads rest, which starts on line, past what may stand
// between a scalar's event and its |: anchors, tags, blanks, comments and
// line breaks, and returns the line of the |, or 0 when anything else comes
// first or rest ends.
func indicatorLine(rest []byte, line int) int32 {
	state := between
	for len(rest) != 0 {
		n := breakAt(rest)
		c := rest[0]
		rest = rest[max(n, 1):]
		if n != 0 {
			line++
			state = between
			continue
		}
		if state == comment {
			continue
		}
		if state == property {
			if c == ' ' || c == '\t' {
				state = between
			}
			continue
		}
		if c == '|' {
			return int32(line) //nolint:gosec // a line of a file of at most MaxMaxBytes, under 2^31
		}
		if c == '#' {
			state = comment
			continue
		}
		if c == '&' || c == '!' {
			state = property
			continue
		}
		if c != ' ' && c != '\t' {
			return 0
		}
	}
	return 0
}

// lineBreaks are the line breaks yaml.v3 breaks lines on, CR LF before CR.
var lineBreaks = [...][]byte{[]byte("\r\n"), []byte("\r"), []byte("\n"), []byte("\u0085"), []byte("\u2028"), []byte("\u2029")}

// breakAt is the length of the line break rest starts with, as yaml.v3
// breaks lines: CR LF, CR, LF, NEL, LS and PS; 0 when there is none.
func breakAt(rest []byte) int {
	for _, br := range lineBreaks {
		if bytes.HasPrefix(rest, br) {
			return len(br)
		}
	}
	return 0
}
