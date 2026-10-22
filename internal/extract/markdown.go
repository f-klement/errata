package extract

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// This file is the Markdown parse that the text extractor reads, kept apart
// from that extractor so that another reader of Markdown can share it:
// parseMarkdown gives goldmark's tree of a file with every node's offsets in
// it, and prose maps text built from pieces of the file back to the file's
// lines.
//
// errata runs goldmark as CommonMark, without extensions. Its parse is
// quadratic in the file on several shapes, unclosed comments and link
// destinations, deep block quotes and long lists among them, and nothing
// outside it can stop it. So errata gives it parsers that wrap goldmark's own
// and look at a guard on every call, and a reader that looks at the guard as
// a regular expression reads it. The guard stops the parse, by a panic that
// parseMarkdown recovers, once it has run past its time limit or past one of
// the bounds below. Between two looks at the guard, goldmark does at most one
// pass over a paragraph, one set of regular expressions over a line, or one
// of the bounded costs the bounds below name.
const (
	// markdownBytesPerNode is how many bytes of max_bytes allow one node,
	// counted as the guard counts them: three for each inline node, two for
	// each line, one for each block and each emphasis. The densest real
	// Markdown file of over 100 KB measured, express's changelog, counts a
	// node for every 6.8 bytes, and the most in one file, 321763, is the
	// aws-sdk-go-v2 changelog of 2.9 MB: the limit at the default is 26
	// times that. Peak memory depends on the file's shape within the
	// bounds. The densest shapes measured at the default, examples and not
	// a ceiling: 2.0 GB for one paragraph of nine-byte lines lazily under 499
	// list items, 2.0 GB for the same lines under a setext underline, and
	// 1.7 GB for table rows read as one paragraph.
	markdownBytesPerNode = 4

	// maxDelimiters is the most runs of * and _ one paragraph or heading may
	// hold. goldmark matches each closing run against every run open before
	// it, so a paragraph costs the square of its runs, some of it between
	// two looks at the guard: 5000 runs take 0.5 s. The most in one
	// paragraph of the real files measured is 797.
	maxDelimiters = 5000

	// maxDefinitionCopies bounds the link reference definitions at the start
	// of a paragraph. goldmark takes each one out by copying the lines of
	// the paragraph after it, in one call, so a paragraph costs up to its
	// definitions times its lines: 3162 definitions, at the limit, take
	// 0.08 s. The most in one paragraph of the real files measured is 343.
	maxDefinitionCopies = 10_000_000

	// markdownTimeLimit bounds the parse of a file of up to the default
	// size; a larger max_bytes raises it in proportion (markdownTime). An
	// ordinary file parses slower than a regex search reads it: 32 MiB of
	// express's changelog, repeated, took 15 s with the machine's four
	// cores loaded three times over.
	markdownTimeLimit = 30 * time.Second

	// maxHTMLLine is the longest line that may start or continue an HTML
	// block. goldmark matches such a line against up to seven regular
	// expressions, in calls the guard cannot stop: one of them took 20 s
	// over a line of 32 MiB, and they take about 1 s over a line at the
	// limit. The longest line of HTML in the real files measured is 1801
	// bytes.
	maxHTMLLine = 1 << 20

	// markdownBytesPerContinuation is how many bytes of max_bytes allow one
	// continuation: a block other than a paragraph that goes on from one
	// line to the next. goldmark records each one, in 24 bytes, until no
	// block is left open, and makes no node for it, so the node limit does
	// not see the record grow. 32 MiB of lines of 100 block
	// quotes made 33 million and took 3.3 GB. Of the real files measured,
	// the aws-sdk-go-v2 changelog makes the most, 56921, under 1% of the
	// limit at the default, and express's changelog the densest, one for
	// every 16 bytes.
	markdownBytesPerContinuation = 4

	// markdownBytesPerPiece is how many bytes of max_bytes allow one piece
	// of the prose, 24 bytes each: a run of the file's bytes, or a space or
	// newline errata writes. A run of blanks rewritten as one space starts
	// two, so 32 MiB of a letter and a tab took 3.3 GB. Of the real files
	// measured, the aws-sdk-go-v2 changelog makes the most, 92147, and
	// express's changelog the densest, one for every 15.7 bytes.
	markdownBytesPerPiece = 4
)

// bom is the UTF-8 byte order mark, which a file may start with and a
// browser does not show.
var bom = []byte("\xef\xbb\xbf")

// markdownTime is the time limit of the parse of a file of up to limit
// bytes: markdownTimeLimit up to the default size, and in proportion to the
// limit above it.
func markdownTime(limit int64) time.Duration {
	if limit <= defaultMaxBytes {
		return markdownTimeLimit
	}
	return markdownTimeLimit * time.Duration(limit) / defaultMaxBytes
}

// markdownParser is goldmark's CommonMark parser with every block parser,
// inline parser and paragraph transformer wrapped to look at the guard.
var markdownParser = func() parser.Parser {
	var blocks, inlines, transformers []util.PrioritizedValue
	for _, v := range parser.DefaultBlockParsers() {
		bp := v.Value.(parser.BlockParser)
		blocks = append(blocks, util.Prioritized(guardedBlock{BlockParser: bp, html: bp == parser.NewHTMLBlockParser()}, v.Priority))
	}
	for _, v := range parser.DefaultInlineParsers() {
		var p parser.InlineParser = guardedInline{v.Value.(parser.InlineParser)}
		if _, ok := v.Value.(parser.CloseBlocker); ok {
			p = guardedCloser{p.(guardedInline)}
		}
		inlines = append(inlines, util.Prioritized(p, v.Priority))
	}
	for _, v := range parser.DefaultParagraphTransformers() {
		transformers = append(transformers, util.Prioritized(guardedTransformer{v.Value.(parser.ParagraphTransformer)}, v.Priority))
	}
	return parser.NewParser(parser.WithBlockParsers(blocks...), parser.WithInlineParsers(inlines...),
		parser.WithParagraphTransformers(transformers...))
}()

// markdownDoc is a Markdown file parsed. Every node's offsets index src.
type markdownDoc struct {
	// src is the file with every CRLF read as LF, so its lines are the
	// file's, and with its front matter, if any, written over with spaces.
	src  []byte
	root ast.Node
	// front is the front matter's text between its fences, from offset
	// frontAt of src; nil when the file has none.
	front   []byte
	frontAt int
	// maxPieces is the most pieces its prose may hold.
	maxPieces int
	// nodes is the guard's count of the nodes the parse made, an upper
	// bound on those in the tree, and goOn its count of continuations.
	nodes, goOn int64
}

// parseMarkdown parses data, which it may change, as CommonMark, past a
// UTF-8 byte order mark at its start. A file that starts with a line of --- or +++ and has a line of the same, or of ... after
// ---, further on, starts with front matter, which the parse skips. The parse
// takes at most timeLimit and makes at most limit/markdownBytesPerNode nodes.
// Its tree nests at most maxDepth levels below the document: the text of a
// paragraph in 998 block quotes is at the limit. Past any bound the parse is
// refused.
func parseMarkdown(file string, data []byte, limit int64, timeLimit time.Duration) (doc *markdownDoc, err error) {
	if crlf := []byte("\r\n"); bytes.Contains(data, crlf) {
		data = bytes.ReplaceAll(data, crlf, []byte("\n"))
	}
	data = bytes.TrimPrefix(data, bom) // three bytes on the first line: the lines stay the file's
	doc = &markdownDoc{src: data, maxPieces: int(limit / markdownBytesPerPiece)}
	if at, end, ok := frontMatter(data); ok {
		doc.front, doc.frontAt = bytes.Clone(data[at:end]), at
		for i := range data[:end+3] {
			if data[i] != '\n' {
				data[i] = ' '
			}
		}
	}
	g := &guard{
		file:      file,
		src:       data,
		deadline:  time.Now().Add(timeLimit),
		timeLimit: timeLimit,
		maxNodes:  limit / markdownBytesPerNode,
		maxGoOn:   limit / markdownBytesPerContinuation,
	}
	g.reader.g = g
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stopParse); !ok {
				panic(r)
			}
			doc, err = nil, g.err
		}
	}()
	// A line holds at most two text nodes that the guard does not count with
	// an inline node: the one that ends it, and a code span's. The document
	// is one node more.
	g.add(2*int64(bytes.Count(data, newline)) + 3)
	pc := parser.NewContext()
	pc.Set(guardKey, g)
	doc.root = markdownParser.Parse(text.NewReader(data), parser.WithContext(pc))
	doc.nodes, doc.goOn = g.nodes, g.goOn
	if err := checkDepth(doc.root, data); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return doc, nil
}

// frontMatter finds front matter at the start of data: the offsets where the
// text between its fences starts and ends. The end is the start of the
// closing fence, which is three bytes long.
func frontMatter(data []byte) (at, end int, ok bool) {
	var closers []string
	switch {
	case bytes.HasPrefix(data, []byte("---\n")):
		closers = []string{"---", "..."}
	case bytes.HasPrefix(data, []byte("+++\n")):
		closers = []string{"+++"}
	default:
		return 0, 0, false
	}
	for start := 4; start < len(data); {
		line, _, _ := bytes.Cut(data[start:], newline)
		for _, c := range closers {
			if string(line) == c {
				return 4, start, true
			}
		}
		start += len(line) + 1
	}
	return 0, 0, false
}

// checkDepth refuses a tree that nests more than maxDepth levels below its
// root. The guard has refused blocks nested deeper already; this is for
// inline nodes, emphasis within emphasis and images within images. It walks
// the tree without recursion, since the tree is not yet known to be shallow.
func checkDepth(root ast.Node, src []byte) error {
	depth := 0
	for n := root; n != nil; {
		if c := n.FirstChild(); c != nil {
			if depth++; depth > maxDepth {
				return tooDeep(lineOf(src, nodeStart(c)))
			}
			n = c
			continue
		}
		for n != root && n.NextSibling() == nil {
			n = n.Parent()
			depth--
		}
		if n == root {
			break
		}
		n = n.NextSibling()
	}
	return nil
}

// nodeStart is the offset where n or the nearest node above it starts, or 0.
func nodeStart(n ast.Node) int {
	for ; n != nil; n = n.Parent() {
		if p := n.Pos(); p >= 0 {
			return p
		}
	}
	return 0
}

// lineOf is the line, counted from 1, that holds offset off of src.
func lineOf(src []byte, off int) int {
	return bytes.Count(src[:min(off, len(src))], newline) + 1
}

// guard is what one parse may still spend. Every wrapped parser calls it on
// entry, and it panics with stopParse once the parse is to stop, having set
// err to the refusal.
type guard struct {
	file      string
	src       []byte
	deadline  time.Time
	timeLimit time.Duration
	nodes     int64
	maxNodes  int64
	// goOn counts the continuations, blocks other than paragraphs that go
	// on from a line to the next.
	goOn    int64
	maxGoOn int64
	// block is the block whose inline nodes the parse is reading, and
	// delimiters the runs of * and _ in it so far.
	block      ast.Node
	delimiters int
	// reader is the block an inline parser reads, read through the guard.
	reader guardedReader
	err    error
}

// stopParse is the panic that ends a parse the guard has refused.
type stopParse struct{}

var guardKey = parser.NewContextKey()

func guardOf(pc parser.Context) *guard {
	return pc.Get(guardKey).(*guard)
}

func (g *guard) stop(err error) {
	g.err = err
	panic(stopParse{})
}

// tick stops the parse once its time limit has passed.
func (g *guard) tick() {
	if time.Now().After(g.deadline) {
		g.stop(fmt.Errorf("%s: parsing the Markdown took more than %s, the limit; %s", g.file, g.timeLimit, otherWay))
	}
}

// add counts n nodes made, and stops the parse past the node limit.
func (g *guard) add(n int64) {
	if g.nodes += n; g.nodes > g.maxNodes {
		g.stop(fmt.Errorf("%s: the Markdown parse makes more than %d nodes, the limit, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this dense",
			g.file, g.maxNodes, markdownBytesPerNode))
	}
}

// guardedBlock is a block parser that looks at the guard on every call, and
// refuses to open a block below maxDepth others. The HTML block parser's
// refuses a line longer than maxHTMLLine too.
type guardedBlock struct {
	parser.BlockParser
	html bool
}

func (b guardedBlock) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	g := guardOf(pc)
	g.tick()
	if len(pc.OpenedBlocks()) >= maxDepth {
		_, seg := reader.Position()
		g.stop(fmt.Errorf("%s: %w", g.file, tooDeep(lineOf(g.src, seg.Start))))
	}
	b.checkLine(g, reader)
	n, state := b.BlockParser.Open(parent, reader, pc)
	if n != nil {
		g.add(1)
	}
	return n, state
}

func (b guardedBlock) Continue(n ast.Node, reader text.Reader, pc parser.Context) parser.State {
	g := guardOf(pc)
	g.tick()
	b.checkLine(g, reader)
	if !ast.IsParagraph(n) {
		if g.goOn++; g.goOn > g.maxGoOn {
			_, seg := reader.Position()
			g.stop(fmt.Errorf("%s: line %d: the Markdown parse goes on with more than %d open blocks from line to line, the limit, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this nested",
				g.file, lineOf(g.src, seg.Start), g.maxGoOn, markdownBytesPerContinuation))
		}
	}
	return b.BlockParser.Continue(n, reader, pc)
}

// checkLine refuses a line longer than maxHTMLLine that the HTML block
// parser is about to read.
func (b guardedBlock) checkLine(g *guard, reader text.Reader) {
	if !b.html {
		return
	}
	if line, seg := reader.PeekLine(); len(line) > maxHTMLLine {
		g.stop(fmt.Errorf("%s: line %d: a line of HTML of %d bytes, over the limit of %d; %s",
			g.file, lineOf(g.src, seg.Start), len(line), maxHTMLLine, otherWay))
	}
}

func (b guardedBlock) Close(n ast.Node, reader text.Reader, pc parser.Context) {
	guardOf(pc).tick()
	b.BlockParser.Close(n, reader, pc)
}

// guardedInline is an inline parser that looks at the guard on every call,
// and gives the parser it wraps the guard's reader. A node it returns counts
// three: itself, the text before it, and the text a code span holds on the
// line it starts. A run of * or _ it returns counts against maxDelimiters,
// and gets a processor that looks at the guard when goldmark matches runs.
type guardedInline struct{ parser.InlineParser }

func (p guardedInline) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	g := guardOf(pc)
	g.tick()
	g.reader.Reader = block
	n := p.InlineParser.Parse(parent, &g.reader, pc)
	if n == nil {
		return nil
	}
	g.add(3)
	if d, ok := n.(*parser.Delimiter); ok {
		if parent != g.block {
			g.block, g.delimiters = parent, 0
		}
		if g.delimiters++; g.delimiters > maxDelimiters {
			g.stop(fmt.Errorf("%s: line %d: a paragraph holds more than %d runs of * and _, the limit; %s",
				g.file, lineOf(g.src, d.Segment.Start), maxDelimiters, otherWay))
		}
		d.Processor = guardedProcessor{d.Processor, g}
	}
	return n
}

// guardedReader is the reader of a block's text that an inline parser
// gets. A regular expression it matches reads the text a rune at a time
// through it, and it looks at the guard every clockEvery runes, so a match
// over the rest of a long paragraph stops too. goldmark's raw HTML parser
// matches one from every <.
type guardedReader struct {
	text.Reader
	g     *guard
	runes int
}

func (r *guardedReader) ReadRune() (rune, int, error) {
	if r.runes++; r.runes == clockEvery {
		r.runes = 0
		r.g.tick()
	}
	return r.Reader.ReadRune()
}

// Match is goldmark's own, reading through r.
func (r *guardedReader) Match(reg *regexp.Regexp) bool {
	line, seg := r.Position()
	m := reg.FindReaderSubmatchIndex(r)
	r.SetPosition(line, seg)
	if m == nil {
		return false
	}
	r.Advance(m[1] - m[0])
	return true
}

// guardedCloser is guardedInline for a parser that goldmark also calls when
// a block closes.
type guardedCloser struct{ guardedInline }

func (p guardedCloser) CloseBlock(parent ast.Node, block text.Reader, pc parser.Context) {
	guardOf(pc).tick()
	p.InlineParser.(parser.CloseBlocker).CloseBlock(parent, block, pc)
}

// guardedProcessor matches runs of * and _ as goldmark does, looking at the
// guard on every call. Each match makes a node.
type guardedProcessor struct {
	parser.DelimiterProcessor
	g *guard
}

func (p guardedProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	p.g.tick()
	return p.DelimiterProcessor.CanOpenCloser(opener, closer)
}

func (p guardedProcessor) OnMatch(consumes int) ast.Node {
	p.g.tick()
	p.g.add(1)
	return p.DelimiterProcessor.OnMatch(consumes)
}

// guardedTransformer is a paragraph transformer, goldmark's reader of link
// reference definitions, that looks at the guard on every call.
type guardedTransformer struct{ parser.ParagraphTransformer }

func (t guardedTransformer) Transform(n *ast.Paragraph, reader text.Reader, pc parser.Context) {
	g := guardOf(pc)
	g.tick()
	lines := n.Lines()
	if defs := definitions(lines, g.src); defs*lines.Len() > maxDefinitionCopies {
		g.stop(fmt.Errorf("%s: line %d: a paragraph of %d lines may hold %d link reference definitions, and the two multiplied are over the limit of %d; put blank lines between the definitions",
			g.file, lineOf(g.src, lines.At(0).Start), lines.Len(), defs, maxDefinitionCopies))
	}
	t.ParagraphTransformer.Transform(n, reader, pc)
}

// definitions is an upper bound on the link reference definitions at the
// start of a paragraph of these lines: none unless the first line starts
// with [, and otherwise the lines that do, after an indent of up to three
// spaces.
func definitions(lines *text.Segments, src []byte) int {
	n := 0
	for i := range lines.Len() {
		s := lines.At(i)
		v := s.Value(src)
		line := bytes.TrimLeft(v, " ")
		if len(line) > 0 && line[0] == '[' && len(v)-len(line) <= 3 {
			n++
		} else if i == 0 {
			return 0
		}
	}
	return n
}

// prose is text joined from pieces of a file, with the offset in the file
// where each piece starts, so that an offset in the text maps back to one in
// the file. A piece is either bytes copied from the file or a byte errata
// wrote, a space or a newline, that maps to the offset it stands for.
type prose struct {
	text   []byte
	pieces []piece // in text order
	// maxPieces is the most pieces the prose may hold; zero, as in the zero
	// value, is no bound. over is whether it ran out, and overAt the offset
	// of the file where it would have taken one more.
	maxPieces int
	over      bool
	overAt    int
}

// newProse is prose for this document, bounded to its pieces.
func (d *markdownDoc) newProse() *prose {
	return &prose{maxPieces: d.maxPieces}
}

// full reports whether the prose has as many pieces as it may hold, and
// marks where it ran out, at offset from of the file, the first time. Once
// full the prose takes nothing more.
func (p *prose) full(from int) bool {
	if !p.over && p.maxPieces > 0 && len(p.pieces) >= p.maxPieces {
		p.over, p.overAt = true, from
	}
	return p.over
}

type piece struct {
	at    int  // where the piece starts in the text
	from  int  // where it starts in the file
	wrote bool // whether errata wrote it rather than copied it
}

// copyFrom appends b, which starts at offset from of the file, as a piece of
// its own unless it continues the piece before in the file.
func (p *prose) copyFrom(b []byte, from int) {
	if len(b) == 0 || p.over {
		return
	}
	if n := len(p.pieces); n == 0 || p.pieces[n-1].wrote || p.pieces[n-1].from+len(p.text)-p.pieces[n-1].at != from {
		if p.full(from) {
			return
		}
		p.pieces = append(p.pieces, piece{at: len(p.text), from: from})
	}
	p.text = append(p.text, b...)
}

// write appends b, which stands for offset from of the file.
func (p *prose) write(b byte, from int) {
	if p.full(from) {
		return
	}
	p.pieces = append(p.pieces, piece{at: len(p.text), from: from, wrote: true})
	p.text = append(p.text, b)
}

// offset is the offset in the file of offset at of the text, or 0 when the
// text is empty.
func (p *prose) offset(at int) int {
	if len(p.pieces) == 0 {
		return 0
	}
	lo, hi := 0, len(p.pieces)
	for lo+1 < hi { // the last piece that starts at or before at
		mid := (lo + hi) / 2
		if p.pieces[mid].at <= at {
			lo = mid
		} else {
			hi = mid
		}
	}
	pc := p.pieces[lo]
	if pc.wrote {
		return pc.from
	}
	return pc.from + at - pc.at
}

// atSpace reports whether the text is empty or ends in a space or newline,
// where a browser shows no further space.
func (p *prose) atSpace() bool {
	n := len(p.text)
	return n == 0 || p.text[n-1] == ' ' || p.text[n-1] == '\n'
}

// space appends a space that stands for offset from, unless atSpace.
func (p *prose) space(from int) {
	if !p.atSpace() {
		p.write(' ', from)
	}
}

// copyText appends b, which starts at offset from of the file, as a browser
// shows text: each run of spaces, tabs and newlines as one space, and none
// where atSpace. A lone space is copied, so ordinary text stays one piece.
func (p *prose) copyText(b []byte, from int) {
	start := 0
	for i := 0; i < len(b); {
		if !isBlank(b[i]) {
			i++
			continue
		}
		j := i
		for j < len(b) && isBlank(b[j]) {
			j++
		}
		p.copyFrom(b[start:i], from+start)
		switch {
		case p.atSpace():
		case j-i == 1 && b[i] == ' ':
			p.copyFrom(b[i:j], from+i)
		default:
			p.write(' ', from+i)
		}
		start, i = j, j
	}
	p.copyFrom(b[start:], from+start)
}

// isBlank reports whether b is HTML white space, a browser's blank: a
// space, a tab, a line feed, a form feed or a carriage return, which
// CommonMark leaves in the text when it stands alone.
func isBlank(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\f' || b == '\r' }

// endBlock ends a line of prose: it drops a space at its end, then appends
// a newline that stands for the offset after the text's last byte, unless
// the text is empty or ends in one already.
func (p *prose) endBlock() {
	if n := len(p.text); n > 0 && p.text[n-1] == ' ' {
		p.text = p.text[:n-1]
		if last := len(p.pieces) - 1; p.pieces[last].at == n-1 {
			p.pieces = p.pieces[:last]
		}
	}
	if n := len(p.text); n > 0 && p.text[n-1] != '\n' {
		p.write('\n', p.offset(n-1)+1)
	}
}

// prose is the document's prose: the text of each paragraph, heading and
// list item, as inline writes it, each on a line of its own, and the text a
// browser shows after an HTML block's end on its last line. With code it
// holds instead each code block, HTML block and the front matter, line for
// line as the block holds them. A block of a kind it does not know is an
// error, so that a goldmark extension cannot make text disappear.
func (d *markdownDoc) prose(code bool) (*prose, error) {
	p := d.newProse()
	if code && d.front != nil {
		p.copyFrom(d.front, d.frontAt)
	}
	if err := p.blocks(d.src, d.root, code); err != nil {
		return nil, err
	}
	if p.over {
		return nil, fmt.Errorf("line %d: the prose is made of more than %d pieces of the file, the limit, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this broken up",
			lineOf(d.src, p.overAt), p.maxPieces, markdownBytesPerPiece)
	}
	// The last block's newline ends nothing, and would give ^$ a match.
	if n := len(p.text); n > 0 && p.text[n-1] == '\n' {
		p.text = p.text[:n-1]
		if last := len(p.pieces) - 1; p.pieces[last].at == n-1 {
			p.pieces = p.pieces[:last]
		}
	}
	return p, nil
}

// blocks appends the prose of n's child blocks.
func (p *prose) blocks(src []byte, n ast.Node, code bool) error {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c.Kind() {
		case ast.KindParagraph, ast.KindTextBlock, ast.KindHeading:
			p.inline(src, c)
			p.endBlock()
		case ast.KindFencedCodeBlock, ast.KindCodeBlock:
			if code {
				p.lines(src, c)
			}
		case ast.KindHTMLBlock:
			if code {
				p.lines(src, c)
			} else {
				p.htmlTail(src, c.(*ast.HTMLBlock))
			}
		case ast.KindBlockquote, ast.KindList, ast.KindListItem:
			if err := p.blocks(src, c, code); err != nil {
				return err
			}
		case ast.KindThematicBreak, ast.KindLinkReferenceDefinition:
		default:
			return fmt.Errorf("line %d: a block of kind %s, which the text extractor does not read", lineOf(src, firstOffset(c)), c.Kind())
		}
	}
	return nil
}

// firstOffset is the first offset in the file that n or a node below it
// holds, or where the nearest node above it starts.
func firstOffset(n ast.Node) int {
	for c := n; c != nil; c = c.FirstChild() {
		if c.Type() == ast.TypeBlock && c.Lines().Len() > 0 {
			return c.Lines().At(0).Start
		}
		if t, ok := c.(*ast.Text); ok {
			return t.Segment.Start
		}
	}
	return nodeStart(n)
}

// lines appends the lines of a code or HTML block as the block holds them,
// without fences or the markers and indent of the blocks around it, a tab
// that the block's indent splits as the spaces left of it.
func (p *prose) lines(src []byte, n ast.Node) {
	segs := n.Lines()
	for i := range segs.Len() {
		s := segs.At(i)
		for range s.Padding {
			p.write(' ', s.Start)
		}
		p.copyFrom(src[s.Start:s.Stop], s.Start)
	}
	if h, ok := n.(*ast.HTMLBlock); ok && h.HasClosure() {
		p.copyFrom(src[h.ClosureLine.Start:h.ClosureLine.Stop], h.ClosureLine.Start)
	}
	p.endBlock()
}

// htmlEnds is, for each HTML block that ends on the line holding its end,
// what it opens with and what ends it, in lower case. CommonMark's types 1
// to 5: a script, pre, style or textarea element, a comment, a processing
// instruction, a declaration and CDATA.
var htmlEnds = map[ast.HTMLBlockType]struct {
	open string
	ends []string
}{
	ast.HTMLBlockType1: {"<", []string{"</script>", "</pre>", "</style>", "</textarea>"}},
	ast.HTMLBlockType2: {"<!", []string{"-->"}}, // <!--> and <!---> end at once
	ast.HTMLBlockType3: {"<?", []string{"?>"}},
	ast.HTMLBlockType4: {"<!", []string{">"}},
	ast.HTMLBlockType5: {"<![cdata[", []string{"]]>"}},
}

// htmlTail appends what a browser shows after the end of an HTML block of
// types 1 to 5, on the line that holds the end, as a line of prose of its
// own. A block of type 6 or 7, which a blank line ends, gives nothing.
func (p *prose) htmlTail(src []byte, h *ast.HTMLBlock) {
	e, ok := htmlEnds[h.HTMLBlockType]
	if !ok {
		return
	}
	// The end is on the closing line, or else on the first line, after the
	// opening; a block of more lines without one ran to the end of its
	// container unclosed.
	var last text.Segment
	from := 0
	switch {
	case h.HasClosure():
		last = h.ClosureLine
	case h.Lines().Len() == 1:
		last = h.Lines().At(0)
		from = bytes.IndexByte(src[last.Start:last.Stop], '<') + len(e.open)
	default:
		return
	}
	line := asciiLower(src[last.Start:last.Stop]) // as long as the line: end indexes it
	end := -1
	for _, m := range e.ends {
		if i := bytes.Index(line[min(from, len(line)):], []byte(m)); i >= 0 && (end < 0 || from+i+len(m) < end) {
			end = from + i + len(m)
		}
	}
	if end < 0 {
		return
	}
	p.visible(src[last.Start+end:last.Stop], last.Start+end)
	p.endBlock()
}

// visible appends the text a browser shows of HTML b, which starts at offset
// from of the file: a tag of lineTags stands for a space, any other tag and
// a comment for nothing, and the rest is text, an entity as written.
func (p *prose) visible(b []byte, from int) {
	for i := 0; i < len(b); {
		j := i
		for j < len(b) && !tagAt(b, j) {
			j++
		}
		p.copyText(b[i:j], from+i)
		if j == len(b) {
			return
		}
		end := []byte(">")
		if bytes.HasPrefix(b[j:], []byte("<!--")) {
			end = []byte("-->")
		}
		k := bytes.Index(b[j+1:], end)
		if k < 0 {
			return
		}
		if breaksLine(b[j:]) {
			p.space(from + j)
		}
		i = j + 1 + k + len(end)
	}
}

// asciiLower is b with A to Z in lower case and every other byte as it is,
// so that an offset in it is an offset in b.
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// lineTags are the elements a browser starts a new line at, or puts a cell
// or a rule between: each of their tags is a space in the prose. Any other
// tag, a comment or an inline element such as <code>, <a>, <b> or <sub>,
// joins the text on either side of it.
var lineTags = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`address article aside blockquote br caption center dd details dialog div
		dl dt fieldset figcaption figure footer form h1 h2 h3 h4 h5 h6 header hr legend li main nav
		dir hgroup menu noscript ol optgroup option p pre search section summary table tbody td
		tfoot th thead tr ul`) {
		lineTags[t] = true
	}
}

// breaksLine reports whether b starts with the tag, opening or closing, of
// one of lineTags: the name ends at a blank, a / or a >, so a custom element
// such as <h1-x> is not h1.
func breaksLine(b []byte) bool {
	if len(b) == 0 || b[0] != '<' {
		return false
	}
	i := 1
	if i < len(b) && b[i] == '/' {
		i++
	}
	j := i
	for j < len(b) && !isBlank(b[j]) && b[j] != '/' && b[j] != '>' {
		j++
	}
	return j < len(b) && lineTags[string(asciiLower(b[i:j]))]
}

// tagAt reports whether b holds at i a < that starts a tag, a comment, a
// declaration or a processing instruction.
func tagAt(b []byte, i int) bool {
	if b[i] != '<' || i+1 == len(b) {
		return false
	}
	c := b[i+1]
	return c == '/' || c == '!' || c == '?' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// inline appends the prose of n's inline children: their text, spaced as
// copyText spaces it, with a line break and a tag of lineTags as one space,
// any other tag or comment as nothing, and without code span backticks,
// emphasis markers or link destinations. A link or an image keeps its text, and an autolink its
// address.
func (p *prose) inline(src []byte, n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			p.copyText(src[c.Segment.Start:c.Segment.Stop], c.Segment.Start)
			if c.SoftLineBreak() || c.HardLineBreak() {
				p.space(c.Segment.Stop)
			}
		case *ast.AutoLink: // <address>, so the address starts after <
			p.copyFrom(c.Label(src), c.Pos()+1)
		case *ast.RawHTML:
			if s := c.Segments; s.Len() > 0 && breaksLine(src[s.At(0).Start:s.At(0).Stop]) {
				p.space(nodeStart(c))
			}
		default:
			p.inline(src, c)
		}
	}
}
