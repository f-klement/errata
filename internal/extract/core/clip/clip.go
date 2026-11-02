// Package clip cuts text for a message: a quote of file text or of a path
// that may be long, so no message carries a whole file or a 4096 byte
// path. It is core's, kept apart so that core/gitindex, which core
// imports, quotes as core does.
package clip

import (
	"strconv"
	"unicode/utf8"
)

// MaxQuote is the most bytes of text a message quotes.
const MaxQuote = 64

// Text is file text for a message: s, or its first MaxQuote bytes and ...
// when it is longer.
func Text(s string) string { return Cut(s, MaxQuote) }

// Cut is s, or its first n bytes and ... when it is longer. The cut moves
// back to the start of a rune, at most three bytes, so it splits none.
func Cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for back := 0; back < utf8.UTFMax-1 && !utf8.RuneStart(s[n]); back++ {
		n--
	}
	return s[:n] + "..."
}

// Quote is Text in Go's quotes, as %q writes them, with the ... after the
// closing quote.
func Quote(s string) string {
	if c := Text(s); c != s {
		return strconv.Quote(c[:len(c)-3]) + "..."
	}
	return strconv.Quote(s)
}
