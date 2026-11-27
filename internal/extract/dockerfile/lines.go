package dockerfile

import (
	"bufio"
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/moby/buildkit/frontend/dockerfile/parser"

	"github.com/f-klement/errata/internal/extract/core"
)

// The parser joins an instruction's lines into one, its Original, and
// keeps only the lines it starts and ends on, which it counts at \n alone.
// A value of an instruction over several lines, such as one key of a LABEL
// whose keys each take a line, sits on its own line, and errata counts a
// lone \r as a line end too (core/lineend): positions finds each word's
// byte in the file by joining the instruction's lines again as the parser
// does, noting where each line's part of Original starts in Original and
// in the file, and finding each word of the instruction in Original in
// turn. A LineCursor then gives the byte's line.

// lines is where each line of a file starts, by its number less one, as
// the parser's scanner splits them, at \n.
type lines struct {
	data  []byte
	start []int
}

// newLines is the lines of data.
func newLines(data []byte) lines {
	l := lines{data: data, start: []int{0}}
	for i, c := range data {
		if c == '\n' && i+1 < len(data) {
			l.start = append(l.start, i+1)
		}
	}
	return l
}

// offset is where line n, from 1, starts; the end of the file past the
// last.
func (l lines) offset(n int) int {
	if n < 1 || n > len(l.start) {
		return len(l.data)
	}
	return l.start[n-1]
}

// longLine is where the first line of more than bufio.MaxScanTokenSize
// bytes, its \n counted, starts: the one the parser's scanner refuses; the
// end of the file for none.
func (l lines) longLine() int {
	for n := 1; n <= len(l.start); n++ {
		if len(l.line(n)) > bufio.MaxScanTokenSize {
			return l.offset(n)
		}
	}
	return len(l.data)
}

// line is the line n, from 1, with its \n; empty past the last.
func (l lines) line(n int) []byte {
	end := len(l.data)
	if n < len(l.start) {
		end = l.start[n]
	}
	return l.data[min(l.offset(n), end):end]
}

// part is where a line's part of an instruction's Original starts, in
// Original and in the file.
type part struct {
	at, file int
}

// positions finds the words of one instruction in its Original in turn.
type positions struct {
	text  string
	pos   int
	parts []part // nil: every word is at start
	k     int    // the part offsetOf found last
	start int    // where the instruction's first line starts in the file
}

// positions is the finder of n's words, which starts after the name of the
// instruction and its flags, with the parts of its lines, unless joining
// them does not give Original, when every word is at the start of its
// first line.
func (r *reading) positions(n *parser.Node) positions {
	return positions{text: n.Original, pos: argsStart(n.Original, r.escape), parts: r.parts(n), start: r.lines.offset(n.StartLine)}
}

// parts is the part of n's Original each of its lines gives, as the
// parser's Parse joins them: the first line without a byte order mark on
// the file's first line and the blanks it starts with, then each line
// that is no comment and not blank, each without its \r and \n and the
// escape that continues it; nil when they do not join to Original.
func (r *reading) parts(n *parser.Node) []part {
	var out []part
	at := 0
	for ln := n.StartLine; ln <= n.EndLine; ln++ {
		text, skipped, ok := r.lineText(ln, ln == n.StartLine)
		if !ok {
			continue
		}
		if !strings.HasPrefix(n.Original[at:], string(text)) {
			return nil
		}
		out = append(out, part{at: at, file: r.lines.offset(ln) + skipped})
		at += len(text)
	}
	if at != len(n.Original) {
		return nil
	}
	return out
}

// lineText is the part line ln gives an instruction and how many bytes
// at its start the parser drops, and false for a line the parser skips
// inside one: a comment or a blank line.
func (r *reading) lineText(ln int, first bool) ([]byte, int, bool) {
	raw := r.lines.line(ln)
	text := raw
	if ln == 1 {
		text = bytes.TrimPrefix(text, []byte("\uFEFF"))
	}
	text = bytes.TrimRight(text, "\r\n")
	trimmed := bytes.TrimLeftFunc(text, unicode.IsSpace)
	if first {
		text = trimmed
	} else if len(trimmed) == 0 || trimmed[0] == '#' {
		return nil, 0, false
	}
	return trimContinuation(text, r.escape), len(bytes.TrimRight(raw, "\r\n")) - len(text), true
}

// trimContinuation is line without the escape that ends it, and the blanks
// after that, when the escape continues the line: when it is the last
// byte but spaces and tabs, and the byte before it, if any, is no escape
// too, as the parser's lineContinuationRegex matches.
func trimContinuation(line []byte, escape byte) []byte {
	t := bytes.TrimRight(line, " \t")
	if len(t) == 0 || t[len(t)-1] != escape {
		return line
	}
	t = t[:len(t)-1]
	if len(t) > 0 && t[len(t)-1] == escape {
		return line
	}
	return t
}

// next is where in the file the next word of the instruction starts: at
// the next byte of Original that is not white space, or failing that at
// the word's next occurrence, since a word the parser split drops an
// escape at the end of the line, and its lexer may write a byte that is no
// UTF-8 otherwise. A word it cannot find is where it would start. The
// search past the white space is charged to m, so a file of such words is
// refused for work rather than searched again and again.
func (p *positions) next(word string, m *core.Meter) (int, error) {
	i := skipSpace(p.text, p.pos)
	if !strings.HasPrefix(p.text[i:], word) {
		if err := m.Charge(len(p.text) - i); err != nil {
			return 0, err
		}
		j := strings.Index(p.text[i:], word)
		if j < 0 {
			return p.offsetOf(i), nil
		}
		i += j
	}
	p.pos = i + len(word)
	return p.offsetOf(i), nil
}

// skip passes sep when the instruction's next byte starts it.
func (p *positions) skip(sep string) {
	if strings.HasPrefix(p.text[p.pos:], sep) {
		p.pos += len(sep)
	}
}

// offsetOf is where in the file the byte at off of Original is, off at or
// after the last it was asked of.
func (p *positions) offsetOf(off int) int {
	if p.parts == nil {
		return p.start
	}
	for p.k+1 < len(p.parts) && p.parts[p.k+1].at <= off {
		p.k++
	}
	pt := p.parts[p.k]
	return pt.file + off - pt.at
}

// argsStart is where the arguments of an instruction start in line, as
// the parser's splitCommand and extractBuilderFlags find them: after the
// instruction's name and the white space after it, and after each word
// that starts with -- before them, a word reading its quotes and escapes;
// a word of -- alone ends the flags.
func argsStart(line string, escape byte) int {
	i := skipSpace(line, 0)
	for i < len(line) && !isBlank(line[i]) {
		i++
	}
	for {
		i = skipSpace(line, i)
		if !strings.HasPrefix(line[i:], "--") {
			return i
		}
		end := flagEnd(line, i, escape)
		if line[i:end] == "--" {
			return skipSpace(line, end)
		}
		i = end
	}
}

// flagEnd is where the flag word at i ends.
func flagEnd(line string, i int, escape byte) int {
	var quote byte
	for ; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == 0 && isBlank(c):
			return i
		case c == escape:
			i++
		case quote == 0 && (c == '\'' || c == '"'):
			quote = c
		case c == quote:
			quote = 0
		}
	}
	return len(line)
}

// skipSpace is the first byte from i of line that is no white space, as
// strings.TrimSpace and the parser read it.
func skipSpace(line string, i int) int {
	for i < len(line) {
		c, w := utf8.DecodeRuneInString(line[i:])
		if !unicode.IsSpace(c) {
			return i
		}
		i += w
	}
	return i
}

// isBlank reports whether c is a byte the parser's flags end at.
func isBlank(c byte) bool { return unicode.IsSpace(rune(c)) }
