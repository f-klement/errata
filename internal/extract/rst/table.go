package rst

import (
	"bytes"
	"strconv"
	"unicode/utf8"
)

// gridBorder reports whether t is the top border of a grid table: + then
// runs of -, each ended by +.
func gridBorder(t []byte) bool {
	t = bytes.TrimRight(t, " \t")
	return len(t) >= 3 && t[0] == '+' && t[len(t)-1] == '+' && len(bytes.Trim(t, "+-")) == 0 && !bytes.Contains(t, []byte("++"))
}

// grid reads the grid table whose top border is line i, at column ind: its
// lines up to a blank line, each starting with + or |. The row above the
// border of = is its header, and a table with no such border has none, so
// it gives no value. A row is the lines between two borders, and its cell
// under each column the text between the | at the columns of the top
// border's +, each line's text joined by a space. Where the table target
// reads the table, a header of more than one row, and a line that the top
// border's columns do not cut, as where a cell spans columns or rows, are
// refused; for any other target the table is passed over. It returns the
// line after the table.
func grid(d *doc, r *reading, i, ind int) int {
	g := newGridReading(d, r, i, ind)
	j := i + 1
	for ; j < d.lines() && !d.blank(j); j++ {
		t, ok := g.holds(j)
		if !ok {
			break
		}
		if g.broken {
			continue
		}
		if g.line(j, t) {
			return j
		}
	}
	return j
}

// gridReading is what grid knows of a table as it reads it.
type gridReading struct {
	d                     *doc
	r                     *reading
	ind                   int
	bounds                []int // the columns of the top border's +
	head, cells           [][]span
	headed, reads, broken bool
	rowLine, rows         int // the line the row being read starts at, and the header's rows before it
}

// newGridReading starts the grid table whose top border is line i, at column
// ind.
func newGridReading(d *doc, r *reading, i, ind int) *gridReading {
	_, off := d.indent(i)
	g := &gridReading{d: d, r: r, ind: ind}
	for k, c := range bytes.TrimRight(d.line(i)[off:], " \t") {
		if c == '+' {
			g.bounds = append(g.bounds, ind+k)
		}
	}
	n := len(g.bounds) - 1
	g.head, g.cells = make([][]span, n), make([][]span, n)
	return g
}

// holds is line j from its first character, and reports whether it is a
// line of the table: at the table's column, starting with + or |.
func (g *gridReading) holds(j int) ([]byte, bool) {
	c, o := g.d.indent(j)
	t := g.d.line(j)[o:]
	return t, c == g.ind && (t[0] == '+' || t[0] == '|')
}

// line reads line j, t from its first character, and reports whether the
// table ends at it with a refusal.
func (g *gridReading) line(j int, t []byte) bool {
	if t[0] == '+' {
		return g.border(j, t)
	}
	return g.cellLine(j)
}

// border reads a border, which ends a row, and the header after its first
// border of =.
func (g *gridReading) border(j int, t []byte) bool {
	fill, ok := separator(t, g.bounds, g.ind)
	if g.refusesBorder(j, fill, ok) {
		return true
	}
	switch {
	case !ok:
		g.broken = true
	case !g.headed:
		g.addHead(fill)
	case g.reads && g.rowLine > 0:
		g.r.row(g.d, g.cells, g.rowLine)
	}
	g.cells, g.rowLine = make([][]span, len(g.bounds)-1), 0
	return false
}

// refusesBorder refuses, where the table target reads the table, a line
// of + that is no border of its columns, and a second row of the header.
func (g *gridReading) refusesBorder(j int, fill byte, ok bool) bool {
	if !g.r.readsTables() {
		return false
	}
	switch {
	case !ok:
		g.r.refuse(j+1, spans)
	case !g.headed && fill == '=' && g.rows > 0:
		g.r.refuse(j+1, "the header of the grid table above has more than one row, which the rst extractor does not read")
	default:
		return false
	}
	return true
}

// addHead adds the row just read to the header, which a border of fill =
// ends.
func (g *gridReading) addHead(fill byte) {
	for k := range g.head {
		g.head[k] = append(g.head[k], g.cells[k]...)
	}
	g.rows++
	if fill == '=' {
		g.headed = true
		g.reads = g.r.header(texts(g.d, g.head))
	}
}

// cellLine reads a line of cells, and reports whether the table ends at it
// with a refusal: one the top border's columns do not cut, where the table
// target reads the table.
func (g *gridReading) cellLine(j int) bool {
	l := g.d.line(j)
	offs := offsets(l, g.bounds)
	g.broken = !cutAt(l, offs, len(g.bounds))
	if g.broken && g.r.readsTables() {
		g.r.refuse(j+1, spans)
		return true
	}
	if g.broken {
		return false
	}
	if g.rowLine == 0 {
		g.rowLine = j + 1
	}
	for k := range len(g.bounds) - 1 {
		if s := g.d.cell(j, offs[k]+1, offs[k+1]); s.end > s.start {
			g.cells[k] = append(g.cells[k], s)
		}
	}
	return false
}

// cutAt reports whether l reaches each of n columns, at offs, with a | at
// each.
func cutAt(l []byte, offs []int, n int) bool {
	if len(offs) < n {
		return false
	}
	for _, at := range offs {
		if at == len(l) || l[at] != '|' {
			return false
		}
	}
	return true
}

// spans is the refusal of a grid table line that the top border's columns
// do not cut.
const spans = "a cell of the grid table here spans rows or columns, which the rst extractor does not read"

// separator is the character that fills t, a line of a grid table from its
// first character at column ind, when t is a border of the table whose +
// are at the columns bounds: a + at each of them, and - or = between. ok
// is false when t is no such border.
func separator(t []byte, bounds []int, ind int) (fill byte, ok bool) {
	t = bytes.TrimRight(t, " \t")
	if len(t) != bounds[len(bounds)-1]-ind+1 {
		return 0, false
	}
	fill = t[1]
	if fill != '-' && fill != '=' {
		return 0, false
	}
	k := 0
	for p, c := range t {
		if p == bounds[k]-ind {
			if c != '+' {
				return 0, false
			}
			k++
			continue
		}
		if c != fill {
			return 0, false
		}
	}
	return fill, true
}

// simpleBorder is the offsets in t of the runs of = that make t a border
// of a simple table, t being = and spaces with at least two runs of =, or
// nil when t is none. One run would be a title's adornment.
func simpleBorder(t []byte) []int {
	t = bytes.TrimRight(t, " \t")
	var starts []int
	for k, c := range t {
		switch {
		case c == '=':
			if k == 0 || t[k-1] == ' ' {
				starts = append(starts, k)
			}
		case c != ' ':
			return nil
		}
	}
	if len(starts) < 2 {
		return nil
	}
	return starts
}

// isRule reports whether t is - and spaces only: the line under a header
// cell of a simple table that spans columns.
func isRule(t []byte) bool {
	return len(bytes.Trim(t, "- \t")) == 0
}

// simple reads the simple table whose top border is line i, at column
// ind. Its columns start at the runs of = of the top border, and the last
// runs to the end of its lines. A line whose first column is blank goes on
// the row before. The row between the top border and a border followed by
// more of the table is its header; a table with no such border, only a top
// and a bottom, has none and gives no value. Where the table target reads
// the table, a header of more than one row, or with a rule under a cell
// that spans columns, is refused. The table ends at a border followed by a
// blank line, a line indented less than the table, as where a list item's
// block ends, or the end of the file, as docutils ends it (rst delta review
// 1, finding 1); blank lines inside it are passed over. A table that reaches the end of the file, or a line indented less
// than it, with no such border is refused, whatever the target, as
// docutils reads no table there. It returns the line after.
func simple(d *doc, r *reading, i, ind int) int {
	s := newSimpleReading(d, r, i, ind)
	j := i + 1
	for ; j < d.lines(); j++ {
		if d.blank(j) {
			continue
		}
		c, o := d.indent(j)
		if c < ind {
			break
		}
		t := d.line(j)[o:]
		if c == ind && simpleBorder(t) != nil {
			if next, done := s.border(j); done {
				return next
			}
			continue
		}
		s.textLine(j, t)
	}
	r.refuse(i+1, "the simple table that starts here has no bottom border, a border of = followed by a blank line, a line indented less than the table or the end of the file, so docutils reads no table, and errata would read the rest as its rows")
	return j
}

// simpleReading is what simple knows of a table as it reads it.
type simpleReading struct {
	d                       *doc
	r                       *reading
	i, ind                  int      // the top border's line and column
	cols                    []int    // the columns the top border's runs of = start at
	head, cells             [][]span // the header's cells, and the row being read, nil before the first
	headed, reads, headLine bool
	bad                     int // the first line of the rows above the header border that make it more than one row
	rowLine                 int
}

// newSimpleReading starts the simple table whose top border is line i, at
// column ind.
func newSimpleReading(d *doc, r *reading, i, ind int) *simpleReading {
	_, off := d.indent(i)
	starts := simpleBorder(d.line(i)[off:])
	s := &simpleReading{d: d, r: r, i: i, ind: ind, cols: make([]int, len(starts))}
	for k, start := range starts {
		s.cols[k] = ind + start
	}
	return s
}

// flush hands the row being read to the target, if it reads the table.
func (s *simpleReading) flush() {
	if s.cells != nil && s.reads {
		s.r.row(s.d, s.cells, s.rowLine)
	}
	s.cells = nil
}

// border reads the border at line j: the header's, when more of the table
// follows it and the table has no header yet, and otherwise its last. It
// reports whether the table ends there, and the line after it.
func (s *simpleReading) border(j int) (int, bool) {
	if !s.headed && s.continuesAfter(j) {
		if s.bad > 0 && s.r.readsTables() {
			s.r.refuse(s.bad, "the header of the simple table that starts at line "+strconv.Itoa(s.i+1)+" has more than one row or a cell that spans columns, which the rst extractor does not read")
			return j, true
		}
		s.headed = true
		s.reads = s.r.header(texts(s.d, s.head))
		return 0, false
	}
	s.flush()
	return j + 1, true
}

// continuesAfter reports whether the table goes on after line j: the next
// line holds text, indented as far as the table at least.
func (s *simpleReading) continuesAfter(j int) bool {
	if j+1 >= s.d.lines() || s.d.blank(j+1) {
		return false
	}
	next, _ := s.d.indent(j + 1)
	return next >= s.ind
}

// textLine reads line j, t from its first character, a line of the
// header or of a row, or a rule under a header cell.
func (s *simpleReading) textLine(j int, t []byte) {
	l := s.d.line(j)
	offs := offsets(l, s.cols)
	first := s.d.cell(j, offs[0], end(l, offs, 0))
	s.markBad(j, t, first)
	if isRule(t) {
		return
	}
	s.headLine = true
	row := s.rowFor(j, first)
	for k := range offs {
		if c := s.d.cell(j, offs[k], end(l, offs, k)); c.end > c.start {
			for len(*row) <= k {
				*row = append(*row, nil)
			}
			(*row)[k] = append((*row)[k], c)
		}
	}
}

// markBad notes line j, t from its first character and first its first
// cell, when it makes the header more than one row: a rule, or a second
// line whose first cell holds text.
func (s *simpleReading) markBad(j int, t []byte, first span) {
	if !s.headed && s.bad == 0 && (isRule(t) || s.headLine && first.end > first.start) {
		s.bad = j + 1
	}
}

// rowFor is the row line j adds to: the header before its border, and
// after it the row being read, or a new one when the first cell, first,
// holds text.
func (s *simpleReading) rowFor(j int, first span) *[][]span {
	if !s.headed {
		return &s.head
	}
	if first.end > first.start || s.cells == nil {
		s.flush()
		s.cells, s.rowLine = [][]span{}, j+1
	}
	return &s.cells
}

// end is where the cell of column k ends in l, whose columns start at
// offs: where the next starts, or the end of l for the last.
func end(l []byte, offs []int, k int) int {
	if k+1 < len(offs) {
		return offs[k+1]
	}
	return len(l)
}

// texts is the prose of each cell of a header.
func texts(d *doc, head [][]span) []string {
	out := make([]string, len(head))
	for k, ls := range head {
		out[k] = plain(d, ls)
	}
	return out
}

// offsets is the offset in l of each of cols, columns in ascending order,
// up to the first that l does not reach: of the first character at or
// after the column. A tab moves to the next multiple of 8, and every other
// character takes one column. One pass over l, which stops at its end, so a
// short line of a table with many columns costs what the line does.
func offsets(l []byte, cols []int) []int {
	var out []int
	off, col := 0, 0
	for _, want := range cols {
		for off < len(l) && col < want {
			col = advance(col, l[off])
			_, size := utf8.DecodeRune(l[off:])
			off += size
		}
		if col < want {
			break
		}
		out = append(out, off)
	}
	return out
}

// cell is the text of line i from offset from to end, without the blanks
// at either end; empty when it holds none.
func (d *doc) cell(i, from, end int) span {
	from = min(from, end)
	seg := d.line(i)[from:end]
	lead := len(seg) - len(bytes.TrimLeft(seg, blanks))
	return d.piece(i, from+lead, from+lead+len(bytes.Trim(seg, blanks)))
}
