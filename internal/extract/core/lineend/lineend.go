// Package lineend is the one rule by which errata ends a line: at a LF, a
// CRLF or a lone CR, as the HTML standard reads a file before it tokenizes
// and as an editor shows the CR line ends of classic Mac OS. Every reader
// numbers its values' lines by it, and every reader that builds its own
// text from a file reads each line end as one LF. It is core's, kept apart
// so that extracttest, which core's own tests import, counts lines by the
// same rule.
package lineend

import "bytes"

// Fold is data with every line end one LF: a CRLF and a lone CR each read
// as LF. Data with no CR is returned as it is, without a copy.
func Fold(data []byte) []byte {
	i := bytes.IndexByte(data, '\r')
	if i < 0 {
		return data
	}
	out := make([]byte, 0, len(data))
	for i >= 0 {
		out = append(out, data[:i]...)
		out = append(out, '\n')
		data = bytes.TrimPrefix(data[i+1:], []byte("\n"))
		i = bytes.IndexByte(data, '\r')
	}
	return append(out, data...)
}

// Count is the number of lines that end in data[from:to]: each LF, and
// each CR that no LF follows in data. The CR of a CRLF is not counted, its
// LF is, so a CRLF split between two ranges counts once, and a byte's line
// is the same whichever range ends before it.
func Count(data []byte, from, to int) int {
	n := bytes.Count(data[from:to], []byte("\n"))
	for i := from; ; {
		k := bytes.IndexByte(data[i:to], '\r')
		if k < 0 {
			return n
		}
		i += k + 1
		if i == len(data) || data[i] != '\n' {
			n++
		}
	}
}

// Starts is the offset where each line of data starts, 0 first, and one
// past each line end, so a text that ends with a line end has an empty last
// line.
func Starts(data []byte) []int {
	starts := []int{0}
	for i, c := range data {
		if c == '\n' || c == '\r' && (i+1 == len(data) || data[i+1] != '\n') {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// Line is the line, counted from 1, that holds byte offset off.
func Line(data []byte, off int) int {
	return Count(data, 0, off) + 1
}

// Start is the offset where the line that holds off starts.
func Start(data []byte, off int) int {
	i := bytes.LastIndexAny(data[:off], "\r\n")
	if i == off-1 && i >= 0 && data[i] == '\r' && off < len(data) && data[off] == '\n' {
		i = bytes.LastIndexAny(data[:i], "\r\n") // off is the LF of a CRLF, on the CR's line
	}
	return i + 1
}

// Next is the offset past the end of the line that holds off, its line end
// included, or len(data) for the last line when no line end closes it.
func Next(data []byte, off int) int {
	i := bytes.IndexAny(data[off:], "\r\n")
	if i < 0 {
		return len(data)
	}
	if i += off; data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
		return i + 2
	}
	return i + 1
}
