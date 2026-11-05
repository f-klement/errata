package extracttest

import (
	"strings"

	"pgregory.net/rapid"
)

// ForeignShapes are shapes of foreign content, the inside of an svg or a
// math element, each with what x/net's tree builder makes of it, which the
// readers must follow: an element of svg or math whose name is an HTML
// raw text element's is read as markup, so a breakout tag in it, such as
// <b>, ends the svg; an HTML integration point (an svg foreignObject, desc
// or title, a math annotation-xml of HTML) and a MathML text integration
// point (mi, mo, mn, ms, mtext) read their start tags and text as HTML's
// again, so a style there is HTML's and raw; a font ends foreign content
// only with a color, a face or a size. init puts each in a pre, a heading
// and a table cell among BrowserShapes.
var ForeignShapes = []string{
	"<svg><title>a<b>c</b></title></svg>", // an integration point: the b is inside the title, veiled
	"<svg><style>a<b>c</b></style></svg>", // the b ends the svg: c shows
	"<svg><script>a<b>c</b>d</script></svg>e",
	"<svg><textarea>\na<b>c</b></textarea></svg>",
	"<math><title>a<b>c</b></title></math>", // no integration point in math: a and c show
	"<math><style>a<p>c</style></math>",
	"<math><textarea>a<br>c</textarea></math>",
	"<svg><foreignObject><title>a<b>c</b></title>d</foreignObject>e</svg>", // HTML's title: raw, hidden
	"<svg><foreignObject><style>a</style><textarea>\nb<i>c</textarea></foreignObject></svg>",
	"<svg><foreignObject><div>a</svg>b", // </svg> past a div: ignored
	"<svg><foreignObject><svg><style>a<b>c</style></svg>d</foreignObject>e</svg>",
	"<svg><desc><style>a<b>c</style>d</desc>e</svg>", // desc: veiled, its HTML style raw
	"<svg><title><svg><style>a<b>c</style></svg>d</title>e</svg>",
	"<svg><g><title>t</title><text>x<tspan>y</tspan></text></g></svg>z",
	"<svg><title><h2>a</h2>b</title></svg>c",
	"<math><mi><style>a<b>c</style>d</mi>e</math>", // a text integration point: HTML's style
	"<math><mi><mglyph><style>a<b>c</style>d</mglyph></mi>e</math>",
	"<math><mi><b>x</mi>y</math>z",
	"<math><mtext><svg><title>a<b>c</b></title><style>d<i>e</style></svg></mtext></math>",
	"<math><annotation-xml encoding=\"text/html\"><style>a<b>c</style>d</annotation-xml></math>",
	"<math><annotation-xml encoding=Application/XHTML+XML><title>a<b>c</title>d</annotation-xml></math>",
	"<math><annotation-xml encoding=text/xml><style>a<b>c</style>d</annotation-xml></math>",
	"<math><annotation-xml><svg><title>a<b>c</b></title></svg><style>d<i>e</style></annotation-xml></math>",
	"<svg><font color=red>a</font>b</svg>c",
	"<svg><font>a<style>b<i>c</style></font></svg>",
	"<svg><font face=x><style>a</style></font></svg>",
	"<svg><svg/><title>a</title><style>b<i>c</style></svg>",
	"<svg/><style>a<b>c</style>",
	"<svg></foo><title>a</title>b</svg>c",
	"<svg><TITLE>a<b>c</B></Title><Style>d<i>e</style></svg>",
	"<svg><td>a<tr>b<th>c</svg>d",
	"<svg><a href=#x>¶</a></svg>",
	"<svg><p>a<style>b<i>c</style></svg>",
	"<svg><title>a</svg>b<style>c</style>",
	"<math><mi><title>a<b>c</title></mi></math>",
	"<math><ms></math>a<style>b</style>",
	"<math><mo><svg><desc>a<b>c</b></desc></svg>d</mo></math>",
	"<svg><iframe>a<b>c</b></iframe></svg>",
	"<math><noembed>a<i>c</noembed><noframes>d</noframes></math>",
	"<svg><plaintext>a</svg>b<h3>c</h3>",
	"<svg><g></p><style>a<i>b</style>c", // </p> opens a p first, which breaks out
	"<p>a<svg><g></p><style>b</style>c",
	"<math><mrow></br><style>a</style>b",
	"Install <svg><title>icon</title><path d=x/></svg> it", // an icon in a heading: its title shows nothing
}

// LonecrShapes are the six probes of the lonecr review (findings 2 and 5)
// as written, each in a pre already: a textarea in an integration point is
// HTML's, raw (the desc's is veiled), and the hidden elements, plaintext
// among them, are foreign in svg or math, so they end at </svg> or </math>
// and hide nothing after.
var LonecrShapes = []string{
	"<pre><svg><foreignObject><textarea>\nx<b>y</b></textarea></foreignObject></svg></pre>",
	"<pre><math><mi><textarea>\nx<b>y</b></textarea></mi></math></pre>",
	"<pre><svg><desc><textarea>\nx<b>y</b></textarea></desc></svg></pre>",
	"<pre><svg><plaintext></svg>z</pre><h2>U</h2>",
	"<pre><svg><xmp></svg>z</xmp></pre>",
	"<pre><math><noscript></math>z</noscript></pre>",
}

// RoundTwoShapes are the foreign review's shapes (findings 2, 3, 4 and 6)
// and their neighbours, as written. The adoption agency takes the
// formatting element and the elements not on its list between it and the
// furthest block off the stack, so a later end tag of its name closes no
// svg; it stops after eight passes, with the svg above the eighth special
// element still open; an svg in a template's content is foreign, so its
// title or style hides nothing after the template; and a math annotation
// or an svg defs draws nothing.
var RoundTwoShapes = []string{
	"<table><tr><td><a><div></a></div><svg><g></a><desc>q</desc>r<td>y</table>",
	"<table><tr><td><b><span><div></b></div><svg></span><desc>q</desc>r<td>y</table>",
	"<table><tr><td><b>" + strings.Repeat("<div>", 7) + "<svg></b><desc>q</desc>r<td>y</table>",
	"<table><tr><td><b>" + strings.Repeat("<div>", 8) + "<svg></b><desc>q</desc>r<td>y</table>",
	"<table><tr><td><b>" + strings.Repeat("<div>", 9) + "<svg></b><desc>q</desc>r<td>y</table>",
	"<table><tr><td><b><i><u><s><em><div></b><svg><style>q</style>r<td>y</table>",
	"<template><svg><title>x</svg></template><h2>V</h2>",
	"<template><svg><style>a</template><pre>z</pre>",
	"<template><template></template><svg><style>a</template><pre>z</pre>",
	"<template><template></template><h2>X</h2></template><h3>Y</h3>", // the inner template, first in the content, closes alone
	"<template><svg><template></template><style>a</style></svg></template><pre>z</pre>",
	"<template><div><svg><textarea>a</div></template><h3>W</h3>",
	"<template><math><mi><style>a</template>b</style><pre>z</pre>",
	"<h2>a<math><semantics><mi>E</mi><annotation encoding=\"application/x-tex\">E=mc^2</annotation></semantics></math></h2>",
	"<h2>b<svg><defs><text>hidden</text></defs><symbol><text>s</text></symbol><text>shown</text></svg></h2>",
	"<pre><svg><clipPath><text>c</text></clipPath><linearGradient>g</linearGradient></svg>x</pre>",
}

// RoundThreeShapes are the foreign delta review's shapes: stray svg text,
// an mphantom, a filter and a switch draw nothing a reader reads, and the
// shapes of StandardReads but the one RoundTwoShapes holds.
var RoundThreeShapes = []string{
	"<h2>a<svg><g>stray</g></svg>b</h2>",
	"<h2>a<math><mphantom><mi>p</mi></mphantom></math>b</h2>",
	"<h2>a<svg><filter><text>f</text></filter></svg>b</h2>", // a text element, whose text a filter veils
	"<h2>a<svg><switch><text>one</text><text>two</text></switch></svg>b</h2>",
	"<h2>a<svg><switch><text systemLanguage=en>one</text><desc>d</desc><text>two</text><text>three</text></switch></svg>b</h2>",
	"<h2>a<svg><switch><rect/><text>two</text></switch></svg>b</h2>",
	"<h2>a<svg><text>t<tspan>s</tspan><a href=x>l</a></text>stray<textPath>p</textPath><foreignObject>f<svg>g</svg></foreignObject></svg>b</h2>",
	"<pre><svg><![CDATA[c]]><text><![CDATA[d]]></text></svg></pre>",
	// After eight passes the rebuild leaves the svg above the eighth div
	// open as it was: the text stays the switch's unrendered child, and
	// the switch keeps the child it rendered (foreign delta review 2,
	// finding 1; the planted break: the draw carry through adopt broken).
	"<table><tr><td><b>" + strings.Repeat("<div>", 8) + "<svg><switch><text systemLanguage=x></b>q</text></switch></svg><td>y</table>",
	"<table><tr><td><b>" + strings.Repeat("<div>", 8) + "<svg><switch><rect/></b><text>q</text></switch></svg><td>y</table>",
	// One listed formatting element between the bold and the div, where the
	// standard and x/net agree: no StandardReads entry, so a StandardOnly
	// flag set at every listed element fails here (foreign delta review 3,
	// finding 1).
	"<table><tr><td><b><i><div></b></div><svg><g></i><desc>q</desc>r<td>y</table>",
	// A col, a colgroup or a caption closes the cell, and the pre or the
	// heading in it with it (foreign delta review 4; the planted break: the
	// three left out of the nest's table parts, which reads ab).
	"<table><tr><td><pre>a<col>b</pre></table>",
	"<table><tr><td><pre>a<colgroup>b</pre></table>",
	"<table><tr><td><pre>a<caption>b</pre></table>",
	"<table><tr><td><h2>a<col>b</h2></table><h3>c</h3>",
	"<table><tr><td><b><i><u><s><em><div></b></div><svg><g></i><desc>q</desc>r<td>y</table>",
	"<table><tr><td><b><i><u><s><em><div></b></div><svg><g></i><desc>q</desc><text>r</text><td>y</table>",
	"<svg><title><template>t</template></title></svg><pre>x</pre>",
	"<math><mi><template>t</template></mi></math><h2>H</h2>",
	"<svg><foreignObject><template>t</template></foreignObject></svg><table><tr><td>c</table>",
}

// StandardReads are shapes where x/net v0.62.0, the browser tests'
// oracle, is not the HTML standard, each with the standard's read, which
// a reader must give: in each pre, heading and row, as BrowserRead holds
// them. x/net leaves on the stack a fourth formatting element the adoption
// agency's inner loop takes off the list (the standard's tree
// construction, adoption agency, inner loop steps 4.4 and 4.5; the
// foreign delta review, finding B), and it drops a template opened in an
// integration point and everything after it (XNetDropsTemplate).
var StandardReads = map[string]BrowserRead{
	// The i is the fourth below the div: off the list and the stack, so
	// </i> closes nothing and the svg stays open, its desc veiled and its
	// stray text not drawn; x/net closes the svg at </i> and reads q|r|y.
	"<table><tr><td><b><i><u><s><em><div></b></div><svg><g></i><desc>q</desc>r<td>y</table>":              {Rows: []string{""}},
	"<table><tr><td><b><i><u><s><em><div></b></div><svg><g></i><desc>q</desc><text>r</text><td>y</table>": {Rows: []string{"r"}},
	"<table><tr><td><b><i><u><s><em><div></b><svg><style>q</style>r<td>y</table>":                         {Rows: []string{""}},
	// A template in an integration point keeps what follows it.
	"<svg><title><template>t</template></title></svg><pre>x</pre>":                             {Pres: []string{"x"}},
	"<math><mi><template>t</template></mi></math><h2>H</h2>":                                   {Headings: []string{"H"}},
	"<svg><foreignObject><template>t</template></foreignObject></svg><table><tr><td>c</table>": {Rows: []string{"c"}},
}

func init() {
	for _, s := range ForeignShapes {
		BrowserShapes = append(BrowserShapes, "<pre>"+s+"</pre>x", "<h2>"+s+"</h2>x", "<table><tr><td>"+s+"<td>y</table>")
	}
	BrowserShapes = append(BrowserShapes, LonecrShapes...)
	BrowserShapes = append(BrowserShapes, RoundTwoShapes...)
	BrowserShapes = append(BrowserShapes, RoundThreeShapes...)
	// The adoption agency's last pass closes the svg above the p it
	// rebuilt around, so the style after it is HTML's, and its <pre> text
	// (TestTheReadingIsTheBrowsers, 20000 checks).
	BrowserShapes = append(BrowserShapes, "<div><b><p>y<svg></b><style>xx<pre>z</pre>", "<div>x<div><svg><font color=red><p>y<svg></font><style>xx<pre>")
	// The title is an integration point, where the inner h2 opens inside
	// the outer, as the tree holds it, which a reader refuses.
	BrowserRefusals["<h2><svg><title><h2>a</h2>b</title></svg>c</h2>x"] = "a heading opens inside a heading"
}

// foreignNames are the elements genForeign draws inside an svg or a math:
// those whose names are HTML raw text elements', the integration points,
// and plain ones. An integration point holds HTML; any other element holds
// foreign content.
var foreignNames = map[string][]string{
	"svg": {"title", "style", "script", "textarea", "desc", "foreignObject", "g", "text", "text", "tspan", "font", "font color=red",
		"switch", "filter", "text systemLanguage=en"},
	"math": {"title", "style", "textarea", "mi", "mtext", "mglyph", "annotation-xml", "annotation-xml encoding=text/html", "mrow", "mphantom"},
}

// foreignPoints are the drawn elements where HTML resumes.
var foreignPoints = map[string]bool{"title": true, "desc": true, "foreignObject": true, "mi": true, "mtext": true,
	"annotation-xml encoding=text/html": true}

// foreignLeaves are what genForeign draws as a leaf: text, a self closing
// element, breakout tags (b, p, a font with a color), a font without one,
// stray end tags, and elements a browser draws nothing of.
var foreignLeaves = []string{"x", "y z", "\n", "<path/>", "<b>y</b>", "<p>y", "<font color=red>y</font>", "<font>y</font>",
	"</title>", "</svg>", "</math>", "<style>s<i>t</style>", "<svg>", "<math>", "</b>", "<defs>d</defs>", "<annotation>a</annotation>"}

// genForeign draws an svg or a math element of ns, with foreign content
// nested at most depth deep, its end tag sometimes left out.
func genForeign(t *rapid.T, ns string, depth int) string {
	var b strings.Builder
	b.WriteString("<" + ns + ">")
	for range rapid.IntRange(0, 3).Draw(t, "foreign children") {
		b.WriteString(genForeignNode(t, ns, depth-1))
	}
	if rapid.IntRange(0, 3).Draw(t, "omit foreign") != 0 {
		b.WriteString("</" + ns + ">")
	}
	return b.String()
}

func genForeignNode(t *rapid.T, ns string, depth int) string {
	if depth <= 0 || rapid.IntRange(0, 2).Draw(t, "foreign leaf") == 0 {
		return rapid.SampledFrom(foreignLeaves).Draw(t, "foreign text")
	}
	tag := rapid.SampledFrom(foreignNames[ns]).Draw(t, "foreign tag")
	name, _, _ := strings.Cut(tag, " ")
	var b strings.Builder
	b.WriteString("<" + tag + ">")
	if foreignPoints[tag] && rapid.Bool().Draw(t, "html inside") {
		b.WriteString(genHTML(t, depth-1))
	} else {
		for range rapid.IntRange(0, 2).Draw(t, "foreign children") {
			b.WriteString(genForeignNode(t, ns, depth-1))
		}
	}
	if rapid.IntRange(0, 3).Draw(t, "omit foreign end") != 0 {
		b.WriteString("</" + name + ">")
	}
	return b.String()
}
