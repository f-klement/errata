package jenkins

import (
	"bytes"

	"github.com/f-klement/errata/internal/extract/core"
)

// The lexer splits a Jenkinsfile into Groovy's tokens: names, numbers,
// strings, operators and line ends, with comments left out. It knows
// Groovy's comments (//, /* */ and a #! first line) and its string forms:
// '...', "...", '''...''', """...""", /.../ and $/.../$, with the
// interpolation of ${...} and $name in the forms that interpolate. A string
// is one token, the code inside its ${ } included: a string inside that
// code, and the code inside that string, are read with an explicit stack,
// never by recursion, so a file nests at most core.MaxDepth levels and the
// lexer's memory does not grow with the file. Each token is read once, in
// one pass, and the lexer keeps none it has handed on.

// kind is what a token is.
type kind uint8

const (
	tokEOF kind = iota
	tokNewline
	tokName   // a name or a keyword
	tokNumber // a number, as written
	tokString // a string of any form; quote says which
	tokOp     // an operator or a bracket; op holds its text
	tokAt     // @, which starts an annotation
)

// quote is a string's form.
type quote uint8

const (
	single       quote = iota + 1 // '...'
	double                        // "..."
	tripleSingle                  // '''...'''
	tripleDouble                  // """..."""
	slashy                        // /.../
	dollarSlashy                  // $/.../$
)

// open and closing are the delimiters of each form.
var (
	opening = [...]string{single: "'", double: `"`, tripleSingle: "'''", tripleDouble: `"""`, slashy: "/", dollarSlashy: "$/"}
	closing = [...]string{single: "'", double: `"`, tripleSingle: "'''", tripleDouble: `"""`, slashy: "/", dollarSlashy: "/$"}
)

// token is one token: its kind, where it starts and ends in the file, and
// the line it starts on.
type token struct {
	kind       kind
	quote      quote  // a string's form
	op         string // an operator's text
	start, end int
	line       int
}

// text is the token's bytes in data.
func (t token) text(data []byte) []byte { return data[t.start:t.end] }

// is reports whether t is the operator op.
func (t token) is(op string) bool { return t.kind == tokOp && t.op == op }

// body is a string token's text between its delimiters.
func (t token) body(data []byte) []byte {
	return data[t.start+len(opening[t.quote]) : t.end-len(closing[t.quote])]
}

// lexer reads data's tokens in order.
type lexer struct {
	data []byte
	pos  int
	// lines is the line of an offset, counted on from the offset asked before.
	lines *core.LineCursor
	// regexOK is whether a / here starts a slashy string rather than a
	// division: after a token that ends a value it does not.
	regexOK bool
	// stack is what the lexer is inside of while it reads one string token:
	// the string's form, then the code of a ${ } inside it, and so on.
	stack []frame
	// parens counts the ( and [ open at each level of { } in code, the
	// innermost last: Groovy 2.4's lexer skips a line end inside ( ) or
	// [ ], so a / after one divides as it would on the same line
	// (groovy.g, rule NLS).
	parens []int
	m      *core.Meter
	// code, when set, is handed the tokens of the code inside each ${ },
	// which next does not return.
	code codeWatcher
}

// codeWatcher is told of the code inside a string's ${ }: enterCode when
// a ${ opens, codeToken for each token of its code in order, a string
// inside it once the string is read whole, and leaveCode at the } that
// closes it.
type codeWatcher interface {
	enterCode()
	codeToken(t token)
	leaveCode()
}

// frame is one level of the stack: a string of form q, or, when q is 0,
// the code of a ${ }, with the braces open inside it and whether a / there
// starts a slashy string. tok is a string's token when the string is in
// the code of a ${ }, for the code watcher once it is read whole.
type frame struct {
	q       quote
	braces  int
	regexOK bool
	tok     token
}

func newLexer(data []byte, m *core.Meter) *lexer {
	return &lexer{data: data, lines: core.NewLineCursor(data), regexOK: true, parens: []int{0}, m: m}
}

// lineOf is the line of offset off, which never moves back.
func (l *lexer) lineOf(off int) int {
	return l.lines.At(off)
}

// next is the next token. A string is read whole, with what it
// interpolates.
func (l *lexer) next() (token, error) {
	t, err := l.read(&l.regexOK)
	if err != nil || t.kind != tokString {
		return t, err
	}
	l.stack = append(l.stack[:0], frame{q: t.quote})
	if err := l.inside(); err != nil {
		return t, err
	}
	t.end = l.pos
	return t, nil
}

// lexError is a refusal at the line of offset off.
func (l *lexer) lexError(off int, format string, args ...any) error {
	return core.LineError(l.lineOf(off), format, args...)
}

// read reads one token of code at pos, skipping blanks and comments. A
// string token stops after its opening delimiter, for the caller to read
// the rest. regexOK is the code's state of whether a / starts a slashy
// string, which read updates for the token it reads.
func (l *lexer) read(regexOK *bool) (token, error) {
	for {
		if err := l.m.Charge(1); err != nil {
			return token{}, err
		}
		if l.pos >= len(l.data) {
			return token{kind: tokEOF, start: len(l.data), end: len(l.data), line: l.lineOf(len(l.data))}, nil
		}
		if l.blank() {
			continue
		}
		skipped, err := l.comment()
		if err != nil {
			return token{}, err
		}
		if !skipped {
			return l.token(regexOK)
		}
	}
}

// blank moves past the blank at pos, or the backslash that joins its line
// to the next, and reports whether there was one.
func (l *lexer) blank() bool {
	d := l.data
	switch c := d[l.pos]; {
	case c == ' ' || c == '\t' || c == '\f':
		l.pos++
	case c == '\\' && lineEndAt(d, l.pos+1):
		// A backslash before a line end, a LF, a CRLF or a lone CR as
		// Groovy's ONE_NL reads them, joins the two lines.
		l.pos = core.NextLine(d, l.pos+1)
	default:
		return false
	}
	return true
}

// comment moves past the comment at pos, a #! line at the file's start, a
// // line or a /* */ block, and reports whether there was one.
func (l *lexer) comment() (bool, error) {
	d := l.data
	at := d[l.pos:]
	switch {
	case l.pos == 0 && bytes.HasPrefix(at, []byte("#!")):
		l.pos = lineEnd(d, 0)
	case bytes.HasPrefix(at, []byte("//")):
		l.pos = lineEnd(d, l.pos)
	case bytes.HasPrefix(at, []byte("/*")):
		end := bytes.Index(at[2:], []byte("*/"))
		if end < 0 {
			return false, l.lexError(l.pos, "a /* comment is not closed")
		}
		l.pos += 2 + end + 2
	default:
		return false, nil
	}
	return true, nil
}

// token reads the token that starts at pos, which is neither a blank nor a
// comment.
func (l *lexer) token(regexOK *bool) (token, error) {
	start := l.pos
	t := token{start: start, line: l.lineOf(start)}
	switch c := l.data[start]; {
	case c == '\n' || c == '\r':
		l.newline(&t, regexOK)
	case l.stringStart(&t, *regexOK):
		*regexOK = false
	case isNameStart(c) || isDigit(c):
		l.word(&t, regexOK)
	default:
		if err := l.punct(&t, regexOK); err != nil {
			return token{}, err
		}
	}
	t.end = l.pos
	return t, nil
}

// newline reads a line end into t: a CRLF is one, and so is a lone CR, the
// Macintosh newline of groovy.g's ONE_NL. Outside ( and [ a / after it
// starts a slashy string.
func (l *lexer) newline(t *token, regexOK *bool) {
	l.pos = core.NextLine(l.data, l.pos)
	t.kind = tokNewline
	if l.parens[len(l.parens)-1] == 0 {
		*regexOK = true
	}
}

// stringStart reads the opening delimiter of a string at t's start into t,
// and reports whether one is there. A / or $/ opens one only where regexOK
// says a / starts a slashy string.
func (l *lexer) stringStart(t *token, regexOK bool) bool {
	d, start := l.data, t.start
	switch c := d[start]; {
	case c == '\'' || c == '"':
		t.quote = quoteAt(d, start)
	case c == '/' && regexOK:
		t.quote = slashy
	case regexOK && bytes.HasPrefix(d[start:], []byte("$/")):
		t.quote = dollarSlashy
	default:
		return false
	}
	t.kind = tokString
	l.pos += len(opening[t.quote])
	return true
}

// quoteAt is the form of the string whose ' or " is at off: single or
// double, or the triple form of either.
func quoteAt(d []byte, off int) quote {
	c, q := d[off], single
	if c == '"' {
		q = double
	}
	if bytes.HasPrefix(d[off:], []byte{c, c, c}) {
		q += tripleSingle - single
	}
	return q
}

// word reads the name or the number at t's start into t.
func (l *lexer) word(t *token, regexOK *bool) {
	d, start := l.data, t.start
	if isNameStart(d[start]) {
		l.pos = nameEnd(d, start)
		t.kind = tokName
		*regexOK = regexAfterName[string(d[start:l.pos])]
		return
	}
	l.pos = numberEnd(d, start)
	t.kind, *regexOK = tokNumber, false
}

// punct reads the @ or the operator at t's start into t, and keeps the
// count of brackets for it.
func (l *lexer) punct(t *token, regexOK *bool) error {
	if l.data[t.start] == '@' {
		l.pos++
		t.kind, *regexOK = tokAt, true
		return nil
	}
	t.kind, t.op = tokOp, operator(l.data[t.start:])
	l.pos += len(t.op)
	*regexOK = !valueEnds[t.op]
	return l.bracket(*t)
}

// bracket keeps the count of ( and [ for the bracket t: a { starts a level
// of its own, and the } that closes it returns to the level around it, as
// in groovy.g's pushParenLevel and popParenLevel. A ) or ] that closes
// nothing at its level is Groovy's error, and is refused here, before the
// count goes below 0 and lets a / after a line end start a slashy string.
func (l *lexer) bracket(t token) error {
	top := &l.parens[len(l.parens)-1]
	switch t.op {
	case "(", "[", "?[":
		*top++
	case ")", "]":
		if *top == 0 {
			return l.lexError(t.start, "a %s closes no ( or [", t.op)
		}
		*top--
	case "{":
		return l.enter(t.start)
	case "}":
		if len(l.parens) > 1 {
			l.parens = l.parens[:len(l.parens)-1]
		}
	}
	return nil
}

// enter starts a level of { } at offset off, or of a string's ${ }.
func (l *lexer) enter(off int) error {
	if len(l.parens) > core.MaxDepth {
		return core.TooDeep(l.lineOf(off))
	}
	l.parens = append(l.parens, 0)
	return nil
}

// inside reads the rest of a string token whose opening delimiter read
// has read, to its closing delimiter, with the code of each ${ } inside it
// and the strings inside that code.
func (l *lexer) inside() error {
	for len(l.stack) > 0 {
		if len(l.stack) > core.MaxDepth {
			return core.TooDeep(l.lineOf(l.pos))
		}
		top := &l.stack[len(l.stack)-1]
		var err error
		if top.q == 0 {
			err = l.interpolated(top)
		} else {
			err = l.text(top)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// text reads the text of the string of frame top, to its closing
// delimiter or to a ${ that opens code inside it.
func (l *lexer) text(top *frame) error {
	d, q, start := l.data, top.q, l.pos
	for {
		if err := l.textByte(q, start); err != nil {
			return err
		}
		// An escape comes before the end, so \" and $/ do not close.
		if n := escape(d, l.pos, q); n > 0 {
			l.pos += n
			continue
		}
		if bytes.HasPrefix(d[l.pos:], []byte(closing[q])) {
			l.closeString(top)
			return nil
		}
		if entered, err := l.plain(q); entered || err != nil {
			return err
		}
	}
}

// textByte refuses the string of form q that starts at offset start when
// the file ends in it, or a line ends in it and its form is one line long,
// and charges the meter for the byte at pos.
func (l *lexer) textByte(q quote, start int) error {
	if l.pos >= len(l.data) {
		return l.lexError(start, "a string is not closed: errata looked for its closing %s to the end of the file", closing[q])
	}
	if err := l.m.Charge(1); err != nil {
		return err
	}
	if c := l.data[l.pos]; (c == '\n' || c == '\r') && (q == single || q == double) {
		return l.lexError(l.pos, "a string in %s ends at the line's end; Groovy reads a string over several lines only in ''' or \"\"\"", opening[q])
	}
	return nil
}

// closeString moves past the closing delimiter of the string of frame top
// and leaves it. A string in the code of a ${ } goes to the code watcher.
func (l *lexer) closeString(top *frame) {
	l.pos += len(closing[top.q])
	t := top.tok
	l.stack = l.stack[:len(l.stack)-1]
	if len(l.stack) > 0 {
		t.end = l.pos
		l.watch(t)
	}
}

// plain moves past the text at pos in a string of form q: a $name the
// string interpolates, or one byte. At a ${ it opens the code inside and
// reports true.
func (l *lexer) plain(q quote) (bool, error) {
	d, p := l.data, l.pos
	if d[p] == '$' && interpolates(q) && p+1 < len(d) {
		if d[p+1] == '{' {
			return true, l.openCode()
		}
		if isNameStart(d[p+1]) && d[p+1] != '$' {
			l.pos = dottedEnd(d, p+1)
			return false, nil
		}
	}
	l.pos++
	return false, nil
}

// openCode moves past the ${ at pos and enters the code inside it.
func (l *lexer) openCode() error {
	if err := l.enter(l.pos); err != nil {
		return err
	}
	l.pos += 2
	l.stack = append(l.stack, frame{regexOK: true})
	if l.code != nil {
		l.code.enterCode()
	}
	return nil
}

// interpolated reads one token of the code of a ${ }, which ends at the }
// that closes it, and enters a string the token starts. The code watcher
// is handed every token but that }.
func (l *lexer) interpolated(top *frame) error {
	t, err := l.read(&top.regexOK)
	if err != nil {
		return err
	}
	switch {
	case t.kind == tokEOF:
		return l.lexError(l.pos, "a ${ in a string is not closed: errata looked for its } to the end of the file")
	case t.kind == tokString:
		l.stack = append(l.stack, frame{q: t.quote, tok: t})
		return nil
	case t.is("{"):
		top.braces++
	case t.is("}") && top.braces == 0:
		l.leaveCode()
		return nil
	case t.is("}"):
		top.braces--
	}
	l.watch(t)
	return nil
}

// leaveCode leaves the code of the ${ } on top of the stack.
func (l *lexer) leaveCode() {
	l.stack = l.stack[:len(l.stack)-1]
	if l.code != nil {
		l.code.leaveCode()
	}
}

// watch hands t to the code watcher, if there is one.
func (l *lexer) watch(t token) {
	if l.code != nil {
		l.code.codeToken(t)
	}
}

// escape is the length of an escape at off in a string of form q, which
// the string's end does not close on, or 0. In ' and " strings and their
// triple forms a backslash escapes the byte after it, or a CR LF; in a
// slashy string only \/ is an escape, and in a dollar slashy string only
// $$ and $/, since Groovy 2.4 reads a backslash there as itself and a / as
// the end when a $ follows it (groovy.g, DOLLAR_REGEXP_SYMBOL).
func escape(d []byte, off int, q quote) int {
	if off+1 >= len(d) {
		return 0
	}
	c, next := d[off], d[off+1]
	switch q {
	case slashy:
		if c == '\\' && next == '/' {
			return 2
		}
	case dollarSlashy:
		if dollarEscape(c, next) {
			return 2
		}
	default:
		return backslashEscape(d, off)
	}
	return 0
}

// dollarEscape reports whether the bytes c and next are an escape in a
// dollar slashy string: $$ or $/.
func dollarEscape(c, next byte) bool {
	return c == '$' && (next == '$' || next == '/')
}

// backslashEscape is the length of the escape at off, which a byte
// follows, in a ' or " string or a triple form of either: a backslash and
// a CR LF, a backslash and the byte after it, or 0.
func backslashEscape(d []byte, off int) int {
	if d[off] != '\\' {
		return 0
	}
	if d[off+1] == '\r' && off+2 < len(d) && d[off+2] == '\n' {
		return 3
	}
	return 2
}

// interpolates reports whether a string of form q reads ${ } and $name.
func interpolates(q quote) bool { return q != single && q != tripleSingle }

// lineEndAt reports whether a line end, a LF or a CR, starts at offset i
// of d. It reads the one byte: the lexer asks at every backslash, and a
// scan to the next line end there made a run of backslashes quadratic.
func lineEndAt(d []byte, i int) bool { return i < len(d) && (d[i] == '\n' || d[i] == '\r') }

// lineEnd is the offset of the line end at or after off, a LF or a CR, or
// the end of d: groovy.g's SL_COMMENT stops before either.
func lineEnd(d []byte, off int) int {
	if i := bytes.IndexAny(d[off:], "\r\n"); i >= 0 {
		return off + i
	}
	return len(d)
}

// isNameStart reports whether c starts a name: a letter, _, $, or a byte of
// a character outside ASCII, which Groovy reads as a letter.
func isNameStart(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_' || c == '$' || c >= 0x80
}

// nameEnd is the end of the name that starts at off.
func nameEnd(d []byte, off int) int {
	off++
	for off < len(d) && (isNameStart(d[off]) || '0' <= d[off] && d[off] <= '9') {
		off++
	}
	return off
}

// dottedEnd is the end of the $a.b.c a string interpolates, starting at
// the name after the $.
func dottedEnd(d []byte, off int) int {
	off = nameEnd(d, off)
	for off+1 < len(d) && d[off] == '.' && isNameStart(d[off+1]) && d[off+1] != '$' {
		off = nameEnd(d, off+1)
	}
	return off
}

// numberEnd is the end of the number that starts at off: its digits,
// letters and underscores, a fraction when a digit follows its point, and
// an exponent's sign.
func numberEnd(d []byte, off int) int {
	off = numberWord(d, off)
	if off+1 < len(d) && d[off] == '.' && isDigit(d[off+1]) {
		off = numberWord(d, off+1)
	}
	return off
}

// numberWord is the end of the digits, letters and underscores of a number
// from off, with the sign of an exponent among them.
func numberWord(d []byte, off int) int {
	for off < len(d) && (isNameStart(d[off]) && d[off] != '$' || isDigit(d[off])) {
		if signedExponent(d, off) {
			off += 2
		}
		off++
	}
	return off
}

// signedExponent reports whether an exponent with a sign and a digit, such
// as e+1, starts at off.
func signedExponent(d []byte, off int) bool {
	return (d[off] == 'e' || d[off] == 'E') && off+2 < len(d) && (d[off+1] == '+' || d[off+1] == '-') && isDigit(d[off+2])
}

// operators are Groovy's operators of more than one byte, longest first,
// so the lexer takes the longest that matches.
var operators = []string{
	">>>=", "<=>", "===", "!==", "==~", "**=", "<<=", ">>=", ">>>", "..<", "...", "?.@",
	"==", "!=", "<=", ">=", "&&", "||", "++", "--", "**", "+=", "-=", "*=", "/=", "%=", "&=", "^=", "|=",
	"=~", "<<", ">>", "->", "::", "?.", "*.", ".&", ".@", "?:", "?=", "..", "?[",
}

// operator is the operator at the start of d, one byte if no longer one
// matches.
func operator(d []byte) string {
	for _, op := range operators {
		if bytes.HasPrefix(d, []byte(op)) {
			return op
		}
	}
	return string(d[:1])
}

// valueEnds are the operators after which a / divides, as in Groovy's own
// lexer: a closing bracket and ++ or --.
var valueEnds = map[string]bool{")": true, "]": true, "}": true, "++": true, "--": true}

// regexAfterName are the names after which a / starts a slashy string. In
// Groovy 2.4, which Jenkins compiles a pipeline with, a / divides after a
// name and after every keyword but these six (groovy.g,
// isExpressionEndingToken), so return /re/ is a division there.
var regexAfterName = map[string]bool{
	"abstract": true, "const": true, "do": true, "final": true, "goto": true, "strictfp": true,
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool { return '0' <= c && c <= '9' }
