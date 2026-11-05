package asciidoc

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Over arbitrary AsciiDoc, every value of every target is at a line inside
// its file, and at its own line of the unnormalised file: a comment's line
// holds //, a table cell's row line a separator, a section's heading line
// is a title, a code block's first line is the line the block's value
// starts with, and the first character of a match is on its line, or ends
// it. The text read is at most the file and a line break, and a file is
// refused only by a bound, by a table or a two line title the reader does
// not read, or by a block with no closing line.
func FuzzAsciidoc(f *testing.F) {
	for _, s := range []string{comments, sections, code, tables, lines, "", "////", "|===\n|Port\n\n2+|a", "\xef\xbb\xbf= A\r\n\r\n// x *y*\r\n", "----\n\n0"} {
		for i := range specs {
			f.Add(s, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, data string, which uint8) {
		s := specs[int(which)%len(specs)]
		s.File = "f"
		e, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		x := e.(extractor)
		values, err := x.FromData([]byte(data))
		if err != nil {
			if !bounded(err) {
				t.Fatalf("%+v: refused: %v", s, err)
			}
			return
		}
		d, err := parse([]byte(data), x.sel, x.src.Limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.text) > len(data)+1 {
			t.Fatalf("%+v: the text read is %d bytes, of a file of %d", s, len(d.text), len(data))
		}
		// The file's lines as the reader numbers them, every line end one
		// LF (core.LineFeeds): a lone CR ends a line too.
		file := strings.Split(string(core.LineFeeds([]byte(strings.TrimPrefix(data, core.BOM)))), "\n")
		for _, v := range values {
			if v.At.Line < 1 || v.At.Line > len(file) {
				t.Fatalf("%+v: %q at line %d of %d", s, v.Text, v.At.Line, len(file))
			}
			if !atItsLine(s, v, file[v.At.Line-1]) {
				t.Fatalf("%+v: %q is not at its line %d, %q", s, v.Text, v.At.Line, file[v.At.Line-1])
			}
		}
	})
}

// atItsLine reports whether v, a value of s, is at line, the text of the
// line it names.
func atItsLine(s Spec, v check.Value, line string) bool {
	switch {
	case s.Target == "comment":
		return strings.Contains(line, "//")
	case s.Target == "table":
		return strings.ContainsAny(line, "|!")
	case v.Text == "":
		return true
	case s.Pattern != "": // a match that starts with a line break starts at the end of its line
		return v.Text[0] == '\n' || strings.Contains(line, v.Text[:1])
	case s.Target == "section":
		_, _, ok := heading([]byte(strings.TrimRight(line, " \t")))
		return ok
	default: // a code block's first line
		first, _, _ := strings.Cut(v.Text, "\n")
		return first == line
	}
}

// bounded reports whether err is a refusal by a bound of the read or of the
// search, a table or a two line title the reader does not read, or a block
// with no closing line.
func bounded(err error) bool {
	for _, b := range []string{"levels deep, the limit", "runs of lines, the limit", "values, the limit", "matching took more than",
		"which the asciidoc extractor does not read", "has no closing line"} {
		if strings.Contains(err.Error(), b) {
			return true
		}
	}
	return false
}
