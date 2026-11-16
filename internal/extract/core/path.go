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
//	filter     = "?" S term *( S "&&" S term )      (at most MaxComparisons terms)
//	term       = compare | [ "!" S ] query
//	compare    = current S "==" S literal | literal S "==" S current
//	current    = "@" *( S ( "." name | "[" S ( quoted | index ) S "]" ) )
//	query      = "@" *( S ( "." name | "[" S ( quoted | index | "*" | filter ) S "]" ) )
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
// A filter is RFC 9535's filter selector without ||, parentheses, functions
// and any comparison but ==: it selects each member of a sequence and each
// value of a mapping, in the file's order, for which every term holds. A
// comparison holds when the path after @ selects a scalar of the literal's
// type and value; a path that selects nothing, or a mapping or a sequence,
// equals no literal. A query alone is RFC 9535's existence test: it holds
// when the query selects at least one node from the member, of any shape and
// null included, and with ! before it when it selects none. A query takes
// [*] and filters, so a filter nests inside another: [?@.ports[?@.port ==
// 6443]] keeps the members with a port 6443, and [?@.tags[?@ == 'docker']]
// those whose tags hold docker. Filters nest at most MaxFilterDepth deep.
// Every other construct RFC 9535 allows in a filter is refused, by name, at
// its column.

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
	filter []term // every one holds for a member the filter selects
	cost   int64  // what testing one member costs before its queries run: see MaxFilterBytes
}

// term is one test of a filter: a comparison, @path == literal with the
// literal on either side, or a query, which tests that a path from @
// selects a node.
type term struct {
	current []selector // the steps from the member, none for @ itself
	lit     literal    // what a comparison compares with
	query   bool       // a test that current selects a node, not a comparison
	not     bool       // a query written after !, which holds when current selects none
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
	if err := checkPathText(s); err != nil {
		return p, err
	}
	sels, i, err := scanHead(s)
	if err != nil {
		return p, err
	}
	p.sels, err = scanSelectors(s, i, sels)
	return p, err
}

// checkPathText refuses a path that is not valid UTF-8 or holds a control
// character, at the first byte that is either.
func checkPathText(s string) error {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			return PathError(s, i, "the path is not valid UTF-8")
		case r < 0x20:
			return PathError(s, i, "control character U+%04X is not allowed in a path", r)
		}
		i += size
	}
	return nil
}

// scanHead reads the start of a non-empty path: the $ of the whole
// document, or a key written without a dot before it, or nothing before a
// . or a [. It returns the selectors it read and the byte after them.
func scanHead(s string) ([]selector, int, error) {
	if s[0] == '$' {
		return nil, 1, nil
	}
	if s[0] == '.' || s[0] == '[' {
		return nil, 0, nil
	}
	name, next, err := scanName(s, 0)
	if err != nil {
		return nil, 0, err
	}
	return []selector{{kind: selName, name: name}}, next, nil
}

// scanSelectors reads the steps from byte i to the end of the path, each a
// .key or a bracket, after sels. On an error it returns the steps read
// before it.
func scanSelectors(s string, i int, sels []selector) ([]selector, error) {
	for i < len(s) {
		var sel selector
		var err error
		switch s[i] {
		case '.':
			sel.name, i, err = scanName(s, i+1)
		case '[':
			sel, i, err = scanBracket(s, i+1, 0)
		default:
			err = PathError(s, i, "expected . or [ but found %q", firstRune(s[i:]))
		}
		if err != nil {
			return sels, err
		}
		sels = append(sels, sel)
	}
	return sels, nil
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
	if i == len(s) {
		return "", i, PathError(s, i, "expected a key name but the path ends")
	}
	j := nameEnd(s, i)
	if isNameFirst(firstRune(s[i:])) { // a name character, so j > i
		return s[i:j], j, nil
	}
	return "", i, nameError(s, i, j)
}

// nameEnd returns the byte after the run of name characters from byte j.
func nameEnd(s string, j int) int {
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if !isNameRune(r) {
			break
		}
		j += size
	}
	return j
}

// nameError names what stands at byte i where a key should, when the name
// characters from i end at byte j: a run that does not start as a key does,
// or a character no key holds.
func nameError(s string, i, j int) error {
	if j > i {
		return badNameStart(s, i, j)
	}
	switch first := firstRune(s[i:]); first {
	case '.':
		return PathError(s, i, "recursive descent (..) is not supported")
	case '*':
		star := ".*"
		if i == 0 {
			star = "*"
		}
		return PathError(s, i, "write [*] for every member, not %s", star)
	default:
		return PathError(s, i, "expected a key name but found %q; write ['...'] for a key with that character", first)
	}
}

// badNameStart names a run of name characters from byte i to byte j that
// starts with a digit or a -, which no key written without quotes does.
func badNameStart(s string, i, j int) error {
	after := "a key after ."
	if i == 0 {
		after = "a key"
	}
	if isDigit(s[i]) {
		return PathError(s, i, "%s cannot start with a digit; write [n] for an index, or ['%s'] for a key", after, Clip(s[i:j]))
	}
	return PathError(s, i, "%s cannot start with -; write ['%s']", after, Clip(s[i:j]))
}

// scanBracket reads what follows a "[" at byte i, through the closing "]".
// depth is the filters the bracket stands in: 0 in the path itself, 1 after
// @ in a filter, 2 in a filter inside that, and so on.
func scanBracket(s string, i, depth int) (selector, int, error) {
	i = skipSpace(s, i)
	if i == len(s) {
		return selector{}, i, PathError(s, i, "expected a quoted key, an index or * after [ but the path ends")
	}
	sel, j, err := scanSelector(s, i, depth)
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

// scanSelector reads what a bracket holds, from byte i, which is in the
// path.
func scanSelector(s string, i, depth int) (selector, int, error) {
	switch s[i] {
	case '*':
		return selector{kind: selWild}, i + 1, nil
	case '\'', '"':
		name, j, err := scanQuoted(s, i, "key")
		return selector{name: name}, j, err
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return scanIndex(s, i)
	case '?':
		if depth == MaxFilterDepth {
			return selector{}, i, PathError(s, i, "filters nest more than %d deep, the limit", MaxFilterDepth)
		}
		return scanFilter(s, i+1, depth+1)
	}
	return selector{}, i, PathError(s, i, "expected a quoted key, an index or * after [ but found %q", firstRune(s[i:]))
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
	r, ok := hex4(s, j)
	switch {
	case !ok:
		return 0, 0, PathError(s, j, "\\u takes four hex digits")
	case isLowSurrogate(r):
		return 0, 0, PathError(s, j, "\\u%04X is a low surrogate without a high one", r)
	case r < 0xD800 || r > 0xDBFF:
		return r, 6, nil
	}
	lo, ok := hex4(s, j+6)
	if !ok || !isLowSurrogate(lo) {
		return 0, 0, PathError(s, j, "\\u%04X is a high surrogate without a low one after it", r)
	}
	return utf16.DecodeRune(r, lo), 12, nil
}

// isLowSurrogate reports whether r is a low surrogate, the second half of
// a pair.
func isLowSurrogate(r rune) bool { return 0xDC00 <= r && r <= 0xDFFF }

// hex4 reads \uXXXX at byte at, and reports false for anything else.
func hex4(s string, at int) (rune, bool) {
	if at+6 > len(s) || s[at] != '\\' || s[at+1] != 'u' {
		return 0, false
	}
	var r rune
	for _, c := range s[at+2 : at+6] {
		v, ok := hexValue(c)
		if !ok {
			return 0, false
		}
		r = r<<4 | v
	}
	return r, true
}

// hexValue is the value of the hex digit c, or false when c is none.
func hexValue(c rune) (rune, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// scanIndex reads an index starting at byte i.
func scanIndex(s string, i int) (selector, int, error) {
	j := i
	if s[j] == '-' {
		j++
	}
	start := j
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	if err := indexError(s, i, s[start:j], s[i:j]); err != nil {
		return selector{}, j, err
	}
	n, err := strconv.Atoi(s[i:j])
	if err != nil {
		return selector{}, j, PathError(s, i, "index %s is out of range", Clip(s[i:j]))
	}
	return selector{kind: selIndex, index: n}, j, nil
}

// indexError refuses an index at byte i, written text, whose digits are
// digits, that RFC 9535 does not allow: a - alone, a leading zero, or -0.
func indexError(s string, i int, digits, text string) error {
	switch {
	case digits == "":
		return PathError(s, i, "expected digits after -")
	case len(digits) > 1 && digits[0] == '0':
		return PathError(s, i, "an index has no leading zero")
	case text == "-0":
		return PathError(s, i, "-0 is not an index; write 0")
	}
	return nil
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

// scanFilter reads a filter from byte i, just after "[?", at depth, and
// returns the offset where it ends, which scanBracket checks is the closing
// "]".
func scanFilter(s string, i, depth int) (selector, int, error) {
	sel := selector{kind: selFilter}
	for {
		at := skipSpace(s, i)
		if len(sel.filter) == MaxComparisons {
			return sel, at, PathError(s, at, "a filter joins more than %d terms, the limit", MaxComparisons)
		}
		c, j, err := scanTerm(s, at, depth)
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
			return sel, j, PathError(s, j, "|| is not supported in a filter; only && joins terms")
		case rest == "" || rest[0] == ']':
			return sel, j, nil
		default:
			return sel, j, PathError(s, j, "expected && or ] after a comparison but found %q", firstRune(rest))
		}
	}
}

// operand is one side of a comparison, a path from @ or a literal, or a
// query.
type operand struct {
	current bool
	sels    []selector
	many    int // the byte of the first step after @ that selects many nodes, or 0 when there is none, since no step after @ starts at byte 0
	lit     literal
}

// endsTerm reports whether rest, the path after a term and its spaces,
// starts with what may follow a term.
func endsTerm(rest string) bool {
	return rest == "" || rest[0] == ']' || strings.HasPrefix(rest, "&&") || strings.HasPrefix(rest, "||")
}

// scanTerm reads one term at byte i, in a filter at depth: a comparison, a
// path from @ and a literal joined by == in either order, or a query, a
// path from @ with no == after it.
func scanTerm(s string, i, depth int) (term, int, error) {
	if i < len(s) && s[i] == '!' {
		return scanNot(s, i, depth)
	}
	left, j, err := scanOperand(s, i, depth)
	if err != nil {
		return term{}, j, err
	}
	k := skipSpace(s, j)
	if left.current && endsTerm(s[k:]) {
		return term{current: left.sels, query: true}, j, nil
	}
	if at, err := comparesFrom(s, k, left); err != nil {
		return term{}, at, err
	}
	at := skipSpace(s, k+2)
	right, j, err := scanOperand(s, at, depth)
	if err != nil {
		return term{}, j, err
	}
	return pairTerm(s, at, j, left, right)
}

// comparesFrom checks that a comparison follows left at byte k: an == and,
// when left is a path from @, one that selects one node. On an error it
// returns the byte the error names.
func comparesFrom(s string, k int, left operand) (int, error) {
	if err := scanEquals(s, k); err != nil {
		return k, err
	}
	if left.current && left.many != 0 {
		return left.many, manyError(s, left.many)
	}
	return k, nil
}

// pairTerm joins the two sides of a comparison, the right one at byte at
// and ending at byte j, into a term: one side a path from @ that selects
// one node, the other a literal.
func pairTerm(s string, at, j int, left, right operand) (term, int, error) {
	if left.current == right.current {
		if left.current {
			return term{}, at, PathError(s, at, "a filter compares a path from @ with a literal, not with another path")
		}
		return term{}, at, PathError(s, at, "a filter compares a path from @ with a literal, not two literals")
	}
	if left.current {
		return term{current: left.sels, lit: right.lit}, j, nil
	}
	if right.many != 0 {
		return term{}, right.many, manyError(s, right.many)
	}
	return term{current: right.sels, lit: left.lit}, j, nil
}

// manyError names the step at byte at, [*] or a filter, which selects many
// nodes where a comparison takes one.
func manyError(s string, at int) error {
	if s[at] == '*' {
		return PathError(s, at, "[*] selects many nodes, and a comparison takes one; after @ write only .key, ['key'] or [n], or leave out the == to test that the path selects a node")
	}
	return PathError(s, at, "a filter after @ selects many nodes, and a comparison takes one; leave out the == to test that the path selects a node")
}

// scanNot reads a query after the ! at byte i.
func scanNot(s string, i, depth int) (term, int, error) {
	at := skipSpace(s, i+1)
	switch {
	case at < len(s) && s[at] == '(':
		return term{}, at, PathError(s, at, "parentheses are not supported in a filter; ! takes a path from @, such as !@.key")
	case at == len(s) || s[at] != '@':
		return term{}, i, PathError(s, i, "! takes a path from @, such as !@.key, and tests that it selects nothing")
	}
	sels, _, j, err := scanCurrent(s, at+1, depth)
	if err != nil {
		return term{}, j, err
	}
	if k := skipSpace(s, j); !endsTerm(s[k:]) {
		return term{}, i, PathError(s, i, "! takes a path from @ alone, not a comparison; parentheses, which RFC 9535 needs for !(@.key == 1), are not supported")
	}
	return term{current: sels, query: true, not: true}, j, nil
}

// scanEquals checks for == at byte k, and names what stands there instead.
func scanEquals(s string, k int) error {
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
	case rest == "":
		return PathError(s, k, "expected == but the path ends")
	default:
		return PathError(s, k, "expected == but found %q", firstRune(rest))
	}
}

// scanOperand reads one side of a comparison, or a query, at byte i, in a
// filter at depth.
func scanOperand(s string, i, depth int) (operand, int, error) {
	if i == len(s) {
		return operand{}, i, PathError(s, i, "expected @ or a literal in the filter but the path ends")
	}
	switch s[i] {
	case '@':
		sels, many, j, err := scanCurrent(s, i+1, depth)
		return operand{current: true, sels: sels, many: many}, j, err
	case '\'', '"':
		return scanStringOperand(s, i)
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return scanNumberOperand(s, i)
	}
	if isLetter(s[i]) {
		return scanWord(s, i)
	}
	return operand{}, i, operandError(s, i)
}

// scanStringOperand reads a quoted string at byte i, at most MaxLiteral
// bytes once read.
func scanStringOperand(s string, i int) (operand, int, error) {
	str, j, err := scanQuoted(s, i, "string")
	if err == nil && len(str) > MaxLiteral {
		err = PathError(s, i, "a string in a filter holds more than %d bytes, the limit", MaxLiteral)
	}
	return operand{lit: literal{typ: TypeString, str: str}}, j, err
}

// scanNumberOperand reads a number at byte i, written in at most
// MaxLiteral bytes.
func scanNumberOperand(s string, i int) (operand, int, error) {
	lit, j, err := scanNumber(s, i)
	if err == nil && j-i > MaxLiteral {
		err = PathError(s, i, "a number in a filter is written in more than %d bytes, the limit", MaxLiteral)
	}
	return operand{lit: lit}, j, err
}

// operandError names what stands at byte i where an operand should and no
// operand starts.
func operandError(s string, i int) error {
	switch s[i] {
	case '$':
		return PathError(s, i, "$ is not supported in a filter; a comparison starts its path from the member, @")
	case '!':
		return PathError(s, i, "! is not supported in a comparison; it stands only before a path from @ that a term tests, such as !@.key")
	case '(':
		return PathError(s, i, "parentheses are not supported in a filter; join terms with &&")
	}
	return PathError(s, i, "expected @ or a literal in the filter but found %q", firstRune(s[i:]))
}

// isLetter reports whether c is an ASCII letter.
func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

// isWordByte reports whether c may stand in a word of a filter: a letter,
// a digit or _.
func isWordByte(c byte) bool { return isDigit(c) || c == '_' || isLetter(c) }

// scanWord reads true, false or null at byte i, and names a function or a
// word that is neither.
func scanWord(s string, i int) (operand, int, error) {
	j := i
	for j < len(s) && isWordByte(s[j]) {
		j++
	}
	switch word := s[i:j]; word {
	case "true", "false":
		return operand{lit: literal{typ: TypeBool, b: word == "true"}}, j, nil
	case "null":
		return operand{lit: literal{typ: TypeNull}}, j, nil
	}
	return operand{}, i, wordError(s, i, j)
}

// wordError names the word from byte i to byte j, which is not a literal:
// a function, a literal not in lower case, or a word to quote.
func wordError(s string, i, j int) error {
	word := s[i:j]
	switch {
	case j < len(s) && s[j] == '(':
		return PathError(s, i, "function %s() is not supported in a filter", Clip(word))
	case strings.EqualFold(word, "true") || strings.EqualFold(word, "false") || strings.EqualFold(word, "null"):
		return PathError(s, i, "write %s in lower case", strings.ToLower(word))
	default:
		return PathError(s, i, "expected @ or a literal in the filter but found %s; quote a string", Clip(word))
	}
}

// scanCurrent reads the steps after @, from byte i, in a filter at depth.
// RFC 9535 allows blank space before each of them: segments = *(S
// segment). It returns the byte of the first step that selects many nodes,
// [*] or a filter, or 0 when there is none, since no step after @ starts at
// byte 0: a comparison takes only a path without one.
func scanCurrent(s string, i, depth int) ([]selector, int, int, error) {
	var sels []selector
	many := 0
	for i < len(s) {
		j := skipSpace(s, i)
		sel, next, ok, err := scanStep(s, j, depth)
		if !ok {
			return sels, many, i, nil
		}
		if err == nil {
			err = checkFilterKey(s, j, sel)
		}
		if err != nil {
			return nil, many, next, err
		}
		if many == 0 && (sel.kind == selWild || sel.kind == selFilter) {
			many = skipSpace(s, j+1)
		}
		sels = append(sels, sel)
		i = next
	}
	return sels, many, i, nil
}

// scanStep reads a step after @ at byte j, a .key or a bracket, in a
// filter at depth. It reports false when no step starts there.
func scanStep(s string, j, depth int) (selector, int, bool, error) {
	if j == len(s) {
		return selector{}, j, false, nil
	}
	switch s[j] {
	case '.':
		name, next, err := scanName(s, j+1)
		return selector{name: name}, next, true, err
	case '[':
		sel, next, err := scanBracket(s, j+1, depth)
		return sel, next, true, err
	}
	return selector{}, j, false, nil
}

// checkFilterKey refuses a key step at byte j after @ that holds more than
// MaxLiteral bytes.
func checkFilterKey(s string, j int, sel selector) error {
	if sel.kind == selName && len(sel.name) > MaxLiteral {
		return PathError(s, j, "a key in a filter holds more than %d bytes, the limit", MaxLiteral)
	}
	return nil
}

// cost is what testing one member costs c before a query runs, in bytes.
// For a comparison it is StepBytes for the comparison and for each step
// after @, the bytes of each key, which the lookup hashes, and the bytes of
// the literal, which comparing may read. For a query it is StepBytes: the
// query charges its steps as it takes them (meter.exists), since how many it
// takes depends on the file.
func (c term) cost() int64 {
	if c.query {
		return StepBytes
	}
	n := int64(StepBytes + len(c.lit.str) + len(c.lit.num.digits) + len(c.lit.num.exp))
	for _, step := range c.current {
		n += int64(StepBytes + len(step.name))
	}
	return n
}

// scanNumber reads a number literal at byte i, in RFC 9535's grammar.
func scanNumber(s string, i int) (literal, int, error) {
	j, err := scanInteger(s, i)
	if err == nil {
		j, err = scanFraction(s, j)
	}
	if err == nil {
		j, err = scanExponent(s, j)
	}
	if err != nil {
		return literal{}, i, err
	}
	d, _ := parseDecimal(s[i:j]) // reads every number the grammar above takes
	return literal{typ: TypeNumber, num: d}, j, nil
}

// scanDigits returns the byte after the run of digits from byte j.
func scanDigits(s string, j int) int {
	for j < len(s) && isDigit(s[j]) {
		j++
	}
	return j
}

// digitAt reports whether byte j of s is a digit.
func digitAt(s string, j int) bool { return j < len(s) && isDigit(s[j]) }

// scanInteger reads the integer part of a number at byte i, with its -.
func scanInteger(s string, i int) (int, error) {
	j := i
	if s[j] == '-' {
		j++
	}
	switch {
	case !digitAt(s, j):
		return 0, PathError(s, i, "expected digits after -")
	case s[j] == '0' && digitAt(s, j+1):
		return 0, PathError(s, i, "a number has no leading zero")
	}
	return scanDigits(s, j), nil
}

// scanFraction reads the fraction of a number at byte j, if one is there.
func scanFraction(s string, j int) (int, error) {
	if j == len(s) || s[j] != '.' {
		return j, nil
	}
	if !digitAt(s, j+1) {
		return 0, PathError(s, j+1, "expected digits after the . of a number")
	}
	return scanDigits(s, j+1), nil
}

// scanExponent reads the exponent of a number at byte j, if one is there.
func scanExponent(s string, j int) (int, error) {
	if j == len(s) || s[j] != 'e' && s[j] != 'E' {
		return j, nil
	}
	_, j = signAt(s, j+1)
	if !digitAt(s, j) {
		return 0, PathError(s, j, "expected digits in the exponent of a number")
	}
	return scanDigits(s, j), nil
}

// signAt reads a + or - at byte i, if one is there, and reports whether it
// was a - and the byte after it.
func signAt(s string, i int) (bool, int) {
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		return s[i] == '-', i + 1
	}
	return false, i
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
	var i int
	d.neg, i = signAt(s, 0)
	whole, frac, i := mantissaText(s, i)
	if whole == "" && frac == "" {
		return d, false
	}
	expNeg, exp, i, ok := exponentText(s, i)
	if !ok || i != len(s) {
		return d, false
	}
	return normalDecimal(d.neg, whole, frac, expNeg, exp), true
}

// mantissaText reads the digits of a number's integer part and of its
// fraction from byte i, and returns the byte after them.
func mantissaText(s string, i int) (string, string, int) {
	j := scanDigits(s, i)
	whole := s[i:j]
	if j == len(s) || s[j] != '.' {
		return whole, "", j
	}
	k := scanDigits(s, j+1)
	return whole, s[j+1 : k], k
}

// exponentText reads an exponent from byte i, if one is there: its sign,
// its digits and the byte after them. It reports false for an e with no
// digits after it.
func exponentText(s string, i int) (bool, string, int, bool) {
	if i == len(s) || s[i] != 'e' && s[i] != 'E' {
		return false, "", i, true
	}
	neg, j := signAt(s, i+1)
	k := scanDigits(s, j)
	return neg, s[j:k], k, k > j
}

// normalDecimal is the decimal of the number whose integer part and
// fraction have the digits whole and frac, times 10 to the power -exp (when
// expNeg) or exp.
func normalDecimal(neg bool, whole, frac string, expNeg bool, exp string) decimal {
	// The value is 0.all times 10^(exp + point).
	all := strings.TrimLeft(whole+frac, "0")
	point := int64(len(whole)) - int64(len(whole)+len(frac)-len(all))
	if all = strings.TrimRight(all, "0"); all == "" {
		return decimal{}
	}
	return decimal{neg: neg, digits: all, exp: addExponent(expNeg, strings.TrimLeft(exp, "0"), point)}
}

// addExponent returns the exponent -mag (when neg) or mag, plus delta, in
// decimal. mag has no leading zero. A delta is at most the length of a
// number's text, so for a mag of 19 digits or more the sum keeps mag's sign
// and only its last digits change.
func addExponent(neg bool, mag string, delta int64) string {
	if len(mag) > 18 {
		return addLongExponent(neg, mag, delta)
	}
	v, _ := strconv.ParseInt("0"+mag, 10, 64)
	if neg {
		v = -v
	}
	return strconv.FormatInt(v+delta, 10)
}

// addLongExponent is addExponent for a mag too long for an int64, done
// digit by digit from the last.
func addLongExponent(neg bool, mag string, delta int64) string {
	b := []byte(mag)
	sub := (delta < 0) != neg
	d := delta
	if d < 0 {
		d = -d
	}
	carry := int64(0)
	for k := len(b) - 1; k >= 0 && (d > 0 || carry != 0); k-- {
		step := d % 10
		if sub {
			step = -step
		}
		var digit int64
		digit, carry = carried(int64(b[k]-'0') + carry + step)
		d /= 10
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

// carried brings a digit sum of -10 to 19 into 0 to 9, and returns the
// carry out of it: -1, 0 or 1.
func carried(digit int64) (int64, int64) {
	switch {
	case digit < 0:
		return digit + 10, -1
	case digit > 9:
		return digit - 10, 1
	}
	return digit, 0
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

// notLiteralError is a filter's refusal to compare a value the file only
// computes: its text is not the value, so neither == nor its absence may be
// read from it.
type notLiteralError struct{ n *Node }

func (e notLiteralError) Error() string {
	return fmt.Sprintf("a filter compares %s at line %d, a value the file only computes, which a filter does not compare as text; select the value itself, or filter on another key", Quote(e.n.Text), e.n.Line)
}

// meter counts the work of a path's filters, in the bytes MaxFilterBytes
// bounds.
type meter int64

// charge adds n to the work, and fails once the work passes MaxFilterBytes.
func (m *meter) charge(n int64) error {
	if *m += meter(n); *m > MaxFilterBytes {
		return ErrFilterWork
	}
	return nil
}

// holds reports whether every term of a filter holds for n.
func (m *meter) holds(sel selector, n *Node) (bool, error) {
	for _, c := range sel.filter {
		ok, err := m.holdsTerm(c, n)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// holdsTerm reports whether one term of a filter holds for n: a query
// selects a node, or selects none when it is negated, and a comparison's
// path selects the node that equals its literal.
func (m *meter) holdsTerm(c term, n *Node) (bool, error) {
	if c.query {
		found, err := m.exists(n, c.current)
		if err != nil {
			return false, err
		}
		return found != c.not, nil
	}
	cur := follow(n, c.current)
	if cur != nil && cur.NotLiteral {
		return false, notLiteralError{cur}
	}
	return c.lit.equals(cur), nil
}

// follow is the node the steps, each a key or an index, select from n, or
// nil.
func follow(n *Node, steps []selector) *Node {
	for _, step := range steps {
		if n = step.child(n); n == nil {
			return nil
		}
	}
	return n
}

// exists reports whether sels select a node from n, and stops at the first
// one. It goes depth first, without recursion but through a nested filter's
// terms: a frame for each step that selects many nodes holds the members
// that step has not yet taken, so the frames are at most the steps. Each
// step it takes from a node charges StepBytes and the bytes of its key
// before it is taken, and a filter charges its cost for each member before
// testing them, as in Eval.
func (m *meter) exists(n *Node, sels []selector) (bool, error) {
	var stack []frame
	cur, step := n, 0
	for {
		var err error
		if cur, err = m.descend(cur, step, sels, &stack); err != nil {
			return false, err
		}
		if cur != nil {
			return true, nil
		}
		v, next, err := m.pop(&stack)
		if v == nil || err != nil {
			return false, err
		}
		cur, step = v, next
	}
}

// descend takes the steps of sels from step on, from cur, while each
// selects one node. It returns the node the last step selects, or nil once
// a step selects none or pushes a frame for many onto stack.
func (m *meter) descend(cur *Node, step int, sels []selector, stack *[]frame) (*Node, error) {
	for cur != nil && step < len(sels) {
		var err error
		if cur, step, err = m.stepInto(&sels[step], cur, step, stack); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// stepInto takes the step sel, at step in a query, from cur, after charging
// for it. A key or an index returns the node it selects and the next step;
// [*] and a filter push a frame of cur's members onto stack and return
// nil. Every kind is named and none has a default, so the exhaustive linter
// refuses a kind added without its case here.
func (m *meter) stepInto(sel *selector, cur *Node, step int, stack *[]frame) (*Node, int, error) {
	if err := m.charge(StepBytes + int64(len(sel.name))); err != nil {
		return nil, step, err
	}
	switch sel.kind {
	case selName, selIndex:
		return sel.child(cur), step + 1, nil
	case selFilter:
		if err := m.charge(sel.cost * int64(len(cur.Vals)+len(cur.Items))); err != nil {
			return nil, step, err
		}
		*stack = append(*stack, frame{filter: sel, next: step + 1, vals: cur.Vals, items: cur.Items})
	case selWild:
		*stack = append(*stack, frame{next: step + 1, vals: cur.Vals, items: cur.Items})
	}
	return nil, step, nil
}

// frame is a step of a query that selects many nodes, with the members of
// the node it fans out from that it has not yet taken.
type frame struct {
	filter      *selector // the filter that tests each member, or nil for [*]
	next        int       // the step after this one
	vals, items []*Node   // the members not yet taken
}

// pop takes the next member of the innermost frame that the frame's filter
// keeps, dropping each frame it empties, and returns it with the step after
// that frame. It returns nil once the stack is empty, so it needs every member
// to be non-nil, and no reader puts a nil node in Vals or Items. Each frame
// holds members that sit in the file, so the loop ends without the work budget.
func (m *meter) pop(stack *[]frame) (*Node, int, error) {
	for len(*stack) > 0 {
		f := &(*stack)[len(*stack)-1]
		if len(f.vals) == 0 && len(f.items) == 0 {
			*stack = (*stack)[:len(*stack)-1]
			continue
		}
		var v *Node
		if len(f.vals) > 0 {
			v, f.vals = f.vals[0], f.vals[1:]
		} else {
			v, f.items = f.items[0], f.items[1:]
		}
		if f.filter == nil {
			return v, f.next, nil
		}
		ok, err := m.holds(*f.filter, v)
		if err != nil || ok {
			return v, f.next, err
		}
	}
	return nil, 0, nil
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
	var m meter
	return p.eval(&m, roots)
}

// eval is Eval charging the work of the filters to m, so the paths of one
// extractor, its own and those of its embedded steps, share one budget, and
// so do the files of a set, whose meter starts at the work the files before
// it spent.
func (p Path) eval(m *meter, roots []*Node) ([]*Node, error) {
	cur := roots
	for _, sel := range p.sels {
		var next []*Node
		for _, n := range cur {
			var err error
			if next, err = m.apply(sel, n, next); err != nil {
				return nil, err
			}
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

// apply appends to out what sel selects from n. A filter charges its cost
// for each member of n before it tests them. Every kind is named and none
// has a default, so the exhaustive linter refuses a kind added without its
// case here.
func (m *meter) apply(sel selector, n *Node, out []*Node) ([]*Node, error) {
	switch sel.kind {
	case selWild:
		out = append(out, n.Vals...)
		out = append(out, n.Items...)
	case selFilter:
		return m.filter(sel, n, out)
	case selName, selIndex:
		if v := sel.child(n); v != nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// filter appends to out the members of n for which the filter sel holds,
// after charging its cost for each of them.
func (m *meter) filter(sel selector, n *Node, out []*Node) ([]*Node, error) {
	if err := m.charge(sel.cost * int64(len(n.Vals)+len(n.Items))); err != nil {
		return nil, err
	}
	for _, members := range [2][]*Node{n.Vals, n.Items} {
		for _, v := range members {
			ok, err := m.holds(sel, v)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

// ErrTooManyValues is a path that selects more than MaxValues nodes at a
// step.
var ErrTooManyValues = errors.New("too many values")

// The bounds on a filter, whose cost is the members it tests times what the
// comparisons take on each. The path comes from the configuration and the
// members from the file, so each is bounded.
const (
	// MaxComparisons is the most terms one filter joins with &&, and the
	// most comparisons an XPath predicate joins. A selector by a child's
	// value takes one to three; the number is far past any of them.
	MaxComparisons = 64

	// MaxFilterDepth is how deep filters nest, the filter of the path
	// itself counting 1. Parsing a filter and testing a member each recurse
	// once for each level, so the depth bounds the stack. A test of
	// membership, [?@.tags[?@ == 'docker']], takes 2.
	MaxFilterDepth = 4

	// MaxLiteral is the most bytes a filter's literal holds, a string as
	// its escapes decode it and a number as written, and the most bytes a
	// key after @ holds. Comparing a literal and looking a key up each cost
	// up to its length on every member. The longest literals a selector by
	// value needs are an image reference with a sha256 digest, about 150
	// bytes, and a URL, which the sitemaps protocol and most tools keep
	// under 2048 characters; a key is shorter.
	MaxLiteral = 2048

	// MaxFilterBytes is what the filters of one path may cost, in bytes:
	// for each member tested, the cost of each term (see term.cost),
	// counted before the members are tested, and for each step a query
	// takes from a node, StepBytes and the bytes of its key, counted before
	// it takes it. A filter inside a query counts its members the same way,
	// against the same total. Visiting a member hashes no key: a mapping's
	// values are kept in order.
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

// child is the node a key or an index selects from n, or nil. [*] and a
// filter select many nodes, through apply and exists, so they have none.
func (sel selector) child(n *Node) *Node {
	switch sel.kind {
	case selName:
		if v, ok := n.Field[sel.name]; ok && n.Shape == Mapping {
			return v
		}
	case selIndex:
		return itemAt(n, sel.index)
	case selWild, selFilter:
	}
	return nil
}

// itemAt is the item at index i of the sequence n, counting from the end
// for a negative i, or nil.
func itemAt(n *Node, i int) *Node {
	if i < 0 {
		i += len(n.Items)
	}
	if n.Shape == Sequence && 0 <= i && i < len(n.Items) {
		return n.Items[i]
	}
	return nil
}
