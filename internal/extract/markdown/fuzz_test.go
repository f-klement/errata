package markdown

import (
	"bytes"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
)

// Every value of every target over arbitrary Markdown is at a line inside
// its file, and a file is refused only by a bound of the parse, of the text
// read or of the search. The last seed is a table refused for its padding.
func FuzzMarkdown(f *testing.F) {
	for _, s := range []string{comments, sections, code, table, "", "<!--", "| a |\n|-|\n|`\\|`|\n", "|Port|" + strings.Repeat("a|", 9) + "\n|" + strings.Repeat("-|", 10) + "\n" + strings.Repeat("b\n", 10)} {
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
		values, err := e.(extractor).FromData([]byte(data))
		if err != nil {
			if !bounded(err) {
				t.Fatalf("%+v: refused: %v", s, err)
			}
			return
		}
		file := core.LineFeeds([]byte(data))
		lines := bytes.Count(file, []byte("\n")) + 1
		for _, v := range values {
			if v.At.Line < 1 || v.At.Line > lines {
				t.Fatalf("%+v: %q at line %d of %d", s, v.Text, v.At.Line, lines)
			}
		}
	})
}

// bounded reports whether err is a refusal by a bound of the parse, of the
// prose, of the text read, or of the search.
func bounded(err error) bool {
	for _, b := range []string{"levels deep, the limit", "which errata cannot tell from the block alone", "errata does not follow", "runs of * and _, the limit", "link reference definitions, and the two multiplied",
		"nodes, the limit", "open blocks from line to line, the limit", "a line of HTML of", "parsing the Markdown took more than",
		"pieces of the file, the limit", "tokens, the limit", "empty cells, more than its", "times the file's size", "values, the limit", "matching took more than"} {
		if strings.Contains(err.Error(), b) {
			return true
		}
	}
	return false
}
