package comment

import (
	"bytes"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/lexer"
)

// pieces are what a generated file is made of: every opener and closer of the
// table, quotes, escapes, line ends and words.
var pieces = func() []string {
	out := []string{"\n", "\r\n", " ", "\t", "\\", "port 1", "x", "=", "é", "(", ")"}
	for _, l := range lexer.Languages() {
		for _, f := range l.Forms {
			out = append(out, f.Open, f.Close, f.Inner, f.Outer, f.Fence)
		}
	}
	return out
}()

// patterns are the searches a generated file is read with: a word, a line
// end, an empty match, and one across lines.
var patterns = []string{`(\S+)`, `(\n)`, `()`, `(?s)(.+)`, `port (\d)`, `(?-m)(.)$`}

// commentLines are the lines, counted from 1, that some comment of data
// covers in l's reading.
func commentLines(l lexer.Language, data []byte) map[int]bool {
	lines := map[int]bool{}
	for s := range l.Spans(data) {
		if s.Kind != lexer.Comment {
			continue
		}
		first := bytes.Count(data[:s.Start], []byte("\n")) + 1
		last := first + bytes.Count(data[s.Start:s.End], []byte("\n"))
		for n := first; n <= last; n++ {
			lines[n] = true
		}
	}
	return lines
}

// checkLines fails unless every value of reading data as lang with pattern
// lies inside the file and on a line the lexer classed as comment.
func checkLines(t interface{ Fatalf(string, ...any) }, lang lexer.Language, pattern string, data []byte) {
	e, err := Build(Spec{OneFile: core.OneFile{File: "f"}, Pattern: pattern, Language: lang.Name})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	vs, err := e.(commentExtractor).FromData(data)
	if err != nil {
		// A file past the value cap, or one the lexer lost its place in, is
		// refused, which the property allows.
		if strings.Contains(err.Error(), "values, the limit") || strings.Contains(err.Error(), "lexer lost its place") {
			return
		}
		t.Fatalf("%s %q: %v", lang.Name, data, err)
	}
	lf := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	total := bytes.Count(lf, []byte("\n")) + 1
	classed := commentLines(lang, lf)
	for _, v := range vs {
		if v.At.Line < 1 || v.At.Line > total {
			t.Fatalf("%s %q %s: value %q at line %d, outside the file's %d lines", lang.Name, data, pattern, v.Text, v.At.Line, total)
		}
		if !classed[v.At.Line] {
			t.Fatalf("%s %q %s: value %q at line %d, which holds no comment", lang.Name, data, pattern, v.Text, v.At.Line)
		}
	}
}

// Every value's line lies inside its file and on a line the lexer classed as
// comment, for files made of the table's own delimiters in every language.
// Planted break: offset returning the offset in the text as the file's.
func TestEveryValueIsOnACommentLine(t *testing.T) {
	langs := lexer.Languages()
	rapid.Check(t, func(t *rapid.T) {
		l := rapid.SampledFrom(langs).Draw(t, "language")
		parts := rapid.SliceOfN(rapid.SampledFrom(pieces), 0, 40).Draw(t, "pieces")
		pattern := rapid.SampledFrom(patterns).Draw(t, "pattern")
		checkLines(t, l, pattern, []byte(strings.Join(parts, "")))
	})
}

// The same over any bytes, for the fuzzer.
func FuzzComment(f *testing.F) {
	for name, src := range planted {
		l, _ := lexer.Named(name)
		for i := range patterns {
			f.Add(indexOf(l), uint8(i), src)
		}
	}
	f.Fuzz(func(t *testing.T, row, pattern uint8, src string) {
		langs := lexer.Languages()
		checkLines(t, langs[int(row)%len(langs)], patterns[int(pattern)%len(patterns)], []byte(src))
	})
}

// indexOf is l's row number.
func indexOf(l lexer.Language) uint8 {
	for i, r := range lexer.Languages() {
		if r.Name == l.Name {
			return uint8(i)
		}
	}
	return 0
}
