package extracttest

import (
	"bytes"

	"github.com/f-klement/errata/internal/extract/core/lineend"
)

// RawLine is line n of data, counted from 1, as the file wrote it: without
// its line end, but with the spaces and tabs at either end and a byte order
// mark at the start of line 1. Lines end at a LF, a CRLF or a lone CR, as
// core.NextLine says. A fuzz oracle compares a reader's text with this
// line, not with core.LineText, which trims it: table keeps the tab after a
// group's last member and udev the blanks at a rule's end, and an oracle on
// the trimmed line failed each of them on a correct read
// (CODING_STANDARDS.md, A fuzz oracle reads the line as the reader does).
func RawLine(data []byte, n int) string {
	start := 0
	for range n - 1 {
		start = lineend.Next(data, start)
	}
	line := bytes.TrimSuffix(data[start:lineend.Next(data, start)], []byte("\n"))
	return string(bytes.TrimSuffix(line, []byte("\r")))
}
