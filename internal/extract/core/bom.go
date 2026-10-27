package core

import "bytes"

// BOM is the UTF-8 byte order mark, which an editor on Windows may write at
// the start of a file and a reader does not show. It is the one definition
// of the mark for every reader; a const, so no reader can change it.
const BOM = "\xef\xbb\xbf"

// DropBOM is data without a byte order mark at its start. The mark holds no
// line end, so the lines of data count the same without it.
func DropBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte(BOM))
}
