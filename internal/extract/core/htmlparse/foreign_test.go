package htmlparse

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
)

// foreignTokens is tokens with each foreign element's tag marked by a *,
// and a + after each tag that breaks a line.
func foreignTokens(src string) string {
	var out []string
	s := NewScanner([]byte(src), 1<<20)
	for s.Next() {
		var tok string
		switch s.Kind() {
		case StartTag:
			tok = "<" + s.Name()
		case EndTag:
			tok = "</" + s.Name()
		case Text:
			tok = "text:" + string(s.Bytes())
		case Pre:
			tok = "pre:" + string(s.Bytes())
		case Comment:
			tok = "comment:" + string(s.Bytes())
		}
		if s.Foreign() {
			tok += "*"
		}
		if s.BreaksLine() {
			tok += "+"
		}
		out = append(out, tok)
	}
	if s.Err() != nil {
		out = append(out, "error:"+s.Err().Error())
	}
	return strings.Join(out, "|")
}

// Inside svg or math every element is read as x/net's tree builder reads
// it (parseForeignContent): a start tag of svg or math is never raw text,
// so a breakout tag such as <b> in an svg style ends the svg, and the text
// after it shows; an HTML integration point (an svg title, desc or
// foreignObject, an annotation-xml of HTML) and a MathML text integration
// point (mi, mtext) read their start tags and text as HTML's, so a style
// there is raw; an svg's script, style, title and desc are veiled, their
// text left out. A foreign element's tag is Foreign and breaks no line.
// The planted breaks: an end tag closing a foreign element below an HTML
// one (htmls not kept); every svg or math start tag read raw again (the
// textarea fix of bb8e083 alone); the integration points taken for
// foreign elements; the veil lifted; an end tag that closes HTML's
// element left the svg open.
func TestForeignContentIsTheTreeBuilders(t *testing.T) {
	divs, eight := strings.Repeat("<div>", 8), strings.Repeat("<div+|", 8)
	for src, want := range map[string]string{
		// After eight passes the rebuild pushes the svg again as it was: the
		// text stays the switch's unrendered child, and the switch keeps the
		// child it rendered, so no q, and the veil ends with them: z (the
		// draw carry through adopt, and the count of veils it keeps).
		"<b>" + divs + "<svg><switch><text systemLanguage=x></b>q</text></switch></svg>z":        "<b|" + eight + "<svg*|<switch*|<text*|</b|</text*|</switch*|</svg*|text:z",
		"<b>" + divs + "<svg><defs></b>q</defs></svg>z":                                          "<b|" + eight + "<svg*|<defs*|</b|</defs*|</svg*|text:z", // pushed again and veiled by name: the count taken back first
		"<b>" + divs + "<svg><switch><rect/></b><text>q</text></switch></svg>z":                  "<b|" + eight + "<svg*|<switch*|<rect*|</b|<text*|</text*|</switch*|</svg*|text:z",
		"<svg><style>a<b>c</b></style></svg>":                                                    "<svg*|<style*|<b|text:c|</b|</style|</svg",
		"<svg><title>a<b>c</b></title>d</svg>e":                                                  "<svg*|<title*|<b|</b|</title*|</svg*|text:e",
		"<math><title>a<b>c</b></title></math>":                                                  "<math*|<title*|text:a|<b|text:c|</b|</title|</math",
		"<svg><foreignObject><style>a</style>b</foreignObject></svg>":                            "<svg*|<foreignobject*|<style|</style|text:b|</foreignobject*|</svg*",
		"<math><mi><textarea>\nx<b></textarea></mi></math>":                                      "<math*|<mi*|<textarea|pre:x<b>|</textarea|</mi*|</math*",
		"<math><mi><mglyph><style>a<b>c</style></mi></math>":                                     "<math*|<mi*|<mglyph*|<style*|text:a|<b|text:c|</style|</mi|</math",
		"<math><annotation-xml encoding=TEXT/HTML><style>a<b>c</style>d</annotation-xml></math>": "<math*|<annotation-xml*|<style|</style|</annotation-xml*|</math*",
		"<math><annotation-xml encoding=text/xml><style>a<b>c</style>d</annotation-xml></math>":  "<math*|<annotation-xml*|<style*|<b|text:c|</style|text:d|</annotation-xml|</math",
		"<math><annotation-xml><svg><style>a</style></svg></annotation-xml></math>":              "<math*|<annotation-xml*|<svg*|<style*|</style*|</svg*|</annotation-xml*|</math*",
		"<svg><font color=red>a</font>b</svg>c":                                                  "<svg*|<font|text:a|</font|text:b|</svg|text:c",
		"<svg><font>a<style>b<i>c</style></font></svg>":                                          "<svg*|<font*|<style*|<i|text:c|</style|</font|</svg",
		"<table><tr><td><svg><td>a<tr>b</svg>c":                                                  "<table+|<tr+|<td+|<svg*|<td*|<tr*|</svg*|text:c",
		"<pre><svg></pre><textarea>\nx<b>y</textarea>":                                           "<pre+|<svg*|</pre+|<textarea|pre:x<b>y|</textarea",
		"<svg><TITLE>a</Title><Style>b</STYLE></svg>c":                                           "<svg*|<title*|</title*|<style*|</style*|</svg*|text:c",
		"<svg><svg/><style>a</style></svg>b":                                                     "<svg*|<svg*|<style*|</style*|</svg*|text:b",
		"<svg><foreignObject><div>a</svg>b":                                                      "<svg*|<foreignobject*|<div+|text:a|</svg|text:b",
		"<a><table><a></a></table><svg><g></svg>w":                                               "<a|<table+|<a|</a|</table+|<svg*|<g*|</svg*|text:w",      // the first link, removed past the table, leaves htmls too
		"<svg><g><foreignObject><div><svg></g><text>x":                                           "<svg*|<g*|<foreignobject*|<div+|<svg*|</g|<text*|text:x", // the g is below the div: </g> is HTML's
		"<svg><g></p><style>a</style>b":                                                          "<svg*|<g*|</p+|<style|</style|text:b",
		"<p><svg><g></p><style>a<i>c</style>":                                                    "<p+|<svg*|<g*|</p+|<style|</style",
		"<math><mrow></br><style>a</style>b":                                                     "<math*|<mrow*|</br+|<style|</style|text:b",
	} {
		if got := foreignTokens(src); got != want {
			t.Errorf("%q:\ngot  %s\nwant %s", src, got, want)
		}
	}
}

// Foreign elements nested past core.MaxDepth are refused, as HTML ones
// are, and at the limit they are read.
func TestForeignContentHasTheDepthLimit(t *testing.T) {
	deep := func(n int) error {
		s := NewScanner([]byte("<svg>"+strings.Repeat("<g>", n)), 1<<20)
		for s.Next() {
		}
		return s.Err()
	}
	if err := deep(core.MaxDepth - 1); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	if err := deep(core.MaxDepth); err == nil || !strings.Contains(err.Error(), "levels deep, the limit") {
		t.Errorf("past the limit: %v", err)
	}
}

// An svg or a math left open at a block's end ends with it: one scanner
// over the blocks gives what a scanner of each gives, so the next block
// reads a style as HTML's, raw and hidden, and its <i> as text of it. The
// planted break: the nest not reset at a block, which leaves the svg open.
func TestForeignContentEndsWithItsBlock(t *testing.T) {
	for _, blocks := range [][]string{
		{"<svg><g>a", "<style>b<i>c</style>d"},
		{"<math><mi>a", "<mglyph><style>b<i>c</style>d"},
		{"<svg><foreignObject>a", "<title>b<i>c</title>d"},
	} {
		src, ends := "", []int(nil)
		for _, b := range blocks {
			src += b
			ends = append(ends, len(src))
		}
		if got, want := strings.Join(joined(src, ends, false), "\n"), strings.Join(alone(src, ends, false), "\n"); got != want {
			t.Errorf("%q:\njoined\n%s\nalone\n%s", blocks, got, want)
		}
	}
}

// An end tag in foreign content costs a constant, however deep the svg
// (ADR 0003): 256 KiB of </x> inside an svg holding 999 elements, each of
// which the tag must not close, take no more than ten times the time of
// the same tags with nothing open, where a walk down the stack for each
// took some 175 times as long (foreign review, finding 1). The bound was
// twice, the shape of a sibling that failed under load at 2.4 times
// (flakes review, finding 3). The planted break: the walk from the top to
// the nearest HTML element.
func TestAForeignEndTagCostsAConstant(t *testing.T) {
	const size = 256 << 10
	deep := "<svg>" + strings.Repeat("<g>", core.MaxDepth-1)
	tags := strings.Repeat("</x>", (size-len(deep))/len("</x>"))
	scan := func(src string) time.Duration {
		fastest := time.Duration(math.MaxInt64)
		for range 3 {
			start := time.Now()
			s := NewScanner([]byte(src), 1<<30)
			for s.Next() {
			}
			if s.Err() != nil {
				t.Fatal(s.Err())
			}
			fastest = min(fastest, time.Since(start))
		}
		return fastest
	}
	alone, inside := scan(strings.Repeat(" ", len(deep))+tags), scan(deep+tags)
	t.Logf("the end tags took %v alone, %v inside the svg", alone, inside)
	if inside > 10*alone+50*time.Millisecond {
		t.Errorf("inside an svg %d deep the end tags took %v, over ten times their %v alone", core.MaxDepth, inside, alone)
	}
}

// The adoption agency's work counts against the token budget, one token
// for each element or list entry it touches (adopt), so a file that has
// the nest rebuild a deep stack for end tag after end tag is refused at the
// token limit, its work linear in max_bytes (ADR 0003). <b><div></b> is
// three tokens and a rebuild of eight: two passes over the b and the div
// and the one entry (3 each), then the div popped and pushed again (2). It
// reads at a budget of 11 tokens and is refused at 10. The cycle is the
// foreign delta review's (finding A): a section, the 13 formatting names
// but a opened once each, 950 divs, then 120 end tags of each name, the
// last opened first; uncharged, 1 MiB of it took 14.66 s on builder, and
// charged it is refused at the token limit. The planted breaks: the work
// not charged; a quarter of it charged.
func TestTheAdoptionAgencysWorkIsCharged(t *testing.T) {
	read := func(src string, budget int64) error {
		s := NewScanner([]byte(src), budget)
		for s.Next() {
		}
		return s.Err()
	}
	if err := read("<b><div></b>", 11); err != nil {
		t.Errorf("at the budget: %v", err)
	}
	var many *TooManyTokensError
	if err := read("<b><div></b>", 10); !errors.As(err, &many) {
		t.Errorf("one token under the budget: %v, want the token limit", err)
	}
	names := []string{"b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u"}
	var cycle strings.Builder
	cycle.WriteString("<section>")
	for _, n := range names {
		cycle.WriteString("<" + n + ">")
	}
	cycle.WriteString(strings.Repeat("<div>", 950))
	for k := len(names) - 1; k >= 0; k-- {
		cycle.WriteString(strings.Repeat("</"+names[k]+">", 120))
	}
	cycle.WriteString("</section>")
	const size = 1 << 20
	src := strings.Repeat(cycle.String(), size/cycle.Len()+1)[:size]
	if err := read(src, MaxTokens(size)); !errors.As(err, &many) {
		t.Errorf("1 MiB of the rebuild cycle: %v, want the token limit", err)
	}
}
