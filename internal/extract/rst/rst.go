// Package rst is the rst extractor: the comments, sections, code blocks and
// table cells of a reStructuredText file. No maintained parser of
// reStructuredText in pure Go was found, so the parse is this package's
// own, of the common forms only (parse.go). What a rule writes and the
// values it gets are core/markup's, which the readers of every markup
// share, so one rule reads a claim the same way in Markdown and in rST.
package rst

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
)

// Spec reads one target of File, a reStructuredText file, as
// markup.Selector says: each comment, the prose of each section, each
// literal or code block, or each cell of a grid or simple table. A code
// block's language is the argument of its code-block, code or sourcecode
// directive; a literal block has none.
type Spec struct {
	core.OneFile
	markup.Selector
}

// Kind is the rst kind, which a rule writes under the key rst.
var Kind = core.Kind[Spec]{Key: "rst", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is t with only what decides where it reads: the file cleaned, and
// no max_bytes, since a limit does not change what is read.
func Place(t Spec) Spec {
	t.OneFile = t.Place()
	return t
}

// Resolve is t with its max_bytes written out, for errata list.
func Resolve(t Spec) Spec {
	t.OneFile = t.Resolved()
	return t
}

// Describe is what the kind's extractor returns: flat values, each literal.
func Describe(Spec) check.Description {
	return check.Description{Label: "rst"}
}

// maxTextRatio is how many times the size of its file the pieces the
// extractor reads may be in all. The text they are cut from is no longer
// than the file, give or take a line end, since each piece of it is a line
// of the file with markup left out. Pieces overlap only where sections of
// one heading path nest, "A" under "A", and rST nests sections as deep as
// it has title styles: 64, the 32 punctuation characters with an overline
// or without, against Markdown's six levels. So the sum is held to a bound
// of its own, the markdown extractor's twelve, before a value is copied,
// and the search's time limit, which scales with the file's limit, holds
// with it.
const maxTextRatio = 12

// extractor reads its target of its file.
type extractor struct {
	src core.Source
	sel *markup.Select
}

// Build refuses what markup.Selector.Build refuses, and the file as every
// reader of one file refuses it, under the name rst.
func Build(t Spec) (check.Extractor, error) {
	src, err := t.Check(Kind.Key)
	if err != nil {
		return nil, err
	}
	sel, err := t.Build(Kind.Key, src)
	if err != nil {
		return nil, err
	}
	return extractor{src: src, sel: sel}, nil
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract returns the values of the file under root.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, e.src, e)
}

// FromShared is FromData: an rst read is one linear pass, and spends none
// of a set's budget.
func (e extractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them.
func (e extractor) FromData(data []byte) ([]check.Value, error) {
	r := newReading(e.sel)
	parse(newDoc(data), r)
	if r.err != nil {
		return nil, fmt.Errorf("%s: %w", e.src.File, r.err)
	}
	r.finish()
	total := 0
	for _, it := range r.items {
		total += it.End - it.Start
	}
	if err := checkRatio(e.src.File, total, len(data)); err != nil {
		return nil, err
	}
	return e.sel.Values(e.src.File, r.text.buf, r.items, r.text.lineAt)
}

// checkRatio refuses pieces of more than maxTextRatio times the size of
// their file in all.
func checkRatio(file string, total, size int) error {
	if total > maxTextRatio*size {
		return fmt.Errorf("%s: the text read is %d bytes, over the limit of %d, %d times the file's size; %s",
			file, total, maxTextRatio*size, maxTextRatio, core.OtherWay)
	}
	return nil
}

// text is the text every item is a piece of, built from the file's lines,
// with the line each piece of it came from.
type text struct {
	buf    []byte
	pieces []piece
}

// piece is where in the text the bytes of one line of the file start.
type piece struct{ at, line int }

// add appends b, from line of the file.
func (t *text) add(b []byte, line int) {
	if n := len(t.pieces); n == 0 || t.pieces[n-1].line != line {
		t.pieces = append(t.pieces, piece{at: len(t.buf), line: line})
	}
	t.buf = append(t.buf, b...)
}

// lineAt is the line of the file that holds offset off of the text: the
// line of the last piece that starts at or before it.
func (t *text) lineAt(off int) int {
	k := sort.Search(len(t.pieces), func(k int) bool { return t.pieces[k].at > off })
	if k == 0 {
		return 1
	}
	return t.pieces[k-1].line
}

// reading is one read of a file for one target: the text, the items, and
// where the walk is in the heading path.
type reading struct {
	sel     *markup.Select
	text    text
	items   []markup.Item
	outline *markup.Outline // nil without a path
	open    int             // the named sections the walk is in
	styles  []style         // the title adornments, in the order they first appear
	depth   int             // the level of the section the walk is in, 0 before the first title
	err     error           // the first form the walk refused
	col     int             // the column a table target reads in the table the walk is in
	key     int             // its key column, -1 for none
}

// style is an adornment of a section title: its character, and whether it
// has an overline.
type style struct {
	char byte
	over bool
}

// newReading is a read for sel.
func newReading(sel *markup.Select) *reading {
	r := &reading{sel: sel}
	if sel.Path != nil {
		r.outline = markup.NewOutline(sel.Path)
	}
	return r
}

// inScope reports whether the walk is in a section of the path, or the
// select has none.
func (r *reading) inScope() bool { return r.outline == nil || r.open > 0 }

// refuse stops the walk at a form errata cannot read as docutils does, at
// line, which what says, unless it has stopped already.
func (r *reading) refuse(line int, what string) {
	if r.err == nil {
		r.err = fmt.Errorf("line %d: %s; %s", line, what, core.OtherWay)
	}
}

// level is the level of a title of style s, counted from 0: the order in
// which its style first appears in the file, as docutils counts it. A
// style seen before may be at most one level below the section the walk is
// in, and a new one only below the deepest level so far; docutils reads
// any other title as an error, and ok is false for it.
func (r *reading) level(s style) (int, bool) {
	level := slices.Index(r.styles, s)
	switch {
	case level == -1 && r.depth != len(r.styles):
		return 0, false
	case level == -1:
		r.styles = append(r.styles, s)
		level = len(r.styles) - 1
	case level > r.depth:
		return 0, false
	}
	r.depth = level + 1
	return level, true
}

// title enters the section of a title of style s, whose text, as prose, is
// t, at line. Its text is prose of the sections it lies in.
func (r *reading) title(s style, t string, line int) {
	level, ok := r.level(s)
	if !ok {
		r.refuse(line, "the title here is at a level inconsistent with the titles before it, which docutils reads as an error")
		return
	}
	if r.outline == nil {
		return // no path: every target reads the whole file
	}
	ends, named, _ := r.outline.Heading(level, t)
	r.open -= len(ends)
	if r.sel.Target != markup.TargetSection {
		if named {
			r.open++
		}
		return
	}
	for _, i := range ends {
		r.close(i)
	}
	if r.inScope() {
		r.text.add([]byte(t), line)
		r.text.add(newline, line)
	}
	if named {
		r.open++
		r.items = append(r.items, markup.Item{Start: len(r.text.buf), Line: line})
	}
}

// finish ends the sections still open at the end of the file, as a title
// above every level would: the path names no empty title.
func (r *reading) finish() {
	if r.sel.Target != markup.TargetSection {
		return
	}
	ends, _, _ := r.outline.Heading(-1, "")
	for _, i := range ends {
		r.close(i)
	}
}

// close ends section i where the text ends. An empty section's one
// match, an empty one, is at its title's line, which no piece of the text
// may give, since the next title's text starts where it ends.
func (r *reading) close(i int) {
	it := &r.items[i]
	it.End = r.end(it.Start)
	it.Fixed = it.End == it.Start
}

// end is the end of the text so far, less a newline at its end, for a
// section that starts at start.
func (r *reading) end(start int) int {
	end := len(r.text.buf)
	if end > start && r.text.buf[end-1] == '\n' {
		end--
	}
	return end
}

// prose appends a block of prose of lines ls, for a section target.
func (r *reading) prose(d *doc, ls []span) {
	if r.sel.Target != markup.TargetSection || !r.inScope() || len(ls) == 0 {
		return
	}
	inlineSpans(d, ls, r.text.add)
	r.text.add(newline, ls[len(ls)-1].line)
}

// comment appends a comment whose text is the lines ls, at line.
func (r *reading) comment(d *doc, ls []span, line int) {
	if r.sel.Target != markup.TargetComment || !r.inScope() {
		return
	}
	start := len(r.text.buf)
	r.lines(d, ls)
	from, end := markup.TrimBlanks(r.text.buf, start, len(r.text.buf))
	r.items = append(r.items, markup.Item{Start: from, End: end, Line: line, Fixed: true})
}

// code appends a code block of language lang whose lines are ls, at line.
func (r *reading) code(d *doc, lang string, ls []span, line int) {
	if r.sel.Target != markup.TargetCode || !r.inScope() || (r.sel.Language != "" && lang != r.sel.Language) {
		return
	}
	start := len(r.text.buf)
	r.lines(d, ls)
	end := len(r.text.buf)
	if len(ls) > 0 {
		r.text.add(newline, ls[len(ls)-1].line) // so that an empty match at its end is at its last line
	}
	r.items = append(r.items, markup.Item{Start: start, End: end, Line: line, Fixed: end == start})
}

// lines appends the lines ls as they are, a newline between each two.
func (r *reading) lines(d *doc, ls []span) {
	for k, s := range ls {
		if k > 0 {
			r.text.add(newline, s.line)
		}
		r.text.add(d.data[s.start:s.end], s.line)
	}
}

// readsTables reports whether the target reads the tables where the walk
// is.
func (r *reading) readsTables() bool { return r.sel.Target == markup.TargetTable && r.inScope() }

// header starts a table whose header cells are headers, and reports
// whether the target reads its rows.
func (r *reading) header(headers []string) bool {
	if !r.readsTables() {
		return false
	}
	col, key, ok := r.sel.Columns(headers)
	r.col, r.key = col, key
	return ok
}

// row appends the cell the target reads of a row of the table header
// started, whose cells are the lines of each, at line.
func (r *reading) row(d *doc, cells [][]span, line int) {
	if r.key >= 0 && plain(d, cellAt(cells, r.key)) != r.sel.Row {
		return
	}
	start := len(r.text.buf)
	inlineSpans(d, cellAt(cells, r.col), r.text.add)
	from, end := markup.TrimBlanks(r.text.buf, start, len(r.text.buf))
	r.items = append(r.items, markup.Item{Start: from, End: end, Line: line, Fixed: true})
}

// cellAt is the lines of cell k of a row, none when the row ends before it.
func cellAt(cells [][]span, k int) []span {
	if k < len(cells) {
		return cells[k]
	}
	return nil
}

// newline is a line break, as text.add takes it.
var newline = []byte("\n")
