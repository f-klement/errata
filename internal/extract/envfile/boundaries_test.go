package envfile

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
)

// The exact work of a small read, step by step, so a charge of the wrong
// size fails: the statement A=1\n is a line's work and its 4 bytes, its
// value's substitution 1, the matcher's look at the entry 48 and the key's 1
// byte, and the 3 bytes of white space after the last statement 3.
func TestEnvfileWorkIsExact(t *testing.T) {
	x, err := Build(Spec{File: ".env", Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	want := int64(core.LineWork+4) + 1 + 48 + 1 + 3
	if got := coretest.Spent(t, x.(core.EntryExtractor), []byte("A=1\n \n\t")); got != want {
		t.Errorf("spent %d, want %d", got, want)
	}
}

// expandEscapes alone, at the edges the reader never hands it but a
// change could reach: a \ at the very end, and octal digits at each bound.
func TestExpandEscapesAtTheEdges(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`a\`, `a\`},
		{`\`, `\`},
		{`\0060`, "0"},
		{`\0107`, "G"},
		{`\01014`, "A4"},
		{`\0108`, `\108`},
		{`\012`, `\12`},
		{`\0`, `\`},
		{`\$`, "$$"},
		{`\\n`, `\n`},
	} {
		if got := string(expandEscapes(nil, []byte(c.in))); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

// nameLen agrees with compose's (?i)[_a-z][_a-z0-9]* on every character up
// to U+0250 and the two that fold to k and s, first and after a letter.
func TestNameLenAgreesWithComposesPattern(t *testing.T) {
	re := regexp.MustCompile(`^(?i:[_a-z][_a-z0-9]*)`)
	runes := []rune{kelvin, longS, utf8.RuneError}
	for c := rune(0); c < 0x250; c++ {
		runes = append(runes, c)
	}
	for _, c := range runes {
		for _, s := range []string{string(c) + "-", "a" + string(c) + "-", "\xff", "a\xff"} {
			if got, want := nameLen([]byte(s)), len(re.FindString(s)); got != want {
				t.Errorf("%q: nameLen %d, the pattern %d", s, got, want)
			}
		}
	}
}

// inName and isSeparator are compose's loop over a name: every character
// it lets stand in a name, and the three that end one.
func TestNameCharacters(t *testing.T) {
	for _, c := range "_.-[]aZ9é٣\t\v\f\r \u0085\u00a0" {
		if !inName(c) || isSeparator(c) {
			t.Errorf("%q: inName %v, isSeparator %v", c, inName(c), isSeparator(c))
		}
	}
	for _, c := range "$#;'\"{}()/\\,+*@!~`\u2028\u3000\ufffd" {
		if inName(c) || isSeparator(c) {
			t.Errorf("%q: inName %v, isSeparator %v", c, inName(c), isSeparator(c))
		}
	}
	for _, c := range "=:\n" {
		if !isSeparator(c) {
			t.Errorf("%q is no separator", c)
		}
	}
}

// The bounds of a substitution: ${NAME at the very end of a value, a ${
// with an operator at the start of a line after the first, and a { that
// leaves the closing } unmatched, so the word runs to the line's last }.
func TestEnvfileSubstitutionBounds(t *testing.T) {
	for _, body := range []string{"A=${B\n", "A=${B:\n", "A=${B:-\n", `A="${B` + "\"\n"} {
		if got := refusal(t, body, name("A")); !strings.Contains(got, "starts no substitution compose reads") {
			t.Errorf("%q: %q", body, got)
		}
	}
	for _, c := range []struct{ body, want string }{
		{"A=\nB=\"${A:-x}\n${A:-y}\"\n", "x\ny@2"},
		{"A=\nB=\"x\n${A:-y}\"\n", "x\ny@2"},
		{"A=\nB=${A:-{}}z\n", "{}z@2"},
	} {
		if got := marked(t, c.body, name("B")); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}

// A statement after a comment at the end of the file, and a comment with
// no line break after it, end the read where they should.
func TestEnvfileCommentsAtTheEnd(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"# c\nA=1", "1@2"},
		{"A=1\n# c", "1@1"},
		{"A=1\n#\n#\nA=2\n", "1@1 2@4"},
		{"  \n\t\nA=1 \n  ", "1@3"},
	} {
		if got := marked(t, c.body, name("A")); got != c.want {
			t.Errorf("%q: got %q, want %q", c.body, got, c.want)
		}
	}
}
