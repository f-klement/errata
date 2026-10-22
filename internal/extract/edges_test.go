package extract

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// The tests in this file each pin a boundary that no other test reached:
// Gremlins changed the comparison or the arithmetic there and every test
// still passed. Each row names the edge it holds.

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
	if got := clipError(short); !errors.Is(got, short) {
		t.Fatalf("clipError of %d bytes returned a new error %q", len(short.Error()), got)
	}
	long := errors.New(strings.Repeat("e", 4*maxQuote+1))
	if got := clipError(long).Error(); got != strings.Repeat("e", 4*maxQuote)+"..." {
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

// The clocked search reads UTF-8 as Go's regexp does: a multibyte character
// is one character, and a byte from 0x80 up that is not UTF-8 is U+FFFD,
// never the character of that number. An empty match at the very start
// counts as one, as FindAllSubmatchIndex counts it.
func TestRegexReadsRunesAsRegexpDoes(t *testing.T) {
	for _, c := range []struct{ body, pattern, want string }{
		{"é\n", `(é)`, "é@1"},
		{"aéb\n", `a(.)b`, "é@1"},
		{"\x80\n", `(\x{80})`, ""},
		{"\x80\n", `(\x{FFFD})`, "\x80@1"},
		{"b", `(a*)`, "@1 @1"},
	} {
		root := repo(t, map[string]string{"Dockerfile": c.body})
		if got := texts(mustRun(t, regexSpec(c.pattern), root)); got != c.want {
			t.Errorf("%s over %q: got %q, want %q", c.pattern, c.body, got, c.want)
		}
	}
}

// A header through an array of tables counts the array's element as a level,
// so a key in a table there reaches maxDepth and one more part is refused.
func TestTOMLDepthThroughAnArrayOfTables(t *testing.T) {
	at := "[[a]]\n[a." + keys(maxDepth-3) + "]\nb = 1\n"
	got, err := run(t, tomlSpec("a[0]."+keys(maxDepth-3)+".b"), repo(t, map[string]string{"config.toml": at}))
	if err != nil || texts(got) != "1@3" {
		t.Fatalf("at maxDepth: got %s, %v", texts(got), err)
	}
	_, err = run(t, tomlSpec("a"), repo(t, map[string]string{"config.toml": "[[a]]\n[a." + keys(maxDepth-2) + "]\nb = 1\n"}))
	wantError(t, err, "config.toml: line 3: nested more than 1000 levels deep, the limit")
}

// An offset's two digits run from 0 to 9 at either place, and a syntax error
// at the first byte of a file, or at its end, still names its line.
func TestTOMLEdges(t *testing.T) {
	for _, body := range []string{"a = 2023-01-01T00:00:00+09:59\n", "a = 2023-01-01T00:00:00-10:00\n", "a = 2023-01-01T00:00:00+00:00\n"} {
		if _, err := run(t, tomlSpec("a"), repo(t, map[string]string{"config.toml": body})); err != nil {
			t.Errorf("%q: %v", body, err)
		}
	}
	_, err := run(t, tomlSpec("a"), repo(t, map[string]string{"config.toml": "=1\n"}))
	if err == nil || !strings.HasPrefix(err.Error(), "config.toml: toml: line 1: ") {
		t.Errorf("error at the first byte: %v", err)
	}
	_, err = parseTOML([]byte("a = 1\nb = "))
	if err == nil || err.Error() != "toml: line 2: expected value, not end of input" {
		t.Errorf("error at the end of the input: %v", err)
	}
}
