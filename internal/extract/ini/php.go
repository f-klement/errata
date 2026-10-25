package ini

import (
	"bytes"
	"errors"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// The php dialect reads php.ini as PHP reads it (zend_ini_scanner.l and
// zend_ini_parser.y in PHP 8.4, the normal mode PHP loads php.ini in), and
// returns each value as PHP's parser assembles it, before PHP evaluates it:
//
//   - ; outside quotes starts a comment, to the end of the line;
//   - [name] is a section; key = value; key[] and key[x] are the key;
//   - a value is words, numbers, "double quoted" strings with \" \\ and \$
//     escapes, 'single quoted' strings, and the blanks between words, joined
//     as PHP joins them: blanks before and after a quoted string go, so
//     a "b c" d is ab cd;
//   - a line PHP's grammar refuses, such as = in a value or a boolean word
//     as a key, is refused.
//
// What PHP then computes is returned as written and marked not literal: the
// words true, on, yes, false, off, no, none and null, which PHP reads as 1
// and the empty string; a constant such as E_ALL (isConstantName: UTC is a
// word PHP returns as written); ${VAR} and ${VAR:-x}; and an expression of
// | & ^ ~ ! and parentheses, which is returned as the whole value as
// written. A key with no = is ignored, as PHP ignores it. A file with a NUL
// byte is refused, since PHP stops reading at one. The dialect is chosen
// for php.ini and the names PHP ships it under (php.ini-production,
// php.ini-development, php.ini.dist), but not a template of one such as
// php.ini.j2, and for a .ini file under a directory named php.
var phpDialect = &iniDialect{
	name:       "php",
	notLiteral: computedEntry,
	files: func(file string) bool {
		dirs, base := core.FileParts(file)
		return strings.HasPrefix(base, "php.ini") && !core.IsTemplate(base) || (strings.HasSuffix(base, ".ini") && hasDir(dirs, "php"))
	},
	query: func(i Spec) (core.Query, error) {
		if err := onlyDialect(i, false, false); err != nil {
			return core.Query{}, err
		}
		if i.Key == "" {
			return core.Query{}, errors.New("key is required")
		}
		return core.Query{Section: []byte(i.Section), Key: []byte(i.Key), HasKey: true}, nil
	},
	read: readPHP,
}

// phpTokens and phpOperators are the bytes of TOKENS and OPERATORS, and the
// stops the bytes that end a run of VALUE_CHARS, SECTION_VALUE_CHARS and
// FALLBACK_CHARS.
var (
	phpTokens, phpOperators = byteSet(":,.[]\"'()&|^+-/*=%$!~<>?@{}"), byteSet("&|^~()!")
	phpValueStop            = byteSet("$= \t\n\r;&|^~()!\"'")
	phpSectionStop          = byteSet("$\n\r;\"'\\]")
	phpFallbackStop         = byteSet("$\n\r;\"'}\\")
)

func byteSet(s string) (set [256]bool) {
	for i := range len(s) {
		set[s[i]] = true
	}
	return set
}

// phpTok is a token of PHP's ini scanner.
type phpTok int

const (
	phpEnd      phpTok = iota // the end of the file, or a byte no rule takes
	phpEOL                    // the end of a line or a comment
	phpSection                // [
	phpLabel                  // a key
	phpOffset                 // a key and [
	phpEquals                 // =
	phpBool                   // true, on, yes, false, off, no, none or null
	phpChar                   // a character outside a value that starts nothing
	phpRaw                    // 'single quoted'
	phpConstant               // a word
	phpNumber                 //
	phpString                 // other value characters
	phpBlank                  // blanks inside a value
	phpQuote                  // a " that opens or closes a string
	phpQuoted                 // the text of a double quoted string
	phpDollar                 // ${
	phpVarname                //
	phpFallback               // :-
	phpRBrace                 // }
	phpRBracket               // ]
	phpOperator               // | & ^ ~ ! ( )
)

type phpState int

const (
	phpInitial phpState = iota
	phpValue
	phpSectionValue
	phpOffsetValue
	phpDoubleQuotes
	phpVarName
	phpVarFallback
)

// phpLexer is PHP's ini scanner. The text past the end of the file reads as
// NUL bytes, as PHP's buffer holds them, and a rule that reads past the
// first of them ends the file, as PHP's YYFILL does.
type phpLexer struct {
	data  []byte
	pos   int
	state phpState
	stack []phpState
	// The last token: its kind, where it starts and ends, and its text.
	tok        phpTok
	start, end int
	text       []byte
	buf        []byte // the text of a quoted string, unescaped
}

func (l *phpLexer) at(i int) byte {
	if i < len(l.data) {
		return l.data[i]
	}
	return 0
}

func (l *phpLexer) push(s phpState) { l.stack = append(l.stack, l.state); l.state = s }

func (l *phpLexer) pop() {
	if n := len(l.stack); n > 0 {
		l.state, l.stack = l.stack[n-1], l.stack[:n-1]
	}
}

func isPHPBlank(c byte) bool { return c == ' ' || c == '\t' }

func isPHPLabel(c byte) bool {
	switch c {
	case '=', '\n', '\r', '\t', ';', '&', '|', '^', '$', '~', '(', ')', '{', '}', '!', '"', '[', ']', 0:
		return false
	}
	return true
}

// phpMatch is one rule's longest match: its length, and whether reading it
// went past the first NUL after the file, which ends the file.
type phpMatch struct {
	n    int
	past bool
}

// run is how many bytes from i ok takes, stopping at the end of the file
// whatever ok takes.
func (l *phpLexer) run(i int, ok func(byte) bool) int {
	j := i
	for j < len(l.data) && ok(l.data[j]) {
		j++
	}
	return j - i
}

func (l *phpLexer) blanks(i int) int { return l.run(i, isPHPBlank) }

func (l *phpLexer) newline(i int) int {
	switch {
	case l.at(i) == '\r' && l.at(i+1) == '\n' && i+1 < len(l.data):
		return 2
	case (l.at(i) == '\r' || l.at(i) == '\n') && i < len(l.data):
		return 1
	}
	return 0
}

// word is the length of one of words at i, in any case, then blanks.
func (l *phpLexer) word(i int, words ...string) int {
	if c := lower(l.at(i)); c != 't' && c != 'o' && c != 'y' && c != 'f' && c != 'n' {
		return 0 // no boolean word starts so; the common case, checked first
	}
	best := 0
	for _, w := range words {
		if i+len(w) <= len(l.data) && bytes.EqualFold(l.data[i:i+len(w)], []byte(w)) {
			best = max(best, len(w))
		}
	}
	if best == 0 {
		return 0
	}
	return best + l.blanks(i+best)
}

func (l *phpLexer) constant(i int) int {
	c := l.at(i)
	if (!isAlpha(c) && c != '_') || i >= len(l.data) {
		return 0
	}
	j := i + 1
	for j < len(l.data) && (isAlpha(l.data[j]) || l.data[j] == '_' || isDigit(l.data[j])) {
		j++
	}
	return j - i
}

func (l *phpLexer) digits(i int) int {
	j := i
	for j < len(l.data) && isDigit(l.data[j]) {
		j++
	}
	return j - i
}

// number is [-]?[0-9]+ or [0-9]*.[0-9]+ or [0-9]+.[0-9]*, the longest.
func (l *phpLexer) number(i int) int {
	best := 0
	sign := 0
	if l.at(i) == '-' && i < len(l.data) {
		sign = 1
	}
	if d := l.digits(i + sign); d > 0 {
		best = sign + d
	}
	if d := l.digits(i); l.at(i+d) == '.' && i+d < len(l.data) {
		if f := l.digits(i + d + 1); f > 0 || d > 0 {
			best = max(best, d+1+f)
		}
	}
	return best
}

// chars is a run of PHP's value, section value or fallback characters at
// i: a byte not in stop (a NUL too where nul is set), \ and any byte where
// escape is set, and a $ not before { or NUL, or a $, \ and any byte. A
// NUL past the end of the file is any byte, so a run that takes it reads
// past the end.
func (l *phpLexer) chars(i int, stop *[256]bool, nul, escape bool) phpMatch {
	j := i
	for {
		c := l.at(j)
		w := 0
		switch {
		case c == '$':
			switch n := l.at(j + 1); {
			case n == '\\':
				w = 3
			case n != '{' && n != 0:
				w = 2
			}
		case c == '\\' && escape:
			w = 2
		case c == 0:
			if nul {
				w = 1
			}
		case !stop[c]:
			w = 1
		}
		if w == 0 {
			return phpMatch{j - i, false}
		}
		if j += w; j > len(l.data) {
			return phpMatch{j - i, true}
		}
	}
}

// next reads the next token, and reports false at the end of the file.
func (l *phpLexer) next() {
	l.start = l.pos
	l.text = nil
	for {
		l.start = l.pos
		if l.pos >= len(l.data) {
			l.end = l.pos
			if l.state == phpValue {
				l.state, l.tok = phpInitial, phpEOL
				return
			}
			l.tok = phpEnd
			return
		}
		if l.lex() {
			return
		}
	}
}

// best picks the longest of the rules' matches, the first among equals, and
// reports whether any went past the end of the file.
func best(ms ...phpMatch) (int, int, bool) {
	at, n, past := -1, 0, false
	for i, m := range ms {
		past = past || m.past
		if m.n > n {
			at, n = i, m.n
		}
	}
	return at, n, past
}

// lex reads one token in the current state, and reports false for blanks it
// skips.
func (l *phpLexer) lex() bool {
	i := l.pos
	switch l.state {
	case phpInitial:
		switch c := l.data[i]; {
		case c != ' ' && isPHPLabel(c):
			return l.lexLabel(i)
		case c == '\n' || c == '\r':
			l.pos = i + l.newline(i)
			l.end, l.tok = l.pos, phpEOL
			return true
		case c == '=':
			l.pos = i + 1 + l.blanks(i+1)
			l.end, l.tok, l.state = l.pos, phpEquals, phpValue
			return true
		}
		label := phpMatch{n: l.run(i, isPHPLabel)}
		offset := phpMatch{}
		if label.n > 0 && l.at(i+label.n) == '[' && i+label.n < len(l.data) {
			offset.n = label.n + 1 + l.blanks(i+label.n+1)
		}
		eq := phpMatch{}
		if b := l.blanks(i); l.at(i+b) == '=' && i+b < len(l.data) {
			eq.n = b + 1 + l.blanks(i+b+1)
		}
		tokens := phpMatch{}
		if phpTokens[l.at(i)] {
			tokens.n = 1
		}
		blank := phpMatch{n: l.blanks(i)}
		eol := phpMatch{}
		if b := l.blanks(i); l.newline(i+b) > 0 {
			eol.n = b + l.newline(i+b)
		}
		comment := l.comment(i)
		section := phpMatch{}
		if l.at(i) == '[' {
			section.n = 1
		}
		rule, n, past := best(section, offset, phpMatch{n: l.word(i, "true", "on", "yes")}, phpMatch{n: l.word(i, "false", "off", "no", "none")},
			phpMatch{n: l.word(i, "null")}, label, eq, tokens, blank, eol, comment)
		// No rule matches only a NUL, which readPHP refuses before it lexes.
		if past || rule < 0 {
			l.pos, l.tok = len(l.data)+1, phpEnd
			return true
		}
		l.pos = i + n
		l.end = l.pos
		switch rule {
		case 0:
			l.tok, l.state = phpSection, phpSectionValue
		case 1:
			l.tok, l.state = phpOffset, phpOffsetValue
			l.text = bytes.TrimRight(trimPHPSpace(l.data[i:i+n]), "[ \t\r\n")
		case 2, 3, 4:
			l.tok = phpBool
		case 5:
			l.tok, l.text = phpLabel, trimPHPSpace(l.data[i:i+n])
		case 6:
			l.tok, l.state = phpEquals, phpValue
		case 7:
			l.tok = phpChar
		case 8:
			return false
		case 9, 10:
			l.tok = phpEOL
		}
		return true
	case phpValue:
		return l.lexValue(i)
	case phpDoubleQuotes:
		return l.lexDoubleQuotes(i)
	case phpVarName:
		return l.lexVarName(i)
	default: // section, offset and fallback values
		return l.lexSectionValue(i)
	}
}

// lexLabel is lex in the initial state at a label byte other than a space,
// where only a section's [, a label, a label and [, and the boolean words
// can match: a blank, =, a line break and ; are no label bytes, and TOKENS
// loses a tie to the label, the earlier rule.
func (l *phpLexer) lexLabel(i int) bool {
	label := phpMatch{n: l.run(i, isPHPLabel)}
	n := label.n
	rule := 5
	if b := l.word(i, "true", "on", "yes", "false", "off", "no", "none", "null"); b >= n {
		n, rule = b, 2
	}
	if label.n > 0 && l.at(i+label.n) == '[' && i+label.n < len(l.data) {
		if o := label.n + 1 + l.blanks(i+label.n+1); o > n {
			n, rule = o, 1
		}
	}
	l.pos = i + n
	l.end = l.pos
	switch rule {
	case 1:
		l.tok, l.state = phpOffset, phpOffsetValue
		l.text = bytes.TrimRight(trimPHPSpace(l.data[i:i+n]), "[ \t\r\n")
	case 2:
		l.tok = phpBool
	default:
		l.tok, l.text = phpLabel, trimPHPSpace(l.data[i:i+n])
	}
	return true
}

// lexWord is lexValue at a letter, a digit, _, - or ., where only the
// boolean words, a constant, a number and a run of value bytes can match,
// the first of them among equals.
func (l *phpLexer) lexWord(i int) bool {
	chars := l.chars(i, &phpValueStop, false, false)
	if chars.past {
		l.pos, l.tok = len(l.data)+1, phpEnd
		return true
	}
	n, tok := chars.n, phpString
	if c := l.constant(i); c >= n {
		n, tok = c, phpConstant
	}
	if c := l.number(i); c > 0 && c >= n {
		n, tok = c, phpNumber
	}
	if c := l.word(i, "true", "on", "yes", "false", "off", "no", "none", "null"); c > 0 && c >= n {
		n, tok = c, phpBool
	}
	l.pos = i + n
	l.end, l.tok = l.pos, tok
	if tok == phpBool {
		l.text = bytes.TrimRight(l.data[i:i+n], " \t")
	} else {
		l.text = l.data[i : i+n]
	}
	return true
}

// comment is [ \t]*;[^\r\n]*{NEWLINE}.
func (l *phpLexer) comment(i int) phpMatch {
	b := l.blanks(i)
	if l.at(i+b) != ';' || i+b >= len(l.data) {
		return phpMatch{}
	}
	j := i + b + 1
	for {
		if j >= len(l.data) {
			return phpMatch{j - i, true} // [^\r\n] takes the NULs after the file
		}
		if c := l.data[j]; c == '\r' || c == '\n' {
			return phpMatch{j - i + l.newline(j), false}
		}
		j++
	}
}

// single is '[^']+'.
func (l *phpLexer) single(i int) phpMatch {
	if l.at(i) != '\'' || i >= len(l.data) {
		return phpMatch{}
	}
	end := bytes.IndexByte(l.data[i+1:], '\'')
	switch {
	case end < 0:
		return phpMatch{len(l.data) - i, true}
	case end == 0:
		return phpMatch{}
	}
	return phpMatch{end + 2, false}
}

// quoteStart is [ \t]*".
func (l *phpLexer) quoteStart(i int) phpMatch {
	if b := l.blanks(i); l.at(i+b) == '"' && i+b < len(l.data) {
		return phpMatch{n: b + 1}
	}
	return phpMatch{}
}

func (l *phpLexer) dollar(i int) phpMatch {
	if l.at(i) == '$' && l.at(i+1) == '{' && i+1 < len(l.data) {
		return phpMatch{n: 2}
	}
	return phpMatch{}
}

func (l *phpLexer) lexValue(i int) bool {
	if c := l.data[i]; !phpValueStop[c] && c != 0 && c != '-' && c != '.' && !isDigit(c) && !isAlpha(c) && c != '_' {
		// A value byte no other rule starts with: a run of them, or $.
		m := l.chars(i, &phpValueStop, false, false)
		if m.past {
			l.pos, l.tok = len(l.data)+1, phpEnd
			return true
		}
		if m.n > 0 {
			l.pos = i + m.n
			l.end, l.tok, l.text = l.pos, phpString, l.data[i:l.pos]
			return true
		}
	}
	switch c := l.data[i]; {
	case isAlpha(c) || isDigit(c) || c == '_' || c == '-' || c == '.':
		return l.lexWord(i)
	case c == '\n' || c == '\r':
		l.pos = i + l.newline(i)
		l.end, l.tok, l.state = l.pos, phpEOL, phpInitial
		return true
	}
	single := l.single(i)
	eol := phpMatch{}
	if b := l.blanks(i); l.newline(i+b) > 0 {
		eol.n = b + l.newline(i+b)
	}
	op := phpMatch{}
	if phpOperators[l.at(i)] {
		op.n = 1 + l.blanks(i+1)
	}
	eq := phpMatch{}
	if l.at(i) == '=' {
		eq.n = 1
	}
	chars := l.chars(i, &phpValueStop, false, false)
	any := phpMatch{n: 1}
	rule, n, past := best(single, l.dollar(i), phpMatch{n: l.word(i, "true", "on", "yes")}, phpMatch{n: l.word(i, "false", "off", "no", "none")},
		phpMatch{n: l.word(i, "null")}, eol, phpMatch{n: l.constant(i)}, phpMatch{n: l.number(i)}, op, eq, chars, l.quoteStart(i),
		phpMatch{n: l.blanks(i)}, l.comment(i), any)
	if past {
		l.pos, l.tok = len(l.data)+1, phpEnd
		return true
	}
	l.pos = i + n
	l.end = l.pos
	switch rule {
	case 0:
		l.tok, l.text = phpRaw, l.data[i+1:i+n-1]
	case 1:
		l.tok = phpDollar
		l.push(phpVarName)
	case 2, 3, 4:
		l.tok, l.text = phpBool, bytes.TrimRight(l.data[i:i+n], " \t")
	case 5, 13:
		l.tok, l.state = phpEOL, phpInitial
	case 6:
		l.tok, l.text = phpConstant, l.data[i:i+n]
	case 7:
		l.tok, l.text = phpNumber, l.data[i:i+n]
	case 8:
		l.tok, l.text = phpOperator, l.data[i:i+1]
	case 9:
		// = ends the value without being read: the next statement starts at it.
		l.pos, l.end = i, i
		l.tok, l.state = phpEOL, phpInitial
	case 10:
		l.tok, l.text = phpString, l.data[i:i+n]
	case 11:
		l.tok = phpQuote
		l.push(phpDoubleQuotes)
	case 12:
		l.tok, l.text = phpBlank, l.data[i:i+n]
	default: // a byte no other rule takes ends the value
		l.tok, l.state = phpEOL, phpInitial
	}
	return true
}

func (l *phpLexer) lexSectionValue(i int) bool {
	var ms []phpMatch
	switch l.state {
	case phpVarFallback:
		ms = []phpMatch{{}, l.dollar(i), {}, {n: l.constant(i)}, {n: l.number(i)}, l.chars(i, &phpFallbackStop, true, true), l.quoteStart(i), {n: l.blanks(i)}}
		if l.at(i) == '}' && i < len(l.data) {
			ms[0] = phpMatch{n: 1}
		}
	default:
		end := phpMatch{}
		if l.at(i) == ']' && i < len(l.data) {
			if l.state == phpSectionValue {
				end.n = 1 + l.blanks(i+1)
				end.n += l.newline(i + end.n)
			} else {
				end.n = 1
			}
		} else if l.state == phpOffsetValue {
			if b := l.blanks(i); l.at(i+b) == ']' && i+b < len(l.data) {
				end.n = b + 1
			}
		}
		ms = []phpMatch{end, l.dollar(i), l.single(i), {n: l.constant(i)}, {n: l.number(i)}, l.chars(i, &phpSectionStop, true, true), l.quoteStart(i), {n: l.blanks(i)}}
	}
	rule, n, past := best(ms...)
	if past || n == 0 {
		l.pos, l.tok = len(l.data)+1, phpEnd
		return true
	}
	l.pos = i + n
	l.end = l.pos
	switch rule {
	case 0:
		if l.state == phpVarFallback {
			l.tok = phpRBrace
			l.pop()
		} else {
			l.tok, l.state = phpRBracket, phpInitial
		}
	case 1:
		l.tok = phpDollar
		l.push(phpVarName)
	case 2:
		l.tok, l.text = phpRaw, l.data[i+1:i+n-1]
	case 3:
		l.tok, l.text = phpConstant, l.data[i:i+n]
	case 4:
		l.tok, l.text = phpNumber, l.data[i:i+n]
	case 5:
		l.tok, l.text = phpString, l.data[i:i+n]
	case 6:
		l.tok = phpQuote
		l.push(phpDoubleQuotes)
	default:
		l.tok, l.text = phpBlank, l.data[i:i+n]
	}
	return true
}

func (l *phpLexer) lexDoubleQuotes(i int) bool {
	switch {
	case l.dollar(i).n > 0:
		l.pos, l.end, l.tok = i+2, i+2, phpDollar
		l.push(phpVarName)
		return true
	case l.at(i) == '"':
		l.pos = i + 1 + l.blanks(i+1)
		l.end, l.tok = l.pos, phpQuote
		l.pop()
		return true
	}
	s := i
	for s < len(l.data) {
		c := l.data[s]
		s++
		switch c {
		case '"':
		case '$':
			if s < len(l.data) && l.data[s] == '{' {
				break
			}
			continue
		case '\\':
			if s < len(l.data) {
				escaped := l.data[s]
				s++
				// key="C:\path\" ends at that ", as PHP reads a Windows path.
				if escaped == '"' && (s >= len(l.data) || l.data[s] == '\n' || l.data[s] == '\r') {
					break
				}
			}
			continue
		default:
			continue
		}
		s--
		break
	}
	l.pos, l.end, l.tok = s, s, phpQuoted
	l.buf = phpUnescape(l.buf[:0], l.data[i:s])
	l.text = l.buf
	return true
}

// phpUnescape is zend_ini_escape_string for a double quoted string: \" \\
// and \$ are the character, and any other \ stays with the byte after it.
func phpUnescape(out, in []byte) []byte {
	for i := 0; i < len(in); i++ {
		if in[i] != '\\' {
			out = append(out, in[i])
			continue
		}
		if i++; i == len(in) {
			out = append(out, '\\')
			continue
		}
		switch in[i] {
		case '"', '\\', '$':
			out = append(out, in[i])
		default:
			out = append(out, '\\', in[i])
		}
	}
	return out
}

func (l *phpLexer) lexVarName(i int) bool {
	switch {
	case l.at(i) == ':' && l.at(i+1) == '-' && i+1 < len(l.data):
		l.pos, l.end, l.tok = i+2, i+2, phpFallback
		l.pop()
		l.push(phpVarFallback)
		return true
	case l.at(i) == '}':
		l.pos, l.end, l.tok = i+1, i+1, phpRBrace
		l.pop()
		return true
	case !isPHPLabel(l.at(i)):
		l.pos, l.tok = len(l.data)+1, phpEnd
		return true
	}
	// PHP checks for :- only after the name's first byte, and then skips the
	// : and starts the fallback at the -, with no name: ${a:-x} is refused.
	if l.at(i+1) == ':' && l.at(i+2) == '-' && i+2 < len(l.data) {
		l.pos, l.end, l.tok = i+2, i+2, phpFallback
		l.pop()
		l.push(phpVarFallback)
		return true
	}
	j := i + 1
	for ; j < len(l.data); j++ {
		c := l.data[j]
		if c == ':' && j+1 < len(l.data) && l.data[j+1] == '-' {
			break
		}
		if c != ':' && c != 0 && !isPHPLabel(c) {
			break
		}
	}
	name := l.data[i:j]
	if j == len(l.data) {
		name = l.data[i : i+1] // PHP's name stays one byte when it runs to the end
	}
	l.pos, l.end, l.tok, l.text = j, j, phpVarname, trimPHPSpace(name)
	return true
}

// trimPHPSpace is EAT_LEADING_WHITESPACE and EAT_TRAILING_WHITESPACE.
func trimPHPSpace(b []byte) []byte { return bytes.Trim(b, " \t\r\n") }

// phpParser is PHP's ini grammar over the lexer's tokens, assembling each
// value's text as the grammar joins it, without evaluating it.
type phpParser struct {
	l        *phpLexer
	lines    *core.LineCursor
	section  []byte
	seq      int
	value    []byte
	computed bool
	back     bool // the last token is read again by the next read
	listed   bool // the last list read an item
	itemEnd  int  // where the last token of the value ends
	depth    int  // how deeply ~, ! and ( nest in the value
	m        *core.Meter
	err      error  // the meter's, once it is spent
	discard  bool   // inside a fallback: read the list, keep no text
	name     []byte // a section's name as it is read
	scratch  []byte // an offset's or a fallback's text, which errata drops
}

var (
	errPHPSyntax = errors.New("syntax")
	errPHPDeep   = errors.New("deep")
)

func (p *phpParser) read() phpTok {
	if p.back {
		p.back = false
		return p.l.tok
	}
	p.l.next()
	if p.err = p.m.Charge(tokenWork + p.l.end - p.l.start); p.err != nil {
		p.l.tok = phpEnd // the read stops, and readPHP reports the meter
	}
	return p.l.tok
}

// took records that the value holds the token just read.
func (p *phpParser) took() { p.itemEnd = p.l.end }

func readPHP(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return core.LineError(bytes.Count(data[:i], newline)+1, "a NUL byte, at which PHP stops reading the file")
	}
	l := &phpLexer{data: data}
	if len(data) > 3 && bytes.HasPrefix(data, bom) {
		l.pos = len(bom)
	}
	p := &phpParser{l: l, lines: core.NewLineCursor(data), m: m}
	var e core.Entry
	for {
		tok := p.read()
		line := p.lines.At(min(l.start, len(data)))
		var err error
		switch tok {
		case phpEnd:
			return p.err
		case phpEOL:
			continue
		case phpSection:
			var name []byte
			if name, err = p.list(p.name[:0], true); err == nil && p.read() != phpRBracket {
				err = errPHPSyntax
			}
			p.name = name
			if err == nil {
				p.section = append(p.section[:0], name...)
				p.seq++
				continue
			}
		case phpLabel, phpOffset:
			e.Key = append(e.Key[:0], l.text...)
			key := e.Key
			if tok == phpOffset {
				if p.scratch, err = p.list(p.scratch[:0], false); err == nil && (p.read() != phpRBracket || p.read() != phpEquals) {
					err = errPHPSyntax
				}
			} else if p.read() != phpEquals {
				p.back = true // a key with no value: PHP ignores it
				continue
			}
			if err == nil {
				if err = p.valueOf(); err == nil {
					e = core.Entry{Section: p.section, SectionSeq: p.seq, Key: key, Value: p.value, Line: line, Computed: p.computed}
					if err := visit(&e); err != nil {
						return err
					}
					continue
				}
			}
		default:
			err = errPHPSyntax
		}
		switch {
		case p.err != nil: // the work budget ran out inside the line
			return p.err
		case errors.Is(err, errPHPSyntax):
			return core.LineError(line, "PHP refuses this line: %s", core.Quote(string(phpLine(data, l.start))))
		case errors.Is(err, errPHPDeep):
			return core.TooDeep(line)
		}
		return err
	}
}

// phpLine is the line of data that holds offset i, for a message.
func phpLine(data []byte, i int) []byte {
	i = min(i, len(data))
	start := bytes.LastIndexByte(data[:i], '\n') + 1
	end := bytes.IndexByte(data[i:], '\n')
	if end < 0 {
		return data[start:]
	}
	return data[start : i+end]
}

// valueOf reads string_or_value after a key's =.
func (p *phpParser) valueOf() error {
	p.value, p.computed = p.value[:0], false
	start := p.l.end
	switch p.read() {
	case phpBool:
		p.value, p.computed = append(p.value, p.l.text...), true
		return nil
	case phpEOL:
		return nil
	case phpEnd:
		// The end of the file comes as an end of line in a value; an end
		// here is a rule that read past the file, such as a ' left open.
		return errPHPSyntax
	default: // the start of an expression, read again by expr
	}
	p.back = true
	ops, err := p.expr()
	if err != nil {
		return err
	}
	if ops {
		p.value, p.computed = append(p.value[:0], bytes.Trim(p.l.data[start:min(p.itemEnd, len(p.l.data))], " \t")...), true
	}
	return nil
}

// expr reads unary (op unary)*, and reports whether it held an operator.
func (p *phpParser) expr() (bool, error) {
	ops, err := p.unary()
	if err != nil {
		return false, err
	}
	for {
		if p.read() != phpOperator || !bytes.ContainsAny(p.l.text, "|&^") {
			p.back = true
			return ops, nil
		}
		p.took()
		if _, err := p.unary(); err != nil {
			return false, err
		}
		ops = true
	}
}

func (p *phpParser) unary() (bool, error) {
	if p.read() == phpOperator {
		// PHP's parser stops near 10000 levels (9993 parentheses, measured);
		// errata stops sooner, at the depth every format has, and refuses
		// what lies between.
		if p.depth++; p.depth > core.MaxDepth {
			return false, errPHPDeep
		}
		defer func() { p.depth-- }()
		p.took()
		switch p.l.text[0] {
		case '~', '!':
			_, err := p.unary()
			return true, err
		case '(':
			if _, err := p.expr(); err != nil {
				return false, err
			}
			if p.read() != phpOperator || p.l.text[0] != ')' {
				return false, errPHPSyntax
			}
			p.took()
			return true, nil
		}
		return false, errPHPSyntax
	}
	p.back = true
	var err error
	if p.value, err = p.list(p.value, false); err != nil {
		return false, err
	}
	if !p.listed {
		return false, errPHPSyntax
	}
	return false, nil
}

// list reads a var_string_list, or with section a var_string_list_section,
// and returns its text. listed reports whether it read an item.
func (p *phpParser) list(out []byte, section bool) ([]byte, error) {
	p.listed = false
	for {
		switch p.read() {
		case phpConstant:
			if !section && isConstantName(p.l.text) {
				p.computed = true
			}
			if !p.discard {
				out = append(out, p.l.text...)
			}
		case phpRaw, phpNumber, phpString, phpBlank:
			if !p.discard {
				out = append(out, p.l.text...)
			}
		case phpDollar:
			start := p.l.start
			if err := p.ref(); err != nil {
				return nil, err
			}
			if !p.discard {
				out = append(out, p.l.data[start:p.l.end]...)
			}
			p.computed = true
		case phpQuote:
			for {
				t := p.read()
				if t == phpQuote {
					break
				}
				switch t {
				case phpQuoted:
					if !p.discard {
						out = append(out, p.l.text...)
					}
				case phpDollar:
					start := p.l.start
					if err := p.ref(); err != nil {
						return nil, err
					}
					if !p.discard {
						out = append(out, p.l.data[start:p.l.end]...)
					}
					p.computed = true
				default:
					return nil, errPHPSyntax
				}
			}
		default:
			p.back = true
			return out, nil
		}
		p.listed = true
		p.took()
	}
}

// ref reads the rest of ${name} or ${name:-fallback}.
func (p *phpParser) ref() error {
	// A fallback holds a list, which holds references: each ${ is a level of
	// the value's depth, as ~, ! and ( are, so nested fallbacks stop at
	// maxDepth and cost time in proportion to the file.
	if p.depth++; p.depth > core.MaxDepth {
		return errPHPDeep
	}
	defer func() { p.depth-- }()
	if p.read() != phpVarname {
		return errPHPSyntax
	}
	switch p.read() {
	case phpRBrace:
		return nil
	case phpFallback:
		listed := p.listed
		var err error
		// A fallback's text is in the reference's source, which the list
		// around it copies once; reading it into text at every level would
		// copy the nested text once for each level.
		discard := p.discard
		p.discard = true
		p.scratch, err = p.list(p.scratch[:0], false)
		p.discard = discard
		if err != nil {
			return err
		}
		p.listed = listed
		if p.read() != phpRBrace {
			return errPHPSyntax
		}
		return nil
	default:
		return errPHPSyntax
	}
}

// isConstantName reports whether b is named as the constants PHP replaces
// in php.ini are: a capital letter or _, then capital letters, digits and
// _ up to a _, and after it letters of either case too, such as E_ALL,
// PHP_EOL and STREAM_CRYPTO_METHOD_TLSv1_2_CLIENT; and the nine standard
// names with no _ (phpBareConstants). PHP replaces such a word with its
// value, and returns any other word, such as UTC or Neutral, as written.
// Of the constants of PHP 8.4.23 with 40 extensions, it leaves these
// literal: pcntl's SIG* and W* and sockets' SOMAXCONN, which extensions
// only some builds load define, and STDIN, STDOUT and STDERR, which only
// the CLI defines; each is also a word a value may hold as text.
func isConstantName(b []byte) bool {
	if phpBareConstants[string(b)] {
		return true
	}
	if len(b) == 0 || b[0] != '_' && (b[0] < 'A' || b[0] > 'Z') {
		return false
	}
	under := false
	for _, c := range b {
		switch {
		case c == '_':
			under = true
		case 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case under && 'a' <= c && c <= 'z':
		default:
			return false
		}
	}
	return under
}

// phpBareConstants are the constants of PHP's standard extension whose
// names hold no _ (nl_langinfo's).
var phpBareConstants = map[string]bool{
	"CODESET": true, "ERA": true, "RADIXCHAR": true, "THOUSEP": true, "YESEXPR": true,
	"NOEXPR": true, "YESSTR": true, "NOSTR": true, "CRNCYSTR": true,
}
