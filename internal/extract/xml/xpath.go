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
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			return out, p.fail(i, "the path is not valid UTF-8")
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			return out, p.fail(i, "control character U+%04X is not allowed in a path", r)
		}
		i += size
	}
	p.space()
	if p.i == len(s) {
		return out, p.fail(p.i, "the path is empty; write one from the root, such as /project/version")
	}
	if s[p.i] != '/' {
		if isXMLNameStart(firstRune(s[p.i:])) {
			return out, p.fail(p.i, "a path starts at the root, with / or //; write /%s or //%s", core.Clip(s[p.i:]), core.Clip(s[p.i:]))
		}
		return out, p.unsupported("a path starting with / or //")
	}
	for p.i < len(s) {
		at := p.i
		if !strings.HasPrefix(s[p.i:], "/") {
			return out, p.unsupported("/ or // before the next step, or the end of the path")
		}
		if n := len(out.steps); n > 0 && out.steps[n-1].kind != xElement {
			what := "text"
			if out.steps[n-1].kind == xAttribute {
				what = "an attribute"
			}
			return out, p.fail(at, "%s has no children, so its step ends the path", what)
		}
		var st xstep
		p.i++
		if strings.HasPrefix(s[p.i:], "/") {
			st.descendant = true
			p.i++
		}
		p.space()
		switch {
		case p.i == len(s):
			return out, p.fail(p.i, "the path ends after /; name a step, such as an element")
		case s[p.i] == '/':
			return out, p.fail(at, "/// is not a separator; write / for a child or // for a descendant")
		}
		if err := p.step(&st); err != nil {
			return out, err
		}
		out.steps = append(out.steps, st)
		p.space()
	}
	return out, nil
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
	for {
		save := p.i
		p.space()
		if p.i == len(p.s) || p.s[p.i] != '[' {
			p.i = save
			return nil
		}
		at := p.i
		if err := p.count(at); err != nil {
			return err
		}
		pred, err := p.predicate()
		if err != nil {
			return err
		}
		if pred.position == 0 && st.kind != xElement {
			what := "text()"
			if st.kind == xAttribute {
				what = "an attribute"
			}
			return p.fail(at, "a comparison on %s is not supported; compare on the element that holds it, as [@name='value']", what)
		}
		st.preds = append(st.preds, pred)
	}
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
	j := p.i
	for j < len(p.s) && isXMLSpace(p.s[j]) {
		j++
	}
	switch {
	case strings.HasPrefix(p.s[j:], "::"):
		return xname{}, p.fail(at, "the axis %s:: is not supported; the subset has child steps, // and @", core.Clip(first))
	case strings.HasPrefix(p.s[j:], "("):
		return xname{}, p.callError(at, first)
	case p.i < len(p.s) && p.s[p.i] == ':':
	default:
		return xname{local: first}, nil
	}
	p.i++
	switch {
	case p.i < len(p.s) && p.s[p.i] == '*':
		return xname{}, p.fail(at, "the wildcard %s:* is not supported; write * for any element", core.Clip(first))
	case p.i == len(p.s) || !isXMLNameStart(firstRune(p.s[p.i:])):
		return xname{}, p.fail(p.i, "expected a local name after %s:", core.Clip(first))
	}
	local := p.ncname()
	j = p.i
	for j < len(p.s) && isXMLSpace(p.s[j]) {
		j++
	}
	if strings.HasPrefix(p.s[j:], "(") {
		return xname{}, p.callError(at, p.s[at:p.i])
	}
	space, ok := p.ns[first]
	switch {
	case first == "xmlns":
		// checkNamespaces refuses to bind it, so no binding is to suggest.
		return xname{}, p.fail(at, "the prefix xmlns names namespace declarations, which are not nodes a path selects")
	case ok:
	case first == "xml":
		space = xmlNamespace
	default:
		return xname{}, p.fail(at, "the prefix %s is not bound; bind it under namespaces:, as {%s: <namespace URI>}", core.Clip(first), core.Clip(first))
	}
	return xname{space: space, local: local}, nil
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
		return pred, p.fail(p.i, "the path ends inside a predicate")
	case c == ']':
		return pred, p.fail(p.i, "the predicate is empty; write a position, such as [1], or a comparison, such as [name='value']")
	case isDigit(c):
		return p.position()
	case c == '\'' || c == '"':
		return pred, p.fail(p.i, "write the child or attribute before =, and the literal after it")
	case c == '-':
		return pred, p.fail(p.i, "a negative position is not supported; positions count from 1 at the start")
	}
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
		case p.i < len(p.s) && p.s[p.i] == ']':
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
		return xpredicate{}, p.fail(p.i, "the path ends inside a predicate")
	case c == '=':
		return xpredicate{}, p.fail(at, "write the child or attribute before =, and the literal after it")
	case c != ']':
		return xpredicate{}, p.unsupported("] after a position")
	}
	p.i++
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
	case p.i < len(p.s) && p.s[p.i] == '@':
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
	switch {
	case p.i == len(p.s):
		return c, p.fail(p.i, "the path ends inside a predicate")
	case p.s[p.i] == '=':
		p.i++
	case p.s[p.i] == ']' || p.isWord("and") || p.isWord("or"):
		return c, p.fail(p.i, "a predicate that a child or attribute exists is not supported; compare its value, as [%s='value']", core.Clip(strings.TrimRight(p.s[start:p.i], " \t\r\n")))
	default:
		return c, p.unsupported("= after the child or attribute")
	}
	p.space()
	switch q := byteAt(p.s, p.i); {
	case p.i == len(p.s):
		return c, p.fail(p.i, "the path ends inside a predicate")
	case q == '\'' || q == '"':
		end := strings.IndexByte(p.s[p.i+1:], q)
		if end < 0 {
			return c, p.fail(p.i, "the literal is not closed; end it with %c", q)
		}
		c.lit = p.s[p.i+1 : p.i+1+end]
		if len(c.lit) > core.MaxLiteral {
			return c, p.fail(p.i, "the literal holds %d bytes, over the limit of %d", len(c.lit), core.MaxLiteral)
		}
		p.i += end + 2
	case isDigit(q) || q == '-' || q == '.':
		end := p.i + 1
		for end < len(p.s) && (isDigit(p.s[end]) || p.s[end] == '.') {
			end++
		}
		return c, p.fail(p.i, "values compare as strings; quote the number, as '%s'", core.Clip(p.s[p.i:end]))
	case q == '@' || q == '*' || isXMLNameStart(firstRune(p.s[p.i:])):
		return c, p.fail(p.i, "a comparison takes a quoted literal after =; comparing two nodes is not supported")
	default:
		return c, p.unsupported("a quoted literal after =")
	}
	return c, nil
}

// unsupported refuses what comes next, where the path needed expected,
// naming the construct when it is one XPath has and the subset does not.
func (p *xpathParser) unsupported(expected string) error {
	rest := p.s[p.i:]
	if rest == "" {
		return p.fail(p.i, "expected %s but the path ends", expected)
	}
	for _, op := range []string{"!=", "<=", ">=", "<", ">"} {
		if strings.HasPrefix(rest, op) {
			return p.fail(p.i, "the operator %s is not supported; a predicate compares with =", op)
		}
	}
	switch {
	case strings.HasPrefix(rest, ".."):
		return p.fail(p.i, "the parent step (..) is not supported")
	case rest[0] == '.':
		return p.fail(p.i, "the self step (.) is not supported")
	case rest[0] == '|':
		return p.fail(p.i, "a union (|) is not supported; write one path for each extractor")
	case rest[0] == '$':
		return p.fail(p.i, "a variable ($) is not supported")
	case rest[0] == '(' || rest[0] == ')':
		return p.fail(p.i, "a parenthesised expression is not supported")
	case rest[0] == '+' || rest[0] == '-' || rest[0] == '*' || p.isWord("div") || p.isWord("mod"):
		return p.fail(p.i, "arithmetic is not supported")
	case p.isWord("or"):
		return p.fail(p.i, "or is not supported; a predicate joins comparisons with and")
	case rest[0] == '/' && p.inPred:
		return p.fail(p.i, "a path inside a predicate is not supported; a comparison takes one child or attribute")
	case rest[0] == '[' && p.inPred:
		return p.fail(p.i, "a predicate on a comparison's child is not supported")
	case rest[0] == '\'' || rest[0] == '"':
		return p.fail(p.i, "a literal is not supported here; a comparison takes it after =")
	default:
		return p.fail(p.i, "expected %s but found %q", expected, firstRune(rest))
	}
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

// isXMLNameStart reports whether r may start a name without a colon
// (NCName), as XML 1.0 fifth edition and XML Namespaces define it.
func isXMLNameStart(r rune) bool {
	switch {
	case r == '_' || ('A' <= r && r <= 'Z') || ('a' <= r && r <= 'z'):
		return true
	case r < 0xC0:
		return false
	}
	return r <= 0xD6 || (0xD8 <= r && r <= 0xF6) || (0xF8 <= r && r <= 0x2FF) ||
		(0x370 <= r && r <= 0x37D) || (0x37F <= r && r <= 0x1FFF) || (0x200C <= r && r <= 0x200D) ||
		(0x2070 <= r && r <= 0x218F) || (0x2C00 <= r && r <= 0x2FEF) || (0x3001 <= r && r <= 0xD7FF) ||
		(0xF900 <= r && r <= 0xFDCF) || (0xFDF0 <= r && r <= 0xFFFD) || (0x10000 <= r && r <= 0xEFFFF)
}

// isXMLNameRune reports whether r may follow the start of a name without a
// colon.
func isXMLNameRune(r rune) bool {
	return isXMLNameStart(r) || r == '-' || r == '.' || ('0' <= r && r <= '9') || r == 0xB7 ||
		(0x300 <= r && r <= 0x36F) || (0x203F <= r && r <= 0x2040)
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
	if st.descendant && (st.kind == xElement || (st.kind == xAttribute && len(st.preds) == 0)) {
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
	covered := int32(-2) // the last node a // step has visited the children of
	for _, c := range ctx {
		if !st.descendant {
			if err := visit(c); err != nil {
				return nil, err
			}
			continue
		}
		if c <= covered {
			continue // inside a node visited before
		}
		last := i32(len(d.elems) - 1)
		if c >= 0 {
			last = d.elems[c].last
		}
		for x := c; x <= last; x++ {
			if err := visit(x); err != nil {
				return nil, err
			}
		}
		covered = last
	}
	// A node's children come after those of a node before it that it is
	// inside, so a child step over nested nodes selects out of order.
	if !slices.IsSorted(out) {
		slices.Sort(out)
	}
	return out, nil
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
	var stack []openRange
	var counts []int32 // the counters of each open element, len(st.preds) for each
	n := len(st.preds)
	covered := int32(-2) // the last element a range so far has held
	for _, c := range ctx {
		if c <= covered {
			continue // inside a range read before
		}
		hi := i32(len(d.elems) - 1)
		if c >= 0 {
			hi = d.elems[c].last
		}
		covered = hi
		if st.kind == xAttribute {
			var err error
			if out, err = d.descendAttrs(st, max(c, 0), hi, out); err != nil {
				return nil, err
			}
			continue
		}
		stack = append(stack[:0], openRange{last: hi})
		counts = append(counts[:0], make([]int32, n)...)
		for e := c + 1; e <= hi; e++ {
			for stack[len(stack)-1].last < e {
				counts = counts[:stack[len(stack)-1].counts]
				stack = stack[:len(stack)-1]
			}
			parent := stack[len(stack)-1].counts
			if st.rname.matches(d.elems[e].space, d.elems[e].name) {
				ok, err := d.stage(st, e, counts[parent:parent+n], work)
				if err != nil {
					return nil, err
				}
				if ok {
					if out = append(out, e); len(out) > d.valuesAllowed() {
						return nil, core.ErrTooManyValues
					}
				}
			}
			if d.elems[e].kid >= 0 {
				stack = append(stack, openRange{last: d.elems[e].last, counts: len(counts)})
				counts = append(counts, make([]int32, n)...)
			}
		}
	}
	return out, nil
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
		a0, a1 := d.attrsOf(x)
		for a := a0; a < a1; a++ {
			if st.rname.matches(d.attrs[a].space, d.attrs[a].name) {
				out = append(out, a)
			}
		}
		return out
	case st.kind == xText:
		for t := d.elems[x].text; t >= 0; t = d.texts[t].next {
			out = append(out, t)
		}
		return out
	}
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
	el := d.elems[e]
	a0, a1 := d.attrsOf(e)
	for _, c := range compares {
		found := false
		if c.attr {
			for a := a0; a < a1 && !found; a++ {
				at := d.attrs[a]
				match := c.name.matches(at.space, at.name)
				if err := d.charge(work, match && len(at.value) == len(c.lit), len(c.lit)); err != nil {
					return false, err
				}
				found = match && at.value == c.lit
			}
		} else {
			for k := el.kid; k >= 0 && !found; k = d.elems[k].next {
				kid := d.elems[k]
				text := d.buf[kid.trim0:kid.trim1]
				match := c.name.matches(kid.space, kid.name)
				if err := d.charge(work, match && len(text) == len(c.lit), len(c.lit)); err != nil {
					return false, err
				}
				found = match && string(text) == c.lit
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
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
