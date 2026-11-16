package asciidoc

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
)

// table is a table block the table target reads: its cells, in order, the
// text of each in buf.
type table struct {
	sep  byte
	line int // the line of its opening delimiter
	// err is why the reader does not read the table, nil when it does.
	err error
	// header is 1 for a header row the attribute line asks for, -1 for none,
	// and 0 when the first line decides; cols is the columns the attribute
	// line gives, 0 for none.
	header, cols int
	buf          []byte
	cells        []cell
	open         bool // the last cell takes the text that follows
	// lines is the lines of cells read, firstCells the cells of the first,
	// and blankAfter whether the line after it is blank, once decided.
	lines, firstCells   int
	decided, blankAfter bool
	seg                 []byte
}

// cell is a cell's text in buf, and the line of the separator before it.
type cell struct {
	start, end, line int
}

// newTable is the table whose separator is sep, opened at line under the
// attribute line a. A table of comma or colon separated values, or with a
// separator or format of its own, is not read: the table target refuses
// it.
func newTable(a *attrs, sep byte, line int) *table {
	t := &table{sep: sep, line: line}
	if sep == ',' || sep == ':' {
		t.err = t.refuse("is in CSV or DSV form")
	}
	if a != nil {
		t.readAttrs(a)
	}
	return t
}

// readAttrs reads the table's attribute line a: its format, separator,
// header options and cols.
func (t *table) readAttrs(a *attrs) {
	if f := a.named["format"]; f != "" && f != "psv" {
		t.err = t.refuse("has the format " + core.Quote(f))
	}
	if a.named["separator"] != "" {
		t.err = t.refuse("has a separator of its own")
	}
	switch {
	case a.opts["header"]:
		t.header = 1
	case a.opts["noheader"]:
		t.header = -1
	}
	cols, ok := columns(a.named["cols"])
	if !ok {
		t.err = t.refuse("has cols " + core.Quote(a.named["cols"]) + ", a form")
	}
	t.cols = cols
}

// refuse is the refusal of the table for the reason why.
func (t *table) refuse(why string) error {
	return fmt.Errorf("line %d: the table %s, which the asciidoc extractor does not read; a regex extractor can still read the file", t.line, why)
}

// columns is how many columns a cols attribute gives, as Asciidoctor's
// parse_colspecs reads it, blanks left out: a number alone is that many,
// and otherwise the entries split at commas, or at semicolons when there is
// no comma, are one each, or n for an entry n* before its specifier, an
// empty entry one too. 0 is no cols; ok is false for a form Asciidoctor
// does not read, or a count of none.
func columns(cols string) (n int, ok bool) {
	cols = strings.Map(dropBlank, cols)
	if cols == "" {
		return 0, true
	}
	if cols[0] == '-' || allDigits(cols) && (cols == "0" || cols[0] != '0') {
		return countColumns(cols)
	}
	return specColumns(cols)
}

// dropBlank is strings.Map's mapping that leaves out spaces and tabs.
func dropBlank(r rune) rune {
	if r == ' ' || r == '\t' {
		return -1
	}
	return r
}

// countColumns is how many columns cols gives as a number alone.
func countColumns(cols string) (int, bool) {
	count, err := strconv.Atoi(cols)
	if err != nil && allDigits(cols) {
		count = math.MaxInt32 // no row of the table is that long
	}
	return min(count, math.MaxInt32), count > 0
}

// specColumns is how many columns cols gives as a list of entries.
func specColumns(cols string) (n int, ok bool) {
	sep := ";"
	if strings.Contains(cols, ",") {
		sep = ","
	}
	for _, e := range strings.Split(cols, sep) {
		k, ok := entryColumns(e)
		if !ok {
			return 0, false
		}
		n = min(n+k, math.MaxInt32)
	}
	return n, n > 0
}

// entryColumns is how many columns one entry of cols gives: n for n*
// before its specifier, one otherwise. ok is false for a form Asciidoctor
// does not read.
func entryColumns(e string) (int, bool) {
	k := 1
	if times, spec, found := strings.Cut(e, "*"); found {
		if !allDigits(times) {
			return 0, false
		}
		v, err := strconv.Atoi(times)
		if err != nil {
			v = math.MaxInt32 // no row of the table is that long
		}
		k, e = min(v, math.MaxInt32), spec
	}
	return k, isColSpec(e)
}

// allDigits reports whether s is one or more ASCII digits.
func allDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// isColSpec reports whether e is a column specifier after its multiplier,
// as Asciidoctor's ColumnSpecRx reads one: a horizontal alignment, then a
// dot and a vertical one, the vertical one optional after a horizontal one
// (<.), a width (digits, a percentage, or ~), and a style letter, each
// optional.
func isColSpec(e string) bool {
	e = skipWidth(skipAlignment(e))
	if e != "" && 'a' <= e[0] && e[0] <= 'z' {
		e = e[1:]
	}
	return e == ""
}

// skipAlignment is e after the alignments that start it: a horizontal one,
// a dot and a vertical one, the vertical one optional after a horizontal
// one.
func skipAlignment(e string) string {
	horizontal := e != "" && strings.IndexByte("<^>", e[0]) >= 0
	if horizontal {
		e = e[1:]
	}
	return skipVertical(e, horizontal)
}

// skipVertical is e after a dot and a vertical alignment, or after a lone
// dot when a horizontal alignment came before.
func skipVertical(e string, horizontal bool) string {
	switch {
	case len(e) > 1 && e[0] == '.' && strings.IndexByte("<^>", e[1]) >= 0:
		return e[2:]
	case horizontal && strings.HasPrefix(e, "."):
		return e[1:]
	}
	return e
}

// skipWidth is e after the width that starts it: digits, a percentage, or
// ~.
func skipWidth(e string) string {
	if strings.HasPrefix(e, "~") {
		return e[1:]
	}
	w := strings.TrimLeft(e, "0123456789")
	if len(w) < len(e) {
		w = strings.TrimPrefix(w, "%")
	}
	return w
}

// read reads a line of the content of a table the reader reads, blank or
// not.
func (t *table) read(p *parser, line []byte, blank bool) error {
	if t.lines == 1 && !t.decided {
		t.decided, t.blankAfter = true, blank
	}
	if blank {
		if t.open {
			t.buf = append(t.buf, '\n')
		}
		return nil
	}
	return t.readLine(p, line)
}

// readLine reads a line of cells, which is not blank.
func (t *table) readLine(p *parser, line []byte) error {
	first, cells := true, 0
	seg := t.seg[:0]
	for i := 0; i < len(line); i++ {
		if t.escapedSep(line, i) {
			seg = append(seg, t.sep)
			i++
			continue
		}
		if line[i] != t.sep {
			seg = append(seg, line[i])
			continue
		}
		stop, err := t.endSeg(p, seg, first)
		if stop || err != nil {
			return err
		}
		first, seg = false, seg[:0]
		cells++
	}
	t.endLine(seg, first, cells)
	return nil
}

// escapedSep reports whether line holds a backslash and the separator at
// i, which is the separator as text.
func (t *table) escapedSep(line []byte, i int) bool {
	return line[i] == '\\' && i+1 < len(line) && line[i+1] == t.sep
}

// endSeg reads seg, the text before a separator, first when it is the
// first of its line, into the cell open, and opens the cell after it. stop
// is true when the table is refused, for a cell that spans or repeats.
func (t *table) endSeg(p *parser, seg []byte, first bool) (stop bool, err error) {
	text, span := cellSpec(seg, first)
	if span {
		t.err = t.refuse(fmt.Sprintf("has a cell that spans columns or rows or is repeated, at line %d", p.n))
		return true, nil
	}
	if t.open { // an empty text adds a blank the cell's value leaves out
		if first {
			t.buf = append(t.buf, '\n')
		}
		t.buf = append(t.buf, text...)
	}
	return false, t.newCell(p)
}

// endLine reads seg, the text after a line's last separator, first when
// the line has none, and counts the line, which has cells separators.
func (t *table) endLine(seg []byte, first bool, cells int) {
	switch {
	case first && t.open:
		t.buf = append(t.buf, '\n')
		t.buf = append(t.buf, seg...)
	case !first:
		t.buf = append(t.buf, seg...)
	}
	t.seg = seg
	if t.lines == 0 {
		t.firstCells = cells
	}
	t.lines++
}

// newCell starts a cell at the line read.
func (t *table) newCell(p *parser) error {
	if err := p.piece(); err != nil {
		return err
	}
	t.endCell()
	t.cells = append(t.cells, cell{start: len(t.buf), line: p.n})
	t.open = true
	return nil
}

// endCell ends the cell open, if one is.
func (t *table) endCell() {
	if t.open {
		t.cells[len(t.cells)-1].end = len(t.buf)
	}
}

// cellSpec is the text of seg, the text before a separator, less the cell
// specifier at its end, which says how the next cell is laid out: alone on
// its line before the line's first separator, or after a blank. span is
// whether the specifier spans or repeats the cell, which the reader does
// not read.
func cellSpec(seg []byte, first bool) (text []byte, span bool) {
	if first {
		if ok, span := isSpec(bytes.TrimLeft(seg, " \t")); ok {
			return nil, span
		}
	}
	i := bytes.LastIndexAny(seg, " \t")
	if i < 0 {
		return seg, false
	}
	if ok, span := isSpec(seg[i+1:]); ok {
		return bytes.TrimRight(seg[:i], " \t"), span
	}
	return seg, false
}

// isSpec reports whether b is a cell specifier, as Asciidoctor reads one:
// a factor and * or + for a repeat or a span, a horizontal alignment, a dot
// and a vertical one, and a style letter, each optional. span is whether it
// has the factor.
func isSpec(b []byte) (ok, span bool) {
	i, span := specFactor(b)
	i = skipVerticalAt(b, skipByteIn(b, i, "<^>"))
	if i < len(b) && 'a' <= b[i] && b[i] <= 'z' {
		i++
	}
	return i == len(b), i == len(b) && span
}

// specFactor is the end of the factor and * or + that start a cell
// specifier, 0 for none, and whether it has one.
func specFactor(b []byte) (int, bool) {
	i := factorEnd(b)
	if i < len(b) && (b[i] == '*' || b[i] == '+') && bytes.ContainsAny(b[:i], "0123456789") {
		return i + 1, true
	}
	return 0, false
}

// factorEnd is the end of the digits and dots that start b.
func factorEnd(b []byte) int {
	i := 0
	for i < len(b) && ('0' <= b[i] && b[i] <= '9' || b[i] == '.') {
		i++
	}
	return i
}

// skipByteIn is i, or i+1 when the byte of b at i is one of set.
func skipByteIn(b []byte, i int, set string) int {
	if i < len(b) && strings.IndexByte(set, b[i]) >= 0 {
		return i + 1
	}
	return i
}

// skipVerticalAt is i, or the index after a dot and a vertical alignment
// at i.
func skipVerticalAt(b []byte, i int) int {
	if i+1 < len(b) && b[i] == '.' && strings.IndexByte("<^>", b[i+1]) >= 0 {
		return i + 2
	}
	return i
}

// finish appends the table's items: the cell under the select's column in
// each row, or in each row whose cell under its key is its row, read as
// prose, at the row's line. A table without a header row, or without a
// header the select names, gives none, and a row the table ends before is
// left out, as Asciidoctor drops it.
func (t *table) finish(p *parser) error {
	if t.err != nil {
		return t.err
	}
	t.endCell()
	n := t.headerColumns()
	if n == 0 || len(t.cells) < n {
		return nil
	}
	col, key, ok := p.sel.Columns(t.headers(p, n))
	if !ok {
		return nil
	}
	return t.addRows(p, n, col, key)
}

// headerColumns is the columns of the table's header row, 0 when it has
// none.
func (t *table) headerColumns() int {
	if t.header < 0 || t.header == 0 && (!t.decided || !t.blankAfter) {
		return 0
	}
	if t.cols == 0 {
		return t.firstCells
	}
	return t.cols
}

// headers is the prose of each of the header row's n cells.
func (t *table) headers(p *parser, n int) []string {
	headers := make([]string, n)
	for i := range headers {
		headers[i] = t.text(p, t.cells[i])
	}
	return headers
}

// addRows appends the cell under column col of each row of n cells after
// the header, or of each whose cell under column key, -1 for none, is the
// select's row.
func (t *table) addRows(p *parser, n, col, key int) error {
	for r := n; r+n <= len(t.cells); r += n {
		row := t.cells[r : r+n]
		if key >= 0 && t.text(p, row[key]) != p.sel.Row {
			continue
		}
		start := len(p.d.text)
		p.d.text = p.marks.appendProse(p.d.text, t.buf[row[col].start:row[col].end])
		from, end := markup.TrimBlanks(p.d.text, start, len(p.d.text))
		if err := p.add(markup.Item{Start: from, End: end, Line: row[0].line, Fixed: true}); err != nil {
			return err
		}
	}
	return nil
}

// text is a cell's prose, with no blank at either end.
func (t *table) text(p *parser, c cell) string {
	return strings.TrimSpace(string(p.marks.appendProse(nil, t.buf[c.start:c.end])))
}
