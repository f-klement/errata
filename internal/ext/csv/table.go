package csv

import (
	"bytes"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core/lineend"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// read is the cells s names in f: the cell of s's column in each row whose
// cells match s's where, each at the line its cell starts on. A file that
// does not parse, a column the file does not have and a value's cell that
// is not UTF-8 are errors, each naming the file and the line. So is a
// value past room, the values the request's earlier files left of
// wire.MaxValues: read stops there, before it holds more.
func (s spec) read(f wire.File, room int) ([]wire.Value, error) {
	t := newTable(s, f)
	out := []wire.Value{}
	for {
		rec, err := t.next()
		if errors.Is(err, io.EOF) {
			return out, t.end()
		}
		if err != nil {
			return nil, err
		}
		v, ok, err := t.value(rec)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if len(out) == room {
			return nil, fmt.Errorf("%s:%d: the files hold more than %d values, the limit; narrow where or the files", t.path, v.Line, wire.MaxValues)
		}
		out = append(out, v)
	}
}

// table reads one file's rows with encoding/csv, and places each cell on
// the line errata counts it on.
type table struct {
	s     spec
	path  string
	r     *stdcsv.Reader
	at    positions
	cols  columns
	first bool // the first row is read, and cols is set
}

// newTable reads f without its UTF-8 byte order mark, so that a quoted
// first cell after one parses, as Windows PowerShell's Export-Csv writes
// it. The mark holds no line end, so every line stays where it was.
func newTable(s spec, f wire.File) *table {
	data := bytes.TrimPrefix(f.Data, []byte("\ufeff"))
	r := stdcsv.NewReader(bytes.NewReader(data))
	r.Comma = s.delimiter
	return &table{s: s, path: f.Path, r: r, at: newPositions(data)}
}

// next is the next row of data: a header row is read into the columns and
// passed over. At the end of the file it returns io.EOF.
func (t *table) next() ([]string, error) {
	rec, err := t.r.Read()
	if err != nil {
		return nil, t.parseError(rec, err)
	}
	if t.first {
		return rec, nil
	}
	t.first = true
	if t.cols, err = t.s.columns(rec); err != nil {
		return nil, fmt.Errorf("%s:%d: %w", t.path, t.line(0), err)
	}
	if t.s.header {
		return t.next()
	}
	return rec, nil
}

// end is the error of a file that ended with no header row where the spec
// needs one.
func (t *table) end() error {
	if !t.first && t.s.header {
		return fmt.Errorf("%s: the file has no header row to name the column %s; write header: false to name columns by number", t.path, strconv.Quote(t.s.column))
	}
	return nil
}

// value is the cell of the spec's column in rec, and whether rec matches
// the spec's where. A cell that is not UTF-8 is an error.
func (t *table) value(rec []string) (wire.Value, bool, error) {
	for _, c := range t.cols.where {
		if rec[c.index] != c.value {
			return wire.Value{}, false, nil
		}
	}
	text, line := rec[t.cols.column], t.line(t.cols.column)
	if !utf8.ValidString(text) {
		return wire.Value{}, false, fmt.Errorf("%s:%d: the cell of column %s is not UTF-8, and a value is text", t.path, line, strconv.Quote(t.s.column))
	}
	key := t.s.column
	v := wire.Value{Text: &text, File: t.path, Line: line, Key: &key}
	if cond := t.s.condition(); cond != "" {
		v.Condition = &cond
	}
	return v, true, nil
}

// line is the line, as errata counts lines, on which the field i of the
// row last read starts.
func (t *table) line(i int) int {
	return t.at.line(t.r.FieldPos(i))
}

// parseError is err, from encoding/csv reading rec, as the reply's error:
// the file and the line as errata counts it, and what is wrong. A quote
// still open at the end of the file is placed on the line its row starts
// on, near the quote, not on the file's last line, where encoding/csv
// found it open. io.EOF is returned as it is.
func (t *table) parseError(rec []string, err error) error {
	var pe *stdcsv.ParseError
	if !errors.As(err, &pe) {
		return err
	}
	line := t.at.line(pe.Line, pe.Column)
	if errors.Is(pe.Err, stdcsv.ErrQuote) && t.at.offset(pe.Line, pe.Column) == t.at.size {
		line = t.at.line(pe.StartLine, 1)
	}
	if errors.Is(pe.Err, stdcsv.ErrFieldCount) {
		return fmt.Errorf("%s:%d: a row of %d cells, and the first row has %d", t.path, line, len(rec), t.cols.width)
	}
	return fmt.Errorf("%s:%d: %w", t.path, line, pe.Err)
}

// columns are the positions of a spec's column and where keys in a file.
type columns struct {
	column int
	where  []cell
	width  int // the cells of the first row, which every row has
}

// cell is a where key's position and the text it must hold.
type cell struct {
	index int
	value string
}

// columns finds s's column and where keys in first, the file's first row:
// by name in a header, or by number.
func (s spec) columns(first []string) (columns, error) {
	c := columns{width: len(first)}
	var err error
	if c.column, err = s.index(first, s.column); err != nil {
		return columns{}, err
	}
	for _, kv := range s.where {
		i, err := s.index(first, kv.Key)
		if err != nil {
			return columns{}, err
		}
		c.where = append(c.where, cell{i, kv.Value})
	}
	return c, nil
}

// index is the position of the column name in first, the file's first row.
// A header that names it twice is refused, since the value would be one of
// two cells.
func (s spec) index(first []string, name string) (int, error) {
	if !s.header {
		n, _ := strconv.Atoi(name) // checkColumns let only a number through
		if n > len(first) {
			return 0, fmt.Errorf("column %d is past the %d columns of the first row", n, len(first))
		}
		return n - 1, nil
	}
	i := slices.Index(first, name)
	if i < 0 {
		return 0, fmt.Errorf("the header row has no column %s", strconv.Quote(name))
	}
	if j := slices.Index(first[i+1:], name); j >= 0 {
		return 0, fmt.Errorf("the header row names %s twice, in columns %d and %d", strconv.Quote(name), i+1, i+j+2)
	}
	return i, nil
}

// positions turns a place encoding/csv gives, a line and a byte column,
// into the line errata counts. encoding/csv ends a line at a LF alone, and
// errata at a LF, a CRLF or a lone CR (lineend), so the two differ only in
// a file with a lone CR. A file with none, most of them, keeps one slice
// for both.
type positions struct {
	size int
	lf   []int // where each line starts as encoding/csv counts them
	ends []int // where each line starts as errata counts them
}

func newPositions(data []byte) positions {
	lf := []int{0}
	for i, c := range data {
		if c == '\n' {
			lf = append(lf, i+1)
		}
	}
	ends := lf
	if lineend.Count(data, 0, len(data)) != len(lf)-1 {
		ends = lineend.Starts(data)
	}
	return positions{size: len(data), lf: lf, ends: ends}
}

// line is errata's line of the byte at line and col as encoding/csv counts
// them, both from 1. A place past the data, as a parse error at the end of
// a file may give, is the last line.
func (p positions) line(line, col int) int {
	n, _ := slices.BinarySearch(p.ends, p.offset(line, col)+1)
	return n
}

// offset is the byte at line and col as encoding/csv counts them, both
// from 1, held to the size of the data: a place past it is the size.
func (p positions) offset(line, col int) int {
	start := p.lf[min(max(line, 1), len(p.lf))-1]
	return min(start+max(col, 1)-1, p.size)
}
