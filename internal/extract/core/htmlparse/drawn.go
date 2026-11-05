package htmlparse

// errata reads only what a browser draws (owner, 2026-11-04): inside svg
// and math it leaves out the text of an element a browser draws nothing
// of, and of everything inside it, and the text an svg element holds
// outside a text element. The tags inside still open and close what they
// open and close. The names are the tokenizer's, in lower case.

// draw is what a browser draws of an element the nest holds: whether it
// veils all text inside it, whether text right inside it is drawn, and,
// for an svg switch, whether it has its rendered child.
type draw uint8

const (
	veils draw = 1 << iota
	drawsText
	switched
)

// veiled are the foreign elements a browser draws nothing of.
var veiled = map[string]bool{
	// SVG 2, 3.2.1 Definitions, "never-rendered element": clipPath, defs,
	// desc, linearGradient, marker, mask, metadata, pattern,
	// radialGradient, script, style and title, and a symbol that is not
	// the instance root of a use element's shadow tree (one in place).
	"svg clippath": true, "svg defs": true, "svg desc": true, "svg lineargradient": true,
	"svg marker": true, "svg mask": true, "svg metadata": true, "svg pattern": true,
	"svg radialgradient": true, "svg script": true, "svg style": true, "svg title": true,
	"svg symbol": true,
	// Filter Effects Module Level 1, 7 SVG Filter Sources: the filter
	// element: "filter elements are never rendered directly".
	"svg filter": true,
	// MathML Core, 3.8 Semantics and Presentation: a semantics draws its
	// first child, the expression, and its annotations, annotation and
	// annotation-xml (of HTML or not), follow it, hidden by the user agent
	// style sheet's semantics > :not(:first-child) { display: none; }
	// (Appendix A). errata goes by the name, so an annotation outside a
	// semantics, which a browser draws as an mtext, is left out too.
	"math annotation": true, "math annotation-xml": true, htmlAnnotation: true,
	// MathML Core, 3.3.7 Making Sub-Expressions Invisible <mphantom>: its
	// content is laid out but not painted, the user agent style sheet's
	// visibility: hidden.
	"math mphantom": true,
}

// textDrawers are the svg elements whose character data a browser draws:
// SVG 2's text content elements, text, tspan and textPath (11.1.1
// Definitions: "A text content element is an SVG element that causes a
// text string to be rendered onto the canvas"), and a foreignObject,
// whose content is HTML's (Embedded Content, the foreignObject element).
// Character data in any other svg element is not drawn, and an element
// inside one of these draws as it.
var textDrawers = map[string]bool{"svg text": true, "svg tspan": true, "svg textpath": true, "svg foreignobject": true}

// drawOf is what a browser draws of an element of name opened on the
// nest's top: HTML's and math's text is drawn; an svg element's is when
// it is a text content element or a foreignObject, never right inside an
// svg, and else as its parent's.
func (n *nest) drawOf(name string) draw {
	var d draw
	if veiled[name] {
		d = veils
	}
	switch {
	case namespace(name) != "svg" || textDrawers[name]:
		d |= drawsText
	case name == "svg svg":
	case len(n.drawn) > 0 && namespace(n.top()) == "svg":
		d |= n.drawn[len(n.drawn)-1] & drawsText
	}
	return d
}

// textDrawn reports whether a browser draws text right inside the top
// element: none of the page's elements, or one whose text it draws.
func (n *nest) textDrawn() bool {
	return n.veiled == 0 && (len(n.drawn) == 0 || n.drawn[len(n.drawn)-1]&drawsText != 0)
}

// switchChild reports whether a child of an svg switch on top, the start
// tag of key, is one a browser does not render, and marks the switch's
// rendered child. SVG 2, 5.6.3 The 'switch' element: it "processes and
// renders the first child for which these attributes evaluate to true".
// errata does not evaluate requiredExtensions or systemLanguage: a child
// with either is taken as one that does not render, and the first child
// with neither, and not itself never rendered, as the rendered one.
func (n *nest) switchChild(key string, conditional bool) bool {
	top := len(n.drawn) - 1
	if top < 0 || n.stack[top] != "svg switch" {
		return false
	}
	if n.drawn[top]&switched != 0 || conditional {
		return true
	}
	if !veiled[key] {
		n.drawn[top] |= switched
	}
	return false
}

// veil makes the element at index i veil the text inside it.
func (n *nest) veil(i int) {
	if n.drawn[i]&veils == 0 {
		n.drawn[i] |= veils
		n.veiled++
	}
}
