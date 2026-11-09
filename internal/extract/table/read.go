package table

import (
	"context"
	"errors"
	"fmt"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// extractor is a built table extractor of one file: the dialect, the
// column it returns and the criteria a row must meet.
type extractor struct {
	core.Source
	d   *dialect
	col int
	row []criterion
	// valueLimit is the most values the extractor returns, 0 for
	// core.MaxValues: a test lowers it to witness the bound.
	valueLimit int
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (x extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return x.Extract(ctx, root)
}

// Extract reads the file under root and returns the values the spec
// selects.
func (x extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, x.Source, x)
}

// FromShared returns the values of the column in the rows data holds that
// the spec selects, in file order. A read of a table has its own work
// budget, and spends none of a set's.
func (x extractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	m := core.NewMeter(len(data) + x.specBytes())
	mk, err := newMarker(x.File, data, m)
	if err != nil {
		return nil, x.fail(err)
	}
	c := collector{x: &x, out: []check.Value{}}
	err = x.d.read(data, m, mk, c.visit)
	switch {
	case c.stop != nil:
		return nil, c.stop
	case err != nil:
		return nil, x.fail(err)
	}
	return c.out, nil
}

// specBytes are the bytes of the values the row criteria compare, which
// the work budget counts beside the file's.
func (x extractor) specBytes() int {
	n := 0
	for _, c := range x.row {
		n += len(c.want)
	}
	return n
}

// fail names the file in err, which its read returned.
func (x extractor) fail(err error) error {
	if errors.Is(err, core.ErrWork) {
		return fmt.Errorf("%s: reading the table and matching its rows take more than %d units of work for each byte of the file and the spec, the limit; narrow the spec", x.File, core.WorkPerByte)
	}
	return fmt.Errorf("%s: %w", x.File, err)
}

// maxValues is the most values the extractor returns.
func (x *extractor) maxValues() int {
	if x.valueLimit == 0 {
		return core.MaxValues
	}
	return x.valueLimit
}

// match is whether a row holds a criterion's value: no, yes, or perhaps,
// where a cell that does not hold it is one whose text a template's
// engine writes, which may render as the value.
type match uint8

const (
	no match = iota
	yes
	perhaps
)

// selects reports whether the spec selects r, and whether it does only
// perhaps, since a template computes a cell a criterion compares.
func (x *extractor) selects(r row) (selected, maybe bool) {
	for _, cr := range x.row {
		m := x.holds(r[cr.col], cr)
		if m == no {
			return false, false
		}
		maybe = maybe || m == perhaps
	}
	return true, maybe
}

// holds is whether one of cells, a column's, holds cr's value.
func (x *extractor) holds(cells []cell, cr criterion) match {
	m := no
	fold := x.d.columns[cr.col].fold
	for _, c := range cells {
		if equal(c.text, cr.want, fold) {
			return yes
		}
		if c.varies {
			m = perhaps
		}
	}
	return m
}

// equal reports whether text is want, in any case of ASCII letters when
// fold is set; want is in lower case then.
func equal(text []byte, want string, fold bool) bool {
	if len(text) != len(want) {
		return false
	}
	for i, c := range text {
		if fold && 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != want[i] {
			return false
		}
	}
	return true
}

// collector is one read's values so far.
type collector struct {
	x     *extractor
	out   []check.Value
	total int64
	stop  error // the bound a value passed, which ends the read
}

// errStop ends a read from inside a visit.
var errStop = errors.New("stopped")

// visit takes the column's cells of r when the spec selects r, each a
// value, not literal where a template computes it or computes a cell
// the row was selected by.
func (c *collector) visit(r row) error {
	selected, maybe := c.x.selects(r)
	if !selected {
		return nil
	}
	for _, cl := range r[c.x.col] {
		if err := c.add(cl, maybe); err != nil {
			return err
		}
	}
	return nil
}

// add keeps cl as a value, within the bounds on the count of values and on
// their bytes, max_bytes: a default's text and an exports default option
// repeated for each client are no part of the file.
func (c *collector) add(cl cell, maybe bool) error {
	if c.total += int64(len(cl.text)); c.total > c.x.Limit {
		c.stop = fmt.Errorf("%s: the values the extractor selects hold more than %d bytes in all, the extractor's max_bytes; a default and an exports option written once for many clients hold more than the file, so narrow the row, or set max_bytes: higher", c.x.File, c.x.Limit)
		return errStop
	}
	if len(c.out) == c.x.maxValues() {
		c.stop = core.TooManyValues(c.x.File)
		return errStop
	}
	c.out = append(c.out, check.Value{Text: string(cl.text), At: check.Location{File: c.x.File, Line: cl.line}, NotLiteral: cl.computed || maybe})
	return nil
}

// marker marks the cells of a template's lines that the engine computes:
// every cell of a line inside one of its blocks, which may not render or
// render more than once, or holding its marks, whose text the engine
// writes. The marks are found on the whole line, since a Jinja expression
// holds spaces and a line's split cuts it into words that hold none.
type marker struct {
	blocks *core.Blocks
	test   func(*core.Entry) bool // nil for a file that is no template
	// computed and varies are the marks of the line given last: inside a
	// block or holding marks, and holding marks.
	computed, varies bool
}

// newMarker is the marker of file, whose bytes are data, charged to m.
func newMarker(file string, data []byte, m *core.Meter) (*marker, error) {
	blocks, err := core.TemplateBlocks(file, data, m)
	return &marker{blocks: blocks, test: core.TemplateNotLiteral(file, nil)}, err
}

// line notes line n, whose text a row is read from, so the cells made
// next are marked as it is.
func (mk *marker) line(n int, text []byte) error {
	if mk.test == nil {
		return nil
	}
	in, err := mk.blocks.Holds(n)
	mk.varies = mk.test(&core.Entry{Value: text})
	mk.computed = in || mk.varies
	return err
}

// cell is a cell of text at line n, the line given last.
func (mk *marker) cell(text []byte, n int) cell {
	return cell{text: text, line: n, computed: mk.computed, varies: mk.varies}
}
