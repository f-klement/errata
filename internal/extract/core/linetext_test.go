package core

import "testing"

// LineText is the line asked for as the file wrote it, trimmed of spaces,
// tabs and its line end, past a byte order mark, with LF, CRLF and a lone
// CR each ending a line. Planted breaks: the tab left out of the trim; the
// byte order mark kept; the count of lines one short.
func TestLineText(t *testing.T) {
	for _, c := range []struct {
		data string
		n    int
		want string
	}{
		{"a = 1\n\tb\t= { x = true }  # c \r\nd\n", 2, "b\t= { x = true }  # c"},
		{"\xef\xbb\xbfa.b = true\n", 1, "a.b = true"},
		{"a\rb\r\nc\nd", 4, "d"},
		{"a\rb\r\nc\nd", 2, "b"},
		{"only", 1, "only"},
	} {
		if got := LineText([]byte(c.data), c.n); got != c.want {
			t.Errorf("LineText(%q, %d) = %q, want %q", c.data, c.n, got, c.want)
		}
	}
}
