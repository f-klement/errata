package extracttest

import "testing"

// RawLine is the line asked for as the file wrote it, its line end taken
// off and nothing else: the blanks at both ends and a byte order mark stay,
// and LF, CRLF and a lone CR each end a line. Planted breaks: the line
// trimmed of blanks, as core.LineText trims it; the CR of a CRLF kept; the
// count of lines one short.
func TestRawLine(t *testing.T) {
	for _, c := range []struct {
		data string
		n    int
		want string
	}{
		{"a = 1\n\tb\t= x  # c \r\nd\n", 2, "\tb\t= x  # c "},
		{"\xef\xbb\xbfa.b = true\n", 1, "\xef\xbb\xbfa.b = true"},
		{"root:x:0:a,b\t\nz", 1, "root:x:0:a,b\t"},
		{"a\rb\r\nc\nd", 4, "d"},
		{"a\rb\r\nc\nd", 2, "b"},
		{"a\rb\r\nc\nd", 1, "a"},
		{"a\n\n", 2, ""},
		{"only ", 1, "only "},
	} {
		if got := RawLine([]byte(c.data), c.n); got != c.want {
			t.Errorf("RawLine(%q, %d) = %q, want %q", c.data, c.n, got, c.want)
		}
	}
}
