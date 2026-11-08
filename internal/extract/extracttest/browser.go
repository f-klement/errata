package extracttest

import (
	"slices"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
	"pgregory.net/rapid"
)

// BrowserRead is what is read of HTML, as the readers' browser tests
// compare it: each pre's text, each heading's text, and each table row's
// cells, joined by |. Each list is sorted: a browser moves an element
// opened in a table outside a cell to before the table (foster parenting),
// and a reader ends an outer row after the rows of a table inside it, so
// the order of the elements differs where their texts do not.
type BrowserRead struct {
	Pres, Headings, Rows []string
	// NestedHeading is whether the tree holds a heading inside a heading,
	// which a reader may refuse.
	NestedHeading bool
	// RebuiltIn holds each formatting name the tree builder rebuilt around
	// or inside a pre or a heading, as the adoption agency does (rebuiltIn).
	RebuiltIn map[string]bool
	// StandardOnly is, of a reader's read, whether it followed a rule of
	// the HTML standard that x/net, the oracle, does not
	// (htmlparse.Scanner.StandardOnly): such a read is compared with
	// StandardReads, and read again in x/net's way to compare with x/net.
	StandardOnly bool
	// XNetMoved is, of a reader's read in x/net's way, whether text came
	// while an element x/net leaves on the stack was open, which x/net's
	// tree moves before a block (htmlparse.Scanner.XNetMovesText): such a
	// read is not compared.
	XNetMoved bool
	// Rebuilt is, of a reader's read where the reader can tell, whether it
	// followed an adoption agency's rebuild, so that a property test can
	// say how often its documents meet one.
	Rebuilt bool
}

// BrowserReads is what golang.org/x/net/html's tree builder, which follows
// the HTML standard, puts in the same elements: the text of each pre and
// listing not inside another, less a newline at its end; the text of each
// heading, less a link to itself (selfLink); and the cells of each row, a cell's text to the first table
// inside it, each element HTML's, not svg's or math's. Text is what a
// browser shows: a line element's tags are a space, blanks fold, and
// scripts, styles, comments and an svg's title show nothing (leftOut).
// lineTag names the line elements, htmlparse.LineTag, which extracttest
// takes as an argument so that htmlparse's own tests can use it; with
// dropSelfLinks a heading's link to itself is left out, as the html kind
// leaves it out and the scanner, which knows no permalinks, does not.
func BrowserReads(src string, lineTag func(string) bool, dropSelfLinks bool) BrowserRead {
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		panic(err)
	}
	var out BrowserRead
	var walk func(n *xhtml.Node, inPre bool)
	walk = func(n *xhtml.Node, inPre bool) {
		if isTemplate(n) {
			return // a template's content is no part of the page
		}
		if n.Type == xhtml.ElementNode && n.Namespace == "" {
			switch {
			case (n.Data == "pre" || n.Data == "listing") && !inPre:
				out.Pres = append(out.Pres, strings.TrimSuffix(textOf(n, false, false, lineTag, false), "\n"))
				inPre = true
			case isHeading(n.Data):
				out.Headings = append(out.Headings, Fold(textOf(n, true, false, lineTag, dropSelfLinks)))
				for p := n.Parent; p != nil; p = p.Parent {
					out.NestedHeading = out.NestedHeading || p.Type == xhtml.ElementNode && isHeading(p.Data)
				}
			case n.Data == "tr":
				var cells []string
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == xhtml.ElementNode && c.Namespace == "" && (c.Data == "td" || c.Data == "th") {
						cells = append(cells, Fold(textOf(c, true, true, lineTag, false)))
					}
				}
				if len(cells) > 0 {
					out.Rows = append(out.Rows, strings.Join(cells, "|"))
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inPre)
		}
	}
	walk(doc, false)
	out.RebuiltIn = rebuiltIn(src, doc)
	slices.Sort(out.Pres)
	slices.Sort(out.Headings)
	slices.Sort(out.Rows)
	return out
}

// textOf is the text below n, a space at each tag of a line element when
// spaced, and up to the first table below it when toTable.
func textOf(n *xhtml.Node, spaced, toTable bool, lineTag func(string) bool, noSelfLinks bool) string {
	var b strings.Builder
	stopped := false
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		for c := n.FirstChild; c != nil && !stopped; c = c.NextSibling {
			switch c.Type {
			case xhtml.TextNode:
				if !hiddenText(c) {
					b.WriteString(c.Data)
				}
			case xhtml.ElementNode:
				if toTable && c.Data == "table" {
					stopped = true
					return
				}
				if noSelfLinks && selfLink(c, lineTag) || isTemplate(c) {
					continue
				}
				line := spaced && c.Namespace == "" && lineTag(c.Data)
				if line {
					b.WriteByte(' ')
				}
				walk(c)
				if line {
					b.WriteByte(' ')
				}
			case xhtml.ErrorNode, xhtml.DocumentNode, xhtml.CommentNode, xhtml.DoctypeNode, xhtml.RawNode:
				// none shows text
			}
		}
	}
	walk(n)
	return b.String()
}

// isTemplate reports whether n is an HTML template, whose content x/net
// puts below it and a reader leaves out.
func isTemplate(n *xhtml.Node) bool {
	return n.Type == xhtml.ElementNode && n.Namespace == "" && n.Data == "template"
}

// selfLink reports whether n is a heading's link to itself, which a reader
// leaves out of the heading's text: an a whose href starts with # and
// whose text is blank or only permalink marks.
func selfLink(n *xhtml.Node, lineTag func(string) bool) bool {
	if n.Data != "a" || n.Namespace != "" {
		return false
	}
	for _, at := range n.Attr {
		if at.Key == "href" && strings.HasPrefix(at.Val, "#") {
			return strings.Trim(textOf(n, true, false, lineTag, false), " \t\n\r\f\u00a0\u200b\ufeff¶§#🔗") == ""
		}
	}
	return false
}

// leftOut are the HTML elements whose text a reader leaves out: a
// script's, a style's, a title's, an iframe's, a noscript's, a noembed's and
// a noframes', which a browser does not show, and an xmp's and a
// plaintext's, which it shows and errata leaves out (docs/reference.md#html).
var leftOut = map[string]bool{"script": true, "style": true, "title": true, "iframe": true, "noscript": true,
	"noembed": true, "noframes": true, "xmp": true, "plaintext": true}

// veiledForeign are the svg and math elements a browser draws nothing of,
// below which a reader leaves out all text, the text of any HTML inside
// them too: SVG 2's never-rendered elements (3.2.1), a filter (Filter
// Effects 1, 7), MathML's annotations, which a semantics hides (MathML
// Core 3.8), and an mphantom, which it lays out unpainted (3.3.7). The
// names are x/net's, in its case.
var veiledForeign = map[string]map[string]bool{
	"svg": {"clipPath": true, "defs": true, "desc": true, "linearGradient": true, "marker": true, "mask": true,
		"metadata": true, "pattern": true, "radialGradient": true, "script": true, "style": true, "title": true,
		"symbol": true, "filter": true},
	"math": {"annotation": true, "annotation-xml": true, "mphantom": true},
}

// svgTextDrawers are the svg elements whose character data a browser
// draws: SVG 2's text content elements (11.1.1) and a foreignObject.
var svgTextDrawers = map[string]bool{"text": true, "tspan": true, "textPath": true, "foreignObject": true}

// veiledByElement reports whether a browser draws nothing below p: an
// element of veiledForeign, or a child of an svg switch other than the
// one it renders, the first child element with no requiredExtensions or
// systemLanguage that is not itself veiled (SVG 2, 5.6.3; the conditions
// are not evaluated).
func veiledByElement(p *xhtml.Node) bool {
	if veiledForeign[p.Namespace][p.Data] {
		return true
	}
	sw := p.Parent
	if p.Namespace != "svg" || sw == nil || sw.Type != xhtml.ElementNode || sw.Namespace != "svg" || sw.Data != "switch" {
		return false
	}
	for c := sw.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != xhtml.ElementNode || veiledForeign[c.Namespace][c.Data] {
			continue
		}
		conditional := false
		for _, a := range c.Attr {
			conditional = conditional || strings.EqualFold(a.Key, "requiredExtensions") || strings.EqualFold(a.Key, "systemLanguage")
		}
		if !conditional {
			return c != p
		}
	}
	return true
}

// hiddenText reports whether a reader leaves out the text node n: its
// parent is an HTML element of leftOut, an svg or math element a browser
// draws nothing of holds it, however deep (veiledByElement), or it is svg
// character data outside a text content element.
func hiddenText(n *xhtml.Node) bool {
	if p := n.Parent; p != nil && p.Type == xhtml.ElementNode && p.Namespace == "" && leftOut[p.Data] {
		return true
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == xhtml.ElementNode && veiledByElement(p) {
			return true
		}
	}
	// In svg a browser draws character data only in a text content element
	// or a foreignObject, nearer than the svg element holding it.
	for p := n.Parent; p != nil && p.Type == xhtml.ElementNode && p.Namespace == "svg"; p = p.Parent {
		if svgTextDrawers[p.Data] {
			return false
		}
		if p.Data == "svg" {
			return true
		}
	}
	return false
}

// formattingNames are the HTML standard's formatting elements.
var formattingNames = map[string]bool{"a": true, "b": true, "big": true, "code": true, "em": true, "font": true, "i": true,
	"nobr": true, "s": true, "small": true, "strike": true, "strong": true, "tt": true, "u": true}

// rebuiltIn names the formatting elements the tree builder rebuilt around
// or inside a pre or a heading, where errata refuses the end tag: more
// elements of the name than start tags, and one that is the first child
// of an element no formatting one, or of a formatting element that is so
// in turn, with a pre or a heading around it or inside it. The adoption
// agency leaves its clone so, as the first child of the block it rebuilt,
// or inside a clone the next rebuild put there. A reconstruction's clone sits so too where text
// starts a block, so the property tests still cap how often it is met.
func rebuiltIn(src string, doc *xhtml.Node) map[string]bool {
	tags := startTags(src)
	elements, rebuilt := map[string]int{}, map[string]bool{}
	formatting := func(n *xhtml.Node) bool {
		return n.Type == xhtml.ElementNode && n.Namespace == "" && formattingNames[n.Data]
	}
	var walk func(n *xhtml.Node, in bool)
	walk = func(n *xhtml.Node, in bool) {
		if formatting(n) {
			elements[n.Data]++
			top := n // the outermost of the formatting elements n is the first child of
			for p := top.Parent; p != nil && formatting(p) && p.FirstChild == top; p = top.Parent {
				top = p
			}
			p := top.Parent
			if p != nil && p.Type == xhtml.ElementNode && !formatting(p) && p.FirstChild == top && (in || holdsBlock(n)) {
				rebuilt[n.Data] = true
			}
		}
		if n.Type == xhtml.ElementNode && n.Namespace == "" {
			in = in || blockWatched(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, in)
		}
	}
	walk(doc, false)
	out := map[string]bool{}
	for name := range rebuilt {
		out[name] = elements[name] > tags[name]
	}
	return out
}

// startTags counts the start tags of each formatting element's name in src
// that make an HTML element, as the tree builder reads src. The tokenizer
// alone reads it so but in svg and math, where the tree builder reads a
// title, a style or a textarea as markup (NextIsNotRawText), and a start
// tag of a formatting name may make an svg's element. With svg or math in
// src, each start tag of such a name is placed by parsing src up to its
// end and finding the element it made, the last of its name: a cost of
// the file for each, which the documents compared bear.
func startTags(src string) map[string]int {
	lower := strings.ToLower(src)
	foreign := strings.Contains(lower, "<svg") || strings.Contains(lower, "<math")
	tags := map[string]int{}
	treeTags(src, func(name string, _, end int) {
		if !foreign || !formattingNames[name] || madeIn(src[:end], name) == "" {
			tags[name]++
		}
	})
	return tags
}

// treeTags calls tag with the name and the offsets of each start tag in
// src, as the tree builder's tokenizer reads them: in a page with svg or
// math, the text after a raw text element's start tag is read as markup
// where the tag made a foreign element (madeIn), as x/net's parser tells
// its tokenizer (NextIsNotRawText).
func treeTags(src string, tag func(name string, start, end int)) {
	lower := strings.ToLower(src)
	foreign := strings.Contains(lower, "<svg") || strings.Contains(lower, "<math")
	z := xhtml.NewTokenizer(strings.NewReader(src))
	end := 0
	for tt := z.Next(); tt != xhtml.ErrorToken; tt = z.Next() {
		start := end
		end += len(z.Raw())
		if tt != xhtml.StartTagToken && tt != xhtml.SelfClosingTagToken {
			continue
		}
		b, _ := z.TagName()
		name := string(b)
		if foreign && rawNames[name] && madeIn(src[:end], name) != "" {
			z.NextIsNotRawText()
		}
		tag(name, start, end)
	}
}

// rawNames are the elements whose text the tokenizer reads raw after their
// start tag.
var rawNames = map[string]bool{"title": true, "textarea": true, "style": true, "xmp": true, "iframe": true,
	"noembed": true, "noframes": true, "script": true, "noscript": true, "plaintext": true}

// madeIn is the namespace of the last element of name in the tree of
// prefix, in tree order, "" for HTML's or for none. That is most often the
// element the last start tag of prefix made, but not always: a clone the
// adoption agency made, or an element fostered out of a table, moves the
// elements' order away from the tags'. The gap points the strict way, so at
// worst a caller rejects a refusal that is in fact correct (foreign delta
// review 1).
func madeIn(prefix, name string) string {
	doc, err := xhtml.Parse(strings.NewReader(prefix))
	if err != nil {
		panic(err)
	}
	ns := ""
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && strings.EqualFold(n.Data, name) {
			ns = n.Namespace
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return ns
}

// blockWatched reports whether an element of name is a pre or a heading.
func blockWatched(name string) bool { return name == "pre" || name == "listing" || isHeading(name) }

// holdsBlock reports whether a pre or a heading is below n.
func holdsBlock(n *xhtml.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && (blockWatched(c.Data) || holdsBlock(c)) {
			return true
		}
	}
	return false
}

// isHeading reports whether name is h1 to h6.
func isHeading(name string) bool {
	return len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6'
}

// Fold is s with each run of HTML's blanks one space and none at either
// end: a vertical tab or a no break space is text.
func Fold(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\f' || r == '\r' // HTML's blanks, not Unicode's
	}), " ")
}

// GenHTML draws HTML of the elements the readers follow, nested at most
// depth deep, each end tag sometimes left out, with text, comments, scripts,
// textareas, svg and math (genForeign) and stray end tags between them. A table holds sections and
// rows, and a row cells, as pages write them; a table's end tag is never
// left out, and no stray end tag of a table's part is drawn, so that no
// text lands in a table outside a cell: a browser moves such text before
// the table (foster parenting), and errata, which reads in file order, does
// not.
func GenHTML(t *rapid.T, depth int) string {
	src := genHTML(t, depth)
	if rapid.IntRange(0, 11).Draw(t, "rebuild") == 11 { // rapid draws the low end most: rebuild at the high one
		src = genRebuild(t) + src
	}
	return src
}

// genRebuild draws an end tag the adoption agency rebuilds around a
// block, at the start of a document, so that no pre or heading is open
// around it, where a reader refuses one: a bold holding text, up to two
// formatting elements, a div or a p, and inside that, sometimes, an svg or
// a math left open, then the bold's end tag and the block's. In about one
// document in twelve, so that the property tests meet a rebuild in a few
// of each hundred (foreign delta review 3, finding 1).
func genRebuild(t *rapid.T) string {
	inner := rapid.SampledFrom([]string{"", "<i>", "<i><u>", "<em>"}).Draw(t, "rebuild inner")
	block := rapid.SampledFrom([]string{"div", "p"}).Draw(t, "rebuild block")
	foreign := rapid.SampledFrom([]string{"", "<svg><g>", "<svg><desc>", "<svg><switch><text>", "<svg><text>", "<math><mi>"}).Draw(t, "rebuild foreign")
	return "<b>x" + inner + "y<" + block + ">z" + foreign + "q</b>w</" + block + ">v"
}

func genHTML(t *rapid.T, depth int) string {
	var b strings.Builder
	n := rapid.IntRange(0, 4).Draw(t, "children")
	for range n {
		b.WriteString(genHTMLNode(t, depth))
	}
	return b.String()
}

var (
	htmlFlowTags = []string{"div", "details", "pre", "listing", "h1", "h2", "h3", "h6", "table", "a", "a", "p", "ul", "ol", "li", "svg", "math"} // "a" draws an inline element
	htmlLeaves   = []string{"x", "y z", "\n", "  w\n", "<!--c-->", "<script>s<p>q</script>", "<textarea>t</textarea>", "</div>", "</p>", "</li>", "</h2>", "</pre>", "<br>",
		"<button>t</button>", "<xmp>t</xmp>", "<span>s</span>", "<img>", templateLeaf} // these reopen formatting elements first
	htmlInline = []string{"a", "code", "b"}
	// templateLeaf is a template whose content holds an svg style, which
	// hides nothing after the template. Where x/net drops it, in an
	// integration point, the tests compare nothing (XNetDropsTemplate).
	templateLeaf = "<template><svg><style>s</template>"
)

// genHTMLInline draws a formatting element, a link, code or bold, around
// text and other formatting elements, as pages write them. Its end tag is
// left out in one draw in 60, as a page rarely leaves it out: a formatting
// element left open around a block's end is what the adoption agency
// rebuilds and errata refuses, and the generator keeps that rare, so that
// the property tests' cap on such refusals has room.
func genHTMLInline(t *rapid.T, tag string, depth int) string {
	var b strings.Builder
	b.WriteString("<" + tag + ">")
	for range rapid.IntRange(0, 3).Draw(t, "inline children") {
		if depth > 0 && rapid.IntRange(0, 3).Draw(t, "nest inline") == 0 {
			inner := rapid.SampledFrom(htmlInline).Draw(t, "inline tag")
			if inner != tag && inner != "a" {
				b.WriteString(genHTMLInline(t, inner, depth-1))
				continue
			}
		}
		b.WriteString(rapid.SampledFrom([]string{"x", "y z", "\n", "  w\n"}).Draw(t, "inline text"))
	}
	if rapid.IntRange(0, 59).Draw(t, "omit inline") != 59 { // rapid draws the low end most: omit at the high one
		b.WriteString("</" + tag + ">")
	}
	return b.String()
}

func genHTMLNode(t *rapid.T, depth int) string {
	if depth == 0 || rapid.IntRange(0, 2).Draw(t, "leaf") == 0 {
		return rapid.SampledFrom(htmlLeaves).Draw(t, "text")
	}
	tag := rapid.SampledFrom(htmlFlowTags).Draw(t, "tag")
	switch tag {
	case "table":
		return genHTMLTable(t, depth)
	case "a":
		return genHTMLInline(t, rapid.SampledFrom(htmlInline).Draw(t, "inline tag"), depth-1)
	case "svg", "math":
		return genForeign(t, tag, depth)
	}
	end := "</" + tag + ">"
	if rapid.IntRange(0, 3).Draw(t, "omit") == 0 {
		end = ""
	}
	return "<" + tag + ">" + genHTML(t, depth-1) + end
}

func genHTMLTable(t *rapid.T, depth int) string {
	var b strings.Builder
	b.WriteString("<table>")
	for range rapid.IntRange(0, 3).Draw(t, "rows") {
		section := rapid.SampledFrom([]string{"", "tbody", "thead", "tfoot"}).Draw(t, "section")
		if section != "" {
			b.WriteString("<" + section + ">")
		}
		b.WriteString("<tr>")
		for range rapid.IntRange(0, 3).Draw(t, "cells") {
			cell := rapid.SampledFrom([]string{"td", "th"}).Draw(t, "cell")
			b.WriteString("<" + cell + ">" + genHTML(t, depth-1))
			if rapid.Bool().Draw(t, "close cell") {
				b.WriteString("</" + cell + ">")
			}
		}
		if rapid.Bool().Draw(t, "close row") {
			b.WriteString("</tr>")
		}
		if section != "" && rapid.Bool().Draw(t, "close section") {
			b.WriteString("</" + section + ">")
		}
	}
	b.WriteString("</table>")
	return b.String()
}

// BrowserShapes are the shapes the reviews of #86 found errata reading
// otherwise than a browser, and the edges the nest's rules name.
var BrowserShapes = []string{
	"<div><table><tr><td>x</table><pre>code</div>more",
	"<table><tr><td>x</table>after",
	"<table><tr><th>Host<th>Port</table>",
	"<table><tbody><tr><td>x</tbody></table>",
	"<details><summary>Log</summary><pre>\nout\n</details>after",
	"<div align=center><pre>\nx\n</div>y",
	"<div><pre>a</div>b",
	"<div><h2>A\n</div>B</h2>",
	"<pre>a\n</listing>b</pre>",
	"<pre><div>a</div>b</pre><p>c</p>",
	"<h2>A</h3>x<h2>B</h2>y",
	"<table><tr><th>Port</th><tr><td>9</td><table><tr><th>Port<tr><td>1</table></table>",
	"<table><caption><table><tr><th>Port<tr><td>1</table></caption><tr><th>Port<tr><td>2</table>",
	"<h2>Foo<br>Bar</h2>",
	"<pre>\na\n\nb\nc</pre>",
	"<pre><b>a</pre>b",
	"<pre><span><div>a</span>b</pre>",
	"<pre><p>a<div>b</p>c</pre>",
	"<pre><ul><li>a<li>b</ul>c</pre>",
	"<ul><li><pre>a<li>b</ul>",
	"<a>x<a>y</a>",
	"<h1>a<pre>b</h1>c",
	"<table><tr><td>a<td>b<tr><td>c</table>",
	"<p><pre>x</pre></p>",
	"<pre><textarea>t</textarea>a</pre>",
	"<pre><script>s</script>a</pre>",
	"<h1>A<a>x</h1>y",
	// Found by the generated cases and the fuzz campaigns of round 3, and by
	// delta review 3 of a13dfeb.
	"<div><table><tr><td><pre>xx<td><td>x</table>",
	"xx<div><div><div><div><pre></li>\nxx",
	"<table><tr><tr><td><div><div><div><div></td><pre><td>x</table>",
	"<pre><pre></pre>\nx",
	"x<div>x<div><div><div><h1>x</li>x",
	"<table><tr><th><div></td>x</table>",
	"xx<div><div><h1><li><ul>x</li>x",
	"<pre></p>\nx",
	"<div><h1><pre>x<div>x<div><div><a></pre>x<div></div><h1>x",
	"<div>x<h1><listing><div>x</listing>x",
	"<pre>\n</li>\nx",
	"<h1>J\v>",
	"<h1>\v",
	"<table><tr><td></tbody>x",
	"<li>x<pre>xxx<ul></li>x",
	"<h2>A<h3>B</h3>",
	"xx<table><tr><td></table>x",
	// Ordinary formatting a reader must read, never refuse (delta review 4
	// of 8c0fb83): a link and inline code in a pre, a heading, a pre in a
	// list item, and a table cell.
	"<pre><a href=x>y</a></pre>",
	"<pre><code>x<b>y</b></code></pre>",
	"<pre><code class=language-go>a <a href=#b>b</a> c</code></pre>",
	"<h2>A<a href=\"#a\">¶</a></h2>",
	"<h2>A <a href=x>link</a> <code>c</code></h2>",
	"<ul><li><pre><a>x</a></pre></ul>",
	"<ul><li><pre><code>x <b>y</b></code>\nz</pre><li>w</ul>",
	"<table><tr><td><a href=x>a</a> <code>c</code><td><pre><a>p</a></pre></table>",
	"<table><tr><td><h3>H <a href=#h>¶</a></h3></table>",
	"<h3><a href=#>¶</>", // a link left open ends with its heading (FuzzBrowser)
	// The class of FuzzHTML's find in the gate of 8c0fb83: a heading after
	// a block closed a formatting element. With no text between, the next
	// heading ends the first; after text, the browser reopens the element
	// and opens the heading inside the first, which a reader refuses.
	"<h1>a<div><b></div><h2>b",
	"<h1>a\n<div><b>x</div><h2>b</h2>\n<p>c</p>",
	"<h1>a\n<div><b>x</div>\n<h2>b</h2>\n<p>c</p>", // a newline is text too: refused
	"<h1>a<div><b></div>c<h2>d",
	// A button and an xmp reopen formatting elements before them, as an
	// inline element does (delta review 5 of 2f0bc8c).
	"<h1>A<div><b></div><button>t</button><h2>B</h2>x",
	"<h1>A<div><b></div><xmp>t</xmp><h2>B</h2>x",
	"<h1>A<div><b></div><span>t</span><h2>B</h2>x",
	// A link past a table leaves the stack when the next link opens, so
	// the heading after the table is the first's sibling (FuzzBrowser).
	"<h1><A><tABle><A 000000></tABle><h1>",
	"<h1><a><table><a></table><h1>x",
	// A link ends a self link open, which is then cut (FuzzBrowser).
	"<h1><A href=#>#<A >",
	"<h1><a href=#>#<a>x</a> y</h1>",
	// A pre's first line feed, which a browser drops, reopens nothing
	// (FuzzHTML).
	"<h1>T<p><code><pre>\n<p></code>x</pre><h2>U</h2>",
	"<h1>T<p><code><pre>\nx<p></code></pre><h2>U</h2>",
	"<p ><Code ><pre>\r<p></Code>", // a lone CR is a line feed too (FuzzHTML, the round after 04c52e9)
	// A textarea in svg or math is a foreign element: its text is markup,
	// a <b> in it ends the svg, and the line feed after its tag is text.
	"<h1>A<svg><textarea>B<b>C</b></textarea></svg>D</h1>",
	"<pre><svg><textarea>\nx<p>y</textarea></svg></pre>",
	"<pre><math><textarea>\nx</textarea></math></pre>",
	"<pre><svg/><textarea>\nx<b>y</textarea></pre>",
	// The page's mode by the standard's rules on its first doctype: a table
	// opens in a p in quirks mode, and closes it in standards mode, where
	// the h2 lands in the reopened code inside the h1. The names are in
	// lower case: x/net compares the name as written, where the standard's
	// tokenizer lowercases it and errata follows the standard
	// (TestDoctypeMode holds <!DOCTYPE HTML>).
	"<h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	"<!DOCTYPE html><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	"<!doctype html ><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	"<!DOCTYPE html SYSTEM \"about:legacy-compat\"><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	"<!DOCTYPE html PUBLIC \"-//W3C//DTD XHTML 1.0 Strict//EN\" \"http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd\"><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	"<!DOCTYPE html PUBLIC \"-//W3C//DTD HTML 4.01 Transitional//EN\"><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	"<!DOCTYPE html PUBLIC \"-//W3C//DTD HTML 4.01 Transitional//EN\" \"http://www.w3.org/TR/html4/loose.dtd\"><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	"<p>a</p><!DOCTYPE html><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	"<!-- c -->\n<!DOCTYPE html><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x",
	// A table opens in a p in quirks mode, a page without <!DOCTYPE html>,
	// and closes it in standards mode (FuzzHTML, the round after 8c0fb83).
	"<h1><p><code><table>w<td>8</table>\n<h2>",
	"<!doctype html><p>a<table><td>x</table>",
	"<!DOCTYPE html><h1><p><code><table><td>8</table>\n<h2>",
	"<pre><code><table><tr><th>Host<th>Port<tr><td>x</code>y</table>", // </code> out of scope past a table: ignored (FuzzHTML)
	// A button's start tag ends a button in scope, and the heading or pre
	// opened in it (FuzzBrowser, the flakes seat's fix round 2); past a
	// table the button is out of scope and stays.
	"<Button><h1><Button>0",
	"<div><button><h2>a<button>b</h2>c",
	"<button><pre>x<button>y</pre>z",
	"<button><table><tr><td><h3>a<button>b</table>",
	// A select ends the scope of what is open below it, so </pre> in it
	// closes nothing, and a select's or an input's start tag in it ends
	// the select first: the HTML standard's "in body" mode, which x/net
	// follows (flakes delta review 3, finding 1).
	"<pre><select>x</pre>y",
	"<select><h1>a<select>b</h1>",
	"<select><h1>a<input>b</h1>",
	"<table><td><select><p>a<select>b</table>",
	"<pre><select><select>x</pre>y", // the second select opens nothing, so </pre> closes the pre
	// With a select in scope an hr, an option or an optgroup generates
	// implied end tags, so the li ends and </li> then closes nothing;
	// outside one an optgroup ends no optgroup (flakes delta review 4,
	// finding 1).
	"<select><h1><li>a<hr>b</li>c</h1>",
	"<select><h1><dt>a<hr>b</dt>c</h1>",
	"<table><td><select><li>a<option>b</li>c</table>",
	"<table><td><select><dd>a<option>b</dd>c</table>",
	"<table><td><select><li>a<optgroup>b</li>c</table>",
	"<h1><optgroup>a<optgroup>b</optgroup>c</optgroup>d</h1>",
}

// BrowserRefusals are the shapes of BrowserShapes a reader refuses, each
// with its refusal's words: every other shape it must read.
var BrowserRefusals = map[string]string{
	"<h1>T<p><code><pre>\nx<p></code></pre><h2>U</h2>": "around a block still open",
	"<!DOCTYPE html PUBLIC \"-//W3C//DTD HTML 4.01 Transitional//EN\" \"http://www.w3.org/TR/html4/loose.dtd\"><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x": "a heading opens inside a heading",
	"<h1>A<div><b></div><button>t</button><h2>B</h2>x":                                              "a heading opens inside a heading",
	"<h1>A<div><b></div><xmp>t</xmp><h2>B</h2>x":                                                    "a heading opens inside a heading",
	"<h1>A<div><b></div><span>t</span><h2>B</h2>x":                                                  "a heading opens inside a heading",
	"<!DOCTYPE html><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x":                                "a heading opens inside a heading",
	"<!doctype html ><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x":                               "a heading opens inside a heading",
	"<!DOCTYPE html SYSTEM \"about:legacy-compat\"><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x": "a heading opens inside a heading",
	"<!DOCTYPE html PUBLIC \"-//W3C//DTD XHTML 1.0 Strict//EN\" \"http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd\"><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x": "a heading opens inside a heading",
	"<!-- c -->\n<!DOCTYPE html><h1>T<p><code><table><td>8</table>\n<h2>A</h2>x":                                                                                       "a heading opens inside a heading",
	"<div><h1><pre>x<div>x<div><div><a></pre>x<div></div><h1>x":                                                                                                        "a heading opens inside a heading", // the text reopens the a, and the h1 opens in it
	"<h1>a<div><b></div>c<h2>d":                              "a heading opens inside a heading",
	"<!DOCTYPE html><h1><p><code><table><td>8</table>\n<h2>": "a heading opens inside a heading",
	"<h1>a\n<div><b>x</div>\n<h2>b</h2>\n<p>c</p>":           "a heading opens inside a heading",
}

// BrowserComparable reports whether a reader's text of src can be compared
// with the tree builder's. References, CRs and NULs read alike but render
// apart, and a doctype changes the parse's mode. golang.org/x/net/html
// matches an end tag in foreign content to its element by Unicode case
// folding, where the standard folds ASCII alone (XNetFoldsName).
// Its caption mode leaves th out of the tags that end a caption, which
// the HTML standard lists (x/net v0.62.0, inCaptionIM), so with a caption
// the oracle is not the standard, and it drops a template opened in an svg's or a
// math's integration point and everything after it (XNetDropsTemplate),
// where it is not either. A reader's read that followed the standard
// where x/net does not (BrowserRead.StandardOnly) is compared as the
// reader reads it in x/net's way (htmlparse.Scanner.FollowXNetAdoption).
func BrowserComparable(src string) bool {
	return !strings.ContainsAny(src, "&\r\x00") && !strings.Contains(src, "<!") && !strings.Contains(src, "<?") &&
		!strings.Contains(strings.ToLower(src), "<caption") && !XNetDropsTemplate(src) && !XNetFoldsName(src)
}

// XNetFoldsName reports whether a tag name in src holds a byte of 0x80 or
// above, where x/net v0.62.0 is not the standard. It matches an end tag
// in foreign content to an element with strings.EqualFold (parse.go,
// parseForeignContent), which folds Unicode: </test> closes <te\u017ft>,
// and </te\x85t> closes <te\x80t>, both U+FFFD to it. The standard folds
// ASCII letters alone, and leaves the read of a byte that is not UTF-8 to
// the browser's guess at the encoding. Where a name is a tag depends on
// the tree: after an xmp, a textarea or a plaintext opened in svg or math
// the tree builder reads tags (NextIsNotRawText) where x/net's tokenizer
// alone reads raw text, and a comment in an HTML textarea is text to it.
// So every < or </ followed by an ASCII letter starts a name here, as the
// tokenizer starts one, wherever it stands, and the name runs to a blank,
// a / or a >, a < inside it included, as the tokenizer reads it; one in
// text, a comment or an attribute only leaves out one input more. A byte of 0x80 or above
// anywhere else changes no match, and is compared.
func XNetFoldsName(src string) bool {
	for rest := src; ; {
		i := strings.IndexByte(rest, '<')
		if i < 0 {
			return false
		}
		rest = strings.TrimPrefix(rest[i+1:], "/")
		if rest == "" || rest[0]|0x20 < 'a' || rest[0]|0x20 > 'z' {
			continue
		}
		end := strings.IndexAny(rest, "\t\n\f\r />")
		if end < 0 {
			end = len(rest)
		}
		if strings.ContainsFunc(rest[:end], func(r rune) bool { return r >= utf8.RuneSelf }) {
			return true
		}
		rest = rest[end:] // a < inside the name is the name's, and is read once
	}
}

// XNetDropsTemplate reports whether x/net v0.62.0's tree builder makes no
// element of a template start tag in src, as it does for one opened in an
// svg's or a math's integration point, dropping all that follows, where
// the HTML standard keeps them: the tree of src up to the tag holds no
// more template elements than the tree up to its start. Only a page with
// svg or math is parsed so, twice for each such tag.
func XNetDropsTemplate(src string) bool {
	lower := strings.ToLower(src)
	if !strings.Contains(lower, "<template") || !strings.Contains(lower, "<svg") && !strings.Contains(lower, "<math") {
		return false
	}
	dropped := false
	treeTags(src, func(name string, start, end int) {
		dropped = dropped || name == "template" && templates(src[:end]) == templates(src[:start])
	})
	return dropped
}

// templates counts the template elements, of any namespace, in the tree
// x/net builds of src.
func templates(src string) int {
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		panic(err)
	}
	count := 0
	var walk func(n *xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode && n.Data == "template" {
			count++
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return count
}
