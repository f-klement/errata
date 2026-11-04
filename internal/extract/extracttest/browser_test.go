package extracttest

import (
	"strings"
	"testing"

	xhtml "golang.org/x/net/html"
)

// TestRebuiltIn holds the rebuild check to the adoption agency's mark: a
// clone of the refused name as the first child of the block it rebuilt,
// around or inside a pre or a heading. A clone the reconstruction opened
// after a comment in a heading is no such mark.
func TestRebuiltIn(t *testing.T) {
	for src, want := range map[string]bool{
		"<b><pre>x</b>y</pre>":             true,
		"<b><div><h1>x</b>y</h1></div>":    true,
		"<div><b></div><h1><!--c-->x</h1>": false,
		"<b><pre>x</pre></b>":              false, // no clone
		"<code><b><pre><b></code>":         true,  // inside the code a later rebuild put first
	} {
		doc, err := xhtml.Parse(strings.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		if got := rebuiltIn(src, doc)["b"]; got != want {
			t.Errorf("%q: rebuilt b %v, want %v", src, got, want)
		}
	}
}
