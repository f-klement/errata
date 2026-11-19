package htmlparse

import (
	"slices"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// pres is the text of each pre a scan of src gives, and whether a
// frameset took the page; in Markdown blocks when block is true.
func pres(src string, block bool) ([]string, bool) {
	s := NewScanner([]byte(src), 1<<30)
	if block {
		s.Blocks([]int{len(src)})
	}
	var out []string
	for s.Next() {
		if s.Kind() == Pre && s.InPre() {
			out = append(out, string(s.Bytes()))
		}
	}
	return out, s.Framed()
}

// A frameset takes a page while the frameset-ok flag holds, and then the
// scan gives nothing after it but comments; in a Markdown file's HTML
// block, inside a site's body, it never does. Before the body starts it
// takes the page whatever the flag, and text other than blanks clears the
// flag, NULs and blanks alone do not. The planted breaks: a <title> that
// starts the body, so that the template after it clears the flag in the
// body and the third row reads a; NULs counted as text, which reads the
// NUL row as a; the Markdown block left to take a frameset, which reads
// its row as nothing.
func TestFramesetTakesThePage(t *testing.T) {
	for _, row := range []struct {
		src    string
		block  bool
		pres   []string
		framed bool
	}{
		{"<frameset><pre>a", false, nil, true},
		{"<frameset><pre>a", true, []string{"a"}, false},
		{"<title>t</title><template></template><frameset><pre>a", false, nil, true},
		{"<template></template><div><frameset><pre>a", false, []string{"a"}, false},
		{"<div>\x00 <frameset><pre>a", false, nil, true},
		{"<div>\x00x<frameset><pre>a", false, []string{"a"}, false},
		{"<svg><![CDATA[ ]]></svg><frameset><pre>a", false, nil, true},
		{"<svg><![CDATA[c]]></svg><frameset><pre>a", false, []string{"a"}, false},
		{"<body><frameset><pre>a", false, []string{"a"}, false},
		{"<frameset><!--c--><pre>a", false, nil, true},
	} {
		got, framed := pres(string(core.LineFeeds([]byte(row.src))), row.block)
		if !slices.Equal(got, row.pres) || framed != row.framed {
			t.Errorf("%q, block %t: pres %q, framed %t; want %q, %t", row.src, row.block, got, framed, row.pres, row.framed)
		}
	}
}

// A comment after a frameset that took the page is the page's, as in a
// browser's tree; the frameset's own tags and text are not given.
func TestFramesetKeepsComments(t *testing.T) {
	s := NewScanner([]byte("<frameset>x<!--c--><frame></frameset>"), 1<<30)
	var kinds []Kind
	for s.Next() {
		kinds = append(kinds, s.Kind())
	}
	if !slices.Equal(kinds, []Kind{StartTag, Comment}) {
		t.Errorf("kinds %v, want the frameset's start tag and the comment", kinds)
	}
}

// Where a template in the head clears the flag, the standard ignores a
// frameset in the body and x/net, which sets the flag back at the body it
// implies, lets it take the page: the scan says so (StandardOnly), and in
// x/net's way reads as x/net. Before the body, both let it take the page.
// The planted break: x/net's flag never set back, which leaves the first
// row unmarked.
func TestFramesetXNetWay(t *testing.T) {
	for _, row := range []struct {
		src          string
		standardOnly bool
	}{
		{"<template></template><p><frameset><pre>a", true},
		{"<template></template><frameset><pre>a", false},
		{"<p>x<frameset><pre>a", false},
	} {
		s := NewScanner([]byte(row.src), 1<<30)
		for s.Next() {
		}
		x := NewScanner([]byte(row.src), 1<<30)
		x.FollowXNetAdoption()
		for x.Next() {
		}
		if s.StandardOnly() != row.standardOnly || row.standardOnly && (s.Framed() || !x.Framed()) {
			t.Errorf("%q: standard only %t, framed %t, in x/net's way %t", row.src, s.StandardOnly(), s.Framed(), x.Framed())
		}
	}
}
