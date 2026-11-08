// Package html is the html extractor: the comments, sections, code blocks
// and table cells of an HTML file, read by its structure in one pass of the
// tokenizer in core/htmlparse, which the text and markdown kinds read a
// Markdown file's HTML blocks with too. What a rule writes and the values
// it gets are core/markup's, which the readers of every markup share; this
// package holds the walk of HTML's tokens.
package html

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/htmlparse"
	"github.com/f-klement/errata/internal/extract/core/markup"
)

// Spec reads one target of File, an HTML file, as markup.Selector says:
// each comment, the text of each section under a heading, each pre
// element, or each cell of a table. A code block's language is the one a
// class such as language-go or lang-go names, on the pre or on a code
// element inside it.
type Spec struct {
	core.OneFile
	markup.Selector
}

// Kind is the html kind, which a rule writes under the key html.
var Kind = core.Kind[Spec]{Key: "html", Place: Place, Resolve: Resolve, Describe: Describe}

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
	return check.Description{Label: "html"}
}

const (
	// maxTextRatio is how many times the size of its file the text the
	// extractor reads may be. The text holds each byte of the file at most
	// once, a character reference decoded to at most 1.2 times its bytes,
	// and a space or newline for a run of blanks or a tag, so it is at most
	// about twice the file. Sections of one heading path nest at most six
	// deep, h1 to h6, and each holds the ones below it, so the pieces the
	// search reads are at most twelve times the file, and the search's time
	// limit, which scales with the file's limit, holds with them.
	maxTextRatio = 12

	// bytesPerPiece is how many bytes of max_bytes allow one piece of the
	// text, 24 bytes each: a run of the file's bytes, or bytes errata
	// writes. A run of blanks folded to one space starts two pieces, as in
	// the Markdown prose (mdparse).
	bytesPerPiece = 4
)

// extractor reads its target of its file.
type extractor struct {
	src core.Source
	sel *markup.Select
}

// Build refuses what markup.Selector.Build refuses, and the file as every
// reader of one file refuses it, under the name html.
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

// FromShared is FromData: an html read spends none of a set's budget.
func (e extractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// errTooManyValues stops a reading once it has more items than the cap on
// values, before it builds more.
var errTooManyValues = errors.New("too many values")

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them. Every line end reads as LF, a CRLF and a lone
// CR alike, as the HTML standard reads them before it tokenizes, and a byte
// order mark is dropped, so the lines are the file's.
func (e extractor) FromData(data []byte) ([]check.Value, error) {
	size := len(data)
	data = core.DropBOM(core.LineFeeds(data))
	r := newReading(e.sel, data, e.src.Limit)
	if err := r.read(); err != nil {
		if errors.Is(err, errTooManyValues) {
			return nil, core.TooManyValues(e.src.File)
		}
		return nil, fmt.Errorf("%s: %w", e.src.File, err)
	}
	if err := checkRatio(e.src.File, readOf(r.items), size); err != nil {
		return nil, err
	}
	return e.sel.Values(e.src.File, r.p.text, r.items, r.lineAt)
}

// readOf is the bytes of the text the items read, a byte as many times as
// items hold it.
func readOf(items []markup.Item) int {
	read := 0
	for _, it := range items {
		read += it.End - it.Start
	}
	return read
}

// checkRatio refuses text of more than maxTextRatio times the size of its
// file.
func checkRatio(file string, text, size int) error {
	if text > maxTextRatio*size {
		return fmt.Errorf("%s: the text read is %d bytes, over the limit of %d, %d times the file's size; %s",
			file, text, maxTextRatio*size, maxTextRatio, core.OtherWay)
	}
	return nil
}

// reading is one pass over a file's tokens: the text every item is a piece
// of, the items, and what is open where the pass is.
type reading struct {
	sel   *markup.Select
	src   []byte
	s     *htmlparse.Scanner
	p     prose
	lines *core.LineCursor
	items []markup.Item
	// outline names the headings of the path; sections are the numbers of
	// the named headings whose sections are open, and itemOf the item of
	// each named heading, for a section target.
	outline  *markup.Outline
	sections []int
	itemOf   []int
	head     *heading
	code     *codeBlock
	tables   []*table
	// trace, nil but in a test, sees each heading's text and each row's
	// cells as the reading ends them (TestTheReadingIsTheBrowsers).
	trace func(kind, text string)
}

// heading is a heading element open: its level, 1 for h1, its line, its
// text so far, and where the text of the reading was when it started. A
// link open in it started at link of its text and linkP of the reading's.
type heading struct {
	level, line int
	name        prose
	textAt      int
	inLink      bool
	self        bool // whether the link open goes to a place in the page, its href a #
	link, linkP int
}

// codeBlock is a pre element open: where its text starts in the reading's
// text, the line of its tag and of its first text, its language, and
// whether it is in the scope of the path.
type codeBlock struct {
	start, tagLine, line int
	language             string
	scoped               bool
}

// table is a table element open: its rows so far, the indexes of the
// columns the select reads once its first row gave the headers, and the
// row and cell open.
type table struct {
	scoped         bool
	rows           int
	col, key       int
	ok             bool
	cells          [][2]int // of the row open, from to end in the text
	inRow, inCell  bool
	inCaption      bool
	rowLine, start int
	// cut is where the text of the cell open stops, at the start of the
	// first table inside it; math.MaxInt while it has none.
	cut int
}

// followXNet is whether a reading follows x/net v0.62.0 where it is not
// the standard (htmlparse's FollowXNetAdoption). The browser tests alone
// set it, to compare a page errata reads by the standard there. A plain
// variable is safe only because no html test calls t.Parallel.
var followXNet bool

func newReading(sel *markup.Select, src []byte, limit int64) *reading {
	r := &reading{sel: sel, src: src, lines: core.NewLineCursor(src),
		s: htmlparse.NewScanner(src, htmlparse.MaxTokens(limit)), p: prose{maxPieces: int(limit / bytesPerPiece)}}
	r.s.Headings()
	if followXNet {
		r.s.FollowXNetAdoption()
	}
	if sel.Path != nil {
		r.outline = markup.NewOutline(sel.Path)
	}
	return r
}

// lineAt is the line of the file that holds offset off of the text.
func (r *reading) lineAt(off int) int { return r.lines.At(r.p.offset(off)) }

// inScope reports whether the path keeps what is read now: there is no
// path, or a section it names is open.
func (r *reading) inScope() bool { return r.outline == nil || len(r.sections) > 0 }

// read makes the pass and appends the target's items.
func (r *reading) read() error {
	for r.s.Next() {
		var err error
		switch r.s.Kind() {
		case htmlparse.Comment:
			r.comment()
		case htmlparse.Text, htmlparse.Pre:
			r.text()
		default:
			err = r.tag()
		}
		if err != nil {
			return err
		}
		if r.sel.Search == nil && len(r.items) > core.MaxValues {
			return errTooManyValues // with a pattern, the search caps its matches
		}
	}
	if err := r.s.Err(); err != nil {
		var many *htmlparse.TooManyTokensError
		var nest *htmlparse.NestError
		switch {
		case errors.As(err, &many):
			return fmt.Errorf("line %d: %w", r.lines.At(many.At), err)
		case errors.As(err, &nest):
			return fmt.Errorf("line %d: %w", r.lines.At(nest.At), err)
		}
		return err
	}
	r.finish()
	if r.p.over {
		return fmt.Errorf("line %d: the text is made of more than %d pieces of the file, the limit, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this broken up",
			r.lines.At(r.p.overAt), r.p.maxPieces, bytesPerPiece)
	}
	return nil
}

// finish ends what the end of the file leaves open, in the order the
// elements opened: the heading, the code block, the tables and the
// sections.
func (r *reading) finish() {
	r.endHeading()
	r.endCode()
	for len(r.tables) > 0 {
		r.endTable()
	}
	for _, n := range r.sections {
		r.endSection(n, len(r.p.text))
	}
	r.sections = nil
}

// comment appends the item of a comment, its text without the blanks at
// either end, at the line where it starts.
func (r *reading) comment() {
	if r.sel.Target != markup.TargetComment || !r.inScope() {
		return
	}
	start, end, ok := r.s.CommentText()
	if !ok {
		return
	}
	at := len(r.p.text)
	r.p.add(r.src[start:end], start, false)
	from, to := markup.TrimBlanks(r.p.text, at, len(r.p.text))
	r.items = append(r.items, markup.Item{Start: from, End: to, Line: r.lines.At(r.s.Start()), Fixed: true})
}

// text appends a run of text to what reads it: the heading open, and the
// target's piece open.
func (r *reading) text() {
	b, at := r.s.Bytes(), r.s.Start()
	if r.head != nil {
		r.head.name.shown(b, at)
	}
	switch r.sel.Target {
	case markup.TargetSection:
		if len(r.sections) == 0 {
			return
		}
		if r.s.Kind() == htmlparse.Text {
			r.p.shown(b, at)
			return
		}
		for len(b) > 0 { // a pre's lines stay lines
			line, rest, cut := bytes.Cut(b, []byte("\n"))
			r.p.shown(line, at)
			if cut {
				r.p.endLine()
			}
			at += len(b) - len(rest)
			b = rest
		}
	case markup.TargetCode:
		if r.code != nil {
			if r.code.line == 0 {
				r.code.line = r.lines.At(at)
			}
			r.p.written(b, at)
		}
	case markup.TargetTable:
		switch t := r.table(); {
		case t != nil && t.inCell:
			r.p.shown(b, at)
		case t != nil && !t.inCaption && r.trace != nil && len(bytes.Trim(b, htmlBlanks)) > 0:
			r.trace("foster", "") // a browser moves it before the table; errata reads it where it is
		}
	}
}

// tag follows the elements a start or end tag opens and closes.
func (r *reading) tag() error {
	if r.s.Foreign() {
		return nil // an svg's td is no cell, nor its a a link: it ends no pre or heading
	}
	name, opens, at := r.s.Name(), r.s.Kind() == htmlparse.StartTag, r.s.Start()
	if r.s.BreaksLine() {
		if r.sel.Target == markup.TargetSection {
			r.p.endLine()
		}
		if r.head != nil {
			r.head.name.space(at) // <br> in a heading is a space, as in markdown's
		}
	}
	level := headingLevel(name)
	if r.head != nil && (!r.s.HeadingOpen() || opens && level > 0) {
		r.endHeading() // the scanner ends a heading where a browser does
	}
	if opens && level > 0 {
		r.head = &heading{level: level, line: r.lines.At(at), name: prose{maxPieces: r.p.maxPieces}, textAt: len(r.p.text)}
	}
	if name == "a" && r.head != nil {
		r.link(opens)
	}
	switch r.sel.Target {
	case markup.TargetCode:
		r.codeTag(name, opens, at)
	case markup.TargetTable:
		return r.tableTag(name, opens, at)
	}
	return nil
}

// link follows a link in the heading open. A link to a place in the page,
// its href starting with #, whose text is blank or only permalink marks, such as Sphinx's and MkDocs' ¶, rustdoc's §,
// Docusaurus's zero width space or 🔗, is a heading's link to itself, and
// is no part of its text, nor of the section's prose.
func (r *reading) link(opens bool) {
	h := r.head
	if opens {
		if h.inLink {
			r.link(false) // a link ends a link open, as in a browser
		}
		h.inLink, h.self = true, strings.HasPrefix(r.s.Href(), "#")
		h.link, h.linkP = len(h.name.text), len(r.p.text)
		return
	}
	if !h.inLink {
		return
	}
	h.inLink = false
	if !h.self || strings.Trim(string(h.name.text[h.link:]), permalinkMarks) != "" {
		return
	}
	h.name.truncate(h.link)
	if r.sel.Target == markup.TargetSection {
		r.p.truncate(h.linkP)
	}
}

// htmlBlanks are HTML's blanks: a vertical tab or a no break space is
// text.
const htmlBlanks = " \t\n\f\r"

// permalinkMarks are what the text of a heading's link to itself may hold:
// blanks, a no break space, zero width spaces and the marks generators
// write.
const permalinkMarks = " \t\n\r\f\u00a0\u200b\ufeff¶§#🔗"

// headingLevels are the heading elements' levels, h1 to h6.
var headingLevels = map[string]int{"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}

// headingLevel is the level of a heading element's name, or 0.
func headingLevel(name string) int { return headingLevels[name] }

// endHeading ends the heading open, if any: the sections it ends end where
// its text started, and the section of a heading the path names starts
// after it.
func (r *reading) endHeading() {
	h := r.head
	if h == nil {
		return
	}
	if h.inLink {
		r.link(false) // a link open ends with its heading
	}
	r.head = nil
	if h.name.over && !r.p.over { // a heading cut short names nothing: refuse it
		r.p.over, r.p.overAt = true, h.name.overAt
	}
	if r.trace != nil {
		r.trace("heading", strings.Trim(string(h.name.text), htmlBlanks))
	}
	if r.outline == nil {
		return
	}
	ends, named, _ := r.outline.Heading(h.level, strings.Trim(string(h.name.text), htmlBlanks))
	for _, n := range ends {
		r.endSection(n, h.textAt)
	}
	if !named {
		return
	}
	r.sections = append(r.sections, len(r.itemOf))
	r.itemOf = append(r.itemOf, len(r.items))
	if r.sel.Target == markup.TargetSection {
		r.p.endLine()
		r.items = append(r.items, markup.Item{Start: len(r.p.text), End: len(r.p.text), Line: h.line})
	}
}

// endSection ends the section of named heading n at offset end of the
// text.
func (r *reading) endSection(n, end int) {
	for i, s := range r.sections {
		if s == n {
			r.sections = append(r.sections[:i], r.sections[i+1:]...)
			break
		}
	}
	if r.sel.Target == markup.TargetSection {
		it := &r.items[r.itemOf[n]]
		it.Start, it.End = markup.TrimBlanks(r.p.text, it.Start, max(it.Start, end))
	}
}

// codeTag follows the pre elements and the code elements inside them, for
// a code target.
func (r *reading) codeTag(name string, opens bool, at int) {
	if r.code != nil && !r.s.InPre() {
		r.endCode() // an end tag, or a start tag such as a cell's, ended the pre
	}
	switch {
	case opens && r.code == nil && (name == "pre" || name == "listing"):
		r.code = &codeBlock{start: len(r.p.text), tagLine: r.lines.At(at), language: language(r.s.Class()), scoped: r.inScope()}
	case opens && r.code != nil && name == "code" && r.code.language == "":
		r.code.language = language(r.s.Class())
	}
}

// endCode ends the code block open, if any, and appends its item when the
// path and the language keep it: its text less a newline at its end, at
// the line of its first text, or of its tag when it has none.
func (r *reading) endCode() {
	c := r.code
	if c == nil {
		return
	}
	r.code = nil
	if !c.scoped || (r.sel.Language != "" && c.language != r.sel.Language) {
		return
	}
	end := len(r.p.text)
	if end > c.start && r.p.text[end-1] == '\n' {
		end--
	}
	line := c.line
	if line == 0 {
		line = c.tagLine
	}
	r.items = append(r.items, markup.Item{Start: c.start, End: end, Line: line})
}

// tableTags are the tags a table holds outside a cell without moving them.
var tableTags = map[string]bool{"tr": true, "td": true, "th": true, "tbody": true, "thead": true, "tfoot": true,
	"caption": true, "colgroup": true, "col": true, "table": true, "script": true, "style": true, "template": true, "input": true, "form": true}

// language is the language a class attribute names: the rest of its first
// class that starts with language- or lang-, or "" for none.
func language(class string) string {
	for _, f := range strings.Fields(class) {
		for _, prefix := range []string{"language-", "lang-"} {
			if l, ok := strings.CutPrefix(f, prefix); ok && l != "" {
				return l
			}
		}
	}
	return ""
}

// table is the innermost table open, or nil.
func (r *reading) table() *table {
	if len(r.tables) == 0 {
		return nil
	}
	return r.tables[len(r.tables)-1]
}

// tableTag follows the tables, rows and cells, for a table target. The
// scanner bounds how deep they nest (htmlparse's nest: each table and each
// cell is an element open). A
// table inside a cell is a table of its own, and the text of the cell
// around it ends where it starts. A cell or a row a tag implies, as a browser reads <td>
// with no </td>, ends where the next one starts.
func (r *reading) tableTag(name string, opens bool, at int) error {
	if r.s.Ignored() {
		return nil // a browser ignores it: </td> with a th open, say
	}
	t := r.table()
	switch {
	case name == "table" && opens:
		if t != nil && !t.inCell && !t.inCaption {
			r.endTable() // a table in a table, outside a cell, ends it in a browser
			t = r.table()
		}
		if t != nil {
			t.cut = min(t.cut, len(r.p.text))
		}
		r.tables = append(r.tables, &table{scoped: r.inScope()})
	case t == nil:
	case name == "table":
		r.endTable()
	case name == "tr" || name == "thead" || name == "tbody" || name == "tfoot" || name == "caption" ||
		(name == "col" || name == "colgroup") && opens: // a column closes the row, as the scanner does
		r.endRow(t)
		t.inCaption = name == "caption" && opens
		if name == "tr" && opens {
			t.inRow, t.rowLine = true, r.lines.At(at)
		}
	case name == "td" || name == "th":
		r.endCell(t)
		t.inCaption = false
		if opens {
			if !t.inRow {
				t.inRow, t.rowLine = true, r.lines.At(at)
			}
			t.inCell, t.start, t.cut = true, len(r.p.text), math.MaxInt
		}
	case t.inCell && r.s.BreaksLine():
		r.p.space(at)
	case !t.inCell && !t.inCaption && opens && r.trace != nil && !tableTags[name]:
		r.trace("foster", "") // as for text: a browser moves the element before the table
	}
	return nil
}

// endTable ends the innermost table, with its row open.
func (r *reading) endTable() {
	r.endRow(r.table())
	r.tables = r.tables[:len(r.tables)-1]
}

// endCell ends the cell open of t, if any.
func (r *reading) endCell(t *table) {
	if !t.inCell {
		return
	}
	t.inCell = false
	start, end := markup.TrimBlanks(r.p.text, t.start, min(len(r.p.text), t.cut))
	t.cells = append(t.cells, [2]int{start, end})
	r.p.endLine()
}

// endRow ends the row open of t, if any. The first row with a cell holds
// the headers; each row after it gives the cell under the column the
// select reads, when its key cell holds the row the select names.
func (r *reading) endRow(t *table) {
	r.endCell(t)
	if !t.inRow {
		return
	}
	t.inRow = false
	cells := t.cells
	t.cells = t.cells[:0]
	if len(cells) == 0 {
		return
	}
	if r.trace != nil {
		texts := make([]string, len(cells))
		for i, c := range cells {
			texts[i] = string(r.p.text[c[0]:c[1]])
		}
		r.trace("row", strings.Join(texts, "|"))
	}
	if t.rows++; t.rows == 1 {
		headers := make([]string, len(cells))
		for i, c := range cells {
			headers[i] = string(r.p.text[c[0]:c[1]])
		}
		t.col, t.key, t.ok = r.sel.Columns(headers)
		return
	}
	if !t.ok || !t.scoped || t.col >= len(cells) {
		return
	}
	if t.key >= 0 && (t.key >= len(cells) || string(r.p.text[cells[t.key][0]:cells[t.key][1]]) != r.sel.Row) {
		return
	}
	c := cells[t.col]
	r.items = append(r.items, markup.Item{Start: c[0], End: c[1], Line: t.rowLine, Fixed: true})
}
