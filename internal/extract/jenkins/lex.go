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
	// line is the line pos is on, and lineAt the offset it was counted to.
	line, lineAt int
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
}

// frame is one level of the stack: a string of form q, or, when q is 0,
// the code of a ${ }, with the braces open inside it and whether a / there
// starts a slashy string.
type frame struct {
	q       quote
	braces  int
	regexOK bool
}

func newLexer(data []byte, m *core.Meter) *lexer {
	return &lexer{data: data, line: 1, regexOK: true, parens: []int{0}, m: m}
}

// lineOf is the line of offset off, which never moves back.
func (l *lexer) lineOf(off int) int {
	l.line += bytes.Count(l.data[l.lineAt:off], []byte("\n"))
	l.lineAt = off
	return l.line
}

// next is the next token. A string is read whole, with what it
// interpolates.
func (l *lexer) next() (token, error) {
	t, err := l.code(&l.regexOK)
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

// code reads one token of code at pos, skipping blanks and comments. A
// string token stops after its opening delimiter, for the caller to read
// the rest. regexOK is the code's state of whether a / starts a slashy
// string, which code updates for the token it reads.
func (l *lexer) code(regexOK *bool) (token, error) {
	d := l.data
	for {
		if err := l.m.Charge(1); err != nil {
			return token{}, err
		}
		if l.pos >= len(d) {
			return token{kind: tokEOF, start: len(d), end: len(d), line: l.lineOf(len(d))}, nil
		}
		c := d[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			l.pos++
			continue
		case c == '\\' && l.pos+1 < len(d) && d[l.pos+1] == '\n':
			// A backslash before a line end joins the two lines.
			l.pos += 2
			continue
		case c == '\\' && l.pos+2 < len(d) && d[l.pos+1] == '\r' && d[l.pos+2] == '\n':
			l.pos += 3
			continue
		case c == '#' && l.pos == 0 && len(d) > 1 && d[1] == '!':
			l.pos = lineEnd(d, 0)
			continue
		case c == '/' && l.pos+1 < len(d) && d[l.pos+1] == '/':
			l.pos = lineEnd(d, l.pos)
			continue
		case c == '/' && l.pos+1 < len(d) && d[l.pos+1] == '*':
			end := bytes.Index(d[l.pos+2:], []byte("*/"))
			if end < 0 {
				return token{}, l.lexError(l.pos, "a /* comment is not closed")
			}
			l.pos += 2 + end + 2
			continue
		}
		start := l.pos
		t := token{start: start, line: l.lineOf(start)}
		switch {
		case c == '\n':
			l.pos++
			t.kind = tokNewline
			if l.parens[len(l.parens)-1] == 0 {
				*regexOK = true
			}
		case c == '\'' || c == '"':
			t.kind, t.quote = tokString, single
			if c == '"' {
				t.quote = double
			}
			if bytes.HasPrefix(d[start:], []byte{c, c, c}) {
				t.quote += tripleSingle - single
			}
			l.pos += len(opening[t.quote])
			*regexOK = false
		case c == '/' && *regexOK:
			t.kind, t.quote = tokString, slashy
			l.pos++
			*regexOK = false
		case c == '$' && *regexOK && start+1 < len(d) && d[start+1] == '/':
			t.kind, t.quote = tokString, dollarSlashy
			l.pos += 2
			*regexOK = false
		case isNameStart(c):
			l.pos = nameEnd(d, start)
			t.kind = tokName
			*regexOK = regexAfterName[string(d[start:l.pos])]
		case '0' <= c && c <= '9':
			l.pos = numberEnd(d, start)
			t.kind, *regexOK = tokNumber, false
		case c == '@':
			l.pos++
			t.kind, *regexOK = tokAt, true
		default:
			t.kind, t.op = tokOp, operator(d[start:])
			l.pos += len(t.op)
			*regexOK = !valueEnds[t.op]
			if err := l.bracket(t); err != nil {
				return token{}, err
			}
		}
		t.end = l.pos
		return t, nil
	}
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

// inside reads the rest of a string token whose opening delimiter code
// has read, to its closing delimiter, with the code of each ${ } inside it
// and the strings inside that code.
func (l *lexer) inside() error {
	d := l.data
	for len(l.stack) > 0 {
		if len(l.stack) > core.MaxDepth {
			return core.TooDeep(l.lineOf(l.pos))
		}
		top := &l.stack[len(l.stack)-1]
		if top.q == 0 {
			if err := l.interpolated(top); err != nil {
				return err
			}
			continue
		}
		q := top.q
		start := l.pos
		for {
			if l.pos >= len(d) {
				return l.lexError(start, "a string is not closed: errata looked for its closing %s to the end of the file", closing[q])
			}
			if err := l.m.Charge(1); err != nil {
				return err
			}
			c := d[l.pos]
			if c == '\n' && (q == single || q == double) {
				return l.lexError(l.pos, "a string in %s ends at the line's end; Groovy reads a string over several lines only in ''' or \"\"\"", opening[q])
			}
			// An escape comes before the end, so \" and $/ do not close.
			if n := escape(d, l.pos, q); n > 0 {
				l.pos += n
				continue
			}
			if bytes.HasPrefix(d[l.pos:], []byte(closing[q])) {
				l.pos += len(closing[q])
				l.stack = l.stack[:len(l.stack)-1]
				break
			}
			if c == '$' && interpolates(q) && l.pos+1 < len(d) {
				if d[l.pos+1] == '{' {
					if err := l.enter(l.pos); err != nil {
						return err
					}
					l.pos += 2
					l.stack = append(l.stack, frame{regexOK: true})
					break
				}
				if isNameStart(d[l.pos+1]) && d[l.pos+1] != '$' {
					l.pos = dottedEnd(d, l.pos+1)
					continue
				}
			}
			l.pos++
		}
	}
	return nil
}

// interpolated reads one token of the code of a ${ }, which ends at the }
// that closes it, and enters a string the token starts.
func (l *lexer) interpolated(top *frame) error {
	t, err := l.code(&top.regexOK)
	if err != nil {
		return err
	}
	switch {
	case t.kind == tokEOF:
		return l.lexError(l.pos, "a ${ in a string is not closed: errata looked for its } to the end of the file")
	case t.kind == tokString:
		l.stack = append(l.stack, frame{q: t.quote})
	case t.is("{"):
		top.braces++
	case t.is("}") && top.braces == 0:
		l.stack = l.stack[:len(l.stack)-1]
	case t.is("}"):
		top.braces--
	}
	return nil
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
		if c == '$' && (next == '$' || next == '/') {
			return 2
		}
	default:
		if c == '\\' && next == '\r' && off+2 < len(d) && d[off+2] == '\n' {
			return 3
		}
		if c == '\\' {
			return 2
		}
	}
	return 0
}

// interpolates reports whether a string of form q reads ${ } and $name.
func interpolates(q quote) bool { return q != single && q != tripleSingle }

// lineEnd is the offset of the line end at or after off, or the end of d.
func lineEnd(d []byte, off int) int {
	if i := bytes.IndexByte(d[off:], '\n'); i >= 0 {
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
	word := func(off int) int {
		for off < len(d) && (isNameStart(d[off]) && d[off] != '$' || '0' <= d[off] && d[off] <= '9') {
			if (d[off] == 'e' || d[off] == 'E') && off+2 < len(d) && (d[off+1] == '+' || d[off+1] == '-') && '0' <= d[off+2] && d[off+2] <= '9' {
				off += 2
			}
			off++
		}
		return off
	}
	off = word(off)
	if off+1 < len(d) && d[off] == '.' && '0' <= d[off+1] && d[off+1] <= '9' {
		off = word(off + 1)
	}
	return off
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
