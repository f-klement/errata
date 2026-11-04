// Package markdown is the markdown extractor: the comments, sections, code
// blocks and table cells of a Markdown file, read by its structure from the
// parse in core/mdparse that the text extractor reads too. What a rule
// writes and the values it gets are core/markup's, which the readers of
// every markup share; this package holds the walk of goldmark's tree.
package markdown

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yuin/goldmark/v2/ast"
	east "github.com/yuin/goldmark/v2/extension/ast"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
	"github.com/f-klement/errata/internal/extract/core/mdparse"
)

// Spec reads one target of File, a Markdown file, as markup.Selector says:
// each HTML comment, the prose of each section, each fenced code block, or
// each cell of a GitHub table. A code block's language is the first word of
// its info string.
type Spec struct {
	core.OneFile
	markup.Selector
}

// Kind is the markdown kind, which a rule writes under the key markdown.
var Kind = core.Kind[Spec]{Key: "markdown", Place: Place, Resolve: Resolve, Describe: Describe}

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
	return check.Description{Label: "markdown"}
}

// maxProseRatio is how many times the size of its file the text the
// extractor reads may be. Prose is at most twice what it reads of the file
// (text's maxProseRatio). Sections of one heading path may nest, "# A" over
// "## A", and each holds the ones below it, at most six levels; a table
// target reads each cell of its key column twice, as key and as value, and
// its header once more. So twelve times the file holds for every target, and
// the search's time limit, which scales with the file's limit, holds with it.
const maxProseRatio = 12

// extractor reads its target of its file.
type extractor struct {
	src       core.Source
	sel       *markup.Select
	parseTime time.Duration
}

// Build refuses what markup.Selector.Build refuses, and the file as every
// reader of one file refuses it, under the name markdown.
func Build(t Spec) (check.Extractor, error) {
	src, err := t.Check(Kind.Key)
	if err != nil {
		return nil, err
	}
	sel, err := t.Build(Kind.Key, src)
	if err != nil {
		return nil, err
	}
	return extractor{src: src, sel: sel, parseTime: mdparse.Time(src.Limit)}, nil
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract returns the values of the file under root.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, e.src, e)
}

// FromShared is FromData: a markdown read has its own time limits, and
// spends none of a set's budget.
func (e extractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// reading is one read of a file: its parse, the prose every item is a piece
// of, and the items.
type reading struct {
	doc   *mdparse.Doc
	prose *mdparse.Prose
	lines *core.LineCursor
	items []markup.Item
	err   error // the HTML parse's refusal of an HTML block, from a walk that cannot return one
}

// lineAt is the line of the file that holds offset off of the prose.
func (r *reading) lineAt(off int) int { return r.lines.At(r.prose.Offset(off)) }

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them.
func (e extractor) FromData(data []byte) ([]check.Value, error) {
	size := len(data)
	parse := mdparse.Parse
	if e.sel.Target == markup.TargetTable {
		parse = mdparse.ParseTables
	}
	doc, err := parse(e.src.File, data, e.src.Limit, e.parseTime)
	if err != nil {
		return nil, err
	}
	r := &reading{doc: doc, prose: doc.NewProse(), lines: core.NewLineCursor(doc.Src)}
	if err := e.read(r); err != nil {
		return nil, fmt.Errorf("%s: %w", e.src.File, err)
	}
	if r.err != nil {
		return nil, fmt.Errorf("%s: %w", e.src.File, r.err)
	}
	if err := doc.Err(r.prose); err != nil {
		return nil, fmt.Errorf("%s: %w", e.src.File, err)
	}
	if err := checkRatio(e.src.File, len(r.prose.Text), size); err != nil {
		return nil, err
	}
	return e.sel.Values(e.src.File, r.prose.Text, r.items, r.lineAt)
}

// checkRatio refuses text of more than maxProseRatio times the size of its
// file.
func checkRatio(file string, text, size int) error {
	if text > maxProseRatio*size {
		return fmt.Errorf("%s: the text read is %d bytes, over the limit of %d, %d times the file's size; %s",
			file, text, maxProseRatio*size, maxProseRatio, core.OtherWay)
	}
	return nil
}

// read appends the items of the target in the scopes of the heading path.
func (e extractor) read(r *reading) error {
	src := r.doc.Src
	for _, s := range scopes(r, e.sel.Path) {
		if s.nested && e.sel.Target != markup.TargetSection {
			continue // its blocks are in the scope before it
		}
		if e.sel.Target == markup.TargetSection {
			start := len(r.prose.Text)
			if err := r.prose.Blocks(src, s.first, s.stop, false); err != nil {
				return err
			}
			r.items = append(r.items, markup.Item{Start: start, End: trimNewline(r.prose.Text, start), Line: s.line})
			continue
		}
		for b := s.first; b != s.stop; b = b.NextSibling() {
			_ = ast.Walk(b, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
				if !entering {
					return ast.WalkContinue, nil
				}
				return e.visit(r, n), nil
			})
		}
	}
	return nil
}

// visit appends the items of node n for a target other than section, and
// says whether the walk goes on into n's children.
func (e extractor) visit(r *reading, n ast.Node) ast.WalkStatus {
	src := r.doc.Src
	switch e.sel.Target {
	case markup.TargetCode:
		c, ok := n.(*ast.CodeBlock)
		if !ok {
			return ast.WalkContinue
		}
		if c.CodeBlockKind == ast.CodeBlockKindFenced && (e.sel.Language == "" || language(c, src) == e.sel.Language) {
			start := len(r.prose.Text)
			r.prose.Lines(src, c.Value.Segments())
			at := mdparse.NodeStart(c)
			if segs := c.Value.Segments(); len(segs) > 0 {
				at = segs[0].Start
			}
			r.items = append(r.items, markup.Item{Start: start, End: trimNewline(r.prose.Text, start), Line: r.lines.At(at)})
		}
		return ast.WalkSkipChildren
	case markup.TargetComment:
		switch h := n.(type) {
		case *ast.HTMLBlock:
			read, err := r.prose.HTMLComments(src, h, func(open, start, end int) {
				from, to := markup.TrimBlanks(r.prose.Text, start, end)
				r.items = append(r.items, markup.Item{Start: from, End: to, Line: r.lines.At(open), Fixed: true})
			})
			if err != nil && r.err == nil {
				r.err = err
			}
			if !read && !scriptLike(h, src) {
				start := len(r.prose.Text)
				for _, s := range h.Value.Segments() {
					r.prose.CopyFrom(src[s.Start:s.Stop], s.Start)
				}
				r.comments(start)
			}
			return ast.WalkSkipChildren
		case *ast.RawHTML:
			start := len(r.prose.Text)
			for _, s := range h.Value.Indices() {
				r.prose.CopyFrom(src[s.Start:s.Stop], s.Start)
			}
			r.comments(start)
		}
		return ast.WalkContinue
	default: // markup.TargetTable
		if n.Kind() != east.KindTable {
			return ast.WalkContinue
		}
		r.table(n, e.sel)
		return ast.WalkSkipChildren
	}
}

// language is the first word of a fenced code block's info string.
func language(c *ast.CodeBlock, src []byte) string {
	if f := strings.Fields(c.Info.Value(src)); len(f) > 0 {
		return f[0]
	}
	return ""
}

// scriptLike reports whether h is a script, style or textarea element, whose
// <!-- is no comment in HTML: an HTML block of CommonMark's type 1 that does
// not open a pre element.
func scriptLike(h *ast.HTMLBlock, src []byte) bool {
	segs := h.Value.Segments()
	if h.HTMLBlockKind != ast.HTMLBlockKind1 || len(segs) == 0 {
		return false
	}
	first := bytes.TrimLeft(src[segs[0].Start:segs[0].Stop], " \t")
	return len(first) < 4 || !strings.EqualFold(string(first[:4]), "<pre")
}

// comments appends an item for each HTML comment in the prose from start,
// at the line where it starts.
func (r *reading) comments(start int) {
	for _, c := range markup.HTMLComments(r.prose.Text, start) {
		r.items = append(r.items, markup.Item{Start: c.Start, End: c.End, Line: r.lineAt(c.Open), Fixed: true})
	}
}

// table appends an item for each cell sel keeps of table t, at its row's
// line (markup.Select.Columns).
func (r *reading) table(t ast.Node, sel *markup.Select) {
	header := t.FirstChild() // goldmark's table starts with its header
	var headers []string
	for c := header.FirstChild(); c != nil; c = c.NextSibling() {
		headers = append(headers, r.cell(c))
	}
	col, key, ok := sel.Columns(headers)
	if !ok {
		return
	}
	for body := header.NextSibling(); body != nil; body = body.NextSibling() {
		for tr := body.FirstChild(); tr != nil; tr = tr.NextSibling() {
			cells := make([]ast.Node, 0, tr.ChildCount())
			for c := tr.FirstChild(); c != nil; c = c.NextSibling() {
				cells = append(cells, c)
			}
			if col >= len(cells) || (key >= 0 && (key >= len(cells) || r.cell(cells[key]) != sel.Row)) {
				continue
			}
			start := len(r.prose.Text)
			r.prose.Cell(r.doc.Src, cells[col])
			from, end := markup.TrimBlanks(r.prose.Text, start, len(r.prose.Text))
			r.items = append(r.items, markup.Item{Start: from, End: end, Line: r.lines.At(tr.Pos()), Fixed: true})
			r.prose.EndBlock()
		}
	}
}

// cell is the prose of a table cell, its blanks at either end left out. It
// is written to the reading's prose, which a line ends.
func (r *reading) cell(c ast.Node) string {
	start := len(r.prose.Text)
	r.prose.Cell(r.doc.Src, c)
	text := strings.TrimSpace(string(r.prose.Text[start:]))
	r.prose.EndBlock()
	return text
}

// trimNewline is the end of text after start, less a newline at its end.
func trimNewline(text []byte, start int) int {
	end := len(text)
	if end > start && text[end-1] == '\n' {
		end--
	}
	return end
}

// scope is the blocks a heading path keeps: the document's blocks from
// first up to stop, nil for the end of the file. line is the heading's
// line. nested is whether an earlier scope holds this one.
type scope struct {
	first, stop ast.Node
	line        int
	nested      bool
}

// scopes is the scope of each heading the path names, in file order
// (markup.Outline), or the whole file for no path. A heading's text is its
// prose, as the text extractor reads it. Only headings of the document
// itself count, not one in a block quote or a list item.
func scopes(r *reading, path []string) []scope {
	root := r.doc.Root
	if path == nil {
		return []scope{{first: root.FirstChild(), line: 1}}
	}
	outline := markup.NewOutline(path)
	var out []scope
	for b := root.FirstChild(); b != nil; b = b.NextSibling() {
		h, ok := b.(*ast.Heading)
		if !ok {
			continue
		}
		ends, named, nested := outline.Heading(h.Level, r.headingText(h))
		for _, i := range ends {
			out[i].stop = h
		}
		if named {
			out = append(out, scope{first: h.NextSibling(), line: r.lines.At(mdparse.NodeStart(h)), nested: nested})
		}
	}
	return out
}

// headingText is the prose of heading h, as the text extractor reads it,
// with no blank at either end.
func (r *reading) headingText(h ast.Node) string {
	p := r.doc.NewProse()
	p.Inline(r.doc.Src, h)
	return strings.TrimSpace(string(p.Text))
}
