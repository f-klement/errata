package htmlparse

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// tokens is each token a scan of src gives, as "kind:bytes", a tag as
// "<name" or "</name", with the scan's error last if any.
func tokens(src string, max int64) []string {
	s := NewScanner([]byte(src), max)
	var out []string
	for s.Next() {
		switch s.Kind() {
		case StartTag:
			out = append(out, "<"+s.Name())
		case EndTag:
			out = append(out, "</"+s.Name())
		case Text:
			out = append(out, "text:"+string(s.Bytes()))
		case Pre:
			out = append(out, "pre:"+string(s.Bytes()))
		case Comment:
			out = append(out, "comment:"+string(s.Bytes()))
		}
	}
	if s.Err() != nil {
		out = append(out, "error:"+s.Err().Error())
	}
	return out
}

// The scanner gives the tags, comments and text a browser shows, and
// leaves out the text of a script, a style, a title and the other hidden
// elements, everything inside a template however nested, a doctype and the
// bogus comments. Text in a pre or a textarea is Pre, without the line
// feed that follows the start tag. The planted breaks: the hidden text
// given (script's x), a template's end taken at its first </template>
// (inner's after), the line feed after <pre> kept.
func TestScannerGivesWhatABrowserShows(t *testing.T) {
	src := "<!doctype html><P Class=x>a &amp; b<script>x<y</script><style>s</style>" +
		"<template><p>t<template>u</template>v</template><title>w</title>" +
		"<pre>\n p\n</pre><textarea>\nq</textarea><!-- c --><?pi?><!x></ y></>z"
	if got := strings.Join(tokens("</template><template>x</template>y", 100), "|"); got != "text:y" {
		t.Errorf("a stray </template>: got %q", got) // it opened no template, so it closes none
	}
	got := strings.Join(tokens(src, 100), "|")
	want := "<p|text:a &amp; b|<script|</script|<style|</style|<title|</title|<pre|pre: p\n|</pre|<textarea|pre:q|</textarea|comment:<!-- c -->|text:z"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// Every token's offsets are its bytes in the source, from the tokenizer's
// raw bytes, so a token's line is the file's; Start moves past only the
// line feed after <pre>. Class reads a start tag's class, once or twice.
func TestScannerOffsetsAndClass(t *testing.T) {
	src := "a\n<pre class='lang-go x'>\nb</pre>\n<code id=1 class=\"y\"><br>"
	s := NewScanner([]byte(src), 100)
	var got []string
	for s.Next() {
		got = append(got, fmt.Sprintf("%d-%d:%s", s.Start(), s.End(), s.Bytes()))
		if s.Kind() == StartTag {
			got = append(got, "class="+s.Class()+"="+s.Class())
		}
	}
	want := []string{"0-2:a\n", "2-25:<pre class='lang-go x'>", "class=lang-go x=lang-go x", "26-27:b", "27-33:</pre>", "33-34:\n",
		"34-55:<code id=1 class=\"y\">", "class=y=y", "55-59:<br>", "class==", "59-59:"}
	got = append(got, fmt.Sprintf("%d-%d:", s.End(), len(src)))
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if s.InPre() {
		t.Error("in a pre after </pre>")
	}
}

// A comment's text runs from its <!-- to its --> or --!>; <!--> and
// <!---> are empty, and one with no end is none.
func TestCommentText(t *testing.T) {
	for src, want := range map[string]string{
		"<!-- a -->": " a ", "<!--a--!>": "a", "<!----!>": "", "<!-->": "", "<!--->": "", "<!---->": "", "<!-- a": "none", "<!-- a -- >": "none",
	} {
		s := NewScanner([]byte(src), 10)
		if !s.Next() || s.Kind() != Comment {
			t.Errorf("%q: no comment", src)
			continue
		}
		got := "none"
		if start, end, ok := s.CommentText(); ok {
			got = src[start:end]
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

// The scan reads at most maxTokens tokens, and refuses at the start of the
// one after, which a reader turns into its line. The planted break: no
// count, which reads every token.
func TestScannerRefusesTokensPastItsLimit(t *testing.T) {
	if got := tokens("<a>x</a>", 3); len(got) != 3 {
		t.Errorf("at the limit: %q", got)
	}
	s := NewScanner([]byte("<a>x</a>y"), 3)
	for s.Next() {
	}
	var many *TooManyTokensError
	if !errors.As(s.Err(), &many) || many.At != 8 || many.Max != 3 {
		t.Fatalf("got %v", s.Err())
	}
	if want := "the HTML holds more than 3 tokens, the limit, one for every 4 bytes of max_bytes; set max_bytes: higher to read a file this broken up"; s.Err().Error() != want {
		t.Errorf("got %q", s.Err())
	}
	if MaxTokens(0) != 1 || MaxTokens(40) != 11 {
		t.Errorf("MaxTokens: %d %d", MaxTokens(0), MaxTokens(40))
	}
}

// A line element breaks a line, in either tag; an inline one, a custom
// element named like one, and text do not. A listing's text is Pre, and
// the text after its end is not.
func TestBreaksLine(t *testing.T) {
	var got []string
	s := NewScanner([]byte("<p>a<b>c</b><h1-x>d</P><BR/>"), 100)
	for s.Next() {
		if s.BreaksLine() {
			got = append(got, s.Name())
		}
	}
	if strings.Join(got, " ") != "p p br" {
		t.Errorf("got %q", got)
	}
	got = nil
	for s := NewScanner([]byte("<div></div><listing>a</listing>b"), 100); s.Next(); {
		if s.BreaksLine() {
			got = append(got, s.Name())
		}
		if s.Kind() == Text || s.Kind() == Pre {
			got = append(got, strconv.FormatBool(s.Kind() == Pre))
		}
	}
	if strings.Join(got, " ") != "div div true false" {
		t.Errorf("got %q", got)
	}
}

// Unescape gives each character reference decoded, at the offset of its
// &, and every other byte at its own offset, legacy references without a
// semicolon too; a & that starts no reference is text.
func TestUnescape(t *testing.T) {
	unescape := func(src string) string {
		var got []string
		Unescape([]byte(src), 10, func(b []byte, at int, wrote bool) {
			if len(b) > 0 {
				got = append(got, fmt.Sprintf("%s@%d/%t", b, at, wrote))
			}
		})
		return strings.Join(got, "|")
	}
	for src, want := range map[string]string{
		"a&amp;b &lt;&gt;&copy &x &#65;&nGt;z": "a@10/false|&@11/true|b @16/false|<@18/true|>@22/true|©@26/true| @31/false|&x @32/false|A@35/true|≫⃒@40/true|z@45/false",
		"&&amp;":                               "&@10/false|&@11/true", // a & before a & starts no reference
		"&#x30":                                "0@14/false",           // its text ends as its name does, so that byte is copied
		"plain":                                "plain@10/false",
	} {
		if got := unescape(src); got != want {
			t.Errorf("%q: got  %q\nwant %q", src, got, want)
		}
	}
}
