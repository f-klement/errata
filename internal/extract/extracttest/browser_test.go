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
