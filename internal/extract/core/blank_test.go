package core

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

// language is the row named name, which the table must have.
func language(tb testing.TB, name string) Language {
	tb.Helper()
	l, ok := LanguageNamed(name)
	if !ok {
		tb.Fatalf("no row %q", name)
	}
	return l
}

// Each comment becomes a space for each of its characters with its line
// feeds kept, a string's comment openers stay text, CRs fold first, and a
// file with no comment is the folded data itself. Planted breaks: a space
// for each byte; the comment's line feed blanked; the CRs folded after
// the lexer reads.
func TestBlankComments(t *testing.T) {
	g := language(t, "go")
	for _, c := range []struct{ in, want string }{
		{"a /* é\n\xff */ b // c\nd", "a     \n     b     \nd"},
		{`s := "//x" /*y*/ + "/*"`, `s := "//x"       + "/*"`},
		{"// a\rb", "    \nb"},
		{"// a\r\nb", "    \nb"},
		{"x\r\ny", "x\ny"},
		{"", ""},
	} {
		got, err := BlankComments("f.go", g, []byte(c.in))
		if err != nil || string(got) != c.want {
			t.Errorf("BlankComments(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	data := []byte("no comment\n")
	if got, _ := BlankComments("f.go", g, data); &got[0] != &data[0] {
		t.Errorf("a file with no comment was copied")
	}
	_, err := BlankComments("f.go", g, []byte("x\n/* open"))
	if err == nil || !strings.Contains(err.Error(), "f.go: line 2: a comment opens here and runs to the end of the file") {
		t.Errorf("error %v, want the span's refusal", err)
	}
}

// segment is one piece of a generated Go file: its text, and what
// BlankComments must make of it, worked out from the piece alone.
type segment struct{ text, blanked string }

// code, comment and quoted are what a generated file's pieces hold: code
// holds no byte that opens a comment or a string, a comment's text and a
// string's nothing that closes them, and both hold openers of the other.
// A comment's /* has a letter after it, since /*/ would close it.
var (
	codeLetters    = []string{"a", "b", "x", " ", "\n", "é", "\xff", "K", "\r"}
	commentLetters = []string{"a", "b", "x", " ", "é", "\xff", "//", "/*a", "\"", "`"}
	quotedLetters  = []string{"a", "b", "x", " ", "é", "//", "/*", "*/"}
)

// randomSegment is a random piece of a Go file: code, a line comment with
// the line feed that ends it, a block comment that may run over lines, or
// a string that holds comment openers.
func randomSegment(r *rand.Rand) segment {
	pick := func(from []string) []string { // at most 6
		out := make([]string, r.IntN(7))
		for i := range out {
			out[i] = from[r.IntN(len(from))]
		}
		return out
	}
	switch r.IntN(4) {
	case 0:
		s := strings.Join(pick(codeLetters), "")
		return segment{s, s}
	case 1:
		s := strings.Join(pick(quotedLetters), "")
		return segment{`"` + s + `"`, `"` + s + `"`}
	}
	body := pick(append(commentLetters, "\n"))
	open, end, kept := "/*", "*/", "  "
	if r.IntN(2) == 0 {
		body = pick(commentLetters)
		open, end, kept = "//", "\n", "\n"
	}
	var want strings.Builder
	want.WriteString("  ")
	for _, s := range body {
		if s == "\n" {
			want.WriteString(s)
		} else {
			want.WriteString(strings.Repeat(" ", utf8.RuneCountInString(s)))
		}
	}
	return segment{open + strings.Join(body, "") + end, want.String() + kept}
}

// The blanked file is the file with every comment's characters spaces and
// everything else as it was, worked out piece by piece without the lexer;
// and on it the search with a seeker finds what the search of every place
// finds (prefilterCase, whose prefilterOracles blank the comments as
// skip: [comments] does), so the prefilter finds no match in a comment and
// loses none outside one. Planted breaks: the prefilter's own (a match
// lost outside the comments); a space for each byte.
func TestPrefilterOnBlankedComments(t *testing.T) {
	r := rand.New(rand.NewPCG(294, 1))
	g := language(t, "go")
	seeking := 0
	for range 1500 {
		var text, want strings.Builder
		for range r.IntN(8) {
			s := randomSegment(r)
			text.WriteString(s.text)
			want.WriteString(s.blanked)
		}
		got, err := BlankComments("f.go", g, []byte(text.String()))
		folded := LineFeeds([]byte(want.String()))
		if err != nil || !bytes.Equal(got, folded) {
			t.Fatalf("BlankComments(%q) = %q, %v; want %q", text.String(), got, err, folded)
		}
		for range 4 {
			if prefilterCase(t, "("+randomPattern(r, 4)+")", text.String()) {
				seeking++
			}
		}
	}
	if seeking < 2000 {
		t.Errorf("%d cases had a seeker, want 2000 or more", seeking)
	}
}

// fuzzLanguages are the rows FuzzBlankComments reads with, one of each
// family of the table.
var fuzzLanguages = []string{"go", "typescript", "python", "shell", "ruby", "sql", "lua", "html", "rust", "php"}

// FuzzBlankComments holds BlankComments, over any file and a row of each
// family, to what it promises: each line of the folded file keeps its
// characters, each either as it was or a space, and the characters of each
// comment the lexer finds are spaces while those outside every comment are
// as they were.
func FuzzBlankComments(f *testing.F) {
	f.Add(uint8(0), "a /* é */ b // c\n\"//\"")
	f.Add(uint8(2), "x = '#' # y\r\nz")
	f.Add(uint8(7), "<p>a<!-- b\nc --></p>")
	f.Add(uint8(4), "a = <<~E\n# x\nE\n# y")
	f.Fuzz(func(t *testing.T, row uint8, data string) {
		l := language(t, fuzzLanguages[int(row)%len(fuzzLanguages)])
		got, err := BlankComments("f", l, []byte(data))
		if err != nil {
			return
		}
		folded := LineFeeds([]byte(data))
		inComment := commentRunes(l, folded)
		want, have := []rune(string(folded)), []rune(string(got))
		if len(want) != len(have) {
			t.Fatalf("%q: %d characters blanked, %d in the file", data, len(have), len(want))
		}
		for i, c := range want {
			ok := have[i] == c
			if inComment[i] {
				ok = have[i] == ' ' || c == '\n' && have[i] == '\n'
			}
			if !ok {
				t.Fatalf("%q: character %d is %q, from %q (in a comment: %v)", data, i, have[i], c, inComment[i])
			}
		}
	})
}

// commentRunes is, for each character of data, whether it lies in a
// comment l finds there.
func commentRunes(l Language, data []byte) []bool {
	in := make([]bool, len(data))
	for s := range l.Spans(data) {
		if s.Kind == SpanComment {
			for i := s.Start; i < s.End; i++ {
				in[i] = true
			}
		}
	}
	var out []bool
	for i := 0; i < len(data); {
		_, n := utf8.DecodeRune(data[i:])
		out = append(out, in[i])
		i += n
	}
	return out
}
