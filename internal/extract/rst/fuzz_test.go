package rst

import (
	"bytes"
	"strings"
	"testing"
)

// Every value of every target over arbitrary input is at a line inside its
// file, and a file is refused only by a bound of the text read or of the
// search, or for a form the reader names as one it does not read.
func FuzzRST(f *testing.F) {
	for _, s := range []string{comments, sections, titles, code, grids, simpleTable, lines, "", "..", "::", "``", "+-+\n|", "== ==\na\n== ==\n"} {
		for i := range specs {
			f.Add(s, uint8(i))
		}
	}
	f.Fuzz(func(t *testing.T, data string, which uint8) {
		s := specs[int(which)%len(specs)]
		values, err := extracted(s, data)
		if err != nil {
			if !bounded(err) {
				t.Fatalf("%+v: refused: %v", s, err)
			}
			return
		}
		lines := bytes.Count([]byte(data), []byte("\n")) + 1
		for _, v := range values {
			if v.At.Line < 1 || v.At.Line > lines {
				t.Fatalf("%+v: %q at line %d of %d", s, v.Text, v.At.Line, lines)
			}
		}
	})
}

// bounded reports whether err is a refusal by a bound of the text read or
// of the search, or of a form the reader does not read.
func bounded(err error) bool {
	for _, b := range []string{"times the file's size", "values, the limit", "matching took more than", "which the rst extractor does not read", "no bottom border", "which docutils reads as an error"} {
		if strings.Contains(err.Error(), b) {
			return true
		}
	}
	return false
}
