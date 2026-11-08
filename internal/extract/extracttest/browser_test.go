package extracttest

import (
	"strings"
	"testing"

	xhtml "golang.org/x/net/html"
)

// TestRebuiltIn holds the rebuild check to the adoption agency's mark: a
// clone of the refused name as the first child of the block it rebuilt,
// around or inside a pre or a heading. A clone the reconstruction opened
// after a comment in a heading is no such mark. In a page with svg or math
// a tag counts only where the tree builder made an HTML element of it, so
// a b first in an svg title, with no rebuild, is none either (foreign
// review, round 2, finding 5; the planted break: any shape counted there).
func TestRebuiltIn(t *testing.T) {
	for src, want := range map[string]bool{
		"<b><pre>x</b>y</pre>":                                        true,
		"<b><div><h1>x</b>y</h1></div>":                               true,
		"<div><b></div><h1><!--c-->x</h1>":                            false,
		"<b><pre>x</pre></b>":                                         false, // no clone
		"<code><b><pre><b></code>":                                    true,  // inside the code a later rebuild put first
		"<h2><svg><title><b>x</b></title></svg></h2>":                 false, // the b is the title's first child: no rebuild
		"<h2><svg><title><textarea></title><b>x</b></textarea></svg>": false, // the textarea is HTML's and raw: no b at all
		"<svg><b><pre>x</b>y</pre></svg>":                             true,  // a rebuild after the svg's breakout
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

// XNetDropsTemplate holds where x/net v0.62.0 makes no element of a
// template start tag: below an svg's or a math's integration point, a
// textarea the tokenizer alone reads raw before it included, and not a
// template outside svg and math, nor one of svg's (foreign delta review,
// template exclusion). The planted break: no source counted.
func TestXNetDropsTemplate(t *testing.T) {
	for src, want := range map[string]bool{
		"<svg><title><template>t</template></title></svg><pre>x</pre>":           true,
		"<math><mi><template>t</template></mi></math>":                           true,
		"<svg><foreignObject><div><template>t</template></div></foreignObject>":  true,
		"<svg><textarea>y<title><pre><template>t</template></pre></title></svg>": true,
		"<template>t</template><svg><g></g></svg><pre>x</pre>":                   false,
		"<svg><template>t</template></svg>":                                      false,
		"<pre><template>t</template></pre>":                                      false,
	} {
		if got := XNetDropsTemplate(src); got != want {
			t.Errorf("%q: %v, want %v", src, got, want)
		}
	}
}

// XNetFoldsName holds where x/net v0.62.0 matches an end tag in foreign
// content by Unicode case folding (strings.EqualFold), where the standard
// folds ASCII alone: a tag name with a byte of 0x80 or above, valid UTF-8
// or not, which the switch's first child then is, or is not, in x/net's
// tree (flakes review, finding 1), wherever the name stands: after an
// element x/net's tokenizer alone reads as raw text, as the tree builder
// does not in svg. A byte of 0x80 or above in text or in an attribute
// matches no name, and stays compared. The planted breaks: no source
// counted, which the ſ fails; x/net's tokenizer alone in place of the
// scan, which the xmp fails.
func TestXNetFoldsName(t *testing.T) {
	for src, want := range map[string]bool{
		"<h1><svg><switch><te\u017ft ></test><text>0":  true, // ſ folds to s
		"<h1><svg><switch><te\u212at ></tekt><text>0":  true, // the Kelvin sign folds to k
		"<h1><svg><switch><te\x80t ></te\x85t><text>0": true, // both U+FFFD to x/net
		// In svg an xmp, a textarea, a plaintext, a noscript or an iframe
		// leaves the tags after it tags (flakes delta review 1, finding 1).
		"<h1><svg><xmp><switch><te\u017ft ></test><text>0":       true,
		"<h1><svg><textarea><switch><te\u017ft ></test><text>0":  true,
		"<h1><svg><plaintext><switch><te\u017ft ></test><text>0": true,
		"<h1><svg><noscript><switch><te\u017ft ></test><text>0":  true,
		"<h1><svg><iframe><switch><te\u017ft ></test><text>0":    true,
		// A < inside a name is the name's (flakes delta review 2, finding 1).
		"<h1><svg><switch><te<\u017ft ></te<st><text>0": true,
		"<a<a<a<a\u017f": true,
		// An HTML textarea's text is no comment, whatever it holds.
		"<textarea><!--</textarea><h1><svg><switch><te\u017ft ></test><text>0": true,
		"<h1>caf\xe9</h1><h2><b>x</h2>":                                        false,
		"<h1>caf\u00e9</h1>":                                                   false,
		"<p title=\"\xe9\">x</p>":                                              false,
		"<h1><svg><switch><teat ></tebt><text>0":                               false,
	} {
		if got := XNetFoldsName(src); got != want {
			t.Errorf("%q: %v, want %v", src, got, want)
		}
		if c := BrowserComparable(src); c == want {
			t.Errorf("%q: comparable %v; a name x/net folds: %v", src, c, want)
		}
	}
}
