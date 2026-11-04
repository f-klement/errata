package html

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core/htmlparse"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Every value of every target over arbitrary HTML is at a line inside its
// file, the text read is never over maxTextRatio times the file, and a file
// is refused only by a bound (of the tokens, the pieces, the nesting, the
// text read, the values or the search), or for a heading inside a heading
// or the adoption agency only where x/net's tree shows it (refusedFor).
func FuzzHTML(f *testing.F) {
	for _, s := range []string{comments, sections, code, tables, lines, "", "<!--", "<h1>A<h2>A<h3>A</h3>x",
		"<table><td>Port<table><td>a</table>b<tr><td>1", "<pre class=lang-x><code class=language-y>\n&lt;</pre>", "<h1>a\n<div><b>x</div>\n<h2>b</h2>", "<h1><div><b></div>\x00<h2>", "<h1><p\n><code ><table>>web<td>80\r\n<tr>\r\n<td>d2>td>\r\n5432</table>\r\n<h2>",
		"<pre><code b<td>8e>\r\n<table><tr><th>Host<th>Port\r\n<tr><</code>", "<Code><A><pre><A></Code>", "0<p 0><Code 0000000><pre>\n<p></Code>0"} {
		for i := range specs {
			f.Add(s, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, data string, which uint8) {
		s := specs[int(which)%len(specs)]
		s.File = "f"
		e, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		values, err := e.(extractor).FromData([]byte(data))
		if err != nil {
			if !bounded(err) && refusedFor(err, extracttest.BrowserReads(data, htmlparse.LineTag, true)) == "" {
				t.Fatalf("%+v: refused: %v", s, err)
			}
			return
		}
		file := bytes.ReplaceAll([]byte(data), []byte("\r\n"), []byte("\n"))
		lines := bytes.Count(file, []byte("\n")) + 1
		for _, v := range values {
			if v.At.Line < 1 || v.At.Line > lines {
				t.Fatalf("%+v: %q at line %d of %d", s, v.Text, v.At.Line, lines)
			}
		}
	})
}

// bounded reports whether err is a refusal by a bound of the scan, the
// text, the nesting, the text read, or the search.
func bounded(err error) bool {
	for _, b := range []string{"tokens, the limit", "pieces of the file, the limit", "levels deep, the limit",
		"times the file's size", "values, the limit", "matching took more than"} {
		if strings.Contains(err.Error(), b) {
			return true
		}
	}
	return false
}
