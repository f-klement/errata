package extracttest

import (
	"slices"
	"strings"

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
}

// BrowserReads is what golang.org/x/net/html's tree builder, which follows
// the HTML standard, puts in the same elements: the text of each pre and
// listing not inside another, less a newline at its end; the text of each
// heading, less a link to itself (selfLink); and the cells of each row, a cell's text to the first table
// inside it. Text is what a browser shows: a line element's tags are a
// space, blanks fold, and scripts, styles and comments show nothing.
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
		if n.Type == xhtml.ElementNode {
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
					if c.Type == xhtml.ElementNode && (c.Data == "td" || c.Data == "th") {
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
				if p := c.Parent; p == nil || p.Type != xhtml.ElementNode || !leftOut[p.Data] {
					b.WriteString(c.Data)
				}
			case xhtml.ElementNode:
				if toTable && c.Data == "table" {
					stopped = true
					return
				}
				if noSelfLinks && selfLink(c, lineTag) {
					continue
				}
				line := spaced && lineTag(c.Data)
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

// selfLink reports whether n is a heading's link to itself, which a reader
// leaves out of the heading's text: an a whose href starts with # and
// whose text is blank or only permalink marks.
func selfLink(n *xhtml.Node, lineTag func(string) bool) bool {
	if n.Data != "a" {
		return false
	}
	for _, at := range n.Attr {
		if at.Key == "href" && strings.HasPrefix(at.Val, "#") {
			return strings.Trim(textOf(n, true, false, lineTag, false), " \t\n\r\f\u00a0\u200b\ufeff¶§#🔗") == ""
		}
	}
	return false
}

// leftOut are the elements whose text a reader leaves out: a script's and
// a style's, which a browser does not show, and an xmp's and a
// plaintext's, which it shows and errata leaves out (README).
var leftOut = map[string]bool{"script": true, "style": true, "xmp": true, "plaintext": true}

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
	tags := map[string]int{}
	z := xhtml.NewTokenizer(strings.NewReader(src))
	for tt := z.Next(); tt != xhtml.ErrorToken; tt = z.Next() {
		if tt == xhtml.StartTagToken || tt == xhtml.SelfClosingTagToken {
			name, _ := z.TagName()
			tags[string(name)]++
		}
	}
	elements, rebuilt := map[string]int{}, map[string]bool{}
	var walk func(n *xhtml.Node, in bool)
	walk = func(n *xhtml.Node, in bool) {
		if n.Type == xhtml.ElementNode {
			if formattingNames[n.Data] {
				elements[n.Data]++
				top := n // the outermost of the formatting elements n is the first child of
				for p := top.Parent; p != nil && p.Type == xhtml.ElementNode && formattingNames[p.Data] && p.FirstChild == top; p = top.Parent {
					top = p
				}
				p := top.Parent
				if p != nil && p.Type == xhtml.ElementNode && !formattingNames[p.Data] && p.FirstChild == top && (in || holdsBlock(n)) {
					rebuilt[n.Data] = true
				}
			}
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
// textareas and stray end tags between them. A table holds sections and
// rows, and a row cells, as pages write them; a table's end tag is never
// left out, and no stray end tag of a table's part is drawn, so that no
// text lands in a table outside a cell: a browser moves such text before
// the table (foster parenting), and errata, which reads in file order, does
// not.
func GenHTML(t *rapid.T, depth int) string {
	var b strings.Builder
	n := rapid.IntRange(0, 4).Draw(t, "children")
	for range n {
		b.WriteString(genHTMLNode(t, depth))
	}
	return b.String()
}

var (
	htmlFlowTags = []string{"div", "details", "pre", "listing", "h1", "h2", "h3", "h6", "table", "a", "a", "p", "ul", "ol", "li"} // "a" draws an inline element
	htmlLeaves   = []string{"x", "y z", "\n", "  w\n", "<!--c-->", "<script>s<p>q</script>", "<textarea>t</textarea>", "</div>", "</p>", "</li>", "</h2>", "</pre>", "<br>",
		"<button>t</button>", "<xmp>t</xmp>", "<span>s</span>", "<img>"} // these reopen formatting elements first
	htmlInline = []string{"a", "code", "b"}
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
	}
	end := "</" + tag + ">"
	if rapid.IntRange(0, 3).Draw(t, "omit") == 0 {
		end = ""
	}
	return "<" + tag + ">" + GenHTML(t, depth-1) + end
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
			b.WriteString("<" + cell + ">" + GenHTML(t, depth-1))
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
// apart, and a doctype changes the parse's mode. golang.org/x/net/html's
// caption mode leaves th out of the tags that end a caption, which the HTML
// standard lists (x/net v0.62.0, inCaptionIM), so with a caption the oracle
// is not the standard.
func BrowserComparable(src string) bool {
	return !strings.ContainsAny(src, "&\r\x00") && !strings.Contains(src, "<!") && !strings.Contains(src, "<?") &&
		!strings.Contains(strings.ToLower(src), "<caption")
}
