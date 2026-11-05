package htmlparse

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// scanned is what a scan gives, one token a line: its block, kind, offsets
// and name, whether a pre and a heading are open, a comment's text, and the
// refusal last. Offsets are moved by shift, and block, when not -1, is the
// block every token is in. Text that runs on from the text before it in
// the same block, as the tokenizer may split it at a < it reads further
// on, is one line: every reader joins it.
func scanned(s *Scanner, shift, block int) []string {
	var out []string
	var last struct {
		block int
		kind  Kind
		end   int
	}
	last.end = -1
	for s.Next() {
		b := s.Block()
		if block >= 0 {
			b = block
		}
		if (s.Kind() == Text || s.Kind() == Pre) && b == last.block && s.Kind() == last.kind && s.Start()+shift == last.end && len(out) > 0 {
			prev := out[len(out)-1]
			out[len(out)-1] = strings.Replace(prev, fmt.Sprintf("-%d ", last.end), fmt.Sprintf("-%d ", s.End()+shift), 1)
			last.end = s.End() + shift
			continue
		}
		last.block, last.kind, last.end = b, s.Kind(), s.End()+shift
		line := fmt.Sprintf("%d %d %d-%d %q %t %t", b, s.Kind(), s.Start()+shift, s.End()+shift, s.Name(), s.InPre(), s.HeadingOpen())
		if s.Kind() == Comment {
			start, end, ok := s.CommentText()
			if !ok {
				continue // an unclosed comment is none, alone or joined
			}
			line += fmt.Sprintf(" %d-%d", start+shift, end+shift)
		}
		out = append(out, line)
	}
	if s.Err() != nil {
		out = append(out, "error: "+s.Err().Error())
	}
	return out
}

// alone scans each block of src, cut at ends, with a scanner of its own,
// shifting the offsets to src's.
func alone(src string, ends []int, headings bool) []string {
	var out []string
	from := 0
	for b, end := range ends {
		s := NewScanner([]byte(src[from:end]), 1<<30)
		s.Blocks([]int{end - from}) // a block, with elements perhaps open before it
		if headings {
			s.Headings()
		}
		out = append(out, scanned(s, from, b)...)
		if s.Err() != nil {
			break // a scanner of the blocks stops at the first refusal too
		}
		from = end
	}
	return out
}

// joined scans src's blocks with one scanner.
func joined(src string, ends []int, headings bool) []string {
	s := NewScanner([]byte(src), 1<<30)
	s.Blocks(ends)
	if headings {
		s.Headings()
	}
	return scanned(s, 0, -1)
}

// One scanner over several blocks gives each block what a scanner of its
// own gives it: text the tokenizer reads past a block's end is split there,
// and a tag, a comment, a CDATA or a raw element's text left open at a
// block's end starts a fresh tokenizer at the next. The planted breaks: no
// split (the text joined across blocks), no restart after an open tag (the
// next block's first tag eaten).
func TestBlocksReadAsAlone(t *testing.T) {
	for _, blocks := range [][]string{
		{"<div>a\n", "  <p>b</p>\n"},
		{"<div a=\"x\n", "<p>b</p>\n"},
		{"<!-- open\n", "<p>b</p>"},
		{"<textarea>\na\n", "<p>b</p>"},
		{"<script>a\n", "<p>b</p>"},
		{"<svg><![CDATA[a\n", "<p>b</p>"},
		{"<plaintext>a\n", "<p>b</p>", "<p>c"},
		{"<pre>\n", "\nb"},
		{"<pre><div>a\n", "</div>b"},
		{"<h1>a\n", "</div>b"},
		{"<template><p>a\n", "</template>b"},
		{"x", "y", "z"},
		{"<script>x</scr", "ipt><p>b"}, // the end tag cut: the rest of the block is the script's (FuzzScan)
		{"<textarea>x</text", "area>b"},
		{"<title></ti", "tle>b"},
		{"<template><script>x\n", "<p>b"},  // a script in a template is read raw too (FuzzScan)
		{"<textarea>\nx</textarea", "b c"}, // its end tag cut where alone it is one (FuzzScan)
		{"<textarea>x</textarea", "><p>b"},
		{"<p>a<script>x</scr", "ipt>b"},
		{"x<textarea>a</textare", "a>"}, // the next block completes its end tag (FuzzScan)
		{"x<textarea>a</textarea", "b>"},
		{"x<textarea>a</textarea", " >"},
		{"x<textarea>a</textarea", "\t"}, // the end tag open at the end of the HTML (FuzzScan)
		{"x<textarea>a</textarea", "/>"},
		{"x<textarea>a</textar", "ea>"},
		{"x<textarea>a<", "/textarea>"},
		{"a<", "b", "c"}, // the tokenizer splits a's text at the <, which alone ends it (FuzzScan)
	} {
		src, ends := strings.Join(blocks, ""), []int{}
		for i := range blocks {
			ends = append(ends, len(strings.Join(blocks[:i+1], "")))
		}
		for _, headings := range []bool{false, true} {
			if got, want := strings.Join(joined(src, ends, headings), "\n"), strings.Join(alone(src, ends, headings), "\n"); got != want {
				t.Errorf("%q headings %t:\njoined\n%s\nalone\n%s", blocks, headings, got, want)
			}
		}
	}
}

// A fresh tokenizer counts restartTokens against the limit, so a file of
// blocks that each end inside a tag is refused before it allocates a
// buffer for each, while blocks that end in text, split where they end,
// need no fresh one. The planted breaks: restarts not counted; a restart
// for text past a block's end (ten blocks refused at 40 tokens).
func TestRestartsCount(t *testing.T) {
	// Each block starts with a blank, so the text that ends the block
	// before runs into it.
	text := NewScanner([]byte(strings.Repeat(" <p>a\n", 10)), 40)
	var ends []int
	for i := range 10 {
		ends = append(ends, 6*(i+1))
	}
	text.Blocks(ends)
	for text.Next() {
	}
	if text.Err() != nil {
		t.Errorf("blocks ending in text: %v", text.Err())
	}
	src := strings.Repeat("<div\n", 3)
	s := NewScanner([]byte(src), 2*restartTokens)
	s.Blocks([]int{5, 10, 15})
	for s.Next() {
	}
	if s.Err() == nil || !strings.Contains(s.Err().Error(), "tokens, the limit") {
		t.Errorf("got %v", s.Err())
	}
}

// pre and heading say where the pre and the heading of html end, as a
// browser ends them: "" while one is open after the last token, or the
// refusal.
func ends(src string) (pre, heading string) {
	s := NewScanner([]byte(src), 1<<20)
	s.Headings()
	var lastPre, lastHead bool
	for s.Next() {
		lastPre, lastHead = s.InPre(), s.HeadingOpen()
	}
	if s.Err() != nil {
		return "refused", "refused"
	}
	return strconv.FormatBool(lastPre), strconv.FormatBool(lastHead)
}

// Inside a pre or a heading the scanner follows a browser's tree builder:
// an end tag closes the nearest element of its name in scope and what is
// above it, an element of no special kind never closes a special one, a
// scope's end hides what lies outside, the start of a p's closer, a list
// item or a table closes what a browser closes, and a heading's start ends
// a heading that is the current element. An end tag that may close an
// element opened before, which the scanner does not follow, is refused.
// The planted breaks: an end tag that is not the top's ignored (div in
// pre), the scope ignored (</div> past a td), a refusal for an element of
// no special kind (</span>).
func TestNestFollowsTheBrowser(t *testing.T) {
	for src, want := range map[string]string{
		"<pre>a":                            "true false",
		"<pre><div>a</div>b":                "true false",
		"<pre><b>a</pre>b":                  "false false",
		"<pre><div>a</pre>b":                "false false",
		"<pre>a</span>b":                    "true false",
		"<pre><table><td>a</div>b":          "true false",
		"<pre><span><div>a</span>b":         "true false",
		"<pre><span>a</span>b":              "true false",
		"<pre><p>a<div>b</p>c":              "true false",
		"<pre><p>a</p></p>c":                "true false",
		"<pre><ul><li>a<li>b</ul>c":         "true false",
		"<pre><dl><dt>a<dd>b</dl>c":         "true false",
		"<pre><table><tr><table>a</table>":  "true false",
		"<pre>a</div>":                      "true false",
		"<pre>a</li>":                       "true false",
		"<pre>a</listing>":                  "true false",
		"<pre>a</p>b":                       "true false",
		"<pre>a</br>b":                      "true false",
		"<pre>a</textarea></script></img>b": "true false",
		"<h2>a</h3>b":                       "false false",
		"<h2>a<h3>b":                        "false true",
		"<h2>a<b><h3>b":                     "refused refused",
		"<h2>a</div>":                       "false true",
		"<h2><pre>a</h2>b":                  "false false",
		"<h2><table><td>a</h2>b":            "false true",
		"<h2>a<br>b<img>c<hr></h2>d":        "false false",
		"<h2><svg><path/></svg>a</h2>":      "false false",
		"<h2><svg><path/>a</h2>":            "false false",
	} {
		pre, head := ends(src)
		if got := pre + " " + head; got != want {
			t.Errorf("%q: got %s, want %s", src, got, want)
		}
	}
	// Elements nested past core.MaxDepth inside a pre are refused.
	s := NewScanner([]byte("<pre>"+strings.Repeat("<b>", 1000)), 1<<20)
	for s.Next() {
	}
	if s.Err() == nil || !strings.Contains(s.Err().Error(), "levels deep, the limit") {
		t.Errorf("deep: %v", s.Err())
	}
	s = NewScanner([]byte("<pre>"+strings.Repeat("<b>", 999)), 1<<20)
	for s.Next() {
	}
	if s.Err() != nil {
		t.Errorf("at the depth limit: %v", s.Err())
	}
}

// CDATA reads as its text inside svg and math, and a bogus comment
// outside, as in a browser; an HTML element that breaks out of svg ends
// it. The planted break: CDATA allowed everywhere.
func TestCDATAInForeignContent(t *testing.T) {
	for src, want := range map[string]string{
		"<svg><text><![CDATA[a > b]]></text></svg>":    "a > b", // in a text element, which draws its text
		"<math><![CDATA[x]]>":                          "x",
		"<![CDATA[a > b]]>c":                           " b]]>c", // a bogus comment, to the first >
		"<svg><p><![CDATA[a]]>b":                       "b",
		"<svg></svg><![CDATA[a]]>b":                    "b",
		"<svg><text><![CDATA[open":                     "open",
		"<svg><text><svg/><![CDATA[a]]></text></svg>b": "ab",
	} {
		var got []string
		for s := NewScanner([]byte(src), 100); s.Next(); {
			if s.Kind() == Text {
				got = append(got, string(s.Bytes()))
			}
		}
		if strings.Join(got, "") != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}
