package xml

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// The path grammar of the xml extractor: the part of XPath 1.0 below, the
// abbreviated syntax only. Anything else XPath has, such as another axis, a
// function, a union, an operator other than = and and, or a number to
// compare with, is refused by name at its column.
//
//	path      = S ( "/" | "//" ) S step *( S ( "/" | "//" ) S step ) S
//	step      = ( test | "@" S test | "text" S "(" S ")" ) *( S predicate )
//	test      = "*" | QName
//	predicate = "[" S ( position | compare *( S "and" S compare ) ) S "]"
//	position  = 1*DIGIT                         (from 1)
//	compare   = ( test | "@" S test ) S "=" S literal
//	literal   = "'" *( any but "'" ) "'" | '"' *( any but '"' ) '"'
//	S         = *( " " | "\t" | "\r" | "\n" )
//
// A QName is a name as XML Namespaces writes one, prefix:local or local. A
// prefix must be bound under the extractor's namespaces:, but xml, which
// names the XML namespace as it does in every file. A step without a prefix
// selects only elements in no namespace, as in XPath, so it does not match
// an element under a default namespace (xmlns="..."). A step after @ or
// text() is refused, since neither has children, and so is a comparison on
// either.
//
// A path is valid UTF-8 and holds no control character but the four of S.

type xstepKind uint8

const (
	xElement xstepKind = iota
	xAttribute
	xText
)

// xname is a name test: * for any name in any namespace, or a namespace,
// "" for none, and a local name.
type xname struct {
	any          bool
	space, local string
}

// xcompare is one child = 'literal' or @attr = 'literal' of a predicate.
type xcompare struct {
	attr bool
	name xname
	lit  string
}

// xpredicate is a position, counted from 1, or comparisons that must all
// hold.
type xpredicate struct {
	position int // 0 for comparisons
	compares []xcompare
}

type xstep struct {
	descendant bool // written after //
	kind       xstepKind
	name       xname // unused for text()
	preds      []xpredicate
}

// xpath is a parsed path, with the text it was parsed from for messages.
type xpath struct {
	text  string
	steps []xstep
}

// maxXPathTerms is the most steps and predicates one path holds, counted
// together. A step reads at most every node of the file once, and a
// predicate every node its step keeps, so a path costs the file's nodes
// times its terms; comparisons are counted against maxFilterBytes as well.
// The longest paths a POM or a .csproj needs hold under ten.
const maxXPathTerms = 32

// xmlNamespace is the namespace the prefix xml names in every file.
const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

// endsInPredicate refuses a path that ends before its predicate's ].
const endsInPredicate = "the path ends inside a predicate"

// xpathParser reads one path. ns binds the prefixes the path may use.
type xpathParser struct {
	s      string
	i      int
	ns     map[string]string
	terms  int
	inPred bool // inside [ ], where a path or a predicate is refused by name
}

func parseXPath(s string, ns map[string]string) (xpath, error) {
	p := &xpathParser{s: s, ns: ns}
	out := xpath{text: s}
	if err := p.checkText(); err != nil {
		return out, err
	}
	if err := p.start(); err != nil {
		return out, err
	}
	for p.i < len(s) {
		st, err := p.nextStep(out.steps)
		if err != nil {
			return out, err
		}
		out.steps = append(out.steps, st)
		p.space()
	}
	return out, nil
}

// checkText refuses a path that is not valid UTF-8 or holds a control
// character other than a blank.
func (p *xpathParser) checkText() error {
	for i := 0; i < len(p.s); {
		r, size := utf8.DecodeRuneInString(p.s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			return p.fail(i, "the path is not valid UTF-8")
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			return p.fail(i, "control character U+%04X is not allowed in a path", r)
		}
		i += size
	}
	return nil
}

// start reads the blanks before the path and refuses one that is empty or
// does not start at the root.
func (p *xpathParser) start() error {
	p.space()
	switch {
	case p.i == len(p.s):
		return p.fail(p.i, "the path is empty; write one from the root, such as /project/version")
	case p.s[p.i] == '/':
		return nil
	case isXMLNameStart(firstRune(p.s[p.i:])):
		return p.fail(p.i, "a path starts at the root, with / or //; write /%s or //%s", core.Clip(p.s[p.i:]), core.Clip(p.s[p.i:]))
	default:
		return p.unsupported("a path starting with / or //")
	}
}

// nextStep reads / or // and the step after it, which follows steps.
func (p *xpathParser) nextStep(steps []xstep) (xstep, error) {
	var st xstep
	at := p.i
	if !strings.HasPrefix(p.s[p.i:], "/") {
		return st, p.unsupported("/ or // before the next step, or the end of the path")
	}
	if n := len(steps); n > 0 && steps[n-1].kind != xElement {
		return st, p.fail(at, "%s has no children, so its step ends the path", leafName(steps[n-1].kind))
	}
	p.i++
	if strings.HasPrefix(p.s[p.i:], "/") {
		st.descendant = true
		p.i++
	}
	p.space()
	switch {
	case p.i == len(p.s):
		return st, p.fail(p.i, "the path ends after /; name a step, such as an element")
	case p.s[p.i] == '/':
		return st, p.fail(at, "/// is not a separator; write / for a child or // for a descendant")
	}
	err := p.step(&st)
	return st, err
}

// leafName names a step of kind, an attribute or text, in a message.
func leafName(kind xstepKind) string {
	if kind == xAttribute {
		return "an attribute"
	}
	return "text"
}

func (p *xpathParser) fail(at int, format string, args ...any) error {
	return core.PathError(p.s, at, format, args...)
}

func (p *xpathParser) space() {
	for p.i < len(p.s) && isXMLSpace(p.s[p.i]) {
		p.i++
	}
}

// count adds one step or predicate, at byte at, and refuses one past
// maxXPathTerms.
func (p *xpathParser) count(at int) error {
	if p.terms++; p.terms > maxXPathTerms {
		return p.fail(at, "the path holds more than %d steps and predicates, the limit", maxXPathTerms)
	}
	return nil
}

// step reads one step and its predicates.
func (p *xpathParser) step(st *xstep) error {
	if err := p.count(p.i); err != nil {
		return err
	}
	switch {
	case p.s[p.i] == '@':
		p.i++
		p.space()
		st.kind = xAttribute
	case p.isCall("text"):
		if err := p.skipCall(); err != nil {
			return err
		}
		st.kind = xText
	}
	if st.kind != xText {
		name, err := p.test("a step: an element, @attribute or text()")
		if err != nil {
			return err
		}
		st.name = name
	}
	return p.predicates(st)
}

// predicates reads the predicates after a step, each counted as a term.
func (p *xpathParser) predicates(st *xstep) error {
	for p.predicateNext() {
		at := p.i
		if err := p.count(at); err != nil {
			return err
		}
		pred, err := p.predicate()
		if err != nil {
			return err
		}
		if pred.position == 0 && st.kind != xElement {
			return p.fail(at, "a comparison on %s is not supported; compare on the element that holds it, as [@name='value']", leafCall(st.kind))
		}
		st.preds = append(st.preds, pred)
	}
	return nil
}

// predicateNext reports whether a [ comes next after any blanks, and
// stands on it if so.
func (p *xpathParser) predicateNext() bool {
	save := p.i
	p.space()
	if byteAt(p.s, p.i) == '[' {
		return true
	}
	p.i = save
	return false
}

// leafCall names a step of kind, an attribute or text(), in a message.
func leafCall(kind xstepKind) string {
	if kind == xAttribute {
		return "an attribute"
	}
	return "text()"
}

// isCall reports whether the name name and then ( come next.
func (p *xpathParser) isCall(name string) bool {
	if !strings.HasPrefix(p.s[p.i:], name) {
		return false
	}
	j := p.i + len(name)
	if j < len(p.s) && isXMLNameRune(firstRune(p.s[j:])) {
		return false
	}
	for j < len(p.s) && isXMLSpace(p.s[j]) {
		j++
	}
	return j < len(p.s) && p.s[j] == '('
}

// skipCall reads text() after isCall found it, and refuses an argument.
func (p *xpathParser) skipCall() error {
	p.i += strings.IndexByte(p.s[p.i:], '(') + 1
	p.space()
	if byteAt(p.s, p.i) != ')' {
		return p.fail(p.i, "text() takes no argument; write text()")
	}
	p.i++
	return nil
}

// test reads a name test: * or a QName, with its prefix bound.
func (p *xpathParser) test(expected string) (xname, error) {
	if p.i == len(p.s) {
		return xname{}, p.fail(p.i, "expected %s but the path ends", expected)
	}
	if p.s[p.i] == '*' {
		p.i++
		return xname{any: true}, nil
	}
	if !isXMLNameStart(firstRune(p.s[p.i:])) {
		return xname{}, p.unsupported(expected)
	}
	at := p.i
	first := p.ncname()
	// A name followed by ( is a function or a node test, and one followed
	// by :: an axis, with or without blanks between.
	switch {
	case p.followedBy("::"):
		return xname{}, p.fail(at, "the axis %s:: is not supported; the subset has child steps, // and @", core.Clip(first))
	case p.followedBy("("):
		return xname{}, p.callError(at, first)
	case byteAt(p.s, p.i) == ':':
		return p.prefixed(at, first)
	}
	return xname{local: first}, nil
}

// followedBy reports whether tok comes next, after any blanks.
func (p *xpathParser) followedBy(tok string) bool {
	j := p.i
	for j < len(p.s) && isXMLSpace(p.s[j]) {
		j++
	}
	return strings.HasPrefix(p.s[j:], tok)
}

// prefixed reads the local name of a QName at byte at, from the colon
// after its prefix first, and binds the prefix.
func (p *xpathParser) prefixed(at int, first string) (xname, error) {
	p.i++
	switch {
	case byteAt(p.s, p.i) == '*':
		return xname{}, p.fail(at, "the wildcard %s:* is not supported; write * for any element", core.Clip(first))
	case p.i == len(p.s) || !isXMLNameStart(firstRune(p.s[p.i:])):
		return xname{}, p.fail(p.i, "expected a local name after %s:", core.Clip(first))
	}
	local := p.ncname()
	if p.followedBy("(") {
		return xname{}, p.callError(at, p.s[at:p.i])
	}
	space, err := p.bind(at, first)
	if err != nil {
		return xname{}, err
	}
	return xname{space: space, local: local}, nil
}

// bind is the namespace the prefix at byte at names: its binding under
// namespaces:, or the XML namespace for xml.
func (p *xpathParser) bind(at int, prefix string) (string, error) {
	space, ok := p.ns[prefix]
	switch {
	case prefix == "xmlns":
		// checkNamespaces refuses to bind it, so no binding is to suggest.
		return "", p.fail(at, "the prefix xmlns names namespace declarations, which are not nodes a path selects")
	case ok:
		return space, nil
	case prefix == "xml":
		return xmlNamespace, nil
	default:
		return "", p.fail(at, "the prefix %s is not bound; bind it under namespaces:, as {%s: <namespace URI>}", core.Clip(prefix), core.Clip(prefix))
	}
}

// ncname reads a name without a colon, which the caller has seen start.
func (p *xpathParser) ncname() string {
	start := p.i
	for p.i < len(p.s) {
		r, size := utf8.DecodeRuneInString(p.s[p.i:])
		if !isXMLNameRune(r) {
			break
		}
		p.i += size
	}
	return p.s[start:p.i]
}

// callError refuses name(...) at byte at.
func (p *xpathParser) callError(at int, name string) error {
	switch name {
	case "text":
		return p.fail(at, "text() is a step of its own, as /a/text(), and takes no prefix or @")
	case "node", "comment", "processing-instruction":
		return p.fail(at, "the node test %s() is not supported; the subset has text()", name)
	default:
		return p.fail(at, "the function %s() is not supported; the subset has no functions", core.Clip(name))
	}
}

// predicate reads [ ... ], from its [.
func (p *xpathParser) predicate() (xpredicate, error) {
	p.inPred = true
	defer func() { p.inPred = false }()
	p.i++
	p.space()
	var pred xpredicate
	switch c := byteAt(p.s, p.i); {
	case p.i == len(p.s):
		return pred, p.fail(p.i, endsInPredicate)
	case c == ']':
		return pred, p.fail(p.i, "the predicate is empty; write a position, such as [1], or a comparison, such as [name='value']")
	case isDigit(c):
		return p.position()
	case c == '\'' || c == '"':
		return pred, p.fail(p.i, "write the child or attribute before =, and the literal after it")
	case c == '-':
		return pred, p.fail(p.i, "a negative position is not supported; positions count from 1 at the start")
	}
	return p.comparisons()
}

// comparisons reads the comparisons of a predicate, joined by and, and its ].
func (p *xpathParser) comparisons() (xpredicate, error) {
	var pred xpredicate
	for {
		at := p.i
		if len(pred.compares) == core.MaxComparisons {
			return pred, p.fail(at, "a predicate holds more than %d comparisons, the limit", core.MaxComparisons)
		}
		c, err := p.compare()
		if err != nil {
			return pred, err
		}
		pred.compares = append(pred.compares, c)
		p.space()
		switch {
		case byteAt(p.s, p.i) == ']':
			p.i++
			return pred, nil
		case p.isWord("and"):
			p.i += len("and")
			p.space()
		default:
			return pred, p.unsupported("and or ] after a comparison")
		}
	}
}

// isWord reports whether the name w comes next, and not a longer name that
// starts with it.
func (p *xpathParser) isWord(w string) bool {
	if !strings.HasPrefix(p.s[p.i:], w) {
		return false
	}
	j := p.i + len(w)
	return j == len(p.s) || !isXMLNameRune(firstRune(p.s[j:]))
}

func (p *xpathParser) position() (xpredicate, error) {
	at := p.i
	for p.i < len(p.s) && isDigit(p.s[p.i]) {
		p.i++
	}
	digits := p.s[at:p.i]
	if p.i < len(p.s) && p.s[p.i] == '.' {
		return xpredicate{}, p.fail(at, "a position is a whole number")
	}
	p.space()
	switch c := byteAt(p.s, p.i); {
	case p.i == len(p.s):
		return xpredicate{}, p.fail(p.i, endsInPredicate)
	case c == '=':
		return xpredicate{}, p.fail(at, "write the child or attribute before =, and the literal after it")
	case c != ']':
		return xpredicate{}, p.unsupported("] after a position")
	}
	p.i++
	return p.positionOf(at, digits)
}

// positionOf is the position the digits at byte at write, refusing 0 and
// one too large for an int.
func (p *xpathParser) positionOf(at int, digits string) (xpredicate, error) {
	n, err := strconv.Atoi(digits)
	switch {
	case err != nil:
		return xpredicate{}, p.fail(at, "the position %s is too large", core.Clip(digits))
	case n == 0:
		return xpredicate{}, p.fail(at, "positions count from 1, so [0] selects nothing; write [1] for the first")
	}
	return xpredicate{position: n}, nil
}

// compare reads child = 'literal' or @attr = 'literal'.
func (p *xpathParser) compare() (xcompare, error) {
	var c xcompare
	start := p.i
	switch {
	case byteAt(p.s, p.i) == '@':
		c.attr = true
		p.i++
		p.space()
	case p.isCall("text"):
		return c, p.fail(p.i, "text() is not supported in a predicate; compare the element itself, as [name='value']")
	}
	name, err := p.test("a child or @attribute to compare")
	if err != nil {
		return c, err
	}
	c.name = name
	p.space()
	if err := p.equals(start); err != nil {
		return c, err
	}
	p.space()
	c.lit, err = p.literal()
	return c, err
}

// equals reads the = of a comparison whose child or attribute starts at
// byte start, and refuses a comparison that has none.
func (p *xpathParser) equals(start int) error {
	switch {
	case p.i == len(p.s):
		return p.fail(p.i, endsInPredicate)
	case p.s[p.i] == '=':
		p.i++
		return nil
	case p.s[p.i] == ']' || p.isWord("and") || p.isWord("or"):
		return p.fail(p.i, "a predicate that a child or attribute exists is not supported; compare its value, as [%s='value']", core.Clip(strings.TrimRight(p.s[start:p.i], " \t\r\n")))
	default:
		return p.unsupported("= after the child or attribute")
	}
}

// literal reads the quoted literal after a comparison's =.
func (p *xpathParser) literal() (string, error) {
	switch q := byteAt(p.s, p.i); {
	case p.i == len(p.s):
		return "", p.fail(p.i, endsInPredicate)
	case q == '\'' || q == '"':
		return p.quoted(q)
	default:
		return "", p.notLiteral(q)
	}
}

// quoted reads a literal from its quote q, and refuses one that is not
// closed or is over core.MaxLiteral.
func (p *xpathParser) quoted(q byte) (string, error) {
	end := strings.IndexByte(p.s[p.i+1:], q)
	if end < 0 {
		return "", p.fail(p.i, "the literal is not closed; end it with %c", q)
	}
	lit := p.s[p.i+1 : p.i+1+end]
	if len(lit) > core.MaxLiteral {
		return lit, p.fail(p.i, "the literal holds %d bytes, over the limit of %d", len(lit), core.MaxLiteral)
	}
	p.i += end + 2
	return lit, nil
}

// notLiteral refuses what stands after = where a quoted literal belongs,
// which starts with the byte q: a number, a node or anything else.
func (p *xpathParser) notLiteral(q byte) error {
	switch {
	case isDigit(q) || q == '-' || q == '.':
		return p.fail(p.i, "values compare as strings; quote the number, as '%s'", core.Clip(p.s[p.i:numberEnd(p.s, p.i+1)]))
	case q == '@' || q == '*' || isXMLNameStart(firstRune(p.s[p.i:])):
		return p.fail(p.i, "a comparison takes a quoted literal after =; comparing two nodes is not supported")
	default:
		return p.unsupported("a quoted literal after =")
	}
}

// numberEnd is the end of the digits and dots of s from byte i.
func numberEnd(s string, i int) int {
	for i < len(s) && (isDigit(s[i]) || s[i] == '.') {
		i++
	}
	return i
}

// unsupported refuses what comes next, where the path needed expected,
// naming the construct when it is one XPath has and the subset does not.
func (p *xpathParser) unsupported(expected string) error {
	rest := p.s[p.i:]
	if rest == "" {
		return p.fail(p.i, "expected %s but the path ends", expected)
	}
	for _, r := range refusals {
		if p.refuses(r) {
			return p.fail(p.i, "%s", r.msg)
		}
	}
	return p.fail(p.i, "expected %s but found %q", expected, firstRune(rest))
}

// refusal is a construct XPath has and the subset does not, which starts
// with any of starts, and the message that names it.
type refusal struct {
	starts []string
	word   bool // each of starts is a name, and a longer name refuses nothing
	inPred bool // refused by name only inside [ ]
	msg    string
}

// refusals are the constructs unsupported names, tried in turn, a longer
// start before a shorter one it begins with.
var refusals = []refusal{
	{starts: []string{"!="}, msg: "the operator != is not supported; a predicate compares with ="},
	{starts: []string{"<="}, msg: "the operator <= is not supported; a predicate compares with ="},
	{starts: []string{">="}, msg: "the operator >= is not supported; a predicate compares with ="},
	{starts: []string{"<"}, msg: "the operator < is not supported; a predicate compares with ="},
	{starts: []string{">"}, msg: "the operator > is not supported; a predicate compares with ="},
	{starts: []string{".."}, msg: "the parent step (..) is not supported"},
	{starts: []string{"."}, msg: "the self step (.) is not supported"},
	{starts: []string{"|"}, msg: "a union (|) is not supported; write one path for each extractor"},
	{starts: []string{"$"}, msg: "a variable ($) is not supported"},
	{starts: []string{"(", ")"}, msg: "a parenthesised expression is not supported"},
	{starts: []string{"+", "-", "*"}, msg: "arithmetic is not supported"},
	{starts: []string{"div", "mod"}, word: true, msg: "arithmetic is not supported"},
	{starts: []string{"or"}, word: true, msg: "or is not supported; a predicate joins comparisons with and"},
	{starts: []string{"/"}, inPred: true, msg: "a path inside a predicate is not supported; a comparison takes one child or attribute"},
	{starts: []string{"["}, inPred: true, msg: "a predicate on a comparison's child is not supported"},
	{starts: []string{"'", `"`}, msg: "a literal is not supported here; a comparison takes it after ="},
}

// refuses reports whether r's construct comes next.
func (p *xpathParser) refuses(r refusal) bool {
	if r.inPred && !p.inPred {
		return false
	}
	for _, start := range r.starts {
		if p.startsWith(start, r.word) {
			return true
		}
	}
	return false
}

// startsWith reports whether s comes next, as a whole name when word is
// set.
func (p *xpathParser) startsWith(s string, word bool) bool {
	if word {
		return p.isWord(s)
	}
	return strings.HasPrefix(p.s[p.i:], s)
}

// byteAt is s[i], or 0 past its end.
func byteAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// isXMLSpace reports whether b is white space as XML and XPath write it.
func isXMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// xmlNameStarts is NameStartChar of XML 1.0 fifth edition and XML
// Namespaces, without the colon, as ranges in ascending order.
var xmlNameStarts = [][2]rune{
	{'A', 'Z'}, {'_', '_'}, {'a', 'z'},
	{0xC0, 0xD6}, {0xD8, 0xF6}, {0xF8, 0x2FF}, {0x370, 0x37D}, {0x37F, 0x1FFF},
	{0x200C, 0x200D}, {0x2070, 0x218F}, {0x2C00, 0x2FEF}, {0x3001, 0xD7FF},
	{0xF900, 0xFDCF}, {0xFDF0, 0xFFFD}, {0x10000, 0xEFFFF},
}

// xmlNameRunes is what NameChar adds to NameStartChar, as ranges in
// ascending order.
var xmlNameRunes = [][2]rune{
	{'-', '.'}, {'0', '9'}, {0xB7, 0xB7}, {0x300, 0x36F}, {0x203F, 0x2040},
}

// inRanges reports whether r lies in one of ranges, which ascend.
func inRanges(r rune, ranges [][2]rune) bool {
	for _, rg := range ranges {
		if r < rg[0] {
			return false
		}
		if r <= rg[1] {
			return true
		}
	}
	return false
}

// isXMLNameStart reports whether r may start a name without a colon
// (NCName), as XML 1.0 fifth edition and XML Namespaces define it.
func isXMLNameStart(r rune) bool {
	return inRanges(r, xmlNameStarts)
}

// isXMLNameRune reports whether r may follow the start of a name without a
// colon.
func isXMLNameRune(r rune) bool {
	return isXMLNameStart(r) || inRanges(r, xmlNameRunes)
}

// isNCName reports whether s is a name without a colon.
func isNCName(s string) bool {
	for i, r := range s {
		if (i == 0 && !isXMLNameStart(r)) || !isXMLNameRune(r) {
			return false
		}
	}
	return s != ""
}

// checkNamespaces refuses a binding that no path could use as written: a
// prefix that is not a name, the reserved prefix xmlns, xml bound to another
// namespace than its own, and an empty namespace, which would make a prefix
// match elements in no namespace.
func checkNamespaces(ns map[string]string) error {
	for _, prefix := range slices.Sorted(maps.Keys(ns)) {
		uri := ns[prefix]
		switch {
		case !isNCName(prefix):
			return fmt.Errorf("namespaces: the prefix %s is not a name without a colon; a path needs one, such as m, to write m:name", core.Quote(prefix))
		case prefix == "xmlns":
			return errors.New("namespaces: the prefix xmlns is reserved and cannot be bound")
		case prefix == "xml" && uri != xmlNamespace:
			return fmt.Errorf("namespaces: the prefix xml names %s and no other namespace", xmlNamespace)
		case uri == "":
			return fmt.Errorf("namespaces: the prefix %s is bound to the empty string; an element in no namespace takes a step without a prefix", core.Quote(prefix))
		}
	}
	return nil
}

// rname is a name test with the file's ids for its namespace and name, or
// -1 for one the file does not have, which matches nothing.
type rname struct {
	any         bool
	space, name int32
}

func (n xname) resolve(d *xmlDoc) rname {
	if n.any {
		return rname{any: true}
	}
	r := rname{space: -1, name: -1}
	if id, ok := d.spaces[n.space]; ok {
		r.space = id
	}
	if id, ok := d.names[n.local]; ok {
		r.name = id
	}
	return r
}

func (n rname) matches(space, name int32) bool {
	return n.any || (n.space == space && n.name == name)
}

type rcompare struct {
	attr bool
	name rname
	lit  string
}

// rstep is a step with its names resolved in one file. compares holds the
// comparisons of each predicate, none for a position.
type rstep struct {
	xstep
	rname    rname
	compares [][]rcompare
}

func (st xstep) resolve(d *xmlDoc) rstep {
	r := rstep{xstep: st, rname: st.name.resolve(d), compares: make([][]rcompare, len(st.preds))}
	for i, pred := range st.preds {
		for _, c := range pred.compares {
			r.compares[i] = append(r.compares[i], rcompare{attr: c.attr, name: c.name.resolve(d), lit: c.lit})
		}
	}
	return r
}

// eval returns the kind of node the path selects in d and their ids, in
// document order. Node -1 is the root, which holds the root element.
//
// Each step visits each node it may select from once: a child step the
// children of every node before it, a // step every node inside them, where
// a node inside another is visited once for both. So a step costs at most
// the file's nodes, and a path its terms times them (maxXPathTerms). The
// comparisons are counted against maxFilterBytes: stepBytes for each child
// or attribute a comparison looks at, and the literal's length for each
// whose value it compares. It stops with errTooManyValues once a step
// selects more than maxValues nodes.
func (p xpath) eval(d *xmlDoc) (xstepKind, []int32, error) {
	ctx := []int32{-1}
	kind := xElement
	var work int64
	for _, st := range p.steps {
		r := st.resolve(d)
		next, err := d.step(&r, ctx, &work)
		if err != nil {
			return kind, nil, err
		}
		ctx, kind = next, st.kind
	}
	return kind, ctx, nil
}

// step selects from each context the nodes its test and predicates keep. A
// child step visits each context's children, attributes or text nodes. A //
// step to elements, or to attributes without a position, is descend's; a //
// step to text(), or to attributes with a position, visits the context and
// every element inside it, each once, which a path does at most once, since
// either step ends it.
func (d *xmlDoc) step(st *rstep, ctx []int32, work *int64) ([]int32, error) {
	if st.descends() {
		return d.descend(st, ctx, work)
	}
	var out, cand []int32
	visit := func(x int32) error {
		var err error
		if cand, err = d.filter(st, d.candidates(st, x, cand[:0]), work); err != nil {
			return err
		}
		if out = append(out, cand...); len(out) > d.valuesAllowed() {
			return core.ErrTooManyValues
		}
		return nil
	}
	walk := eachOf
	if st.descendant {
		walk = d.eachInside
	}
	if err := walk(ctx, visit); err != nil {
		return nil, err
	}
	// A node's children come after those of a node before it that it is
	// inside, so a child step over nested nodes selects out of order.
	if !slices.IsSorted(out) {
		slices.Sort(out)
	}
	return out, nil
}

// descends reports whether the step is descend's: a // step to elements,
// or to attributes without a position.
func (st *rstep) descends() bool {
	return st.descendant && (st.kind == xElement || (st.kind == xAttribute && len(st.preds) == 0))
}

// eachOf visits each context node, for a child step.
func eachOf(ctx []int32, visit func(int32) error) error {
	for _, c := range ctx {
		if err := visit(c); err != nil {
			return err
		}
	}
	return nil
}

// eachInside visits each context node and every element inside it, each
// once, for a // step.
func (d *xmlDoc) eachInside(ctx []int32, visit func(int32) error) error {
	covered := int32(-2) // the last node a // step has visited the children of
	for _, c := range ctx {
		if c <= covered {
			continue // inside a node visited before
		}
		last := d.lastInside(c)
		for x := c; x <= last; x++ {
			if err := visit(x); err != nil {
				return err
			}
		}
		covered = last
	}
	return nil
}

// lastInside is the last element inside node c, or of the file for the
// root.
func (d *xmlDoc) lastInside(c int32) int32 {
	if c >= 0 {
		return d.elems[c].last
	}
	return i32(len(d.elems) - 1)
}

// descend is a // step to elements or attributes. Its nodes lie in one
// range of elems, or of attrs, for each context: the elements inside the
// context, or the attributes of the context and of every element inside it.
// It reads each range in order, so a step costs a read of the file's nodes
// at most, and selects in document order.
//
// A position counts among the children of one parent, so for an element
// step it keeps a stack of the open elements of the range, each with a
// counter for each predicate (stage), which is XPath's position among the
// children the predicates before it kept.
func (d *xmlDoc) descend(st *rstep, ctx []int32, work *int64) ([]int32, error) {
	var out []int32
	var r descent
	covered := int32(-2) // the last element a range so far has held
	for _, c := range ctx {
		if c <= covered {
			continue // inside a range read before
		}
		hi := d.lastInside(c)
		covered = hi
		var err error
		if st.kind == xAttribute {
			out, err = d.descendAttrs(st, max(c, 0), hi, out)
		} else {
			out, err = d.descendElems(st, c, hi, out, work, &r)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// descent is the stack of open elements descendElems keeps, kept from one
// range to the next so their storage is reused.
type descent struct {
	stack  []openRange
	counts []int32 // the counters of each open element, len(st.preds) for each
}

// descendElems appends the elements after c up to hi that the step keeps.
func (d *xmlDoc) descendElems(st *rstep, c, hi int32, out []int32, work *int64, r *descent) ([]int32, error) {
	n := len(st.preds)
	r.stack = append(r.stack[:0], openRange{last: hi})
	r.counts = append(r.counts[:0], make([]int32, n)...)
	for e := c + 1; e <= hi; e++ {
		r.closeBefore(e)
		parent := r.stack[len(r.stack)-1].counts
		ok, err := d.keeps(st, e, r.counts[parent:parent+n], work)
		if err != nil {
			return nil, err
		}
		if ok {
			if out = append(out, e); len(out) > d.valuesAllowed() {
				return nil, core.ErrTooManyValues
			}
		}
		if d.elems[e].kid >= 0 {
			r.stack = append(r.stack, openRange{last: d.elems[e].last, counts: len(r.counts)})
			r.counts = append(r.counts, make([]int32, n)...)
		}
	}
	return out, nil
}

// closeBefore pops the open elements that end before element e.
func (r *descent) closeBefore(e int32) {
	for r.stack[len(r.stack)-1].last < e {
		r.counts = r.counts[:r.stack[len(r.stack)-1].counts]
		r.stack = r.stack[:len(r.stack)-1]
	}
}

// keeps reports whether element e passes the step's test and predicates.
func (d *xmlDoc) keeps(st *rstep, e int32, counts []int32, work *int64) (bool, error) {
	if !st.rname.matches(d.elems[e].space, d.elems[e].name) {
		return false, nil
	}
	return d.stage(st, e, counts, work)
}

// openRange is an element of a range descend reads whose children are still
// to come: the last element inside it, and where its counters start.
type openRange struct {
	last   int32
	counts int
}

// stage runs element e through the step's predicates in turn, with counts
// holding, for each, how many children of e's parent it kept so far, and
// reports whether all keep it.
func (d *xmlDoc) stage(st *rstep, e int32, counts []int32, work *int64) (bool, error) {
	for k, pred := range st.preds {
		if pred.position > 0 {
			if counts[k]++; int(counts[k]) != pred.position {
				return false, nil
			}
			continue
		}
		if ok, err := d.holds(st.compares[k], e, work); !ok || err != nil {
			return false, err
		}
	}
	return true, nil
}

// descendAttrs appends the attributes of elements lo to hi that the step's
// test selects. The step has no predicate: an attribute step takes only a
// position, and one with a position is step's to read.
func (d *xmlDoc) descendAttrs(st *rstep, lo, hi int32, out []int32) ([]int32, error) {
	if lo > hi {
		return out, nil
	}
	_, a1 := d.attrsOf(hi)
	for a := d.elems[lo].attr0; a < a1; a++ {
		if st.rname.matches(d.attrs[a].space, d.attrs[a].name) {
			if out = append(out, a); len(out) > d.valuesAllowed() {
				return nil, core.ErrTooManyValues
			}
		}
	}
	return out, nil
}

// candidates appends to out the nodes of x the step's test selects: its
// child elements, its attributes or its text nodes.
func (d *xmlDoc) candidates(st *rstep, x int32, out []int32) []int32 {
	switch {
	case x < 0 && st.kind != xElement:
		return out // the root has no attribute, and no text the subset keeps
	case st.kind == xAttribute:
		return d.attrCandidates(st, x, out)
	case st.kind == xText:
		return d.textCandidates(x, out)
	}
	return d.childCandidates(st, x, out)
}

// attrCandidates appends the attributes of element x the step's test
// selects.
func (d *xmlDoc) attrCandidates(st *rstep, x int32, out []int32) []int32 {
	a0, a1 := d.attrsOf(x)
	for a := a0; a < a1; a++ {
		if st.rname.matches(d.attrs[a].space, d.attrs[a].name) {
			out = append(out, a)
		}
	}
	return out
}

// textCandidates appends the text nodes of element x.
func (d *xmlDoc) textCandidates(x int32, out []int32) []int32 {
	for t := d.elems[x].text; t >= 0; t = d.texts[t].next {
		out = append(out, t)
	}
	return out
}

// childCandidates appends the child elements of x the step's test selects.
func (d *xmlDoc) childCandidates(st *rstep, x int32, out []int32) []int32 {
	kid := int32(0) // the root element, the root's one child
	if x >= 0 {
		kid = d.elems[x].kid
	}
	for ; kid >= 0; kid = d.elems[kid].next {
		if st.rname.matches(d.elems[kid].space, d.elems[kid].name) {
			out = append(out, kid)
		}
	}
	return out
}

// filter applies the step's predicates in turn, each to what the one
// before kept, so a position counts among the nodes the predicates before it
// kept, as in XPath.
func (d *xmlDoc) filter(st *rstep, cand []int32, work *int64) ([]int32, error) {
	for i, pred := range st.preds {
		if pred.position > 0 {
			if pred.position > len(cand) {
				return cand[:0], nil
			}
			cand = cand[pred.position-1 : pred.position]
			continue
		}
		keep := cand[:0]
		for _, e := range cand {
			ok, err := d.holds(st.compares[i], e, work)
			if err != nil {
				return nil, err
			}
			if ok {
				keep = append(keep, e)
			}
		}
		cand = keep
	}
	return cand, nil
}

// holds reports whether every comparison holds on element e: some child or
// attribute it names has the literal as its value.
func (d *xmlDoc) holds(compares []rcompare, e int32, work *int64) (bool, error) {
	for _, c := range compares {
		found, err := d.holdsOne(c, e, work)
		if !found || err != nil {
			return false, err
		}
	}
	return true, nil
}

// holdsOne reports whether one comparison holds on element e.
func (d *xmlDoc) holdsOne(c rcompare, e int32, work *int64) (bool, error) {
	if c.attr {
		return d.attrHolds(c, e, work)
	}
	return d.childHolds(c, e, work)
}

// attrHolds reports whether an attribute of element e that c names has
// c's literal as its value.
func (d *xmlDoc) attrHolds(c rcompare, e int32, work *int64) (bool, error) {
	a0, a1 := d.attrsOf(e)
	for a := a0; a < a1; a++ {
		at := d.attrs[a]
		match := c.name.matches(at.space, at.name)
		if err := d.charge(work, match && len(at.value) == len(c.lit), len(c.lit)); err != nil {
			return false, err
		}
		if match && at.value == c.lit {
			return true, nil
		}
	}
	return false, nil
}

// childHolds reports whether a child of element e that c names has c's
// literal as its trimmed text.
func (d *xmlDoc) childHolds(c rcompare, e int32, work *int64) (bool, error) {
	for k := d.elems[e].kid; k >= 0; k = d.elems[k].next {
		kid := d.elems[k]
		text := d.buf[kid.trim0:kid.trim1]
		match := c.name.matches(kid.space, kid.name)
		if err := d.charge(work, match && len(text) == len(c.lit), len(c.lit)); err != nil {
			return false, err
		}
		if match && string(text) == c.lit {
			return true, nil
		}
	}
	return false, nil
}

// charge counts one node a comparison looks at, and the bytes of its
// literal when it compares them, and refuses the work past maxFilterBytes.
func (d *xmlDoc) charge(work *int64, compared bool, lit int) error {
	*work += core.StepBytes
	if compared {
		*work += int64(lit)
	}
	if *work > d.workAllowed() {
		return core.ErrFilterWork
	}
	return nil
}
