package text

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/mdparse"
)

// textSamples are documents of every kind of block and inline node.
var textSamples = []string{
	withCode,
	"CI runs on node\n`builder` and *em* **strong** _u_ [link *text*](http://x) <http://auto.example> foo  \nbar\\\nbaz &amp; \\* x<br>y\n",
	"- item one\n  wraps\n- `code\nspan`\n\n> quote\nlazy\n\n1. a\n2. b\n\n   c\n",
	"<!-- c -->\n<?p?>\n<!X>\n<![CDATA[x]]>\n<script>\nx\n</script>\n<div\n>\n\nend\n",
}

// The prose is never over maxProseRatio times the file, and every value of
// the text extractor is in the prose, at a line of the file. FuzzParse in
// core/mdparse holds the parse and the prose to goldmark's and to the file;
// the third argument is unused, and kept so that the corpus under
// testdata/fuzz/FuzzText still reads.
func FuzzText(f *testing.F) {
	for i, s := range textSamples {
		f.Add(s, false, uint16(i*7))
	}
	f.Add("---\na\n---\n- \tx\n\n\t\t\tcode\n", true, uint16(3))
	f.Add("> ```\n> \tcode\n>\n\t> x\n", true, uint16(0))
	f.Add("a\r\nb\r\n\r\n    c\r\n", true, uint16(1))
	f.Add("\xef\xbb\xbf---\na\n...\n<!-- x --> y <b>z</b>\n", false, uint16(2))
	// Seeds that cross each bound: definitions, continuations under the
	// budget, depth at a block's opening and after the parse, runs of * and
	// _, runs that close nothing under the budget, a line of HTML, and folds
	// of case that change a rune's length.
	f.Add(strings.Repeat("[x]: y\n", 3163), false, uint16(500))
	f.Add(strings.Repeat("> > > a\n", 11), false, uint16(4))
	f.Add(strings.Repeat("> ", 1000)+"a\n", false, uint16(9))
	f.Add("x\n"+strings.Repeat("*a ", 999)+"b"+strings.Repeat(" a*", 999)+"\n", false, uint16(9))
	f.Add(strings.Repeat("_a ", 5001)+"\n", false, uint16(9))
	f.Add("x\n"+strings.Repeat("*"+strings.Repeat("a", 98)+" ", 4)+"\n", false, uint16(9))
	f.Add("<pre>\n"+strings.Repeat("b", mdparse.MaxHTMLLine+1)+"\n", false, uint16(9))
	f.Add("<!-- ȺȺ -->x\n<!-- \u212a\u212a -->y <i>z</i>\n<SCRIPT>\xff</script> w\n", false, uint16(5))
	f.Add(strings.Repeat("a\t", 40), false, uint16(1))
	f.Add("> <div><p>a\n> b</p>\n> <!-- c --><script>d</script>\n\n<pre>\n x\n\ty</pre> z\n", false, uint16(1))
	f.Fuzz(func(t *testing.T, data string, code bool, _ uint16) {
		doc, err := mdparse.Parse("f", []byte(data), core.DefaultMaxBytes, time.Minute)
		if err != nil {
			return // FuzzParse holds each refusal to a bound
		}
		p, err := doc.Prose(code)
		if err != nil {
			return
		}
		if len(p.Text) > maxProseRatio*len(data) {
			t.Fatalf("prose of %d bytes from %d", len(p.Text), len(data))
		}
		e, err := Build(Spec{File: "f", Pattern: `(\S+)`, Code: code})
		if err != nil {
			t.Fatal(err)
		}
		values, err := e.(textExtractor).FromData([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Count(doc.Src, []byte("\n")) + 1
		for _, v := range values {
			if v.At.Line < 1 || v.At.Line > lines || !bytes.Contains(p.Text, []byte(v.Text)) {
				t.Fatalf("value %q at line %d of %d", v.Text, v.At.Line, lines)
			}
		}
	})
}
