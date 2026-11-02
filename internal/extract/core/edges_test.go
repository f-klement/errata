package core

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

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
// position, which is when the search needs its second, anchored form, and
// atStart only for one that holds \A, whose search before the first match
// cannot start at the start of a line.
func TestLooksBack(t *testing.T) {
	for pattern, want := range map[string][2]bool{
		`v(\d)`: {false, false}, `(?s)a(.)`: {false, false}, `^v(\d)`: {true, false}, `\bv(\d)`: {true, false},
		`\Bv(\d)`: {true, false}, `(?-m)^v(\d)`: {true, true}, `v(?:\A|w)(\d)`: {true, true},
	} {
		if back, atStart := looksBack(pattern); back != want[0] || atStart != want[1] {
			t.Errorf("looksBack(%s) = %v, %v, want %v", pattern, back, atStart, want)
		}
	}
}
