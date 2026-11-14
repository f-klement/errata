package ini

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// pyValue is a value as ansible's _parse_value makes it, where errata can
// say it exactly: ast.literal_eval reads the value as a Python literal, and
// a value that is no literal stays the text written.
//
//   - A string literal, or several side by side, is the text Python makes
//     of it: its escapes read, the strings joined, b'x' bytes as their
//     bytes, r'x' raw. One errata cannot decode exactly is refused: an \N{name},
//     whose names errata does not hold, and a surrogate, which UTF-8 cannot.
//   - Any other literal (a number, True, a list) keeps its text, without a
//     # comment after it, which Python's tokenizer drops. computed reports
//     one whose value Python writes otherwise (pyComputed), such as 0x10,
//     which ansible holds as 16.
//   - A value that is no literal is the text written, a # in it too: an
//     f-string, a name, a cut escape such as \x4, or text that is not
//     UTF-8, which literal_eval refuses with a ValueError that ansible
//     catches.
//
// literal_eval drops blanks before the value and refuses a NUL, so a value
// that holds one is the text written.
func pyValue(v []byte) (value []byte, computed bool, err error) {
	if bytes.IndexByte(v, 0) >= 0 || !utf8.Valid(v) {
		return v, false, nil
	}
	u := bytes.TrimLeft(v, " \t")
	if s, ok := pyString(u); ok {
		return s, false, nil
	}
	if plainPyWord(u) {
		return v, false, nil // a word, True or None: the text written
	}
	p := &pyLit{s: u}
	kind, ok := p.literal()
	if !ok {
		return v, false, nil // no literal: literal_eval refuses it, and ansible keeps the text
	}
	return p.valueOf(v, kind)
}

// plainPyWord reports whether u, a value without its leading blanks, is
// one pyValue keeps as written before it reads any literal: empty, or with
// no #, no string and no start of a literal Python writes otherwise.
func plainPyWord(u []byte) bool {
	return len(u) == 0 || bytes.IndexByte(u, '#') < 0 && !startsString(u) && !mayBeComputed(u[0])
}

// valueOf is pyValue of v once p has read the first literal of p.s, v
// without its leading blanks, and kind is what that literal is.
func (p *pyLit) valueOf(v []byte, kind litKind) ([]byte, bool, error) {
	u := p.s
	end := p.i
	p.blanks()
	if p.i < len(u) && u[p.i] == ',' {
		return p.tupleValue(v)
	}
	switch {
	case p.i < len(u) && u[p.i] != '#':
		return v, false, nil // more than one literal is a syntax error
	case kind == litString:
		return p.stringValue(v)
	case p.i < len(u):
		return u[:end], pyComputed(u[:end]), nil // the literal without its comment
	}
	return v, pyComputed(u[:end]), nil
}

// tupleValue is pyValue of v when its first literal ends at a comma.
// Literals joined by commas are a tuple, which ansible holds as a list:
// str of it is never the text.
func (p *pyLit) tupleValue(v []byte) ([]byte, bool, error) {
	end, ok := p.tuple()
	if !ok {
		return v, false, nil
	}
	return p.s[:end], true, nil // the tuple, without a comment after it
}

// stringValue is pyValue of v when it is strings: their text, or an error
// for a string errata cannot decode exactly.
func (p *pyLit) stringValue(v []byte) ([]byte, bool, error) {
	if p.undecodable {
		return nil, false, fmt.Errorf("the value %s is a Python string errata cannot decode exactly; write the text it stands for", core.Quote(string(v)))
	}
	return p.text, false, nil
}

// tuple reads the rest of a tuple whose first literal ends at the comma at
// p.i: more literals, each after a comma, and a comma at the end or not,
// through the end of the value or a # comment. It returns where the last
// literal or comma ends, and false for text literal_eval refuses.
func (p *pyLit) tuple() (int, bool) {
	end := p.i
	for p.i < len(p.s) && p.s[p.i] == ',' {
		p.i++
		end = p.i
		p.blanks()
		if p.i == len(p.s) || p.s[p.i] == '#' {
			return end, true
		}
		if _, ok := p.literal(); !ok {
			return 0, false
		}
		end = p.i
		p.blanks()
	}
	return end, p.i == len(p.s) || p.s[p.i] == '#'
}

// mayBeComputed reports whether a literal that starts with c can be one
// whose value Python writes otherwise: a number, a container, set() or
// ....
func mayBeComputed(c byte) bool {
	return isDigit(c) || bytes.IndexByte([]byte("+-.[({s"), c) >= 0
}

// startsString reports whether s starts as a Python string literal does:
// a quote, or a prefix and a quote.
func startsString(s []byte) bool {
	for i := 0; i < len(s) && i < 3; i++ {
		switch c := s[i]; {
		case c == '\'' || c == '"':
			return true
		case bytes.IndexByte([]byte("rRbBuUfF"), c) < 0:
			return false
		}
	}
	return false
}

// litKind is what a literal is.
type litKind int

const (
	litOther  litKind = iota // a number, a word, a container
	litString                // strings side by side, decoded into pyLit.text
)

// pyMaxLevel is the deepest nesting of brackets Python's tokenizer takes.
const pyMaxLevel = 200

// pyLit reads a Python literal as ast.literal_eval takes one: a number with
// a sign, strings, True, False, None, ..., and lists, tuples, sets, dicts
// and set() of literals.
type pyLit struct {
	s     []byte
	i     int
	depth int
	// text is a string literal's value, top level only, and undecodable
	// reports one errata cannot give exactly.
	text        []byte
	undecodable bool
}

func (p *pyLit) blanks() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\f') {
		p.i++
	}
}

func (p *pyLit) peek() byte {
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

// literal reads one literal and reports what it is.
func (p *pyLit) literal() (litKind, bool) {
	p.blanks()
	if c := p.peek(); c == '+' || c == '-' {
		p.i++
		p.blanks()
		return litOther, p.numberLit()
	}
	if ok, read := p.numberOrContainer(); read {
		return litOther, ok
	}
	if p.strings() {
		return litString, true
	}
	if p.i > len(p.s) {
		return litOther, false // a string Python refuses
	}
	return litOther, p.named()
}

// numberOrContainer reads a literal that starts with a digit, a bracket or
// a dot: a number, a list, a tuple, a set or a dict, or .... read is false
// at any other byte, where it reads nothing.
func (p *pyLit) numberOrContainer() (ok, read bool) {
	switch c := p.peek(); {
	case p.numberStarts(c):
		return p.numberLit(), true
	case c == '[':
		return p.items(']', false), true
	case c == '(':
		return p.items(')', false), true
	case c == '{':
		return p.items('}', true), true
	case c == '.':
		return p.ellipsis(), true
	}
	return false, false
}

// numberStarts reports whether a number starts at p.i, whose byte is c: a
// digit, or a dot and a digit.
func (p *pyLit) numberStarts(c byte) bool {
	return isDigit(c) || (c == '.' && p.i+1 < len(p.s) && isDigit(p.s[p.i+1]))
}

// numberLit reads a number and the imaginary part a sum adds to it.
func (p *pyLit) numberLit() bool { return p.number() && p.imaginaryAfter() }

// ellipsis reads ..., the one literal other than a number that starts with
// a dot.
func (p *pyLit) ellipsis() bool {
	if bytes.HasPrefix(p.s[p.i:], []byte("...")) {
		p.i += 3
		return true
	}
	return false
}

// named reads a literal written as a name: True, False, None, or set().
func (p *pyLit) named() bool {
	switch string(p.name()) {
	case "True", "False", "None":
		return true
	case "set":
		return p.emptyCall()
	}
	return false
}

// emptyCall reads the () after set, blanks allowed.
func (p *pyLit) emptyCall() bool {
	p.blanks()
	if p.peek() == '(' {
		p.i++
		p.blanks()
		if p.peek() == ')' {
			p.i++
			return true
		}
	}
	return false
}

// imaginaryAfter reads, after a real number, a + or a - and an imaginary
// number, which literal_eval adds into a complex number such as (1+2j),
// and is true unless the number before was imaginary itself and such a
// sum follows, which literal_eval refuses. Anything else after the number
// is left for the caller.
func (p *pyLit) imaginaryAfter() bool {
	start, end := p.i, p.i
	p.blanks()
	if c := p.peek(); c != '+' && c != '-' {
		p.i = end
		return true
	}
	p.i++
	p.blanks()
	n := pyNumberLen(p.s[p.i:])
	if n == 0 || p.s[p.i+n-1]|0x20 != 'j' {
		p.i = end
		return true
	}
	if left := p.s[:start]; left[len(left)-1]|0x20 == 'j' {
		p.i = len(p.s) + 1 // complex plus complex is no literal
		return false
	}
	p.i += n
	if isPyNameByte(p.peek()) {
		p.i = len(p.s) + 1
		return false
	}
	return true
}

// number reads a number token.
func (p *pyLit) number() bool {
	n := pyNumberLen(p.s[p.i:])
	if n == 0 {
		return false
	}
	p.i += n
	// A letter or digit right after a number is a syntax error.
	if isPyNameByte(p.peek()) {
		p.i = len(p.s) + 1
		return false
	}
	return true
}

func (p *pyLit) name() []byte {
	start := p.i
	for p.i < len(p.s) && isPyNameByte(p.s[p.i]) {
		p.i++
	}
	return p.s[start:p.i]
}

// isPyNameByte reports whether c can be in a Python name: a letter, a
// digit, _, or a byte of a character that is not ASCII.
func isPyNameByte(c byte) bool {
	return isAlpha(c) || isDigit(c) || c == '_' || c >= utf8.RuneSelf
}

// strings reads one or more string literals of one kind, bytes or text,
// and joins their values into text when the literal is at the top level.
// A string Python refuses sets p.i past the end.
func (p *pyLit) strings() bool {
	top := p.depth == 0
	if top {
		p.text = p.text[:0]
	}
	kind := -1
	n := 0
	for {
		save := p.i
		if n > 0 {
			p.blanks()
		}
		k, ok := p.stringLit(top)
		if !ok {
			if p.i <= len(p.s) {
				p.i = save
			}
			return n > 0 && p.i <= len(p.s)
		}
		if kind >= 0 && k != kind {
			p.i = len(p.s) + 1 // bytes beside text is a syntax error
			return false
		}
		kind = k
		n++
	}
}

// stringLit reads one string literal: a prefix, a quote of one or three
// characters, and text to the same quote, decoding it into p.text when
// decode is set. An f-string is no literal literal_eval takes. It reports 1
// for bytes. An escape Python refuses, and a byte that is not ASCII in
// bytes, set p.i past the end.
func (p *pyLit) stringLit(decode bool) (int, bool) {
	prefix, j, ok := p.stringPrefix()
	if !ok {
		return 0, false
	}
	raw, isBytes := strings.Contains(prefix, "r"), strings.Contains(prefix, "b")
	q := p.s[j : j+1]
	if bytes.HasPrefix(p.s[j:], bytes.Repeat(q, 3)) {
		q = p.s[j : j+3]
	}
	return p.stringBody(j+len(q), q, raw, isBytes, decode)
}

// stringPrefix reads a string literal's prefix at p.i, in lower case, and
// returns it and where its quote is. It reports false for a prefix
// literal_eval does not take, such as an f-string's, and where no quote
// follows.
func (p *pyLit) stringPrefix() (string, int, bool) {
	j := p.i + pyPrefixLen(p.s[p.i:])
	prefix := strings.ToLower(string(p.s[p.i:j]))
	switch prefix {
	case "", "r", "u", "b", "br", "rb":
	default:
		return "", 0, false
	}
	if j >= len(p.s) || (p.s[j] != '\'' && p.s[j] != '"') {
		return "", 0, false
	}
	return prefix, j, true
}

// pyPrefixLen is the length of the prefix letters at the start of s, two
// at most.
func pyPrefixLen(s []byte) int {
	n := 0
	for n < len(s) && n < 2 && bytes.IndexByte([]byte("rRbBuUfF"), s[n]) >= 0 {
		n++
	}
	return n
}

// stringBody reads a string literal's text from j through its closing
// quote q, and reports 1 for bytes.
func (p *pyLit) stringBody(j int, q []byte, raw, isBytes, decode bool) (int, bool) {
	for j < len(p.s) {
		if bytes.HasPrefix(p.s[j:], q) {
			p.i = j + len(q)
			if isBytes {
				return 1, true
			}
			return 0, true
		}
		var ok bool
		if j, ok = p.stringChar(j, raw, isBytes, decode); !ok {
			return 0, false
		}
	}
	return 0, false
}

// stringChar reads one character of a string literal's text at j, and
// returns where the next starts. A byte that is not ASCII in bytes, and an
// escape Python refuses, set p.i past the end and report false.
func (p *pyLit) stringChar(j int, raw, isBytes, decode bool) (int, bool) {
	c := p.s[j]
	n, ok := 1, true
	switch {
	case isBytes && c >= utf8.RuneSelf:
		ok = false // bytes can only contain ASCII literal characters
	case c == '\\' && raw:
		p.keep(decode, p.s[j:min(j+2, len(p.s))]...)
		n = 2
	case c == '\\':
		n, ok = p.escape(j, isBytes, decode)
	default:
		p.keep(decode, c)
	}
	if !ok {
		p.i = len(p.s) + 1
	}
	return j + n, ok
}

// keep appends b to p.text when decode is set.
func (p *pyLit) keep(decode bool, b ...byte) {
	if decode {
		p.text = append(p.text, b...)
	}
}

// keepRune appends r to p.text when decode is set. A surrogate, which
// UTF-8 cannot hold, makes the string one errata cannot decode.
func (p *pyLit) keepRune(decode bool, r rune) {
	if utf16.IsSurrogate(r) {
		p.undecodable = true
		return
	}
	if decode {
		p.text = utf8.AppendRune(p.text, r)
	}
}

// keepCode appends the character of code v: in bytes its low byte, as
// Python keeps the low byte of \777, and otherwise the rune.
func (p *pyLit) keepCode(decode, isBytes bool, v rune) {
	if isBytes {
		p.keep(decode, byte(v&0xff))
		return
	}
	p.keepRune(decode, v)
}

// pyEscapes are the escapes of one character after the \ that stand for
// one byte, by that character; 0 for the others.
var pyEscapes = [256]byte{'\\': '\\', '\'': '\'', '"': '"', 'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v'}

// escape reads the escape at s[j], a \, as Python reads it in a string or
// bytes literal, appends its value to p.text when decode is set, and
// returns its length. It reports false for one Python refuses.
func (p *pyLit) escape(j int, isBytes, decode bool) (int, bool) {
	if j+1 >= len(p.s) {
		return 0, false
	}
	c := p.s[j+1]
	if b := pyEscapes[c]; b != 0 {
		p.keep(decode, b)
		return 2, true
	}
	switch c {
	case '0', '1', '2', '3', '4', '5', '6', '7':
		return p.octalEscape(j, isBytes, decode)
	case 'x':
		return p.hexEscape(j, isBytes, decode)
	case 'u', 'U', 'N':
		if !isBytes { // bytes have no such escape
			return p.unicodeEscape(j, decode)
		}
	}
	p.keep(decode, '\\', c) // an unknown escape keeps its \, as Python does
	return 2, true
}

// octalEscape reads \ and one to three octal digits at j.
func (p *pyLit) octalEscape(j int, isBytes, decode bool) (int, bool) {
	s := p.s
	n, v := 1, rune(s[j+1]-'0')
	for n < 3 && j+1+n < len(s) && '0' <= s[j+1+n] && s[j+1+n] <= '7' {
		v = v*8 + rune(s[j+1+n]-'0')
		n++
	}
	p.keepCode(decode, isBytes, v)
	return 1 + n, true
}

// hexEscape reads \x and two hex digits at j.
func (p *pyLit) hexEscape(j int, isBytes, decode bool) (int, bool) {
	v, ok := hexDigits(p.s[j+2:], 2)
	if !ok {
		return 0, false
	}
	p.keepCode(decode, isBytes, v)
	return 4, true
}

// unicodeEscape reads \u and four hex digits, \U and eight, or \N{name}
// at j, in a string.
func (p *pyLit) unicodeEscape(j int, decode bool) (int, bool) {
	s := p.s
	c := s[j+1]
	if c == 'N' {
		return p.namedEscape(j)
	}
	width := map[byte]int{'u': 4, 'U': 8}[c]
	v, ok := hexDigits(s[j+2:], width)
	if !ok || v > unicode.MaxRune {
		return 0, false
	}
	p.keepRune(decode, v)
	return 2 + width, true
}

// namedEscape reads \N{name} at j. errata has no table of the names, so
// the string is one it cannot decode.
func (p *pyLit) namedEscape(j int) (int, bool) {
	s := p.s
	p.undecodable = true
	end := bytes.IndexByte(s[j:], '}')
	if j+2 >= len(s) || s[j+2] != '{' || end < 0 {
		return 0, false
	}
	return end + 1, true
}

// hexDigits reads n hex digits at the start of b.
func hexDigits(b []byte, n int) (rune, bool) {
	if len(b) < n {
		return 0, false
	}
	var v rune
	for _, c := range b[:n] {
		switch {
		case isDigit(c):
			v = v*16 + rune(c-'0')
		case 'a' <= c|0x20 && c|0x20 <= 'f':
			v = v*16 + rune(c|0x20-'a'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

// items reads the elements of a list, a tuple, a set or a dict, after the
// bracket that opens it, through close. In braces, a : after the first
// element makes a dict, whose every element is then a pair.
func (p *pyLit) items(close byte, braces bool) bool {
	// Python's tokenizer refuses brackets nested more than 200 deep
	// (MAXLEVEL), so a value nested deeper is no literal and keeps its text.
	if p.depth++; p.depth > pyMaxLevel {
		return false
	}
	defer func() { p.depth-- }()
	p.i++
	dict := -1 // in braces: unknown, then 0 for a set and 1 for a dict
	for {
		p.blanks()
		if p.peek() == close {
			p.i++
			return true
		}
		if !p.element(braces, &dict) {
			return false
		}
		switch p.peek() {
		case ',':
			p.i++
		case close:
			p.i++
			return true
		default:
			return false
		}
	}
}

// element reads one element of a container and the blanks after it: in
// braces, a pair when dict is 1, and dict set by the first element.
func (p *pyLit) element(braces bool, dict *int) bool {
	if _, ok := p.literal(); !ok {
		return false
	}
	p.blanks()
	return !braces || p.pairRest(dict)
}

// pairRest reads, in braces, the : and value of a dict's pair, where the
// set or dict needs one. The first element decides which it is.
func (p *pyLit) pairRest(dict *int) bool {
	pair := p.peek() == ':'
	if *dict == -1 {
		*dict = map[bool]int{false: 0, true: 1}[pair]
	}
	if (*dict == 1) != pair {
		return false
	}
	if pair {
		p.i++
		if _, ok := p.literal(); !ok {
			return false
		}
		p.blanks()
	}
	return true
}

// pyNumberLen is the length of the longest Python number token at the
// start of s, 0 for none: an integer in base 16, 8 or 2 after 0x, 0o or 0b,
// a decimal integer (no leading 0 but for zeros), a float, or an imaginary
// number, _ allowed between two digits.
func pyNumberLen(s []byte) int {
	if n := radixLen(s); n > 0 {
		return n
	}
	return decimalLen(s)
}

// radixLen is the length of an integer in base 16, 8 or 2 at the start of
// s, with its 0x, 0o or 0b, 1 for a 0 and such a letter with no digit, and
// 0 for none.
func radixLen(s []byte) int {
	if len(s) < 2 || s[0] != '0' {
		return 0
	}
	digit := radixDigit(s[1])
	if digit == nil {
		return 0
	}
	if n := digitRun(s, 2, digit); n > 0 {
		return 2 + n
	}
	return 1 // 0, then a letter: the 0 alone
}

// radixDigit is the digits of the base that the letter c after a 0 names,
// or nil for none.
func radixDigit(c byte) func(byte) bool {
	switch c | 0x20 {
	case 'x':
		return isPyHexDigit
	case 'o':
		return func(c byte) bool { return '0' <= c && c <= '7' }
	case 'b':
		return func(c byte) bool { return c == '0' || c == '1' }
	}
	return nil
}

// isPyHexDigit reports whether c is a hex digit, in either case.
func isPyHexDigit(c byte) bool { return isDigit(c) || ('a' <= c|0x20 && c|0x20 <= 'f') }

// decimalLen is the length of a decimal integer, a float or an imaginary
// number at the start of s, 0 for none.
func decimalLen(s []byte) int {
	dp := digitRun(s, 0, isDigit)
	i, float := pyFraction(s, dp)
	if !float && dp == 0 {
		return 0
	}
	i, float = pyExponent(s, i, float)
	if i < len(s) && s[i]|0x20 == 'j' {
		return i + 1
	}
	if float || s[0] != '0' {
		return i
	}
	return digitRun(s, 0, func(c byte) bool { return c == '0' }) // a decimal 0 takes only zeros
}

// pyFraction reads a . and the digits after it at i, after dp digits, and
// reports whether it read a float. A . needs a digit before or after it.
func pyFraction(s []byte, dp int) (int, bool) {
	if dp < len(s) && s[dp] == '.' {
		if f := digitRun(s, dp+1, isDigit); dp > 0 || f > 0 {
			return dp + 1 + f, true
		}
	}
	return dp, false
}

// pyExponent reads an exponent at i, an e, a sign or none and digits, and
// returns where it ends and whether the number is a float.
func pyExponent(s []byte, i int, float bool) (int, bool) {
	if i >= len(s) || s[i]|0x20 != 'e' {
		return i, float
	}
	j := i + 1
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
	}
	if e := digitRun(s, j, isDigit); e > 0 {
		return j + e, true
	}
	return i, float
}

// digitRun is how many bytes from i are digits, with one _ between two.
func digitRun(s []byte, i int, digit func(byte) bool) int {
	if i >= len(s) || !digit(s[i]) {
		return 0
	}
	j := i + 1
	for j < len(s) {
		switch {
		case digit(s[j]):
			j++
		case s[j] == '_' && j+1 < len(s) && digit(s[j+1]):
			j += 2
		default:
			return j - i
		}
	}
	return j - i
}

// pyString is the text between the quotes of v when v is a Python string
// literal that ast.literal_eval reads as that text: a quote, text holding
// neither that quote nor a backslash, the same quote, and then only blanks
// and a # comment, which Python's tokenizer drops.
func pyString(v []byte) ([]byte, bool) {
	if len(v) < 2 || (v[0] != '\'' && v[0] != '"') {
		return nil, false
	}
	end := bytes.IndexByte(v[1:], v[0]) + 1
	if end == 0 || bytes.IndexByte(v[1:end], '\\') >= 0 {
		return nil, false
	}
	rest := bytes.TrimLeft(v[end+1:], " \t\f")
	if len(rest) > 0 && rest[0] != '#' {
		return nil, false
	}
	return v[1:end], true
}

// The patterns of ansible's parsing/utils/addresses.py, with (?<![_-]) at
// the end of a label written as a last character that is neither.
var (
	hostRange   = `\[(?:[a-z]:[a-z]|[0-9]+:[0-9]+)(?::[0-9]+)?\]`
	numRange    = `\[(?:[0-9]+:[0-9]+)(?::[0-9]+)?\]`
	hexRange    = `\[(?:[0-9a-f]+:[0-9a-f]+)(?::[0-9]+)?\]`
	word        = `[\p{L}\p{N}_]`
	wordNoUnder = `[\p{L}\p{N}]`
	label       = `(?:(?:` + wordNoUnder + `|` + hostRange + `)|(?:` + word + `|` + hostRange + `)(?:[\p{L}\p{N}_-]|` + hostRange + `)*(?:` + wordNoUnder + `|` + hostRange + `))`
	v4c         = `(?:[01]?[0-9]{1,2}|2[0-4][0-9]|25[0-5]|` + numRange + `)`
	v6c         = `(?:[0-9a-f]{1,4}|` + hexRange + `)`

	ipv4 = regexp.MustCompile(`(?i)^(?:` + v4c + `\.){3}` + v4c + `$`)
	ipv6 = regexp.MustCompile(`(?i)^(?:(?:` + v6c + `:){7}` + v6c + `|(?:` + v6c + `:){1,6}:|(?:` + v6c + `:)(?::` + v6c + `){1,6}|` +
		`(?:` + v6c + `:){2}(?::` + v6c + `){1,5}|(?:` + v6c + `:){3}(?::` + v6c + `){1,4}|(?:` + v6c + `:){4}(?::` + v6c + `){1,3}|` +
		`(?:` + v6c + `:){5}(?::` + v6c + `){1,2}|(?:` + v6c + `:){6}(?::` + v6c + `)|:(?::` + v6c + `){1,6}|` + v6c + `?::|` +
		`(?:0:){6}(?:` + v6c + `\.){3}` + v6c + `|::(?:ffff:)?(?:` + v6c + `\.){3}` + v6c + `|(?:0:){5}ffff:(?:` + v6c + `\.){3}` + v6c + `)$`)
	hostname = regexp.MustCompile(`(?i)^` + label + `(?:\.` + label + `)*$`)
)

// checkHost checks a host as ansible's ini plugin does (_expand_hostpattern)
// and reports whether its range expands to no host. It refuses one that ends
// in : with no port, ---, and a pattern whose range expand_hostname_range
// refuses. ansible takes a port off the pattern when the rest is an address
// or a host name (parse_address), and expands a range in what is left;
// errata keeps the host as written and never expands a range: it checks
// each range's bounds and works out whether it holds a host, so a range of
// a million hosts costs its bytes. A range of no host reads no host from
// its line, as ansible reads none.
func checkHost(host []byte, m *core.Meter) (empty bool, err error) {
	// A port is digits after the :, so a host that ends in : has none; and
	// with no [ the pattern is the host or the host without its port, which
	// is --- only when the host is.
	if bytes.HasSuffix(pyStrip(host), []byte(":")) {
		return false, errors.New("ends in :, which ansible keeps for a port")
	}
	if bytes.IndexByte(host, '[') < 0 {
		if string(pyStrip(host)) == "---" {
			return false, errors.New("is refused by ansible, as the start of a YAML file")
		}
		return false, nil
	}
	pattern, done, err := rangesPattern(host, m)
	if err != nil || done {
		return false, err
	}
	empty, err = checkRanges(pattern, m)
	switch {
	case errors.Is(err, core.ErrWork):
		return false, err
	case err != nil:
		return false, fmt.Errorf("has a range ansible refuses: %w", err)
	}
	return empty, nil
}

// rangesPattern is the part of host, which holds a [, whose ranges ansible
// expands, and done true for [x]:22, an address in brackets and its port,
// which holds no range. parse_address changes the ranges a host holds only
// when it takes off the brackets of [address]:port: a port after a name,
// web[1:3]:22, holds no bracket. So the address patterns run, charged,
// only on a host that starts with [ and ends in a port.
func rangesPattern(host []byte, m *core.Meter) (pattern string, done bool, err error) {
	if host[0] != '[' || !hasPort(host) {
		return string(host), false, nil
	}
	stripped := withoutPort(host)
	valid, err := addressOf(m, stripped)
	switch {
	case err != nil:
		return "", false, err
	case !valid:
		return string(host), false, nil
	case bytes.IndexByte(stripped, '[') < 0:
		return "", true, nil
	}
	return string(stripped), false, nil
}

// addressWork is what parse_address's address patterns cost for each byte
// of a host: they take some 400 ns a byte together (measured), so 256
// units of about 1.5 ns.
const addressWork = 256

// hasPort reports whether host ends in : and digits, the only hosts
// parse_address can take a port off. On any other it returns the host as it
// is, so errata runs its patterns only for these.
func hasPort(host []byte) bool { return portStart(host) >= 0 }

// digitsStart is where the digits at the end of a start: Python's \d in a
// str pattern, any decimal digit of Unicode (Nd), such as ٢, which int()
// converts too.
func digitsStart(a []byte) int {
	i := len(a)
	for i > 0 {
		r, w := utf8.DecodeLastRune(a[:i])
		if !unicode.IsDigit(r) {
			break
		}
		i -= w
	}
	return i
}

// portTaken reports whether parse_address takes a port off host: host ends
// in : and digits, and what is left without them (withoutPort) is an
// address or a host name. Otherwise ansible keeps the whole host as its
// name, as it keeps fe80::1, whose : is part of the address, and we$b:22,
// whose we$b is no host name.
func portTaken(host []byte, m *core.Meter) (bool, error) {
	if !hasPort(host) {
		return false, nil
	}
	a := withoutPort(host)
	if len(a) == len(host) {
		return false, nil
	}
	plain, _ := plainHostname(a)
	return plain, m.Charge(len(a))
}

// plainHostname is isAddress for a with no [, ] or :, decided by hand in
// one pass, and ok false for any other a. Such an a is no IPv6 address,
// which holds a :, and an IPv4 address is a host name too, so isAddress is
// the hostname pattern: labels split at dots, each a letter or a number
// (\p{L}, \p{N}), or two or more of those, _ and -, starting with no - and
// ending in a letter or a number. A byte that is not UTF-8 is none of
// these, as the pattern reads it as U+FFFD. Every host with a port that
// portTaken asks about is such an a, since one with a [ is marked for its
// range first, so the patterns' 256 units a byte are never spent there.
func plainHostname(a []byte) (address, ok bool) {
	if bytes.ContainsAny(a, "[]:") {
		return false, false
	}
	for label := range bytes.SplitSeq(a, []byte(".")) {
		if !plainLabel(label) {
			return false, true
		}
	}
	return true, true
}

// plainLabel reports whether label is one label of the hostname pattern:
// letters, numbers, _ and -, starting with no - and ending in a letter or
// a number.
func plainLabel(label []byte) bool {
	first, _ := utf8.DecodeRune(label)
	last, _ := utf8.DecodeLastRune(label)
	if len(label) == 0 || !isAlnumRune(last) || first == '-' {
		return false
	}
	for _, r := range string(label) {
		if !isAlnumRune(r) && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// isAlnumRune reports whether r is a letter or a number, \p{L} or \p{N}.
func isAlnumRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }

// pyMaxDigits is the most digits Python's int() converts
// (sys.get_int_max_str_digits): a range bound past it makes ansible fail the
// inventory, and big.Int's conversion of it takes time in the square of its
// digits.
const pyMaxDigits = 4300

var errTooManyDigits = fmt.Errorf("a bound of more than %d digits, which Python's int() refuses", pyMaxDigits)

const asciiLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// checkRanges checks the ranges of line as expand_hostname_range checks
// them, and reports whether the line expands to no host. ansible splits
// the line at its first [ and its first ], expands the range between, and
// does the same to every host that makes; each such host is the head, a
// value of the range and the tail, so its next [ and ] are the tail's. So
// errata walks the line's ranges in order, by offset, and copies nothing:
// each range costs its own bytes. A range of no host ends the walk, as
// ansible then expands nothing more. Python's recursion stops near 1000
// ranges, and errata at maxDepth.
func checkRanges(line string, m *core.Meter) (bool, error) {
	if strings.Contains(line, "|") {
		return false, errors.New("its brackets do not split it into a head, a range and a tail")
	}
	for pos, n := 0, 0; strings.Contains(line[pos:], "["); n++ {
		if n == core.MaxDepth {
			return false, errors.New("more ranges than errata reads in one host")
		}
		i, j := strings.IndexByte(line[pos:], '['), strings.IndexByte(line[pos:], ']')
		if j < 0 {
			return false, errors.New("its brackets do not split it into a head, a range and a tail")
		}
		lo, hi := pos+min(i, j), pos+max(i, j)
		if err := m.Charge(rangeWork + hi - pos + parseWork(hi-lo)); err != nil {
			return false, err
		}
		empty, err := checkRange(line[lo+1 : hi])
		if err != nil || empty {
			return empty, err
		}
		pos = hi + 1
	}
	return false, nil
}

// parseWork is what converting a number of n digits costs: n, and 32 for
// each digit past what int64 holds, where big.Int's conversion takes 20 ns
// to 37 ns a digit up to the 4300 digits a bound may have (measured).
func parseWork(n int) int {
	if n <= 18 {
		return n
	}
	return 32 * n
}

// rangeSpec is one range of a host pattern, as expand_hostname_range reads
// it: letters from ib to ie by step, or the integers of range(b, e+1, s),
// each written with zeros to width when begin is written so (zfill).
type rangeSpec struct {
	alpha        bool
	ib, ie, step int
	b, e, s      *big.Int
	width        int
	most         int // the most digits a value has: the longer bound, or the width
	// small holds b, e and s as int64 when each is a plain integer of at
	// most 17 digits, as nearly every range's are, so it costs no big.Int.
	small         bool
	b64, e64, s64 int64
}

// checkRange checks one range, the text between its brackets, as
// expand_hostname_range does, and reports whether it holds no host.
func checkRange(nrange string) (bool, error) {
	r, err := parseRange(nrange)
	if err != nil {
		return false, err
	}
	return r.empty(), nil
}

// parseRange reads the text between a range's brackets as
// expand_hostname_range does, and refuses what it refuses.
func parseRange(nrange string) (rangeSpec, error) {
	beg, end, step, err := splitRange(nrange)
	if err != nil {
		return rangeSpec{}, err
	}
	var r rangeSpec
	if r.width, err = rangeWidth(beg, end); err != nil {
		return rangeSpec{}, err
	}
	if ok, err := r.letters(beg, end, step); ok || err != nil {
		return r.or(err)
	}
	if ok, err := r.smallBounds(beg, end, step); ok || err != nil {
		return r.or(err)
	}
	return r.or(r.bigBounds(beg, end, step))
}

// or is r, or the zero range and err when err is not nil.
func (r rangeSpec) or(err error) (rangeSpec, error) {
	if err != nil {
		return rangeSpec{}, err
	}
	return r, nil
}

// splitRange splits a range at its colons into its begin, end and step,
// with a missing begin 0 and a missing step 1.
func splitRange(nrange string) (beg, end, step string, err error) {
	n := strings.Count(nrange, ":") + 1
	if n != 2 && n != 3 {
		return "", "", "", errors.New("host range must be begin:end or begin:end:step")
	}
	beg, rest, _ := strings.Cut(nrange, ":")
	end, step, three := strings.Cut(rest, ":")
	if !three {
		step = "1"
	}
	if beg == "" {
		beg = "0"
	}
	if end == "" {
		return "", "", "", errors.New("host range must specify end value")
	}
	return beg, end, step, nil
}

// rangeWidth is the width a begin written with a leading zero sets, which
// the end must be written to as well, and 0 for any other begin.
func rangeWidth(beg, end string) (int, error) {
	if beg[0] != '0' || len(beg) <= 1 {
		return 0, nil
	}
	if len(beg) != len(end) {
		return 0, errors.New("host range must specify equal-length begin and end formats")
	}
	return len(beg), nil
}

// letters sets r to a range of letters when both bounds are a letter and
// the step an int other than 0, and reports whether it did.
func (r *rangeSpec) letters(beg, end, step string) (bool, error) {
	ib, ie := strings.Index(asciiLetters, beg), strings.Index(asciiLetters, end)
	if ib < 0 || ie < 0 {
		return false, nil
	}
	if ib > ie {
		return false, errors.New("host range must have begin <= end")
	}
	// letters[ib:ie+1:step]: a step that is no int, or 0, makes Python
	// take the numeric path, which then refuses the letters; a negative
	// step from begin to end selects nothing.
	if s, err := pyInt(step); err == nil && s.Sign() != 0 {
		r.alpha, r.ib, r.ie, r.step = true, ib, ie, len(asciiLetters)
		if s.IsInt64() && s.Int64() < int64(len(asciiLetters)) {
			r.step = int(s.Int64()) // a negative step leaves the range empty
		}
		return true, nil
	}
	return false, nil
}

// smallBounds sets r to a range of int64 bounds when each of beg, end and
// step is one (smallInt), and reports whether it did.
func (r *rangeSpec) smallBounds(beg, end, step string) (bool, error) {
	if b, ok := smallInt(beg); ok {
		if e, ok := smallInt(end); ok {
			if st, ok := smallInt(step); ok {
				if st == 0 {
					return false, errors.New("its step is 0")
				}
				r.small, r.b64, r.e64, r.s64 = true, b, e, st
				return true, nil
			}
		}
	}
	return false, nil
}

// bigBounds sets r to a range of big.Int bounds, as Python's int() reads
// them.
func (r *rangeSpec) bigBounds(beg, end, step string) error {
	var err error
	if r.b, err = pyInt(beg); err != nil {
		return err
	}
	if r.e, err = pyInt(end); err != nil {
		return err
	}
	if r.s, err = pyInt(step); err != nil {
		return err
	}
	if r.s.Sign() == 0 {
		return errors.New("its step is 0")
	}
	return nil
}

// rangeOf is the range nrange writes, and false for one ansible refuses,
// which fails its line in checkHost before any match, and for one of no
// host: neither selects a host.
func rangeOf(nrange string) (rangeSpec, bool) {
	r, err := parseRange(nrange)
	return r, err == nil && !r.empty()
}

// empty reports whether the range holds no host.
func (r rangeSpec) empty() bool {
	if r.alpha {
		return r.step < 0
	}
	if r.small {
		if r.s64 > 0 {
			return r.b64 > r.e64
		}
		return r.b64 <= r.e64+1
	}
	// range(b, e+1, s) holds nothing when b > e for a step up, and when
	// b <= e+1 for a step down; only the last needs e+1 itself.
	if r.s.Sign() > 0 {
		return r.b.Cmp(r.e) > 0
	}
	return r.b.Cmp(r.e) <= 0 || r.b.Cmp(new(big.Int).Add(r.e, smallInts[2])) == 0
}

// maxHostMatch is the longest host name a spec's host is matched against a
// pattern for: a DNS name is at most 253 bytes. A longer one selects only a
// host written as it is, and a line whose host has a range or a port, which
// it could be one of, is refused rather than missed.
const maxHostMatch = 1024

var errHostTooLong = fmt.Errorf("has a range or a port, and the spec's host is over %d bytes, the most errata matches against one; write a shorter host", maxHostMatch)

// hostIn reports whether name is one of the hosts ansible reads from the
// host host of a line: the host without its port (parse_address), and each
// host its ranges expand to (expand_hostname_range). It never builds the
// hosts: it walks the pattern's literal text and ranges against name, the
// positions in name each step can reach, and decides each range by
// arithmetic on the part of name it would write. It copies nothing, and
// charges m for every byte it compares or converts.
func hostIn(host, name []byte, m *core.Meter) (bool, error) {
	port := hasPort(host)
	if len(name) > maxHostMatch && (port || bytes.IndexByte(host, '[') >= 0) {
		return false, errHostTooLong
	}
	if !port {
		return inPattern(host, name, m)
	}
	return portHostIn(host, name, m)
}

// portHostIn is hostIn for a host that ends in a port.
func portHostIn(host, name []byte, m *core.Meter) (bool, error) {
	// parse_address takes the port off when what is left is an address or a
	// host name. Which it is takes the address patterns, some 400 ns a byte,
	// so they run only when the name is one of the hosts of the host without
	// its port and not of the host with it, or the other way round.
	stripped := withoutPort(host)
	a, err := inPattern(stripped, name, m)
	if err != nil {
		return false, err
	}
	b, err := inPattern(host, name, m)
	if err != nil || a == b {
		return a, err
	}
	valid, err := addressOf(m, stripped)
	if err != nil {
		return false, err
	}
	if valid {
		return a, nil
	}
	return b, nil
}

// addressOf is isAddress, charged, and kept for the next host of the same
// text.
func addressOf(m *core.Meter, a []byte) (bool, error) {
	return m.Decide(a, addressWork*len(a), isAddress)
}

// withoutPort is host without the port parse_address's patterns take off,
// found by their text alone: the address in [address]:port, and then the
// text before a : and digits outside brackets.
func withoutPort(host []byte) []byte {
	a := host
	if i := portStart(a); len(a) > 2 && a[0] == '[' && i > 2 && a[i-1] == ']' {
		a = a[1 : i-1] // ^\[(.+)\]:([0-9]+)$
	}
	return withoutNamePort(a)
}

// withoutNamePort is a without the : and digits at its end, where no
// bracket holds that :, and a otherwise.
func withoutNamePort(a []byte) []byte {
	// ^((?:[^:\[\]]|\[[^\]]*\])*):([0-9]+)$
	for i := 0; i < len(a); i++ {
		switch a[i] {
		case '[':
			end := bytes.IndexByte(a[i:], ']')
			if end < 0 {
				return a
			}
			i += end
		case ']':
			return a
		case ':':
			if j := portStart(a); j == i {
				return a[:i]
			}
			return a
		}
	}
	return a
}

// portStart is where the : of a trailing :digits starts in a, or -1.
func portStart(a []byte) int {
	if i := digitsStart(a); i < len(a) && i > 0 && a[i-1] == ':' {
		return i - 1
	}
	return -1
}

// isAddress reports whether a is what parse_address takes for an address:
// an IPv4 or IPv6 address or a host name, ranges allowed.
func isAddress(a string) bool {
	return ipv4.MatchString(a) || ipv6.MatchString(a) || hostname.MatchString(a)
}

// inPattern reports whether name is one of the hosts pattern expands to,
// walking the pattern's literal text and ranges against name. reach is the
// positions of name the pattern so far can end at, each once: stamp marks a
// position with the number of the range that reached it, so nothing is
// cleared between ranges. It copies nothing, and charges m for every byte
// it compares and every value it tries.
func inPattern(pattern, name []byte, m *core.Meter) (bool, error) {
	if bytes.IndexByte(pattern, '[') < 0 {
		return bytes.Equal(pattern, name), m.Charge(len(name))
	}
	if bytes.IndexByte(pattern, '|') >= 0 {
		return false, nil // ansible refuses the line
	}
	if err := m.Charge(len(name) + 1); err != nil {
		return false, err
	}
	w := &hostWalk{pattern: pattern, name: name, m: m, stamp: make([]int, len(name)+1), reach: []int{0}}
	return w.walk()
}

// hostWalk is inPattern's walk of a pattern with ranges against name: pos
// is where the pattern's next literal text starts, reach the positions of
// name the pattern up to pos can end at, and next those the range being
// walked reaches.
type hostWalk struct {
	pattern, name []byte
	pos           int
	m             *core.Meter
	stamp         []int
	reach, next   []int
}

// walk walks each range and the literal text before it, then the tail.
func (w *hostWalk) walk() (bool, error) {
	for k := 1; bytes.IndexByte(w.pattern[w.pos:], '[') >= 0; k++ {
		r, lit, ok, err := w.nextRange()
		if err != nil || !ok {
			return false, err
		}
		if ok, err := w.reachAfter(k, lit, r); err != nil || !ok {
			return false, err
		}
	}
	return w.tail()
}

// nextRange reads the next range of the pattern, charged, and returns it
// and the literal text before it, and moves pos past it. It reports false
// for a range that selects no host.
func (w *hostWalk) nextRange() (rangeSpec, []byte, bool, error) {
	rest := w.pattern[w.pos:]
	i, j := bytes.IndexByte(rest, '['), bytes.IndexByte(rest, ']')
	if j < 0 {
		return rangeSpec{}, nil, false, nil
	}
	lo, hi := w.pos+min(i, j), w.pos+max(i, j)
	if err := w.m.Charge(rangeWork + hi - w.pos + parseWork(hi-lo)); err != nil {
		return rangeSpec{}, nil, false, err
	}
	r, ok := rangeOf(string(w.pattern[lo+1 : hi]))
	if !ok {
		return rangeSpec{}, nil, false, nil
	}
	r.setMost()
	lit := w.pattern[w.pos:lo]
	w.pos = hi + 1
	return r, lit, true, nil
}

// setMost sets the most digits a value of a numeric range has.
func (r *rangeSpec) setMost() {
	switch {
	case r.small:
		r.most = max(r.width, decimalDigits(r.b64), decimalDigits(r.e64))
	case !r.alpha:
		r.most = max(r.width, len(r.b.Text(10)), len(r.e.Text(10)))
	}
}

// reachAfter moves reach past lit and r, the k-th range: to each position
// a value of r ends at, from each reached position that lit follows. It
// reports false when it reaches none.
func (w *hostWalk) reachAfter(k int, lit []byte, r rangeSpec) (bool, error) {
	w.next = w.next[:0]
	for _, p := range w.reach {
		if err := w.m.Charge(positionWork + len(lit)); err != nil {
			return false, err
		}
		if !bytes.HasPrefix(w.name[p:], lit) {
			continue
		}
		at := p + len(lit)
		if err := r.matches(w.name[at:], w.m, func(n int) { w.mark(k, at+n) }); err != nil {
			return false, err
		}
	}
	w.reach, w.next = w.next, w.reach
	return len(w.reach) > 0, nil
}

// mark adds position q to next once for the k-th range.
func (w *hostWalk) mark(k, q int) {
	if w.stamp[q] != k {
		w.stamp[q] = k
		w.next = append(w.next, q)
	}
}

// tail reports whether the pattern's text after its last range ends name
// from a reached position.
func (w *hostWalk) tail() (bool, error) {
	tail := w.pattern[w.pos:]
	for _, p := range w.reach {
		if err := w.m.Charge(positionWork + len(tail)); err != nil {
			return false, err
		}
		if bytes.Equal(w.name[p:], tail) {
			return true, nil
		}
	}
	return false, nil
}

// The work of matching, in units of the meter, 0.8 ns to 1.4 ns each,
// measured on the shapes of TestReplayINIShapesAtFullSize: a range's parse
// and set up, a reached position, a digit of a value an int64 range tries,
// and a digit of one a big.Int range tries, which converts and formats it.
const (
	rangeWork    = 256
	positionWork = 8
	digitWork    = 16
	bigDigitWork = 48
)

// decimalDigits is how many digits v is written in, its sign not counted.
func decimalDigits(v int64) int {
	n := 1
	for v <= -10 || v >= 10 {
		v /= 10
		n++
	}
	return n
}

// matches calls mark with the length of each value of r that s starts
// with, charging m for each length it tries. A value is no longer than the
// longer bound, or the width the begin is written to.
func (r rangeSpec) matches(s []byte, m *core.Meter, mark func(n int)) error {
	if r.alpha {
		return r.matchesLetter(s, m, mark)
	}
	sign, digits := r.leadingDigits(s)
	if r.small {
		return r.matchesSmall(s, sign, digits, m, mark)
	}
	return r.matchesBig(s, sign, digits, m, mark)
}

// matchesLetter is matches for a range of letters, whose values are one
// byte.
func (r rangeSpec) matchesLetter(s []byte, m *core.Meter, mark func(n int)) error {
	if len(s) > 0 && r.step > 0 {
		k := strings.IndexByte(asciiLetters, s[0])
		if k >= r.ib && k <= r.ie && (k-r.ib)%r.step == 0 {
			mark(1)
		}
	}
	return m.Charge(digitWork)
}

// leadingDigits is the - at the start of s, 1 or 0, and how many digits
// follow it, up to the most a value of r has.
func (r rangeSpec) leadingDigits(s []byte) (sign, digits int) {
	if len(s) > 0 && s[0] == '-' {
		sign = 1
	}
	for sign+digits < len(s) && isDigit(s[sign+digits]) && digits < r.most {
		digits++
	}
	return sign, digits
}

// matchesBig is matches for a range of big.Int bounds.
func (r rangeSpec) matchesBig(s []byte, sign, digits int, m *core.Meter, mark func(n int)) error {
	for n := 1; n <= digits; n++ {
		if err := m.Charge(bigDigitWork * (sign + n)); err != nil {
			return err
		}
		text := string(s[:sign+n])
		x, ok := new(big.Int).SetString(text, 10)
		if ok && zfill(x.String(), r.width) == text && r.holds(x) {
			mark(sign + n)
		}
	}
	return nil
}

// matchesSmall is matches for a range of int64 bounds: the value grows by
// a digit at each length, and is the text zfill would write when it is the
// width, or longer than the width with no zero before its first digit.
func (r rangeSpec) matchesSmall(s []byte, sign, digits int, m *core.Meter, mark func(n int)) error {
	if err := m.Charge(digitWork * (digits + 1)); err != nil {
		return err
	}
	var x int64
	for n := 1; n <= digits && n <= 18; n++ {
		x = x*10 + int64(s[sign+n-1]-'0')
		if !r.zfilled(s, sign, n, x) {
			continue
		}
		v := x
		if sign == 1 {
			v = -x
		}
		if r.holdsSmall(v) {
			mark(sign + n)
		}
	}
	return nil
}

// zfilled reports whether the sign and n digits at the start of s, whose
// value is x, are the text zfill writes for a value of r: not -0, at
// least the width, and longer than it only with no 0 before the first
// digit.
func (r rangeSpec) zfilled(s []byte, sign, n int, x int64) bool {
	length := sign + n
	zero := s[sign] == '0' && n > 1 // a 0 before the first digit
	switch {
	case sign == 1 && x == 0, length < r.width, length > r.width && zero:
		return false
	}
	return true
}

// holdsSmall is holds for int64 bounds.
func (r rangeSpec) holdsSmall(x int64) bool {
	if r.s64 > 0 {
		return x >= r.b64 && x <= r.e64 && (x-r.b64)%r.s64 == 0
	}
	return x <= r.b64 && x > r.e64+1 && (r.b64-x)%(-r.s64) == 0
}

// smallInt reads s, an optional - and 1 to 17 ASCII digits, as int64: the
// form nearly every range bound has, read as Python's int() reads it.
func smallInt(s string) (int64, bool) {
	d := strings.TrimPrefix(s, "-")
	if d == "" || len(d) > 17 {
		return 0, false
	}
	for i := range len(d) {
		if !isDigit(d[i]) {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

// zfill is Python's str.zfill: s padded with zeros after its sign to width.
func zfill(s string, width int) string {
	if len(s) >= width {
		return s
	}
	if s[0] == '-' {
		return "-" + strings.Repeat("0", width-len(s)) + s[1:]
	}
	return strings.Repeat("0", width-len(s)) + s
}

// holds reports whether x is one of range(b, e+1, s).
func (r rangeSpec) holds(x *big.Int) bool {
	var d big.Int
	if r.s.Sign() > 0 {
		if x.Cmp(r.b) < 0 || x.Cmp(r.e) > 0 {
			return false
		}
		d.Sub(x, r.b)
		return d.Mod(&d, r.s).Sign() == 0
	}
	// A step down: b, b+s, ... while above e+1.
	if x.Cmp(r.b) > 0 || x.Cmp(new(big.Int).Add(r.e, smallInts[2])) <= 0 {
		return false
	}
	d.Sub(r.b, x)
	return d.Mod(&d, new(big.Int).Neg(r.s)).Sign() == 0
}

// smallInts holds the big.Int of each integer from -1 to 9, the bounds of
// most ranges, so reading them allocates nothing.
var smallInts = func() (m [11]*big.Int) {
	for i := range m {
		m[i] = big.NewInt(int64(i - 1))
	}
	return m
}()

// pyInt reads s as Python's int() does: blanks around it, a sign, and ASCII
// digits with single _ between them, at most pyMaxDigits of them. A digit
// that is not ASCII, which Python takes too, is refused as no integer:
// errata fails closed on it. The value must not be changed.
func pyInt(s string) (*big.Int, error) {
	if len(s) == 1 && isDigit(s[0]) {
		return smallInts[s[0]-'0'+1], nil
	}
	t := string(pyStrip([]byte(s)))
	digits := strings.TrimLeft(t, "+-")
	if !pyDigits(len(t)-len(digits), digits) {
		return nil, errNoInteger
	}
	if len(digits)-strings.Count(digits, "_") > pyMaxDigits {
		return nil, errTooManyDigits
	}
	n, ok := new(big.Int).SetString(strings.ReplaceAll(t, "_", ""), 10)
	if !ok {
		return nil, errNoInteger
	}
	return n, nil
}

// pyDigits reports whether digits, after signs signs, are what int() takes:
// one sign at most, and digits with single _ between them.
func pyDigits(signs int, digits string) bool {
	return signs <= 1 && digits != "" && digits[0] != '_' && digits[len(digits)-1] != '_' && !strings.Contains(digits, "__")
}

var errNoInteger = errors.New("its begin, end or step is no integer")
