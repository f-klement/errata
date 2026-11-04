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
	_, off := d.indent(i)
	var bounds []int // the columns of the top border's +
	for k, c := range bytes.TrimRight(d.line(i)[off:], " \t") {
		if c == '+' {
			bounds = append(bounds, ind+k)
		}
	}
	n := len(bounds) - 1
	head, cells := make([][]span, n), make([][]span, n)
	headed, reads, broken := false, false, false
	rowLine, rows := 0, 0 // the line the row being read starts at, and the header's rows before it
	j := i + 1
	for ; j < d.lines() && !d.blank(j); j++ {
		c, o := d.indent(j)
		t := d.line(j)[o:]
		if c != ind || (t[0] != '+' && t[0] != '|') {
			break
		}
		if broken {
			continue
		}
		if t[0] == '+' {
			fill, ok := separator(t, bounds, ind)
			switch {
			case !ok && r.readsTables():
				r.refuse(j+1, spans)
				return j
			case !ok:
				broken = true
			case !headed && fill == '=' && rows > 0 && r.readsTables():
				r.refuse(j+1, "the header of the grid table above has more than one row, which the rst extractor does not read")
				return j
			case !headed:
				for k := range head {
					head[k] = append(head[k], cells[k]...)
				}
				rows++
				if fill == '=' {
					headed = true
					reads = r.header(texts(d, head))
				}
			case reads && rowLine > 0:
				r.row(d, cells, rowLine)
			}
			cells, rowLine = make([][]span, n), 0
			continue
		}
		l := d.line(j)
		offs := offsets(l, bounds)
		broken = len(offs) < len(bounds)
		for _, at := range offs {
			if at == len(l) || l[at] != '|' {
				broken = true
			}
		}
		if broken && r.readsTables() {
			r.refuse(j+1, spans)
			return j
		}
		if broken {
			continue
		}
		if rowLine == 0 {
			rowLine = j + 1
		}
		for k := range n {
			if s := d.cell(j, offs[k]+1, offs[k+1]); s.end > s.start {
				cells[k] = append(cells[k], s)
			}
		}
	}
	return j
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
	_, off := d.indent(i)
	starts := simpleBorder(d.line(i)[off:])
	cols := make([]int, len(starts))
	for k, s := range starts {
		cols[k] = ind + s
	}
	var head, cells [][]span // the header's cells, and the row being read, nil before the first
	headed, reads, headLine := false, false, false
	bad := 0 // the first line of the rows above the header border that make it more than one row
	rowLine := 0
	flush := func() {
		if cells != nil && reads {
			r.row(d, cells, rowLine)
		}
		cells = nil
	}
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
			more := j+1 < d.lines() && !d.blank(j+1)
			if more {
				next, _ := d.indent(j + 1)
				more = next >= ind
			}
			if !headed && more {
				if bad > 0 && r.readsTables() {
					r.refuse(bad, "the header of the simple table that starts at line "+strconv.Itoa(i+1)+" has more than one row or a cell that spans columns, which the rst extractor does not read")
					return j
				}
				headed = true
				reads = r.header(texts(d, head))
				continue
			}
			flush()
			return j + 1
		}
		l := d.line(j)
		offs := offsets(l, cols)
		first := d.cell(j, offs[0], end(l, offs, 0))
		if !headed && bad == 0 && (isRule(t) || headLine && first.end > first.start) {
			bad = j + 1
		}
		if isRule(t) {
			continue
		}
		headLine = true
		row := &head
		if headed {
			if first.end > first.start || cells == nil {
				flush()
				cells, rowLine = [][]span{}, j+1
			}
			row = &cells
		}
		for k := range offs {
			if s := d.cell(j, offs[k], end(l, offs, k)); s.end > s.start {
				for len(*row) <= k {
					*row = append(*row, nil)
				}
				(*row)[k] = append((*row)[k], s)
			}
		}
	}
	r.refuse(i+1, "the simple table that starts here has no bottom border, a border of = followed by a blank line, a line indented less than the table or the end of the file, so docutils reads no table, and errata would read the rest as its rows")
	return j
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
