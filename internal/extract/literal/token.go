package literal

import (
	"bytes"
	"fmt"
	"iter"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// kind is what a token is.
type kind uint8

const (
	tEOF    kind = iota
	tIdent       // a name or a word
	tNumber      // a number as the file wrote it
	tString      // a string literal, one span of the lexer
	tSymbol      // :name, as Ruby and Elixir write a symbol
	tPunct       // an operator or a separator
	tOpen        // ( [ {
	tClose       // ) ] }
)

// token is one token of a file's code: its bytes, and for a string its
// span. Line is whether a line end stands between it and the token before,
// and First whether it is the first token of its line, at column Col.
// Glued is whether nothing at all stands between it and the token before.
type token struct {
	kind       kind
	start, end int
	span       core.Span
	line       bool
	first      bool
	glued      bool
	col        int
}

// puncts are the operators the tokenizer reads as one token, longest first.
var puncts = []string{
	"<<=", ">>=", "**=", "...", "===", "!==", "??=", "||=", "&&=", "//=",
	"=>", "==", "!=", "<=", ">=", "->", "::", ":=", "+=", "-=", "*=", "/=", "%=", ".=", "|=", "&=", "^=",
	"?.", "??", "||", "&&", "<<", ">>", "**", "..", "|>", "<>", "++", "--", "//",
}

// tokenizer reads a file's tokens one at a time, the lexer's spans among
// them: a comment is skipped, a string is one token, and a heredoc's body is
// skipped at the end of its opener's line, as the lexer skips it.
type tokenizer struct {
	file  string
	data  []byte
	syn   *core.Syntax
	lines core.Lines
	next  func() (core.Span, bool)
	stop  func()
	span  core.Span // the next span, valid when more
	more  bool
	pos   int // where the next token is looked for
	// eol is the line feed that ends a line holding heredoc openers, -1
	// for none, and resume where the reading goes on after it.
	eol, resume int
	lineStart   int  // the start of the line pos is on
	sawLine     bool // a line end since the token before
	lineHasTok  bool // a token already stands on the line
	err         error
	// begins is whether the row's code starts after a Begin, as PHP's does,
	// so its text is output; otherwise text is data after the code's end.
	begins bool
}

func newTokenizer(file string, lang core.Language, data []byte, lines core.Lines) *tokenizer {
	next, stop := iter.Pull(lang.Spans(data))
	t := &tokenizer{file: file, data: data, syn: lang.Syntax, lines: lines, next: next, stop: stop, eol: -1, begins: lang.Begin != ""}
	t.span, t.more = t.next()
	return t
}

// line is the line of the byte at off, counted from 1. Every line the
// tokenizer counts is core's (core/lineend): it counts no line end itself.
func (t *tokenizer) line(off int) int { return t.lines.At(off) }

// lineStartOf is where the line that holds off starts.
func (t *tokenizer) lineStartOf(off int) int { return t.lines[t.line(off)-1] }

// endsLine reports whether a line ends at the byte at i: a LF, or a CR no
// LF follows.
func (t *tokenizer) endsLine(i int) bool { return t.line(i+1) > t.line(i) }

// linesIn notes the line ends in data[from:to], if any: the line pos goes
// on from is the one that holds to.
func (t *tokenizer) linesIn(from, to int) {
	if t.line(to) > t.line(from) {
		t.sawLine, t.lineHasTok, t.lineStart = true, false, t.lineStartOf(to)
	}
}

// lost is the refusal of a file the reader lost its place in at off.
func (t *tokenizer) lost(off int, what string) error {
	return fmt.Errorf("%s: line %d: %s, so the literal reader lost its place and cannot tell values from code after it (docs/reference.md#literal)", t.file, t.line(off), what)
}

// newline notes a line end at off.
func (t *tokenizer) newline(off int) {
	t.sawLine, t.lineHasTok, t.lineStart = true, false, off+1
}

// token is the next token, tEOF at the end of the file or of its code.
func (t *tokenizer) token() token {
	if t.err != nil {
		return token{kind: tEOF, start: len(t.data), end: len(t.data)}
	}
	glued := true
	for last := -1; t.pos < len(t.data); {
		i := t.pos
		if i <= last {
			// Each step moves forward; one that does not is a defect, and
			// the file's rule is unknown rather than the walk endless.
			t.err = t.lost(i, "the tokenizer made no progress")
			return token{kind: tEOF, start: len(t.data), end: len(t.data)}
		}
		last = i
		if i == t.eol {
			t.pos, t.eol = t.resume, -1
			t.newline(i)
			// the closer's line holds code after its tag
			t.lineStart = t.lineStartOf(t.pos)
			t.lineHasTok = true
			glued = false
			continue
		}
		if t.more && t.span.Start == i {
			s := t.span
			t.span, t.more = t.next()
			if err := core.CheckSpan(t.file, t.data, s); err != nil {
				t.err = err
				return token{kind: tEOF, start: len(t.data), end: len(t.data)}
			}
			switch {
			case s.Kind == core.SpanText && !t.begins:
				// The data after Ruby's __END__: the code ends.
				t.pos = len(t.data)
				return token{kind: tEOF, start: len(t.data), end: len(t.data)}
			case s.Kind == core.SpanText:
				// Text outside the code, such as a PHP template's HTML, holds
				// no value: one with more than blanks is refused, since the
				// code around it is not all the file says.
				body := t.data[s.BodyStart:s.BodyEnd]
				if text := bytes.TrimLeft(body, " \t\r\n"); len(text) > 0 {
					t.err = t.lost(s.BodyEnd-len(text), "text that is not code stands here, outside <?php ... ?>")
					return token{kind: tEOF, start: len(t.data), end: len(t.data)}
				}
				t.linesIn(s.Start, s.End)
				t.pos, glued = s.End, false
				t.skipOpener()
				continue
			case s.Kind == core.SpanString && !s.Closed:
				// A string that stops unclosed at its line's end: no file its
				// compiler takes holds one, so the lexer lost its place, as
				// at a line end Lua's \z skips.
				t.err = t.lost(s.Start, "a string opens here and ends unclosed at its line's end")
				return token{kind: tEOF, start: len(t.data), end: len(t.data)}
			}
			if s.Kind == core.SpanComment {
				t.linesIn(s.Start, s.End)
				t.pos, glued = s.End, false
				continue
			}
			if s.Here.Close > 0 {
				if t.eol < 0 {
					// The language ends the opener's line at a LF alone, where
					// the lexer finds the body (lexer.Form.here); lines are core's.
					t.eol = s.End + bytes.IndexByte(t.data[s.End:], '\n')
				}
				t.resume = s.Here.Close
			}
			t.pos = s.End
			return t.emit(token{kind: tString, start: s.Start, end: s.End, span: s}, glued)
		}
		b := t.data[i]
		switch {
		case (b == '\n' || b == '\r') && t.endsLine(i):
			t.newline(i) // a lone CR ends a line too
			t.pos, glued = i+1, false
			continue
		case b == ' ' || b == '\t' || b == '\r' || b == '\f' || b == '\v':
			t.pos, glued = i+1, false
			continue
		case b == '\\' && i+1 < len(t.data) && (t.data[i+1] == '\n' || t.data[i+1] == '\r'):
			// a line continued: the line end is no statement's end
			t.pos = core.NextLine(t.data, i+1)
			t.lineStart, glued = t.pos, false
			continue
		}
		return t.emit(t.scan(i), glued)
	}
	return token{kind: tEOF, start: len(t.data), end: len(t.data), line: true}
}

// skipOpener moves past the php or = of a PHP opener, <?php or <?=, whose
// <? ends the text span before pos.
func (t *tokenizer) skipOpener() {
	for _, w := range []string{"php", "="} {
		if bytes.HasPrefix(t.data[t.pos:], []byte(w)) {
			t.pos += len(w)
			return
		}
	}
}

// emit is tok with its place on its line, which moves pos past it.
func (t *tokenizer) emit(tok token, glued bool) token {
	if tok.end <= tok.start {
		t.err = t.lost(tok.start, "the tokenizer read an empty token") // a defect, as above
		return token{kind: tEOF, start: len(t.data), end: len(t.data)}
	}
	tok.line, tok.glued = t.sawLine, glued
	tok.first = !t.lineHasTok
	tok.col = tok.start - t.lineStart
	t.sawLine, t.lineHasTok = false, true
	t.pos = tok.end
	if t.more && t.span.Start < tok.end {
		// A token never holds a span's opener: scan stops before one.
		t.err = t.lost(t.span.Start, "a string or a comment opens inside a token")
	}
	return tok
}

// scan is the token that starts at i, which is no span's start.
func (t *tokenizer) scan(i int) token {
	d := t.data
	limit := len(d)
	if t.more {
		limit = t.span.Start
	}
	b := d[i]
	switch {
	case identStart(b): // a sigil such as PHP's $ starts a name too
		j := i + 1
		for j < limit && identByte(d[j]) {
			j++
		}
		if t.syn.Symbols && j < limit && (d[j] == '?' || d[j] == '!') && (j+1 >= limit || d[j+1] != '=') {
			j++ // Ruby's empty? and save!
		}
		return token{kind: tIdent, start: i, end: j}
	case b >= '0' && b <= '9' || b == '.' && i+1 < limit && d[i+1] >= '0' && d[i+1] <= '9':
		return token{kind: tNumber, start: i, end: number(d[:limit], i)}
	case t.syn.Symbols && b == ':' && operatorEnd(d[:limit], i+1) > i+1 && symbolAt(d, i):
		// An operator's symbol, such as Ruby's :/, :<=> or :[]=.
		return token{kind: tSymbol, start: i, end: operatorEnd(d[:limit], i+1)}
	case t.syn.Symbols && b == ':' && i+1 < limit && identStart(d[i+1]) && symbolAt(d, i):
		j := i + 2
		for j < limit && identByte(d[j]) {
			j++
		}
		if j < limit && (d[j] == '?' || d[j] == '!') {
			j++
		}
		return token{kind: tSymbol, start: i, end: j}
	case b == '(' || b == '[' || b == '{':
		return token{kind: tOpen, start: i, end: i + 1}
	case b == ')' || b == ']' || b == '}':
		return token{kind: tClose, start: i, end: i + 1}
	}
	for _, p := range puncts {
		if i+len(p) <= limit && string(d[i:i+len(p)]) == p {
			return token{kind: tPunct, start: i, end: i + len(p)}
		}
	}
	// One byte, or one character past ASCII.
	j := i + 1
	for j < limit && d[j]&0xC0 == 0x80 {
		j++
	}
	return token{kind: tPunct, start: i, end: j}
}

// symbolAt reports whether the : at i may open a symbol: not after a name,
// as a ternary's x ? a:b, not after a :, as a scope's ::, and not after a
// quote, where it ends a label, as the : of {"a":[]} and {'b':-1}.
func symbolAt(d []byte, i int) bool {
	return i == 0 || !identByte(d[i-1]) && strings.IndexByte(`:"'`, d[i-1]) < 0
}

// operatorSymbol are the bytes of an operator's symbol, as Ruby's :/, :+
// and :<=>.
const operatorSymbol = "+-*/%<>=!~^&|"

// operatorEnd is the end of the operator a symbol names from i in d: a run
// of operatorSymbol's bytes, or [] and []=, the element operators; i when
// none starts there. A bracket stands in no other, so the ] of [:+, :-]
// closes the list and the [ of {"a":[1]} opens one.
func operatorEnd(d []byte, i int) int {
	if bytes.HasPrefix(d[i:], []byte("[]")) {
		if bytes.HasPrefix(d[i+2:], []byte("=")) {
			return i + 3
		}
		return i + 2
	}
	j := i
	for j < len(d) && strings.IndexByte(operatorSymbol, d[j]) >= 0 {
		j++
	}
	return j
}

// number is the end of the number that starts at i: digits, letters for a
// base, an exponent or a suffix, _, and a point before a digit.
func number(d []byte, i int) int {
	j := i
	for j < len(d) {
		b := d[j]
		switch {
		case identByte(b) && b < 0x80:
			if (b == 'e' || b == 'E') && j+1 < len(d) && (d[j+1] == '+' || d[j+1] == '-') && !hex(d[i:j]) {
				j++
			}
		case b == '.' && j+1 < len(d) && d[j+1] >= '0' && d[j+1] <= '9':
		default:
			return j
		}
		j++
	}
	return j
}

// hex reports whether the number so far is written in hex.
func hex(n []byte) bool {
	return len(n) > 1 && n[0] == '0' && (n[1] == 'x' || n[1] == 'X')
}

// identStart reports whether b may start a name: a letter, _, $ or a byte
// past ASCII.
func identStart(b byte) bool {
	return b == '_' || b == '$' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// identByte reports whether b may stand in a name.
func identByte(b byte) bool {
	return identStart(b) || b >= '0' && b <= '9'
}

// is reports whether tok is the punctuation or the word w, which is not
// empty. A string's text holds its quotes, which no word or mark does.
func (t *tokenizer) is(tok token, w string) bool {
	return w != "" && string(t.data[tok.start:tok.end]) == w
}

// text is tok's bytes.
func (t *tokenizer) text(tok token) string { return string(t.data[tok.start:tok.end]) }

// oneOf reports whether tok is one of words.
func (t *tokenizer) oneOf(tok token, words []string) bool {
	for _, w := range words {
		if t.is(tok, w) {
			return true
		}
	}
	return false
}

// fold reports whether tok is one of words, in any case when fold is set.
func (t *tokenizer) fold(tok token, words []string, fold bool) bool {
	if tok.kind != tIdent {
		return false
	}
	for _, w := range words {
		if t.is(tok, w) || fold && strings.EqualFold(t.text(tok), w) {
			return true
		}
	}
	return false
}
