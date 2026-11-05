package asciidoc

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/markup"
)

// bytesPerPiece is how many bytes of max_bytes allow one piece of a read:
// a value's item (24 to 32 bytes), a run of prose or code lines (24 bytes),
// or a table cell (24 bytes). A table of one empty column makes a cell of
// every byte, so the cap keeps what a read holds to about six times its
// limit, as the Markdown parse's cap on pieces does.
const bytesPerPiece = 4

// doc is a read of a file for one target: the text every item is a piece
// of, the items, and where the lines of the text are in the file.
type doc struct {
	text  []byte
	items []markup.Item
	// runs are the stretches of the text made of lines of the file one after
	// the other, in the order of the text.
	runs   []run
	cursor *core.LineCursor
}

// run is a stretch of the text that starts at start, on its line tline
// counted from 0, and holds the file's lines from line on, one text line
// for each.
type run struct {
	start, tline, line int
}

// lineAt is the line of the file that holds offset off of the text. Only a
// section's or a code block's match asks, and the text of each is in runs:
// a section's starts after its title, which starts or goes on a run, and an
// empty code block's matches are at its delimiter, not asked here.
func (d *doc) lineAt(off int) int {
	after := sort.Search(len(d.runs), func(i int) bool { return d.runs[i].start > off })
	r := d.runs[after-1]
	return r.line + d.cursor.At(off) - 1 - r.tline
}

// Kinds of delimited block.
const (
	blockComment  = iota // ////, whose lines are a comment
	blockCode            // ----, .... or ```, read by the code target
	blockPass            // ++++, raw output, read by no target
	blockCompound        // ====, ****, ____ or --, whose lines are AsciiDoc
	blockTable           // |===, !===, ,=== or :===
)

// block is a delimited block the parse is in.
type block struct {
	kind  int
	close string // the line that ends it
	lang  string // a code block's language
	sep   byte   // a table's separator
	line  int    // the line of its opening delimiter
	// read is whether the target reads the block, start where its text
	// starts, and first the line of its first line of content, 0 for none.
	read  bool
	start int
	first int
	table *table
}

// verbatim reports whether b's lines are content as written, with no
// AsciiDoc in them.
func (b *block) verbatim() bool {
	return b.kind == blockComment || b.kind == blockCode || b.kind == blockPass
}

// parser is one pass over a file's lines.
type parser struct {
	d     *doc
	sel   *markup.Select
	stack []*block
	attrs *attrs // the attribute line waiting for its block
	marks marks
	n     int  // the line being read, from 1
	para  bool // a paragraph goes on: a heading or a block title is text
	// first is the line that started the paragraph when it may be a two
	// line title's, -1 otherwise, and firstLen its length in characters.
	first, firstLen int
	// para holds a section target's paragraph until it ends, so that its
	// formatting marks pair across its lines: its lines, each with its line
	// break, and the stretches of the file's lines they came from.
	paraText  []byte
	paraLines []stretch
	scratch   []byte
	// literal is an indented paragraph, read as written by no target.
	literal bool
	// outline names the headings of the select's path, nil without one, and
	// named is how many of their sections the line is in.
	outline *markup.Outline
	named   int
	// tlines is the text's lines so far, and last and lastEnd the file's
	// line the text's last line came from and where it ends.
	tlines, last, lastEnd int
	pieces, maxPieces     int
}

// stretch is count lines of the file one after the other, from line.
type stretch struct{ line, count int }

// parse reads data, past a byte order mark and with every line end read as
// LF (core.LineFeeds), for the target of sel, with at most
// limit/bytesPerPiece pieces. Every line is read once, and only
// the delimited blocks the line is in are kept.
func parse(data []byte, sel *markup.Select, limit int64) (*doc, error) {
	data = core.DropBOM(core.LineFeeds(data)) // three bytes on the first line: the lines stay the file's
	p := &parser{d: &doc{}, sel: sel, maxPieces: int(limit / bytesPerPiece)}
	if sel.Path != nil {
		p.outline = markup.NewOutline(sel.Path)
	}
	for line := range bytes.Lines(data) {
		p.n++
		line = bytes.TrimSuffix(line, []byte("\n"))
		if err := p.line(line); err != nil {
			return nil, err
		}
	}
	if b := p.top(); b != nil {
		return nil, fmt.Errorf("line %d: the block opened here by %s has no closing line, so it would run to the end of the file; close it, or read the file with a regex extractor",
			b.line, core.Quote(b.close))
	}
	if err := p.flush(); err != nil {
		return nil, err
	}
	if p.sel.Target == markup.TargetSection {
		for i, it := range p.d.items {
			if it.End == -1 {
				p.d.items[i].End = trimNewline(p.d.text, it.Start, len(p.d.text))
			}
		}
	}
	p.d.cursor = core.NewLineCursor(p.d.text)
	return p.d, nil
}

// line reads one line of the file, without its line break.
func (p *parser) line(line []byte) error {
	trim := bytes.TrimRight(line, " \t")
	if top := p.top(); top != nil {
		switch {
		case top.verbatim():
			if string(trim) == top.close {
				return p.close()
			}
			return p.content(top, line)
		case top.kind == blockTable:
			return p.tableLine(top, line, trim)
		case string(trim) == top.close:
			return p.close()
		}
	}
	if len(trim) == 0 {
		p.para, p.literal = false, false
		return nil
	}
	if p.para && p.first == p.n-1 && underline(trim, p.firstLen) {
		return fmt.Errorf("line %d: the line before over this line of %c is a two line title, which the asciidoc extractor does not read; write the title on one line, such as == Title, or read the file with a regex extractor",
			p.n, trim[0])
	}
	if b := delimiter(string(trim)); b != nil {
		return p.open(b)
	}
	if p.literal {
		return nil
	}
	if isLineComment(trim) {
		return p.comment(trim)
	}
	if a, ok := attributeLine(trim); ok {
		p.para = false
		switch {
		case a == nil:
		case p.attrs == nil:
			p.attrs = a
		default: // lines one after another make one list
			p.attrs.merge(a)
		}
		return nil
	}
	if !p.para {
		if level, title, ok := heading(trim); ok {
			if len(p.stack) > 0 { // a title in a block is no section's
				p.attrs = nil
				return p.prose(title)
			}
			return p.heading(level, title)
		}
		if title, ok := blockTitle(trim); ok {
			return p.prose(title)
		}
		if attributeEntry(trim) {
			return nil
		}
		if trim[0] == ' ' || trim[0] == '\t' {
			if _, ok := listItem(trim); !ok {
				p.literal, p.attrs = true, nil
				return nil
			}
		}
		p.para, p.attrs = true, nil
		p.first, p.firstLen = -1, utf8.RuneCount(trim)
		if len(p.stack) == 0 && titleLike(trim) { // only a section's paragraph
			p.first = p.n
		}
		if item, ok := listItem(trim); ok {
			return p.paragraph(item, true)
		}
		return p.paragraph(trim, true)
	}
	if item, ok := listItem(trim); ok { // a list item is a paragraph of its own
		return p.paragraph(item, true)
	}
	return p.paragraph(trim, false)
}

// titleLike reports whether a line may be a two line title's, as
// Asciidoctor's SetextSectionTitleRx reads one: it does not start with a
// dot and holds a letter or a digit.
func titleLike(trim []byte) bool {
	return trim[0] != '.' && bytes.IndexFunc(trim, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// underline reports whether trim underlines a line of n characters, as
// Asciidoctor reads a two line title: two or more of one of = - ~ ^ +, as
// many as the line has characters, or one more or less.
func underline(trim []byte, n int) bool {
	return len(trim) >= 2 && strings.IndexByte("=-~^+", trim[0]) >= 0 && same(string(trim)) && len(trim)-n <= 1 && n-len(trim) <= 1
}

// top is the innermost block the parse is in, nil for none.
func (p *parser) top() *block {
	if len(p.stack) == 0 {
		return nil
	}
	return p.stack[len(p.stack)-1]
}

// inScope reports whether the line is in a section the path names, or the
// select names no path.
func (p *parser) inScope() bool { return p.outline == nil || p.named > 0 }

// piece counts one piece of the read against the cap.
func (p *parser) piece() error {
	if p.pieces++; p.pieces > p.maxPieces {
		return fmt.Errorf("line %d: the read is made of more than %d comments, blocks, cells, sections and runs of lines, the limit, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this broken up",
			p.n, p.maxPieces, bytesPerPiece)
	}
	return nil
}

// errTooManyValues stops a read whose items are more values than
// core.MaxValues: without a pattern each item is a value.
var errTooManyValues = errors.New("too many values")

// add appends an item.
func (p *parser) add(it markup.Item) error {
	if err := p.piece(); err != nil {
		return err
	}
	if p.sel.Search == nil && len(p.d.items) == core.MaxValues {
		return errTooManyValues
	}
	p.d.items = append(p.d.items, it)
	return nil
}

// appendLine appends line, line n of the file, to the text as one line,
// its formatting marks left out when prose is set, and counts the run it
// starts unless the text's last line is the file's line before.
func (p *parser) appendLine(line []byte, n int, prose bool) error {
	if p.newRun(n) {
		if err := p.piece(); err != nil {
			return err
		}
	}
	if prose {
		line = p.marks.appendProse(p.scratch[:0], line)
		p.scratch = line
	}
	p.appendRun(line, n)
	return nil
}

// newRun reports whether line n of the file starts a run of the text.
func (p *parser) newRun(n int) bool {
	return len(p.d.runs) == 0 || p.last != n-1 || p.lastEnd != len(p.d.text)
}

// appendRun appends line, line n of the file, to the text as one line, in
// a run of its own unless the text's last line is the file's line before;
// its piece is counted already.
func (p *parser) appendRun(line []byte, n int) {
	d := p.d
	if p.newRun(n) {
		d.runs = append(d.runs, run{start: len(d.text), tline: p.tlines, line: n})
	}
	d.text = append(append(d.text, line...), '\n')
	p.tlines++
	p.last, p.lastEnd = n, len(d.text)
}

// prose appends line, as prose, for a section target, after the paragraph
// before it.
func (p *parser) prose(line []byte) error {
	if p.sel.Target != markup.TargetSection {
		return nil
	}
	if err := p.flush(); err != nil {
		return err
	}
	return p.appendLine(line, p.n, true)
}

// paragraph holds line, a line of a paragraph, for a section target; start
// says it starts the paragraph, which ends the one before. A comment line
// in a paragraph is no part of it, and does not end it.
func (p *parser) paragraph(line []byte, start bool) error {
	if p.sel.Target != markup.TargetSection {
		return nil
	}
	if start {
		if err := p.flush(); err != nil {
			return err
		}
	}
	p.paraText = append(append(p.paraText, line...), '\n')
	if k := len(p.paraLines) - 1; k >= 0 && p.paraLines[k].line+p.paraLines[k].count == p.n {
		p.paraLines[k].count++
		return nil
	}
	if err := p.piece(); err != nil { // its run, which flush does not count again
		return err
	}
	p.paraLines = append(p.paraLines, stretch{line: p.n, count: 1})
	return nil
}

// flush appends the paragraph held, as prose, its formatting marks paired
// over all its lines. appendProse leaves every line break in place, so
// each line of the prose is a line of the paragraph.
func (p *parser) flush() error {
	if len(p.paraLines) == 0 {
		return nil
	}
	p.scratch = p.marks.appendProse(p.scratch[:0], p.paraText)
	rest := p.scratch
	for _, s := range p.paraLines {
		for n := s.line; n < s.line+s.count; n++ {
			i := bytes.IndexByte(rest, '\n')
			p.appendRun(rest[:i], n)
			rest = rest[i+1:]
		}
	}
	p.paraText, p.paraLines = p.paraText[:0], p.paraLines[:0]
	return nil
}

// comment reads a line comment, // and its text.
func (p *parser) comment(trim []byte) error {
	if p.sel.Target != markup.TargetComment || !p.inScope() {
		return nil
	}
	start := len(p.d.text)
	p.d.text = append(p.d.text, trim[2:]...)
	from, end := markup.TrimBlanks(p.d.text, start, len(p.d.text))
	return p.add(markup.Item{Start: from, End: end, Line: p.n, Fixed: true})
}

// heading reads a section title of level, whose text is title. A title
// under the attribute line [discrete] or [float] is prose and no section's.
func (p *parser) heading(level int, title []byte) error {
	discrete := p.attrs != nil && (p.attrs.style == "discrete" || p.attrs.style == "float")
	p.attrs = nil
	if err := p.flush(); err != nil {
		return err
	}
	before := len(p.d.text)
	if err := p.prose(title); err != nil {
		return err
	}
	if discrete || p.outline == nil {
		return nil
	}
	text := strings.TrimSpace(string(p.marks.appendProse(nil, title)))
	ends, named, _ := p.outline.Heading(level, text)
	section := p.sel.Target == markup.TargetSection
	for _, k := range ends {
		if section {
			p.d.items[k].End = trimNewline(p.d.text, p.d.items[k].Start, before)
		}
		p.named--
	}
	if !named {
		return nil
	}
	p.named++
	if section {
		return p.add(markup.Item{Start: len(p.d.text), End: -1, Line: p.n})
	}
	return nil
}

// open enters delimited block b, at the line read, under the attribute
// line before it.
func (p *parser) open(b *block) error {
	if len(p.stack) >= core.MaxDepth {
		return core.TooDeep(p.n)
	}
	b.line, b.start = p.n, len(p.d.text)
	scope := p.inScope()
	switch b.kind {
	case blockCode:
		if b.close != "```" && p.attrs != nil && len(p.attrs.pos) > 1 && (p.attrs.style == "source" || p.attrs.style == "") {
			b.lang = p.attrs.pos[1]
		}
		b.read = p.sel.Target == markup.TargetCode && scope && (p.sel.Language == "" || b.lang == p.sel.Language)
	case blockComment:
		b.read = p.sel.Target == markup.TargetComment && scope
	case blockTable:
		if p.sel.Target == markup.TargetTable && scope {
			b.table = newTable(p.attrs, b.sep, p.n)
		}
	}
	p.attrs, p.para, p.literal = nil, false, false
	p.stack = append(p.stack, b)
	return nil
}

// content reads a line of a verbatim block.
func (p *parser) content(b *block, line []byte) error {
	if !b.read {
		return nil
	}
	if b.first == 0 {
		b.first = p.n
	}
	if b.kind == blockComment {
		p.d.text = append(p.d.text, line...)
		p.d.text = append(p.d.text, '\n')
		return nil
	}
	return p.appendLine(line, p.n, false)
}

// close leaves the innermost block, and appends its item if the target
// reads it.
func (p *parser) close() error {
	b := p.top()
	p.stack = p.stack[:len(p.stack)-1]
	p.attrs, p.para, p.literal = nil, false, false
	switch {
	case b.read && b.kind == blockCode:
		it := markup.Item{Start: b.start, End: trimNewline(p.d.text, b.start, len(p.d.text)), Line: b.first}
		if b.first == 0 { // an empty block's matches are at its opening delimiter
			it.Line, it.Fixed = b.line, true
		}
		return p.add(it)
	case b.read: // a comment block, at its opening delimiter
		from, end := markup.TrimBlanks(p.d.text, b.start, len(p.d.text))
		return p.add(markup.Item{Start: from, End: end, Line: b.line, Fixed: true})
	case b.table != nil:
		return b.table.finish(p)
	}
	return nil
}

// tableLine reads a line of table block b: its end, a comment, or cells.
// For a section target, the table's lines are prose.
func (p *parser) tableLine(b *block, line, trim []byte) error {
	switch {
	case string(trim) == b.close:
		return p.close()
	case isLineComment(trim):
		return p.comment(trim)
	case b.table != nil && b.table.err == nil: // a table refused is refused at its end
		if err := b.table.read(p, line, len(trim) == 0); err != nil {
			return err
		}
	}
	if len(trim) == 0 {
		return nil
	}
	return p.prose(trim)
}

// trimNewline is end, less a newline at the end of text[start:end].
func trimNewline(text []byte, start, end int) int {
	if end > start && text[end-1] == '\n' {
		end--
	}
	return end
}

// delimiter is the block a line opens, nil for none: four or more of one
// of / - . + = * _, two hyphens, ``` and a language, or a table's |, !, ,
// or : and three or more =. The line that ends a block is the one that
// opened it, or ``` for a fenced one.
func delimiter(s string) *block {
	switch {
	case s == "--":
		return &block{kind: blockCompound, close: s}
	case strings.HasPrefix(s, "```") && !strings.HasPrefix(s, "````"):
		lang, _, _ := strings.Cut(s[3:], ",")
		return &block{kind: blockCode, close: "```", lang: strings.TrimSpace(lang)}
	case len(s) >= 4 && same(s):
		if i := strings.IndexByte("/-.+=*_", s[0]); i >= 0 {
			return &block{kind: [...]int{blockComment, blockCode, blockCode, blockPass, blockCompound, blockCompound, blockCompound}[i], close: s}
		}
	case len(s) >= 4 && strings.IndexByte("|!,:", s[0]) >= 0 && s[1] == '=' && same(s[1:]):
		return &block{kind: blockTable, close: s, sep: s[0]}
	}
	return nil
}

// same reports whether every byte of s is its first.
func same(s string) bool { return strings.Count(s, s[:1]) == len(s) }

// isLineComment reports whether a line is a comment: // and not ///.
func isLineComment(trim []byte) bool {
	return bytes.HasPrefix(trim, []byte("//")) && !bytes.HasPrefix(trim, []byte("///"))
}

// heading is the level and text of a section title: one to six = or #, a
// blank, and the title, with the same marks after it left out.
func heading(trim []byte) (level int, title []byte, ok bool) {
	c := trim[0]
	if c != '=' && c != '#' {
		return 0, nil, false
	}
	for level < len(trim) && trim[level] == c {
		level++
	}
	if level > 6 || level == len(trim) || !isBlank(trim[level]) {
		return 0, nil, false
	}
	title = bytes.TrimLeft(trim[level:], " \t")
	if marks := trim[:level]; len(title) > level && bytes.HasSuffix(title, marks) && isBlank(title[len(title)-level-1]) {
		title = bytes.TrimRight(title[:len(title)-level], " \t")
	}
	return level, title, true
}

// blockTitle is the text of a block title: a dot and then neither a blank
// nor another dot.
func blockTitle(trim []byte) ([]byte, bool) {
	if len(trim) < 2 || trim[0] != '.' || trim[1] == '.' || isBlank(trim[1]) {
		return nil, false
	}
	return trim[1:], true
}

// attributeEntry reports whether a line sets or unsets a document
// attribute, :name: value or :name!:, which is no prose.
func attributeEntry(trim []byte) bool {
	if trim[0] != ':' {
		return false
	}
	i := 1
	if i < len(trim) && trim[i] == '!' {
		i++
	}
	from := i
	for i < len(trim) && (isWord(trim[i]) || trim[i] == '-') {
		i++
	}
	if i == from {
		return false
	}
	if i < len(trim) && trim[i] == '!' {
		i++
	}
	return i < len(trim) && trim[i] == ':' && (i+1 == len(trim) || isBlank(trim[i+1]))
}

// listItem is the text of a list item, after its marker: a run of * or of
// dots, a hyphen, or a number and a dot, then a blank.
func listItem(trim []byte) ([]byte, bool) {
	s := bytes.TrimLeft(trim, " \t")
	i := 0
	switch c := s[0]; {
	case c == '*' || c == '.':
		for i < len(s) && s[i] == c {
			i++
		}
	case c == '-':
		i = 1
	case '0' <= c && c <= '9':
		for i < len(s) && '0' <= s[i] && s[i] <= '9' {
			i++
		}
		if i == len(s) || s[i] != '.' {
			return nil, false
		}
		i++
	}
	if i == 0 || i == len(s) || !isBlank(s[i]) {
		return nil, false
	}
	return bytes.TrimLeft(s[i:], " \t"), true
}

// isBlank reports whether c is a space, a tab or a line break.
func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// isWord reports whether c is a letter, a digit, an underscore, or a byte
// of a character past ASCII.
func isWord(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c >= 0x80
}
