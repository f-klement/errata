package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// The path grammar the yaml, json and toml extractors share: the part of
// RFC 9535 JSONPath below, with one extension: a key written after a dot may
// hold "-" after its first character, so a config key such as node-label
// needs no brackets.
//
//	path       = ( "$" | [ name ] ) { segment }
//	segment    = "." name | "[" S ( quoted | index | "*" | filter ) S "]"
//	name       = name-first *( name-first | DIGIT | "-" )
//	name-first = ALPHA | "_" | any non-ASCII character
//	quoted     = "'" chars "'" | '"' chars '"'    (RFC 9535 string literals)
//	index      = [ "-" ] ( "0" | 1-9 *DIGIT )      (negative counts from the end)
//	filter     = "?" S compare *( S "&&" S compare )   (at most maxComparisons)
//	compare    = current S "==" S literal | literal S "==" S current
//	current    = "@" *( S ( "." name | "[" S ( quoted | index ) S "]" ) )
//	literal    = quoted | number | "true" | "false" | "null"
//	number     = ( "0" | [ "-" ] 1-9 *DIGIT | "-0" ) [ "." 1*DIGIT ] [ ( "e" | "E" ) [ "+" | "-" ] 1*DIGIT ]
//	S          = *" "
//
// S is RFC 9535's blank space, which it allows inside brackets, around the
// parts of a filter and before each step after @. Between the segments of
// the path itself errata takes none, as before filters.
//
// A path is valid UTF-8 and holds no control character (below U+0020), not
// even between quotes, where RFC 9535 refuses one too.
//
// A filter is RFC 9535's filter selector cut down to equality: it selects
// each member of a sequence and each value of a mapping, in the file's
// order, for which every comparison holds. A comparison holds when the path
// after @ selects a scalar of the literal's type and value; a path that
// selects nothing, or a mapping or a sequence, equals no literal. Every other
// construct RFC 9535 allows in a filter is refused, by name, at its column.

type selKind int

const (
	selName selKind = iota
	selIndex
	selWild
	selFilter
)

type selector struct {
	kind   selKind
	name   string
	index  int
	filter []comparison // every one holds for a member the filter selects
	cost   int64        // the bytes testing one member costs: see maxFilterBytes
}

// comparison is one @path == literal of a filter, the literal on either
// side.
type comparison struct {
	current []selector // keys and indices from the member, none for @ itself
	lit     literal
}

// literal is the value a comparison compares with.
type literal struct {
	typ ScalarType
	str string  // a string
	num decimal // a number
	b   bool    // true or false
}

// Path is a parsed path, with the text it was parsed from for messages.
type Path struct {
	text string
	sels []selector
}

// PathError names the column, counted from 1 in characters, where a path
// stopped parsing. A path can be as long as the configuration, so the
// message quotes its first maxQuote bytes, as every message that quotes a
// path or a part of one does.
func PathError(s string, i int, format string, args ...any) error {
	col := utf8.RuneCountInString(s[:i]) + 1
	return fmt.Errorf("path %s: column %d: %s", Quote(s), col, fmt.Sprintf(format, args...))
}

func parsePath(s string) (Path, error) {
	p := Path{text: s}
	if s == "" {
		return p, PathError(s, 0, "path is empty; write $ for the whole document")
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			return p, PathError(s, i, "the path is not valid UTF-8")
		case r < 0x20:
			return p, PathError(s, i, "control character U+%04X is not allowed in a path", r)
		}
		i += size
	}
	i := 0
	if s[0] == '$' {
		i = 1
	} else if s[0] != '.' && s[0] != '[' {
		name, next, err := scanName(s, i)
		if err != nil {
			return p, err
		}
		p.sels = append(p.sels, selector{kind: selName, name: name})
		i = next
	}
	for i < len(s) {
		var sel selector
		var err error
		switch s[i] {
		case '.':
			sel.name, i, err = scanName(s, i+1)
		case '[':
			sel, i, err = scanBracket(s, i+1, false)
		default:
			err = PathError(s, i, "expected . or [ but found %q", firstRune(s[i:]))
		}
		if err != nil {
			return p, err
		}
		p.sels = append(p.sels, sel)
	}
	return p, nil
}

func isNameFirst(r rune) bool {
	return r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || r >= utf8.RuneSelf
}

func isNameRune(r rune) bool {
	return isNameFirst(r) || ('0' <= r && r <= '9') || r == '-'
}

// scanName reads a key written after a dot, or at the start of the path,
// starting at byte i.
func scanName(s string, i int) (string, int, error) {
	after := "a key after ."
	star := ".*"
	if i == 0 {
		after, star = "a key", "*"
	}
	if i == len(s) {
		return "", i, PathError(s, i, "expected a key name but the path ends")
	}
	j := i
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if !isNameRune(r) {
			break
		}
		j += size
	}
	switch first := firstRune(s[i:]); {
	case j > i && isNameFirst(first):
		return s[i:j], j, nil
	case j > i && '0' <= first && first <= '9':
		return "", i, PathError(s, i, "%s cannot start with a digit; write [n] for an index, or ['%s'] for a key", after, Clip(s[i:j]))
	case j > i:
		return "", i, PathError(s, i, "%s cannot start with -; write ['%s']", after, Clip(s[i:j]))
	case first == '.':
		return "", i, PathError(s, i, "recursive descent (..) is not supported")
	case first == '*':
		return "", i, PathError(s, i, "write [*] for every member, not %s", star)
	default:
		return "", i, PathError(s, i, "expected a key name but found %q; write ['...'] for a key with that character", first)
	}
}

// scanBracket reads what follows a "[" at byte i, through the closing "]".
// Inside a filter, after @, it takes only a quoted key or an index, since a
// comparison takes the one node they select.
func scanBracket(s string, i int, inFilter bool) (selector, int, error) {
	i = skipSpace(s, i)
	if i == len(s) {
		return selector{}, i, PathError(s, i, "expected a quoted key, an index or * after [ but the path ends")
	}
	var sel selector
	var j int
	var err error
	switch c := s[i]; {
	case c == '*' && inFilter:
		err = PathError(s, i, "[*] in a filter selects many nodes, and a comparison takes one; after @ write only .key, ['key'] or [n]")
	case c == '*':
		sel, j = selector{kind: selWild}, i+1
	case c == '\'' || c == '"':
		sel.name, j, err = scanQuoted(s, i, "key")
	case c == '-' || ('0' <= c && c <= '9'):
		sel, j, err = scanIndex(s, i)
	case c == '?' && inFilter:
		err = PathError(s, i, "a filter inside a filter is not supported")
	case c == '?':
		sel, j, err = scanFilter(s, i+1)
	default:
		err = PathError(s, i, "expected a quoted key, an index or * after [ but found %q", firstRune(s[i:]))
	}
	if err != nil {
		return sel, j, err
	}
	switch j = skipSpace(s, j); {
	case j == len(s):
		return sel, j, PathError(s, j, "expected ] but the path ends")
	case s[j] == ':':
		return sel, j, PathError(s, j, "slices are not supported")
	case s[j] != ']':
		return sel, j, PathError(s, j, "expected ] but found %q", firstRune(s[j:]))
	}
	return sel, j + 1, nil
}

// scanQuoted reads a quoted key, or a string in a filter, starting at its
// opening quote at byte i, and returns the byte after the closing quote.
// what is "key" or "string", for messages. The escapes are RFC 9535's: \b
// \f \n \r \t \/ \\, \uXXXX with a surrogate pair for a character above
// U+FFFF, and the quote that opened the text, but not the other one.
func scanQuoted(s string, i int, what string) (string, int, error) {
	q := s[i]
	var b strings.Builder
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		if c == q {
			return b.String(), j + 1, nil
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if j+1 == len(s) {
			break
		}
		e := s[j+1]
		if r, ok := map[byte]rune{'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', '/': '/', '\\': '\\', q: rune(q)}[e]; ok {
			b.WriteRune(r)
			j++
			continue
		}
		if e != 'u' {
			return "", j, PathError(s, j, "unsupported escape \\%c in a %s %s", rune(e), map[byte]string{'\'': "single-quoted", '"': "double-quoted"}[q], what)
		}
		r, n, err := scanUnicode(s, j)
		if err != nil {
			return "", j, err
		}
		b.WriteRune(r)
		j += n - 1
	}
	return "", len(s), PathError(s, i, "unterminated quoted %s", what)
}

// scanUnicode reads \uXXXX at byte j, and the low surrogate after a high
// one. It returns the character and the bytes it took.
func scanUnicode(s string, j int) (rune, int, error) {
	hex4 := func(at int) (rune, bool) {
		if at+6 > len(s) || s[at] != '\\' || s[at+1] != 'u' {
			return 0, false
		}
		var r rune
		for _, c := range s[at+2 : at+6] {
			switch {
			case '0' <= c && c <= '9':
				r = r<<4 | (c - '0')
			case 'a' <= c && c <= 'f':
				r = r<<4 | (c - 'a' + 10)
			case 'A' <= c && c <= 'F':
				r = r<<4 | (c - 'A' + 10)
			default:
				return 0, false
			}
		}
		return r, true
	}
	r, ok := hex4(j)
	switch {
	case !ok:
		return 0, 0, PathError(s, j, "\\u takes four hex digits")
	case 0xDC00 <= r && r <= 0xDFFF:
		return 0, 0, PathError(s, j, "\\u%04X is a low surrogate without a high one", r)
	case r < 0xD800 || r > 0xDBFF:
		return r, 6, nil
	}
	lo, ok := hex4(j + 6)
	if !ok || lo < 0xDC00 || lo > 0xDFFF {
		return 0, 0, PathError(s, j, "\\u%04X is a high surrogate without a low one after it", r)
	}
	return utf16.DecodeRune(r, lo), 12, nil
}

// scanIndex reads an index starting at byte i.
func scanIndex(s string, i int) (selector, int, error) {
	j := i
	if s[j] == '-' {
		j++
	}
	start := j
	for j < len(s) && '0' <= s[j] && s[j] <= '9' {
		j++
	}
	digits := s[start:j]
	switch {
	case digits == "":
		return selector{}, j, PathError(s, i, "expected digits after -")
	case len(digits) > 1 && digits[0] == '0':
		return selector{}, j, PathError(s, i, "an index has no leading zero")
	case s[i:j] == "-0":
		return selector{}, j, PathError(s, i, "-0 is not an index; write 0")
	}
	n, err := strconv.Atoi(s[i:j])
	if err != nil {
		return selector{}, j, PathError(s, i, "index %s is out of range", Clip(s[i:j]))
	}
	return selector{kind: selIndex, index: n}, j, nil
}

// firstRune is the first rune of s.
func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// skipSpace returns the first byte from i that is not a space: RFC 9535's
// blank space, S, inside brackets, in a filter and before a step after @.
// RFC 9535 also allows a tab, CR and LF there, but a path holds no control
// character.
func skipSpace(s string, i int) int {
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return i
}

// scanFilter reads a filter from byte i, just after "[?", and returns the
// offset where it ends, which scanBracket checks is the closing "]".
func scanFilter(s string, i int) (selector, int, error) {
	sel := selector{kind: selFilter}
	for {
		at := skipSpace(s, i)
		if len(sel.filter) == MaxComparisons {
			return sel, at, PathError(s, at, "a filter holds more than %d comparisons, the limit", MaxComparisons)
		}
		c, j, err := scanComparison(s, at)
		if err != nil {
			return sel, j, err
		}
		sel.filter = append(sel.filter, c)
		sel.cost += c.cost()
		j = skipSpace(s, j)
		switch rest := s[j:]; {
		case strings.HasPrefix(rest, "&&"):
			i = j + 2
		case strings.HasPrefix(rest, "||"):
			return sel, j, PathError(s, j, "|| is not supported in a filter; only && joins comparisons")
		case rest == "" || rest[0] == ']':
			return sel, j, nil
		default:
			return sel, j, PathError(s, j, "expected && or ] after a comparison but found %q", firstRune(rest))
		}
	}
}

// operand is one side of a comparison: a path from @ or a literal.
type operand struct {
	current bool
	sels    []selector
	lit     literal
}

// scanComparison reads one comparison at byte i: a path from @ and a
// literal, joined by ==, in either order.
func scanComparison(s string, i int) (comparison, int, error) {
	left, j, err := scanOperand(s, i)
	if err != nil {
		return comparison{}, j, err
	}
	k := skipSpace(s, j)
	if err := scanEquals(s, k, left.current); err != nil {
		return comparison{}, k, err
	}
	at := skipSpace(s, k+2)
	right, j, err := scanOperand(s, at)
	switch {
	case err != nil:
		return comparison{}, j, err
	case left.current && right.current:
		return comparison{}, at, PathError(s, at, "a filter compares a path from @ with a literal, not with another path")
	case !left.current && !right.current:
		return comparison{}, at, PathError(s, at, "a filter compares a path from @ with a literal, not two literals")
	case left.current:
		return comparison{current: left.sels, lit: right.lit}, j, nil
	default:
		return comparison{current: right.sels, lit: left.lit}, j, nil
	}
}

// scanEquals checks for == at byte k, and names what stands there instead.
func scanEquals(s string, k int, afterCurrent bool) error {
	rest := s[k:]
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if strings.HasPrefix(rest, op) {
			if op == "==" {
				return nil
			}
			return PathError(s, k, "operator %s is not supported in a filter; only == is", op)
		}
	}
	switch {
	case strings.HasPrefix(rest, "="):
		return PathError(s, k, "write == to compare, not =")
	case afterCurrent && (rest == "" || rest[0] == ']' || strings.HasPrefix(rest, "&&") || strings.HasPrefix(rest, "||")):
		return PathError(s, k, "a test that a path exists is not supported in a filter; compare it with ==")
	case rest == "":
		return PathError(s, k, "expected == but the path ends")
	default:
		return PathError(s, k, "expected == but found %q", firstRune(rest))
	}
}

// scanOperand reads one side of a comparison at byte i.
func scanOperand(s string, i int) (operand, int, error) {
	if i == len(s) {
		return operand{}, i, PathError(s, i, "expected @ or a literal in the filter but the path ends")
	}
	switch c := s[i]; {
	case c == '@':
		sels, j, err := scanCurrent(s, i+1)
		return operand{current: true, sels: sels}, j, err
	case c == '\'' || c == '"':
		str, j, err := scanQuoted(s, i, "string")
		if err == nil && len(str) > MaxLiteral {
			err = PathError(s, i, "a string in a filter holds more than %d bytes, the limit", MaxLiteral)
		}
		return operand{lit: literal{typ: TypeString, str: str}}, j, err
	case c == '-' || isDigit(c):
		lit, j, err := scanNumber(s, i)
		if err == nil && j-i > MaxLiteral {
			err = PathError(s, i, "a number in a filter is written in more than %d bytes, the limit", MaxLiteral)
		}
		return operand{lit: lit}, j, err
	case c == '$':
		return operand{}, i, PathError(s, i, "$ is not supported in a filter; a comparison starts its path from the member, @")
	case c == '!':
		return operand{}, i, PathError(s, i, "! is not supported in a filter")
	case c == '(':
		return operand{}, i, PathError(s, i, "parentheses are not supported in a filter; join comparisons with &&")
	case 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z':
		return scanWord(s, i)
	default:
		return operand{}, i, PathError(s, i, "expected @ or a literal in the filter but found %q", firstRune(s[i:]))
	}
}

// scanWord reads true, false or null at byte i, and names a function or a
// word that is neither.
func scanWord(s string, i int) (operand, int, error) {
	j := i
	for j < len(s) && (isDigit(s[j]) || s[j] == '_' || 'a' <= s[j] && s[j] <= 'z' || 'A' <= s[j] && s[j] <= 'Z') {
		j++
	}
	word := s[i:j]
	switch {
	case word == "true" || word == "false":
		return operand{lit: literal{typ: TypeBool, b: word == "true"}}, j, nil
	case word == "null":
		return operand{lit: literal{typ: TypeNull}}, j, nil
	case j < len(s) && s[j] == '(':
		return operand{}, i, PathError(s, i, "function %s() is not supported in a filter", Clip(word))
	case strings.EqualFold(word, "true") || strings.EqualFold(word, "false") || strings.EqualFold(word, "null"):
		return operand{}, i, PathError(s, i, "write %s in lower case", strings.ToLower(word))
	default:
		return operand{}, i, PathError(s, i, "expected @ or a literal in the filter but found %s; quote a string", Clip(word))
	}
}

// scanCurrent reads the keys and indices after @, from byte i. RFC 9535
// allows blank space before each of them: singular-query-segments =
// *(S (name-segment / index-segment)).
func scanCurrent(s string, i int) ([]selector, int, error) {
	var sels []selector
	for i < len(s) {
		var sel selector
		var err error
		start := i
		switch j := skipSpace(s, i); {
		case j < len(s) && s[j] == '.':
			sel.name, i, err = scanName(s, j+1)
		case j < len(s) && s[j] == '[':
			sel, i, err = scanBracket(s, j+1, true)
		default:
			return sels, i, nil
		}
		if err == nil && sel.kind == selName && len(sel.name) > MaxLiteral {
			err = PathError(s, skipSpace(s, start), "a key in a filter holds more than %d bytes, the limit", MaxLiteral)
		}
		if err != nil {
			return nil, i, err
		}
		sels = append(sels, sel)
	}
	return sels, i, nil
}

// cost is what testing one member costs c, in bytes: stepBytes for the
// comparison and for each step after @, the bytes of each key, which the
// lookup hashes, and the bytes of the literal, which comparing may read.
func (c comparison) cost() int64 {
	n := int64(StepBytes + len(c.lit.str) + len(c.lit.num.digits) + len(c.lit.num.exp))
	for _, step := range c.current {
		n += int64(StepBytes + len(step.name))
	}
	return n
}

// scanNumber reads a number literal at byte i, in RFC 9535's grammar.
func scanNumber(s string, i int) (literal, int, error) {
	digits := func(j int) int {
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		return j
	}
	j := i
	if s[j] == '-' {
		j++
	}
	switch {
	case j == len(s) || !isDigit(s[j]):
		return literal{}, i, PathError(s, i, "expected digits after -")
	case s[j] == '0' && j+1 < len(s) && isDigit(s[j+1]):
		return literal{}, i, PathError(s, i, "a number has no leading zero")
	}
	j = digits(j)
	if j < len(s) && s[j] == '.' {
		if j+1 == len(s) || !isDigit(s[j+1]) {
			return literal{}, i, PathError(s, j+1, "expected digits after the . of a number")
		}
		j = digits(j + 1)
	}
	if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
		j++
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j == len(s) || !isDigit(s[j]) {
			return literal{}, i, PathError(s, j, "expected digits in the exponent of a number")
		}
		j = digits(j)
	}
	d, _ := parseDecimal(s[i:j]) // reads every number the grammar above takes
	return literal{typ: TypeNumber, num: d}, j, nil
}

// decimal is a number as 0.digits times 10 to the power exp, with digits
// holding no leading or trailing zero and exp written in decimal, with a -
// before a negative one and no leading zero. Zero has no digits and no
// exponent, so one number has one decimal, and two numbers are equal when
// their decimals are. The exponent is kept as text, so a number compares
// exactly whatever its exponent, and nothing is ever expanded.
type decimal struct {
	neg    bool
	digits string
	exp    string
}

// parseDecimal reads a decimal number, with an optional sign, an integer
// part, a fraction or both, and an exponent of any length. It reports false
// for anything else. It takes time linear in s.
func parseDecimal(s string) (decimal, bool) {
	var d decimal
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		d.neg = s[i] == '-'
		i++
	}
	digits := func() string {
		start := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		return s[start:i]
	}
	whole := digits()
	var frac string
	if i < len(s) && s[i] == '.' {
		i++
		frac = digits()
	}
	if whole == "" && frac == "" {
		return d, false
	}
	expNeg, exp := false, ""
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			expNeg = s[i] == '-'
			i++
		}
		if exp = digits(); exp == "" {
			return d, false
		}
	}
	if i != len(s) {
		return d, false
	}
	// The value is 0.all times 10^(exp + point).
	all := strings.TrimLeft(whole+frac, "0")
	point := int64(len(whole)) - int64(len(whole)+len(frac)-len(all))
	if all = strings.TrimRight(all, "0"); all == "" {
		return decimal{}, true
	}
	d.digits, d.exp = all, addExponent(expNeg, strings.TrimLeft(exp, "0"), point)
	return d, true
}

// addExponent returns the exponent -mag (when neg) or mag, plus delta, in
// decimal. mag has no leading zero. A delta is at most the length of a
// number's text, so for a mag of 19 digits or more the sum keeps mag's sign
// and only its last digits change.
func addExponent(neg bool, mag string, delta int64) string {
	if len(mag) <= 18 {
		v, _ := strconv.ParseInt("0"+mag, 10, 64)
		if neg {
			v = -v
		}
		return strconv.FormatInt(v+delta, 10)
	}
	b := []byte(mag)
	sub := (delta < 0) != neg
	d := delta
	if d < 0 {
		d = -d
	}
	carry := int64(0)
	for k := len(b) - 1; k >= 0 && (d > 0 || carry != 0); k-- {
		digit := int64(b[k]-'0') + carry
		if sub {
			digit -= d % 10
		} else {
			digit += d % 10
		}
		d /= 10
		carry = 0
		switch {
		case digit < 0:
			digit, carry = digit+10, -1
		case digit > 9:
			digit, carry = digit-10, 1
		}
		b[k] = "0123456789"[digit]
	}
	// An addition may carry past the first digit; a subtraction may leave
	// leading zeros, never a borrow, since mag is larger than delta.
	out := strings.TrimLeft(string(b), "0")
	if carry == 1 {
		out = "1" + string(b)
	}
	if neg {
		return "-" + out
	}
	return out
}

// numberOf is the value of a number scalar as the file wrote it: a JSON
// number; a TOML integer, in any base, or float; or a YAML !!int or !!float.
// An integer goes through strconv with base 0, as yaml.v3 resolves one, so
// YAML's 0x1F, 0o17 and 017 are 31, 15 and 15; JSON and TOML write no
// integer that base 0 reads another way. An infinity or a NaN equals no
// literal, and reports false.
//
// numberOf takes time linear in text; number reads it once for each node.
func numberOf(text string) (decimal, bool) {
	t := strings.ReplaceAll(text, "_", "")
	if n, err := strconv.ParseInt(t, 0, 64); err == nil {
		t = strconv.FormatInt(n, 10)
	} else if n, err := strconv.ParseUint(t, 0, 64); err == nil {
		t = strconv.FormatUint(n, 10)
	} else if n, ok := yamlPrefixed(t); ok {
		t = n
	}
	return parseDecimal(t)
}

// yamlPrefixed reads a binary or octal integer the way yaml.v3's resolver
// does after base 0 fails: it parses what follows 0b or 0o, sign and all, so
// 0b+1 is 1 and 0o-7 is -7. JSON and TOML write no such number.
func yamlPrefixed(t string) (string, bool) {
	for _, p := range []struct {
		prefix string
		base   int
	}{{"0b", 2}, {"0o", 8}} {
		switch {
		case strings.HasPrefix(t, p.prefix):
			if n, err := strconv.ParseInt(t[2:], p.base, 64); err == nil {
				return strconv.FormatInt(n, 10), true
			}
			if n, err := strconv.ParseUint(t[2:], p.base, 64); err == nil {
				return strconv.FormatUint(n, 10), true
			}
		case strings.HasPrefix(t, "-"+p.prefix):
			if n, err := strconv.ParseInt("-"+t[3:], p.base, 64); err == nil {
				return strconv.FormatInt(n, 10), true
			}
		}
	}
	return "", false
}

// number is n's value as numberOf reads it, read on the first comparison
// and kept: an alias shares its anchor's node, and many aliases of one long
// number would otherwise read it once for each.
func (n *Node) number() (decimal, bool) {
	if n.num == nil {
		d, ok := numberOf(n.Text)
		n.num = &number{d, ok}
	}
	return n.num.d, n.num.ok
}

// equals reports whether n is a scalar of l's type and value, compared as
// RFC 9535 compares: a number equals a number of the same value, whatever
// the spelling, and a string never equals a number.
func (l literal) equals(n *Node) bool {
	if n == nil || n.Shape != Scalar || n.Type != l.typ {
		return false
	}
	switch l.typ {
	case TypeString:
		return n.Text == l.str
	case TypeNumber:
		d, ok := n.number()
		return ok && d == l.num
	case TypeBool:
		// YAML writes true as true, True or TRUE.
		return strings.EqualFold(n.Text, "true") == l.b
	default: // null
		return true
	}
}

// holds reports whether every comparison of a filter holds for n.
func (sel selector) holds(n *Node) bool {
	for _, c := range sel.filter {
		cur := n
		for _, step := range c.current {
			if cur = step.child(cur); cur == nil {
				break
			}
		}
		if !c.lit.equals(cur) {
			return false
		}
	}
	return true
}

// Eval returns every node the path selects from root, in document order. A
// key or an index that is absent selects nothing, and so does a key applied to
// a sequence or an index applied to a mapping, as in RFC 9535.
//
// It stops with errTooManyValues once a step selects more than maxValues
// nodes, and with errFilterWork before its filters pass maxFilterBytes. A
// number a filter compares is read once for each node (node.number), so
// testing a member costs the steps of the comparisons, whatever the length
// of its text.
func (p Path) Eval(roots ...*Node) ([]*Node, error) {
	cur := roots
	var work int64
	for _, sel := range p.sels {
		var next []*Node
		for _, n := range cur {
			if sel.kind == selFilter {
				if work += sel.cost * int64(len(n.Vals)+len(n.Items)); work > MaxFilterBytes {
					return nil, ErrFilterWork
				}
			}
			next = sel.apply(n, next)
			if len(next) > MaxValues {
				return nil, ErrTooManyValues
			}
		}
		cur = next
	}
	if len(cur) > MaxValues {
		return nil, ErrTooManyValues
	}
	return cur, nil
}

// ErrTooManyValues is a path that selects more than MaxValues nodes at a
// step.
var ErrTooManyValues = errors.New("too many values")

// The bounds on a filter, whose cost is the members it tests times what the
// comparisons take on each. The path comes from the configuration and the
// members from the file, so each is bounded.
const (
	// MaxComparisons is the most comparisons one filter joins with &&. A
	// selector by a child's value takes one to three; the number is far
	// past any of them.
	MaxComparisons = 64

	// MaxLiteral is the most bytes a filter's literal holds, a string as
	// its escapes decode it and a number as written, and the most bytes a
	// key after @ holds. Comparing a literal and looking a key up each cost
	// up to its length on every member. The longest literals a selector by
	// value needs are an image reference with a sha256 digest, about 150
	// bytes, and a URL, which the sitemaps protocol and most tools keep
	// under 2048 characters; a key is shorter.
	MaxLiteral = 2048

	// MaxFilterBytes is what the filters of one path may cost, in bytes:
	// for each member tested, the cost of each comparison (see
	// comparison.cost), counted before the members are tested. Visiting a
	// member hashes no key: a mapping's values are kept in order.
	//
	// stepBytes is the fixed cost of a comparison or a step after @,
	// counted in bytes: measured, one took 25 to 110 ns, the time hashing or
	// comparing a few hundred bytes takes. Measured at the limit, with 64
	// comparisons on each member, the worst shapes took 0.36 s comparing
	// strings of 2048 bytes, 0.80 s looking up a key of 2048 bytes among
	// ten, 1.38 s a number two keys down and 1.42 s a number.
	MaxFilterBytes = 4_000_000_000
	StepBytes      = 256
)

// ErrFilterWork is a path whose filters would pass MaxFilterBytes.
var ErrFilterWork = fmt.Errorf("its filters would hash and compare more than %d bytes, the limit; narrow the path before the filter, or compare fewer or shorter keys and literals", int64(MaxFilterBytes))

// child is the node a key or an index selects from n, or nil.
func (sel selector) child(n *Node) *Node {
	switch sel.kind {
	case selName:
		if v, ok := n.Field[sel.name]; ok && n.Shape == Mapping {
			return v
		}
	case selIndex:
		i := sel.index
		if i < 0 {
			i += len(n.Items)
		}
		if n.Shape == Sequence && 0 <= i && i < len(n.Items) {
			return n.Items[i]
		}
	default: // [*] and a filter select many nodes, through apply
	}
	return nil
}

func (sel selector) apply(n *Node, out []*Node) []*Node {
	switch sel.kind {
	case selWild:
		out = append(out, n.Vals...)
		out = append(out, n.Items...)
	case selFilter:
		for _, v := range n.Vals {
			if sel.holds(v) {
				out = append(out, v)
			}
		}
		for _, v := range n.Items {
			if sel.holds(v) {
				out = append(out, v)
			}
		}
	default:
		if v := sel.child(n); v != nil {
			out = append(out, v)
		}
	}
	return out
}
