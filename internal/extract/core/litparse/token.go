package litparse

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
		return t.eof()
	}
	// glued holds only for the first step: every step that reads no token
	// moves past something.
	for glued, last := true, -1; t.pos < len(t.data); glued = false {
		i := t.pos
		if i <= last {
			// Each step moves forward; one that does not is a defect, and
			// the file's rule is unknown rather than the walk endless.
			t.err = t.lost(i, "the tokenizer made no progress")
			return t.eof()
		}
		last = i
		if tok, done := t.step(i, glued); done {
			return tok
		}
	}
	return token{kind: tEOF, start: len(t.data), end: len(t.data), line: true}
}

// eof is the token that ends the file.
func (t *tokenizer) eof() token { return token{kind: tEOF, start: len(t.data), end: len(t.data)} }

// step reads the file at i: it moves past a heredoc's body, a span that is
// no token, or a blank, or returns the token that starts there, true.
func (t *tokenizer) step(i int, glued bool) (token, bool) {
	if i == t.eol {
		t.resumeLine(i)
		return token{}, false
	}
	if t.more && t.span.Start == i {
		return t.spanToken(glued)
	}
	if t.blank(i) {
		return token{}, false
	}
	return t.emit(t.scan(i), glued), true
}

// resumeLine moves past the line feed at i that ends a line of heredoc
// openers, to the end of the last one's closer, whose line holds code after
// its tag.
func (t *tokenizer) resumeLine(i int) {
	t.pos, t.eol = t.resume, -1
	t.newline(i)
	t.lineStart = t.lineStartOf(t.pos)
	t.lineHasTok = true
}

// spanToken reads the span that starts at pos: a string is a token, true;
// a comment and blank text are moved past; text after the code ends it,
// and other text or a string that ends unclosed at its line refuses the
// file.
func (t *tokenizer) spanToken(glued bool) (token, bool) {
	s := t.span
	t.span, t.more = t.next()
	if err := core.CheckSpan(t.file, t.data, s); err != nil {
		t.err = err
		return t.eof(), true
	}
	switch {
	case s.Kind == core.SpanText && !t.begins:
		// The data after Ruby's __END__: the code ends.
		t.pos = len(t.data)
		return t.eof(), true
	case s.Kind == core.SpanText:
		return t.textSpan(s)
	case s.Kind == core.SpanString && !s.Closed:
		// A string that stops unclosed at its line's end: no file its
		// compiler takes holds one, so the lexer lost its place, as
		// at a line end Lua's \z skips.
		t.err = t.lost(s.Start, "a string opens here and ends unclosed at its line's end")
		return t.eof(), true
	case s.Kind == core.SpanComment:
		t.linesIn(s.Start, s.End)
		t.pos = s.End
		return token{}, false
	}
	return t.stringToken(s, glued), true
}

// textSpan moves past s, text outside the code such as a PHP template's
// HTML, and the opener after it. Such text holds no value: one with more
// than blanks refuses the file, since the code around it is not all the
// file says, and ends the tokens, true.
func (t *tokenizer) textSpan(s core.Span) (token, bool) {
	body := t.data[s.BodyStart:s.BodyEnd]
	if text := bytes.TrimLeft(body, " \t\r\n"); len(text) > 0 {
		t.err = t.lost(s.BodyEnd-len(text), "text that is not code stands here, outside <?php ... ?>")
		return t.eof(), true
	}
	t.linesIn(s.Start, s.End)
	t.pos = s.End
	t.skipOpener()
	return token{}, false
}

// stringToken is the token of s, a string, which notes the line of a
// heredoc's opener, whose body the reading skips at its end.
func (t *tokenizer) stringToken(s core.Span, glued bool) token {
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

// blank moves past a line end, a blank, or a backslash that continues the
// line at i, and reports whether one stood there.
func (t *tokenizer) blank(i int) bool {
	b := t.data[i]
	switch {
	case (b == '\n' || b == '\r') && t.endsLine(i):
		t.newline(i) // a lone CR ends a line too
		t.pos = i + 1
	case space(b):
		t.pos = i + 1
	case t.continued(i):
		// a line continued: the line end is no statement's end
		t.pos = core.NextLine(t.data, i+1)
		t.lineStart = t.pos
	default:
		return false
	}
	return true
}

// space reports whether b is a blank within a line.
func space(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\f' || b == '\v' }

// continued reports whether a backslash at i continues its line.
func (t *tokenizer) continued(i int) bool {
	return t.data[i] == '\\' && i+1 < len(t.data) && (t.data[i+1] == '\n' || t.data[i+1] == '\r')
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
	if t.more && t.span.Start > i {
		d = d[:t.span.Start] // a token stops before a span
	}
	switch {
	case identStart(d[i]): // a sigil such as PHP's $ starts a name too
		return t.ident(d, i)
	case numberStart(d, i):
		return token{kind: tNumber, start: i, end: number(d, i)}
	}
	if tok, ok := t.symbol(d, i); ok {
		return tok
	}
	if k, ok := bracket(d[i]); ok {
		return token{kind: k, start: i, end: i + 1}
	}
	return punct(d, i)
}

// ident is the name that starts at i in d, with the ? or ! that ends a
// name such as Ruby's empty? and save! in a row with Symbols.
func (t *tokenizer) ident(d []byte, i int) token {
	j := identEnd(d, i+1)
	if t.syn.Symbols && j < len(d) && (d[j] == '?' || d[j] == '!') && (j+1 >= len(d) || d[j+1] != '=') {
		j++
	}
	return token{kind: tIdent, start: i, end: j}
}

// identEnd is the end of the run of name bytes from j in d.
func identEnd(d []byte, j int) int {
	for j < len(d) && identByte(d[j]) {
		j++
	}
	return j
}

// numberStart reports whether a number starts at i in d: a digit, or a
// point before one.
func numberStart(d []byte, i int) bool { return d[i] >= '0' && d[i] <= '9' || pointDigit(d, i) }

// pointDigit reports whether a point before a digit stands at j in d.
func pointDigit(d []byte, j int) bool {
	return d[j] == '.' && j+1 < len(d) && d[j+1] >= '0' && d[j+1] <= '9'
}

// symbol is the symbol that starts at i in d in a row with Symbols: an
// operator's, such as Ruby's :/, :<=> or :[]=, or a name's; false when
// none starts there.
func (t *tokenizer) symbol(d []byte, i int) (token, bool) {
	if !t.syn.Symbols || d[i] != ':' || !symbolAt(d, i) {
		return token{}, false
	}
	if end := operatorEnd(d, i+1); end > i+1 {
		return token{kind: tSymbol, start: i, end: end}, true
	}
	if i+1 < len(d) && identStart(d[i+1]) {
		return token{kind: tSymbol, start: i, end: nameSymbol(d, i+2)}, true
	}
	return token{}, false
}

// nameSymbol is the end of a name's symbol whose name goes on at j in d,
// with a ? or ! that ends it.
func nameSymbol(d []byte, j int) int {
	j = identEnd(d, j)
	if j < len(d) && (d[j] == '?' || d[j] == '!') {
		j++
	}
	return j
}

// bracket is the kind of token the bracket b is; false for any other byte.
func bracket(b byte) (kind, bool) {
	switch b {
	case '(', '[', '{':
		return tOpen, true
	case ')', ']', '}':
		return tClose, true
	}
	return 0, false
}

// punct is the operator that starts at i in d, the longest of puncts, or
// else one byte, or one character past ASCII.
func punct(d []byte, i int) token {
	for _, p := range puncts {
		if i+len(p) <= len(d) && string(d[i:i+len(p)]) == p {
			return token{kind: tPunct, start: i, end: i + len(p)}
		}
	}
	j := i + 1
	for j < len(d) && d[j]&0xC0 == 0x80 {
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
		switch {
		case identByte(d[j]) && d[j] < 0x80:
			if exponentSign(d, i, j) {
				j++
			}
		case pointDigit(d, j):
		default:
			return j
		}
		j++
	}
	return j
}

// exponentSign reports whether the byte at j in d, in the number that
// starts at i, is an exponent's e followed by its sign: a decimal number's
// e+ or e-, where a hex number's e is a digit.
func exponentSign(d []byte, i, j int) bool {
	return (d[j] == 'e' || d[j] == 'E') && j+1 < len(d) && (d[j+1] == '+' || d[j+1] == '-') && !hex(d[i:j])
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
