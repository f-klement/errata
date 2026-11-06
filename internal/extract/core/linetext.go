package core

import "bytes"

// LineText is line n of data, counted from 1, as the file wrote it, with
// the spaces, tabs and line end around it taken off: the text of a not
// literal value whose reader quotes the line that makes it computed, such
// as Cargo's version = { workspace = true }, a comment after it on the
// line included. A byte order mark at the start of data is not part of
// line 1. Lines end as NextLine says, so the line is the one a reader that
// numbers lines with core's functions put the value at.
func LineText(data []byte, n int) string {
	data = DropBOM(data)
	start := 0
	for range n - 1 {
		start = NextLine(data, start)
	}
	return string(bytes.Trim(data[start:NextLine(data, start)], " \t\r\n"))
}
