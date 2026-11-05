package htmlparse

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// Foreign content is what a browser reads inside an svg or a math element:
// elements of their namespace, whose start tags the tokenizer never reads
// as raw text, CDATA read as text, and the HTML integration points where
// HTML resumes. The nest holds a foreign element under its namespace and
// name, "svg title", so that no HTML rule takes it for the HTML element of
// its name; the integration points end a scope and are special, as in the
// standard. The scanner follows golang.org/x/net/html's tree builder
// (inForeignContent, parseForeignContent in parse.go, v0.62.0).

// foreignKey is the nest's name for an element of namespace ns, "svg" or
// "math", and name: the two with a space between. An HTML element's name
// holds no space.
func foreignKey(ns, name string) string { return ns + " " + name }

// htmlAnnotation is the nest's name for a math annotation-xml whose
// encoding is HTML, an HTML integration point; one with any other encoding
// is foreignKey("math", "annotation-xml").
const htmlAnnotation = "math annotation-xml html"

// namespace is the namespace of an element the nest holds as key: "" for
// HTML, else "svg" or "math".
func namespace(key string) string {
	ns, _, _ := strings.Cut(key, " ")
	if ns == key {
		return ""
	}
	return ns
}

// htmlPoints are the HTML integration points, where a start tag or text is
// HTML's; textPoints the MathML text integration points, where a start tag
// but mglyph and malignmark, or text, is.
var (
	htmlPoints = map[string]bool{"svg foreignobject": true, "svg desc": true, "svg title": true, htmlAnnotation: true}
	textPoints = map[string]bool{"math mi": true, "math mo": true, "math mn": true, "math ms": true, "math mtext": true}
)

// breakout are the HTML start tags that end the foreign elements open, to
// the nearest HTML element or integration point, and then open as HTML's;
// so does a font with a color, face or size.
var breakout = set(`b big blockquote body br center code dd div dl dt em embed h1 h2 h3 h4 h5 h6
	head hr i img li listing menu meta nobr ol p pre ruby s small span strong strike sub sup table
	tt u ul var`)

func init() {
	// The integration points end a scope, and are special: an end tag of
	// no special element closes nothing past one.
	for _, m := range []map[string]bool{htmlPoints, textPoints, {foreignKey("math", "annotation-xml"): true}} {
		for k := range m {
			scopeEnds[k], special[k] = true, true
		}
	}
}

// foreignToken reports whether the tree builder reads the token as foreign
// content, for a tag (start reports which kind) or for text: a browser
// does when the element on top is foreign, but at an integration point,
// where a start tag or text is HTML's, and at an annotation-xml, where an
// svg is.
func (s *Scanner) foreignToken(n *nest, tag, start bool) bool {
	top := n.top()
	switch {
	case namespace(top) == "":
		return false
	case tag && !start:
		return true
	case textPoints[top] && (!tag || s.name != "mglyph" && s.name != "malignmark"):
		return false
	case top == foreignKey("math", "annotation-xml") && tag && s.name == "svg": // the HTML one is an integration point
		return false
	case htmlPoints[top]:
		return false
	}
	return true
}

// foreignTag follows a tag the tree builder reads as foreign content in
// nest n, and reports whether Next gives the tag, and whether it is read
// as HTML's instead: a breakout start tag, once it has ended the foreign
// elements open, and an end tag that closes no foreign element above the
// nearest HTML one.
func (s *Scanner) foreignTag(n *nest, selfClosing bool) (kept, html bool) {
	s.kind = StartTag
	if s.closing {
		s.kind = EndTag
		// x/net walks down from the top to the first HTML element for an
		// element of the name; the last of the name above the last HTML
		// element is the same one, found at a constant cost.
		i := max(n.last(n.posOf(foreignKey("svg", s.name))), n.last(n.posOf(foreignKey("math", s.name))))
		if s.name == "annotation-xml" {
			i = max(i, n.last(n.posOf(htmlAnnotation)))
		}
		if i > n.last(n.htmls) {
			n.closeForeign(i)
			s.foreign = true
			return true, false
		}
		// HTML's </br> is a <br>, and </p> with no p in scope opens one
		// first: each a start tag the tree builder reads in foreign
		// content again, where it breaks out. With a p in scope the </p>
		// closes it and all above it, which ends the same.
		if s.name == "br" || s.name == "p" {
			n.breakOut()
		}
		return false, true
	}
	if breakout[s.name] || s.name == "font" && s.fontBreaks() {
		n.breakOut()
		return false, true
	}
	// The tokenizer reads what follows any start tag of svg or math as
	// markup, a title's, a style's or a textarea's too.
	s.z.NextIsNotRawText()
	key := foreignKey(namespace(n.top()), s.name)
	if key == foreignKey("math", "annotation-xml") && s.htmlEncoded() {
		key = htmlAnnotation
	}
	s.foreign = true
	hidden := n.switchChild(key, s.conditional())
	if err := n.openForeign(key, !selfClosing, hidden); err != "" {
		s.err = &NestError{At: s.start, msg: err}
		return false, false
	}
	return true, false
}

// fontBreaks reports whether a font start tag has a color, a face or a
// size, which make it end foreign content.
func (s *Scanner) fontBreaks() bool {
	s.readAttrs()
	return s.fontAttr
}

// conditional reports whether a start tag has a requiredExtensions or a
// systemLanguage attribute, the conditions of an svg switch's child.
func (s *Scanner) conditional() bool {
	s.readAttrs()
	return s.cond
}

// htmlEncoded reports whether an annotation-xml start tag's encoding is
// HTML's, text/html or application/xhtml+xml in any case.
func (s *Scanner) htmlEncoded() bool {
	s.readAttrs()
	return bytes.EqualFold(s.encoding, []byte("text/html")) || bytes.EqualFold(s.encoding, []byte("application/xhtml+xml"))
}

// openForeign follows the start tag of a foreign element, the nest's key,
// which opens it unless it is self closing. It returns a refusal past the
// depth limit, or "".
func (n *nest) openForeign(key string, opens, hidden bool) string {
	n.closedLine, n.ignored = false, false
	switch {
	case !opens:
	case len(n.stack) >= core.MaxDepth:
		return fmt.Sprintf("nested more than %d levels deep, the limit; %s", core.MaxDepth, core.OtherWay)
	default:
		if i := n.pushElement(key); hidden {
			n.veil(i) // a switch's child it does not render
		}
	}
	return ""
}

// closeForeign closes the foreign element at index i and every one above
// it, all foreign, as its end tag does.
func (n *nest) closeForeign(i int) {
	n.closedLine, n.ignored = false, false
	n.popThrough(i)
}

// breakOut closes the foreign elements above the nearest HTML element or
// integration point.
func (n *nest) breakOut() {
	i := len(n.stack) - 1
	for i >= 0 && namespace(n.stack[i]) != "" && !htmlPoints[n.stack[i]] && !textPoints[n.stack[i]] {
		i--
	}
	n.popThrough(i + 1)
}
