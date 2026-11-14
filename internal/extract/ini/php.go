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
// and the empty string; a constant PHP defines before it reads php.ini, such
// as E_ALL (isStartupConstant: UTC and an extension's M_PI are words PHP
// returns as written); ${VAR} and ${VAR:-x}; and an expression of
// | & ^ ~ ! and parentheses, which is returned as the whole value as
// written. A key with no = is ignored, as PHP ignores it. A file with a NUL
// byte is refused, since PHP stops reading at one. The dialect is chosen
// for php.ini and the names PHP ships it under (php.ini-production,
// php.ini-development, php.ini.dist), and for a .ini file under a
// directory named php; a template such as php.ini.j2 is chosen by the name
// without its suffix (chooseDialect), as every dialect is.
var phpDialect = &iniDialect{
	name:       "php",
	notLiteral: computedEntry,
	files: func(file string) bool {
		dirs, base := core.FileParts(file)
		return strings.HasPrefix(base, "php.ini") || (strings.HasSuffix(base, ".ini") && hasDir(dirs, "php"))
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
	read:    readPHP,
	readFor: phpReadFor,
}

// phpReadFor is the reader of file in the php dialect, by when PHP reads
// it. PHP reads php.ini and the .ini files of its scan directory at
// startup, before any extension defines its constants (isStartupConstant),
// and errata reads every .ini so, wherever a repository keeps it, such as
// docker/opcache.ini. PHP reads a .user.ini for each request, and an FPM
// pool file after the extensions start, when every constant is defined;
// so errata marks every name of a constant's shape (isConstantName) in a
// .user.ini and in any file whose name does not end in .ini, such as
// www.conf.
func phpReadFor(file string) core.ReadFunc {
	_, base := core.FileParts(core.Untemplated(file))
	if strings.HasPrefix(base, "php.ini") || base != ".user.ini" && strings.HasSuffix(base, ".ini") {
		return readPHP
	}
	return func(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
		return readPHPWith(data, m, isConstantName, visit)
	}
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

// phpTrue and phpFalse are the words PHP's scanner reads as a boolean, and
// phpNull the word it reads as null, in any case; phpWords is all of them.
// A key that is one of them is refused, as PHP refuses it.
var (
	phpTrue  = []string{"true", "on", "yes"}
	phpFalse = []string{"false", "off", "no", "none"}
	phpNull  = "null"
	phpWords = append(append(append([]string{}, phpTrue...), phpFalse...), phpNull)
)

// word is the length of one of words at i, in any case, then blanks.
func (l *phpLexer) word(i int, words ...string) int {
	if !startsPHPWord(lower(l.at(i))) {
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

// startsPHPWord reports whether c, in lower case, starts a boolean word.
func startsPHPWord(c byte) bool {
	switch c {
	case 't', 'o', 'y', 'f', 'n':
		return true
	}
	return false
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
		w := l.charWidth(j, stop, nul, escape)
		if w == 0 {
			return phpMatch{j - i, false}
		}
		if j += w; j > len(l.data) {
			return phpMatch{j - i, true}
		}
	}
}

// charWidth is how many bytes the character chars reads at j takes, or 0
// where the run stops.
func (l *phpLexer) charWidth(j int, stop *[256]bool, nul, escape bool) int {
	switch c := l.at(j); {
	case c == '$':
		return l.dollarWidth(j)
	case c == '\\' && escape:
		return 2
	case c == 0:
		if nul {
			return 1
		}
	case !stop[c]:
		return 1
	}
	return 0
}

// dollarWidth is the width of a run's character at a $ at j: $, \ and any
// byte; $ and a byte other than { or NUL; or 0, where the run stops.
func (l *phpLexer) dollarWidth(j int) int {
	switch n := l.at(j + 1); {
	case n == '\\':
		return 3
	case n != '{' && n != 0:
		return 2
	}
	return 0
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
		return l.lexInitial(i)
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

// lexInitial is lex in the initial state: a byte only one rule can start
// at, or the longest of the initial state's rules.
func (l *phpLexer) lexInitial(i int) bool {
	if l.lexInitialByte(i) {
		return true
	}
	ms := l.initialMatches(i)
	rule, n, past := best(ms[:]...)
	// No rule matches only a NUL, which readPHP refuses before it lexes.
	if past || rule < 0 {
		l.pos, l.tok = len(l.data)+1, phpEnd
		return true
	}
	l.pos = i + n
	l.end = l.pos
	return phpInitialRules[rule](l, i, n)
}

// lexInitialByte is lexInitial at a label byte, a line break or =, which
// decide the token on their own. It reports false at any other byte.
func (l *phpLexer) lexInitialByte(i int) bool {
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
	return false
}

// initialMatches are the matches of the initial state's rules at i, in the
// order phpInitialRules takes them.
func (l *phpLexer) initialMatches(i int) [11]phpMatch {
	label := phpMatch{n: l.run(i, isPHPLabel)}
	tokens := phpMatch{}
	if phpTokens[l.at(i)] {
		tokens.n = 1
	}
	section := phpMatch{}
	if l.at(i) == '[' {
		section.n = 1
	}
	return [...]phpMatch{section, {n: l.offsetAfter(i, label.n)}, {n: l.word(i, phpTrue...)}, {n: l.word(i, phpFalse...)},
		{n: l.word(i, phpNull)}, label, l.equalsAfterBlanks(i), tokens, {n: l.blanks(i)}, l.eolAfterBlanks(i), l.comment(i)}
}

// offsetAfter is the length of a label of n bytes at i, its [ and the blanks
// after it, or 0 where no [ follows the label.
func (l *phpLexer) offsetAfter(i, n int) int {
	if n > 0 && l.at(i+n) == '[' && i+n < len(l.data) {
		return n + 1 + l.blanks(i+n+1)
	}
	return 0
}

// equalsAfterBlanks is [ \t]*=[ \t]*.
func (l *phpLexer) equalsAfterBlanks(i int) phpMatch {
	if b := l.blanks(i); l.at(i+b) == '=' && i+b < len(l.data) {
		return phpMatch{n: b + 1 + l.blanks(i+b+1)}
	}
	return phpMatch{}
}

// phpInitialRules are the initial state's rules, by initialMatches' order.
// Each reports false for blanks lex skips.
var phpInitialRules = [...]func(l *phpLexer, i, n int) bool{
	func(l *phpLexer, _, _ int) bool { l.tok, l.state = phpSection, phpSectionValue; return true },
	func(l *phpLexer, i, n int) bool { l.takeOffset(i, n); return true },
	phpInitialBool, phpInitialBool, phpInitialBool,
	func(l *phpLexer, i, n int) bool { l.tok, l.text = phpLabel, trimPHPSpace(l.data[i:i+n]); return true },
	func(l *phpLexer, _, _ int) bool { l.tok, l.state = phpEquals, phpValue; return true },
	func(l *phpLexer, _, _ int) bool { l.tok = phpChar; return true },
	func(*phpLexer, int, int) bool { return false },
	phpInitialEOL,
	phpInitialEOL,
}

// phpInitialBool is a boolean word outside a value, which only a key can be.
func phpInitialBool(l *phpLexer, _, _ int) bool { l.tok = phpBool; return true }

// phpInitialEOL is the end of a line or a comment outside a value.
func phpInitialEOL(l *phpLexer, _, _ int) bool { l.tok = phpEOL; return true }

// takeOffset is the token of a key and its [, n bytes at i, which starts
// the offset's value.
func (l *phpLexer) takeOffset(i, n int) {
	l.tok, l.state = phpOffset, phpOffsetValue
	l.text = bytes.TrimRight(trimPHPSpace(l.data[i:i+n]), "[ \t\r\n")
}

// lexLabel is lex in the initial state at a label byte other than a space,
// where only a section's [, a label, a label and [, and the boolean words
// can match: a blank, =, a line break and ; are no label bytes, and TOKENS
// loses a tie to the label, the earlier rule.
func (l *phpLexer) lexLabel(i int) bool {
	label := phpMatch{n: l.run(i, isPHPLabel)}
	n := label.n
	rule := 5
	if b := l.word(i, phpWords...); b >= n {
		n, rule = b, 2
	}
	if o := l.offsetAfter(i, label.n); o > n {
		n, rule = o, 1
	}
	l.pos = i + n
	l.end = l.pos
	switch rule {
	case 1:
		l.takeOffset(i, n)
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
	// c > 0 decides nothing here while lexValue, the one caller, calls this
	// only at a byte chars reads, so n is at least 1: its mutant c >= 0 is
	// equivalent (lonecr delta review 3).
	if c := l.word(i, phpWords...); c > 0 && c >= n {
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

// lexValue is lex in a value: a run of value bytes, a word or a number, a
// line break, or the longest of the value's other rules.
func (l *phpLexer) lexValue(i int) bool {
	if l.lexValueRun(i) {
		return true
	}
	switch c := l.data[i]; {
	case isPHPWordByte(c):
		return l.lexWord(i)
	case c == '\n' || c == '\r':
		l.pos = i + l.newline(i)
		l.end, l.tok, l.state = l.pos, phpEOL, phpInitial
		return true
	}
	ms := l.valueMatches(i)
	rule, n, past := best(ms[:]...)
	if past {
		l.pos, l.tok = len(l.data)+1, phpEnd
		return true
	}
	l.pos = i + n
	l.end = l.pos
	phpValueRules[rule](l, i, n)
	return true
}

// isPHPWordByte reports whether c is a byte lexWord reads in a value: a
// letter, a digit, _, - or .
func isPHPWordByte(c byte) bool {
	return isAlpha(c) || isDigit(c) || c == '_' || c == '-' || c == '.'
}

// lexValueRun is lexValue at a value byte no other rule starts with: a run
// of them, or $. It reports false at any other byte, and where the run is
// empty.
func (l *phpLexer) lexValueRun(i int) bool {
	if c := l.data[i]; phpValueStop[c] || c == 0 || isPHPWordByte(c) {
		return false
	}
	m := l.chars(i, &phpValueStop, false, false)
	if m.past {
		l.pos, l.tok = len(l.data)+1, phpEnd
		return true
	}
	if m.n == 0 {
		return false
	}
	l.pos = i + m.n
	l.end, l.tok, l.text = l.pos, phpString, l.data[i:l.pos]
	return true
}

// valueMatches are the matches of a value's rules at i, in the order
// phpValueRules takes them, which is PHP's order. The boolean words (2 to
// 4), a constant (6) and a number (7) start only at a byte isPHPWordByte
// takes, where lexValue hands the value to lexWord before it builds these
// matches; so here they never win, and their rules never run. They stay
// so the table keeps PHP's rule order.
func (l *phpLexer) valueMatches(i int) [15]phpMatch {
	op := phpMatch{}
	if phpOperators[l.at(i)] {
		op.n = 1 + l.blanks(i+1)
	}
	eq := phpMatch{}
	if l.at(i) == '=' {
		eq.n = 1
	}
	return [...]phpMatch{l.single(i), l.dollar(i), {n: l.word(i, phpTrue...)}, {n: l.word(i, phpFalse...)},
		{n: l.word(i, phpNull)}, l.eolAfterBlanks(i), {n: l.constant(i)}, {n: l.number(i)}, op, eq,
		l.chars(i, &phpValueStop, false, false), l.quoteStart(i), {n: l.blanks(i)}, l.comment(i), {n: 1}}
}

// eolAfterBlanks is [ \t]*{NEWLINE}.
func (l *phpLexer) eolAfterBlanks(i int) phpMatch {
	if b := l.blanks(i); l.newline(i+b) > 0 {
		return phpMatch{n: b + l.newline(i+b)}
	}
	return phpMatch{}
}

// phpRule takes the token a rule matched, n bytes at i, once lex has moved
// past it.
type phpRule func(l *phpLexer, i, n int)

// phpEndValue ends the value: the token is an end of line, and the lexer
// is back in its initial state.
func phpEndValue(l *phpLexer, _, _ int) { l.tok, l.state = phpEOL, phpInitial }

// phpValueRules are lexValue's rules, by valueMatches' order. The last, any
// byte no other rule takes, ends the value.
var phpValueRules = [...]phpRule{
	func(l *phpLexer, i, n int) { l.tok, l.text = phpRaw, l.data[i+1:i+n-1] },
	func(l *phpLexer, _, _ int) { l.tok = phpDollar; l.push(phpVarName) },
	phpValueBool, phpValueBool, phpValueBool,
	phpEndValue,
	func(l *phpLexer, i, n int) { l.tok, l.text = phpConstant, l.data[i:i+n] },
	func(l *phpLexer, i, n int) { l.tok, l.text = phpNumber, l.data[i:i+n] },
	func(l *phpLexer, i, _ int) { l.tok, l.text = phpOperator, l.data[i:i+1] },
	func(l *phpLexer, i, _ int) {
		// = ends the value without being read: the next statement starts at it.
		l.pos, l.end = i, i
		l.tok, l.state = phpEOL, phpInitial
	},
	func(l *phpLexer, i, n int) { l.tok, l.text = phpString, l.data[i:i+n] },
	func(l *phpLexer, _, _ int) { l.tok = phpQuote; l.push(phpDoubleQuotes) },
	func(l *phpLexer, i, n int) { l.tok, l.text = phpBlank, l.data[i:i+n] },
	phpEndValue,
	phpEndValue,
}

// phpValueBool is a boolean word, with the blanks after it dropped.
func phpValueBool(l *phpLexer, i, n int) {
	l.tok, l.text = phpBool, bytes.TrimRight(l.data[i:i+n], " \t")
}

// lexSectionValue is lex in a section's name, an offset or a fallback: the
// longest of their rules, the first among equals.
func (l *phpLexer) lexSectionValue(i int) bool {
	ms := l.sectionMatches(i)
	rule, n, past := best(ms[:]...)
	if past || n == 0 {
		l.pos, l.tok = len(l.data)+1, phpEnd
		return true
	}
	l.pos = i + n
	l.end = l.pos
	phpSectionRules[rule](l, i, n)
	return true
}

// sectionMatches are the matches of the rules of a section's name, an
// offset or a fallback at i, in the order phpSectionRules takes them. A
// fallback has no ' string, and ends at its }.
func (l *phpLexer) sectionMatches(i int) [8]phpMatch {
	if l.state == phpVarFallback {
		ms := [8]phpMatch{{}, l.dollar(i), {}, {n: l.constant(i)}, {n: l.number(i)}, l.chars(i, &phpFallbackStop, true, true), l.quoteStart(i), {n: l.blanks(i)}}
		if l.at(i) == '}' && i < len(l.data) {
			ms[0] = phpMatch{n: 1}
		}
		return ms
	}
	return [8]phpMatch{l.sectionEnd(i), l.dollar(i), l.single(i), {n: l.constant(i)}, {n: l.number(i)}, l.chars(i, &phpSectionStop, true, true), l.quoteStart(i), {n: l.blanks(i)}}
}

// sectionEnd is the ] that ends a section's name, with the blanks and the
// line break after it, or the ] that ends an offset, with the blanks
// before it.
func (l *phpLexer) sectionEnd(i int) phpMatch {
	if l.at(i) == ']' && i < len(l.data) {
		if l.state == phpSectionValue {
			n := 1 + l.blanks(i+1)
			return phpMatch{n: n + l.newline(i+n)}
		}
		return phpMatch{n: 1}
	}
	if l.state == phpOffsetValue {
		if b := l.blanks(i); l.at(i+b) == ']' && i+b < len(l.data) {
			return phpMatch{n: b + 1}
		}
	}
	return phpMatch{}
}

// closeSectionValue is the } that ends a fallback, or the ] that ends a
// section's name or an offset.
func (l *phpLexer) closeSectionValue(_, _ int) {
	if l.state == phpVarFallback {
		l.tok = phpRBrace
		l.pop()
	} else {
		l.tok, l.state = phpRBracket, phpInitial
	}
}

// phpSectionRules are the rules of a section's name, an offset and a
// fallback, by sectionMatches' order.
var phpSectionRules = [...]phpRule{
	(*phpLexer).closeSectionValue,
	func(l *phpLexer, _, _ int) { l.tok = phpDollar; l.push(phpVarName) },
	func(l *phpLexer, i, n int) { l.tok, l.text = phpRaw, l.data[i+1:i+n-1] },
	func(l *phpLexer, i, n int) { l.tok, l.text = phpConstant, l.data[i:i+n] },
	func(l *phpLexer, i, n int) { l.tok, l.text = phpNumber, l.data[i:i+n] },
	func(l *phpLexer, i, n int) { l.tok, l.text = phpString, l.data[i:i+n] },
	func(l *phpLexer, _, _ int) { l.tok = phpQuote; l.push(phpDoubleQuotes) },
	func(l *phpLexer, i, n int) { l.tok, l.text = phpBlank, l.data[i:i+n] },
}

// lexDoubleQuotes is lex inside a double quoted string: ${, the closing "
// and the blanks after it, or the text up to either.
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
	s := l.quotedEnd(i)
	l.pos, l.end, l.tok = s, s, phpQuoted
	l.buf = phpUnescape(l.buf[:0], l.data[i:s])
	l.text = l.buf
	return true
}

// quotedEnd is where the text of a double quoted string from i ends: at a
// ", at a ${, at a \" that ends its line, which closes the string, or at
// the end of the file. A \ takes the byte after it.
func (l *phpLexer) quotedEnd(i int) int {
	for s := i; s < len(l.data); s++ {
		switch l.data[s] {
		case '"':
			return s
		case '$':
			if s+1 < len(l.data) && l.data[s+1] == '{' {
				return s
			}
		case '\\':
			if l.pathQuoteAt(s + 1) {
				return s + 1
			}
			s++ // the byte after the \
		}
	}
	return len(l.data)
}

// pathQuoteAt reports whether a " at j, after a \, ends its line, and so
// closes the string: key="C:\path\" ends at that ", as PHP reads a
// Windows path.
func (l *phpLexer) pathQuoteAt(j int) bool {
	return l.at(j) == '"' && j < len(l.data) && l.endsLine(j+1)
}

// endsLine reports whether j is the end of the file or of a line.
func (l *phpLexer) endsLine(j int) bool {
	return j >= len(l.data) || l.data[j] == '\n' || l.data[j] == '\r'
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

// lexVarName is lex in a reference's name: :- that starts its fallback,
// the } that ends it, or the name.
func (l *phpLexer) lexVarName(i int) bool {
	switch {
	case l.fallbackAt(i):
		l.startFallback(i + 2)
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
	if l.fallbackAt(i + 1) {
		l.startFallback(i + 2)
		return true
	}
	j := l.varNameEnd(i)
	name := l.data[i:j]
	if j == len(l.data) {
		name = l.data[i : i+1] // PHP's name stays one byte when it runs to the end
	}
	l.pos, l.end, l.tok, l.text = j, j, phpVarname, trimPHPSpace(name)
	return true
}

// fallbackAt reports whether :- is at j.
func (l *phpLexer) fallbackAt(j int) bool {
	return l.at(j) == ':' && l.at(j+1) == '-' && j+1 < len(l.data)
}

// startFallback is the token :-, which ends at end and starts the
// reference's fallback.
func (l *phpLexer) startFallback(end int) {
	l.pos, l.end, l.tok = end, end, phpFallback
	l.pop()
	l.push(phpVarFallback)
}

// varNameEnd is where a reference's name that starts at i ends: at :-, or
// at a byte other than :, NUL or a label byte.
func (l *phpLexer) varNameEnd(i int) int {
	j := i + 1
	for ; j < len(l.data); j++ {
		if l.fallbackAt(j) {
			break
		}
		if c := l.data[j]; c != ':' && c != 0 && !isPHPLabel(c) {
			break
		}
	}
	return j
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
	err      error             // the meter's, once it is spent
	discard  bool              // inside a fallback: read the list, keep no text
	name     []byte            // a section's name as it is read
	scratch  []byte            // an offset's or a fallback's text, which errata drops
	defined  func([]byte) bool // the constants PHP has defined when it reads the file
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

// readPHP reads data as PHP reads php.ini, at startup.
func readPHP(data []byte, m *core.Meter, visit func(*core.Entry) error) error {
	return readPHPWith(data, m, isStartupConstant, visit)
}

// readPHPWith reads data, replacing the constants defined reports PHP has
// defined when it reads the file.
func readPHPWith(data []byte, m *core.Meter, defined func([]byte) bool, visit func(*core.Entry) error) error {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return core.LineError(core.LineOf(data, i), "a NUL byte, at which PHP stops reading the file")
	}
	l := &phpLexer{data: data}
	// PHP skips a byte order mark, and a file that is only the mark reads as
	// empty (the oracle corpus holds one).
	if bytes.HasPrefix(data, []byte(core.BOM)) {
		l.pos = len(core.BOM)
	}
	p := &phpParser{l: l, lines: core.NewLineCursor(data), m: m, defined: defined}
	var e core.Entry
	for {
		tok := p.read()
		if tok == phpEnd {
			return p.err
		}
		if err := p.statement(tok, &e, visit); err != nil {
			return err
		}
	}
}

// statement reads the statement tok starts: a line break, a section, or a
// key and its value, which it visits. It returns nil to read on, and
// otherwise the error that ends the read.
func (p *phpParser) statement(tok phpTok, e *core.Entry, visit func(*core.Entry) error) error {
	line := p.lines.At(min(p.l.start, len(p.l.data)))
	switch tok {
	case phpEOL:
		return nil
	case phpSection:
		return p.refusal(p.sectionName(), line)
	case phpLabel, phpOffset:
		return p.entry(tok, line, e, visit)
	default: // no statement starts with any other token
		return p.refusal(errPHPSyntax, line)
	}
}

// sectionName reads a section's name and its ], and starts the section.
func (p *phpParser) sectionName() error {
	name, err := p.list(p.name[:0], true)
	if err == nil && p.read() != phpRBracket {
		err = errPHPSyntax
	}
	p.name = name
	if err == nil {
		p.section = append(p.section[:0], name...)
		p.seq++
	}
	return err
}

// entry reads a key, its offset if it has one, its = and its value, and
// visits the entry. A key with no = is ignored, as PHP ignores it.
func (p *phpParser) entry(tok phpTok, line int, e *core.Entry, visit func(*core.Entry) error) error {
	e.Key = append(e.Key[:0], p.l.text...)
	key := e.Key
	if tok == phpOffset {
		if err := p.offset(); err != nil {
			return p.refusal(err, line)
		}
	} else if p.read() != phpEquals {
		p.back = true // a key with no value: PHP ignores it
		return nil
	}
	if err := p.valueOf(); err != nil {
		return p.refusal(err, line)
	}
	*e = core.Entry{Section: p.section, SectionSeq: p.seq, Key: key, Value: p.value, Line: line, Computed: p.computed}
	return visit(e)
}

// offset reads the rest of a key's offset, its ] and the = after it. errata
// drops the offset's text.
func (p *phpParser) offset() error {
	var err error
	if p.scratch, err = p.list(p.scratch[:0], false); err == nil && (p.read() != phpRBracket || p.read() != phpEquals) {
		err = errPHPSyntax
	}
	return err
}

// refusal is the error that ends the read for err, the error of the
// statement that starts at line: nil for none, the meter's once the work
// budget ran out inside the line, and the line PHP refuses or nests too
// deeply.
func (p *phpParser) refusal(err error, line int) error {
	switch {
	case err == nil:
		return nil
	case p.err != nil: // the work budget ran out inside the line
		return p.err
	case errors.Is(err, errPHPSyntax):
		return core.LineError(line, "PHP refuses this line: %s", core.Quote(string(phpLine(p.l.data, p.l.start))))
	case errors.Is(err, errPHPDeep):
		return core.TooDeep(line)
	}
	return err
}

// phpLine is the line of data that holds offset i, for a message.
func phpLine(data []byte, i int) []byte {
	i = min(i, len(data))
	return bytes.TrimRight(data[core.LineStart(data, i):core.NextLine(data, i)], "\r\n")
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

// unary reads an operator and what it applies to, or a list.
func (p *phpParser) unary() (bool, error) {
	if p.read() == phpOperator {
		return p.operator()
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

// operator reads what the operator just read applies to: ~ and ! a unary,
// ( an expression and its ). Any other operator is refused here.
func (p *phpParser) operator() (bool, error) {
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

// list reads a var_string_list, or with section a var_string_list_section,
// and returns its text. listed reports whether it read an item.
func (p *phpParser) list(out []byte, section bool) ([]byte, error) {
	p.listed = false
	for {
		var err error
		switch p.read() {
		case phpConstant:
			out = p.constant(out, section)
		case phpRaw, phpNumber, phpString, phpBlank:
			out = p.keep(out, p.l.text)
		case phpDollar:
			out, err = p.reference(out)
		case phpQuote:
			out, err = p.quoted(out)
		default:
			p.back = true
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		p.listed = true
		p.took()
	}
}

// keep appends text to out, unless the list is a fallback's, whose text
// errata drops.
func (p *phpParser) keep(out, text []byte) []byte {
	if p.discard {
		return out
	}
	return append(out, text...)
}

// constant is a word in a list: a constant PHP replaces with its value
// marks the value computed, outside a section's name.
func (p *phpParser) constant(out []byte, section bool) []byte {
	if !section && p.defined(p.l.text) {
		p.computed = true
	}
	return p.keep(out, p.l.text)
}

// reference reads a reference after its ${, and keeps its source text.
func (p *phpParser) reference(out []byte) ([]byte, error) {
	start := p.l.start
	if err := p.ref(); err != nil {
		return nil, err
	}
	out = p.keep(out, p.l.data[start:p.l.end])
	p.computed = true
	return out, nil
}

// quoted reads a double quoted string after its opening ", up to its
// closing ": its text and the references in it.
func (p *phpParser) quoted(out []byte) ([]byte, error) {
	for {
		var err error
		switch p.read() {
		case phpQuote:
			return out, nil
		case phpQuoted:
			out = p.keep(out, p.l.text)
		case phpDollar:
			out, err = p.reference(out)
		default:
			return nil, errPHPSyntax
		}
		if err != nil {
			return nil, err
		}
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

// isConstantName reports whether b is named as PHP's constants are: a
// capital letter or _, then capital letters, digits and _ up to a _, and
// after it letters of either case too, such as E_ALL, PHP_EOL and
// STREAM_CRYPTO_METHOD_TLSv1_2_CLIENT.
func isConstantName(b []byte) bool {
	if len(b) == 0 || b[0] != '_' && (b[0] < 'A' || b[0] > 'Z') {
		return false
	}
	under := false
	for _, c := range b {
		if c == '_' {
			under = true
		} else if !isConstantByte(c, under) {
			return false
		}
	}
	return under
}

// isConstantByte reports whether c, other than _, can be in a constant's
// name: a capital letter or a digit, and after a _ (under) a small letter
// too.
func isConstantByte(c byte, under bool) bool {
	return 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || under && 'a' <= c && c <= 'z'
}

// isStartupConstant reports whether b names a constant PHP has defined when
// it reads php.ini, and so replaces with its value. PHP reads php.ini in
// php_module_startup, after the Zend engine registers its constants (E_*,
// DEBUG_BACKTRACE_*, ZEND_THREAD_SAFE and ZEND_DEBUG_BUILD) and main.c its
// own (PHP_*, PEAR_*, DEFAULT_INCLUDE_PATH, and UPLOAD_ERR_* for uploads),
// and before any extension's MINIT. An extension's constant, such as M_PI,
// CODESET, SIGTERM, SOMAXCONN or CURL_SSLVERSION_TLSv1_2, and the CLI's
// STDIN, are not yet defined, and PHP keeps the word as written: php -c
// gives get_cfg_var("a") = "CODESET" for a = CODESET. A name of one of
// these prefixes that PHP does not define is marked too, which costs a rule
// only an unknown.
func isStartupConstant(b []byte) bool {
	if !isConstantName(b) {
		return false
	}
	if string(b) == "DEFAULT_INCLUDE_PATH" {
		return true
	}
	for _, prefix := range phpStartupPrefixes {
		if bytes.HasPrefix(b, []byte(prefix)) {
			return true
		}
	}
	return false
}

// phpStartupPrefixes start the names of the constants PHP 8.4 defines
// before it reads php.ini (isStartupConstant).
var phpStartupPrefixes = []string{"E_", "PHP_", "PEAR_", "ZEND_", "DEBUG_BACKTRACE_", "UPLOAD_ERR_"}
