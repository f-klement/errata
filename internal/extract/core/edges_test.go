package core

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// cut keeps a text of exactly n bytes whole, moves back to the start of a
// rune, and moves back at most three bytes, as far as a rune can need, even
// in bytes that are not UTF-8.
func TestCutEdges(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{"abc", 3, "abc"},
		{"abcd", 3, "abc..."},
		{"aé", 2, "a..."},
		{"a😀", 4, "a..."},
		{strings.Repeat("\x80", 5), 4, "\x80..."},
		{strings.Repeat("\x80", 8), 6, "\x80\x80\x80..."},
	} {
		if got := cut(c.s, c.n); got != c.want {
			t.Errorf("cut(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

// clipError returns a short error itself, so errors.Is and a wrapped cause
// still work, and cuts only a message over 4*maxQuote bytes.
func TestClipErrorEdges(t *testing.T) {
	short := errors.New(strings.Repeat("e", 4*maxQuote))
	if got := ClipError(short); !errors.Is(got, short) {
		t.Fatalf("clipError of %d bytes returned a new error %q", len(short.Error()), got)
	}
	long := errors.New(strings.Repeat("e", 4*maxQuote+1))
	if got := ClipError(long).Error(); got != strings.Repeat("e", 4*maxQuote)+"..." {
		t.Fatalf("clipError of %d bytes = %d bytes", len(long.Error()), len(got))
	}
}

// A key after a dot starts with a letter of either case, _ or a non-ASCII
// character; the characters next to the letters in ASCII do not start one.
func TestPathNameFirstEdges(t *testing.T) {
	for _, r := range "azAZ_é" {
		if !isNameFirst(r) {
			t.Errorf("%q does not start a name", r)
		}
	}
	for _, r := range "`{@[0-$" {
		if isNameFirst(r) {
			t.Errorf("%q starts a name", r)
		}
	}
	if !isNameFirst(utf8.RuneSelf) || isNameFirst(utf8.RuneSelf-1) {
		t.Error("the first non-ASCII rune is the edge")
	}
}

// A \u escape needs four hex digits even at the end of the path, and the
// low surrogate after a high one runs from DC00 to DFFF, both ends included.
func TestPathUnicodeEscapeEdges(t *testing.T) {
	for in, want := range map[string]string{
		`['\uD800\uDC00']`: "\U00010000",
		`['\uDBFF\uDFFF']`: "\U0010FFFF",
	} {
		p, err := parsePath(in)
		if err != nil || len(p.sels) != 1 || p.sels[0].kind != selName || p.sels[0].name != want {
			t.Errorf("%s: %+v, %v", in, p.sels, err)
		}
	}
	for in, want := range map[string]string{
		`['\uD800\uDBFF']`: `\uD800 is a high surrogate without a low one after it`,
		`['\uD800\uE000']`: `\uD800 is a high surrogate without a low one after it`,
		`['\u0041`:         "unterminated quoted key",
		`['\u00`:           `\u takes four hex digits`,
		`['\uD800\uDC`:     `\uD800 is a high surrogate without a low one after it`,
	} {
		if _, err := parsePath(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want %q", in, err, want)
		}
	}
}

// looksBack is true only for a pattern that reads the text before a
// position, which is when the search needs its second, anchored form.
func TestLooksBack(t *testing.T) {
	for pattern, want := range map[string]bool{
		`v(\d)`: false, `(?s)a(.)`: false, `^v(\d)`: true, `\bv(\d)`: true, `\Bv(\d)`: true, `(?-m)^v(\d)`: true,
	} {
		if got := looksBack(pattern); got != want {
			t.Errorf("looksBack(%s) = %v, want %v", pattern, got, want)
		}
	}
}
