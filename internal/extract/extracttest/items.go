package extracttest

import (
	"fmt"
	"strings"

	"pgregory.net/rapid"
)

// Item is one value a generated file sets: under a section (a group for
// ansible), a host for an ansible host line, and a key.
type Item struct {
	Section, Host, Key, Value string
	Line                      int // where the renderer wrote the key
}

// DrawItems draws up to n items over a few sections and keys, so keys repeat.
func DrawItems(t *rapid.T, sections, keys, values *rapid.Generator[string]) []Item {
	n := rapid.IntRange(1, 12).Draw(t, "n")
	items := make([]Item, n)
	for i := range items {
		items[i] = Item{Section: sections.Draw(t, "section"), Key: keys.Draw(t, "key"), Value: values.Draw(t, "value")}
	}
	return items
}

// LineWriter writes lines and counts them.
type LineWriter struct {
	Text strings.Builder
	Line int
	// End gives the line end each Add writes, a LF when End is nil. A
	// property draws a LF, a CRLF or a lone CR from it, each of which ends
	// one line (core/lineend).
	End func() string
}

// Add writes a line of format and args, and returns its number.
func (w *LineWriter) Add(format string, args ...any) int {
	w.Line++
	fmt.Fprintf(&w.Text, format, args...)
	end := "\n"
	if w.End != nil {
		end = w.End()
	}
	w.Text.WriteString(end)
	return w.Line
}
