package clip

import (
	"strings"
	"testing"
)

// Cut keeps a text of exactly n bytes whole, moves back to the start of a
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
		if got := Cut(c.s, c.n); got != c.want {
			t.Errorf("Cut(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

// Quote quotes a text of MaxQuote bytes whole, and a longer one cut, with
// the ... after the closing quote. Planted break: the comparison with the
// cut text dropped, which quoted the ... inside.
func TestQuote(t *testing.T) {
	whole := strings.Repeat("a", MaxQuote)
	if got := Quote(whole); got != `"`+whole+`"` {
		t.Errorf("%d bytes: %s", MaxQuote, got)
	}
	if got := Quote(whole + "b\n"); got != `"`+whole+`"...` {
		t.Errorf("%d bytes: %s", MaxQuote+2, got)
	}
	if got := Text(whole + "b"); got != whole+"..." {
		t.Errorf("Text of %d bytes: %s", MaxQuote+1, got)
	}
}
