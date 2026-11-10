// Package mdparse is the Markdown parse that the readers of Markdown share:
// Parse gives goldmark's tree of a file with every node's offsets in it,
// under a guard that bounds its time, its nodes and its continuations, and
// Prose maps text built from pieces of the file back to the file's lines.
// It lives below core, as gitindex does, since no reader imports another.
package mdparse

import (
	"bytes"
	"fmt"
	"slices"
	"time"
	"unicode"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/htmlparse"
)

// errata runs goldmark as CommonMark, without extensions. Its parse is
// quadratic in the file on several shapes, unclosed comments and link
// destinations, deep block quotes and long lists among them, and nothing
// outside it can stop it. So errata gives it parsers that wrap goldmark's own
// and look at a guard on every call. The guard stops the parse, by a panic
// that parse recovers, once it has run past its time limit or past
// one of the bounds below. Between two looks at the guard, goldmark does at
// most one pass over a paragraph or one of the bounded costs the bounds below
// name: it scans HTML and link destinations by hand, a byte at a time. Runs
// of * and _ errata reads itself (parseRun), so that goldmark's matching of
// them happens in calls the guard sees, and no bound models goldmark's code.
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
	// hold. goldmark pairs each run that may close with the runs open before
	// it when the paragraph closes, so a paragraph costs up to the square of
	// its runs, each pair tried a look at the guard (guardedProcessor). Of
	// seven shapes of 4990 runs on one line, the slowest, "****_a*a" 1663
	// times, takes 0.063 s on the build node. errata reads every run itself
	// (parseRun), so an emphasis is two runs, and the most in one paragraph
	// of 5342 real files is 796. The real files are the distinct Markdown
	// files of Go module caches and npm packages' node_modules on a
	// development machine on 2026-10-26, 54 MB; they are not kept. Each
	// renders from errata's parse as from goldmark's own.
	maxDelimiters = 5000

	// maxDefinitionCopies bounds the link reference definitions at the start
	// of a paragraph. goldmark takes each one out by copying the lines of
	// the paragraph after it, in one call, so a paragraph costs up to its
	// definitions times its lines: 3162 definitions, at the limit, take
	// 0.08 s. The most in one paragraph of the real files measured is 343.
	maxDefinitionCopies = 10_000_000

	// markdownTimeLimit bounds the parse of a file of up to the default
	// size; a larger max_bytes raises it in proportion (Time). An
	// ordinary file parses slower than a regex search reads it: 32 MiB of
	// express's changelog, repeated, took 15 s with the machine's four
	// cores loaded three times over.
	markdownTimeLimit = 30 * time.Second

	// MaxHTMLLine is the longest line that may start or continue an HTML
	// block. goldmark scans such a line in calls the guard cannot stop: a
	// tag of 32 MiB took 0.59 s, and 0.02 s at the limit. The limit dates
	// from goldmark v1, whose regular expressions took 11 s over the same
	// line. The longest line of HTML in the real files measured is 1801
	// bytes.
	MaxHTMLLine = 1 << 20

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

// Time is the time limit of the parse of a file of up to limit
// bytes: markdownTimeLimit up to the default size, and in proportion to the
// limit above it.
func Time(limit int64) time.Duration {
	if limit <= core.DefaultMaxBytes {
		return markdownTimeLimit
	}
	return markdownTimeLimit * time.Duration(limit) / core.DefaultMaxBytes
}

// The parsers of goldmark's CommonMark, with their priorities: the list that
// parser.CommonMark gives parser.New, which keeps it out of reach.
// TestCommonMarkParsersAreGoldmarks holds these lists equal to it.
var (
	commonMarkBlocks = []util.PrioritizedValue[parser.BlockParser]{
		util.Prioritized(parser.NewSetextHeadingParser(), 100),
		util.Prioritized(parser.NewThematicBreakParser(), 200),
		util.Prioritized(parser.NewListParser(), 300),
		util.Prioritized(parser.NewListItemParser(), 400),
		util.Prioritized(parser.NewCodeBlockParser(), 500),
		util.Prioritized(parser.NewATXHeadingParser(), 600),
		util.Prioritized(parser.NewFencedCodeBlockParser(), 700),
		util.Prioritized(parser.NewBlockquoteParser(), 800),
		util.Prioritized(parser.NewHTMLBlockParser(), 900),
		util.Prioritized(parser.NewParagraphParser(), 1000),
	}
	commonMarkInlines = []util.PrioritizedValue[parser.InlineParser]{
		util.Prioritized(parser.NewCodeSpanParser(), 100),
		util.Prioritized(parser.NewLinkParser(), 200),
		util.Prioritized(parser.NewAutoLinkParser(), 300),
		util.Prioritized(parser.NewRawHTMLParser(), 400),
		util.Prioritized(parser.NewEmphasisParser(parser.WithParseDelimiterFunc(parseRun)), 500),
	}
	commonMarkTransformers = []util.PrioritizedValue[parser.ParagraphTransformer]{
		util.Prioritized(parser.LinkReferenceParagraphTransformer, 100),
	}
)

// markdownParser is goldmark's CommonMark parser with every block parser,
// inline parser and paragraph transformer wrapped to look at the guard, in
// place of goldmark's own, which WithDefaultParsers(false) leaves out.
// tablesParser is the same with GitHub's tables as well (ParseTables).
var markdownParser, tablesParser = guardedParser(false), guardedParser(true)

// tableTransformers is the option of goldmark's table extension that reads
// a table from a paragraph's lines: the first of the two its ParserOptions
// gives. The second rewrites a code span in a cell that holds \| so that
// its value is a string of its own, which has no offsets in the file, and
// errata leaves it out (Prose.dropPipeEscapes reads the cell instead).
// TestTablesParseAsGoldmark holds the option to the extension's.
var tableTransformers = extension.NewTableParser().ParserOptions(nil)[0]

// guardedParser is markdownParser, with GitHub's tables when tables is set.
func guardedParser(tables bool) parser.Parser {
	var blocks []util.PrioritizedValue[parser.BlockParser]
	var inlines []util.PrioritizedValue[parser.InlineParser]
	var transformers []util.PrioritizedValue[parser.ParagraphTransformer]
	for _, v := range commonMarkBlocks {
		blocks = append(blocks, util.Prioritized[parser.BlockParser](guardedBlock{BlockParser: v.Value, html: v.Value == parser.NewHTMLBlockParser()}, v.Priority))
	}
	for _, v := range commonMarkInlines {
		var p parser.InlineParser = guardedInline{v.Value}
		if _, ok := v.Value.(parser.CloseBlocker); ok {
			p = guardedCloser{p.(guardedInline)}
		}
		inlines = append(inlines, util.Prioritized(p, v.Priority))
	}
	for _, v := range commonMarkTransformers {
		transformers = append(transformers, util.Prioritized[parser.ParagraphTransformer](guardedTransformer{v.Value}, v.Priority))
	}
	opts := []parser.Option{parser.WithDefaultParsers(false), parser.WithBlockParsers(blocks...),
		parser.WithInlineParsers(inlines...), parser.WithParagraphTransformers(transformers...)}
	if tables {
		// goldmark's table transformer has the priority 200, after the link
		// reference definitions at 100; the guard looks before it.
		opts = append(opts, tableTransformers,
			parser.WithParagraphTransformers(util.Prioritized[parser.ParagraphTransformer](tableGuard{}, 150)))
	}
	return parser.New(opts...)
}

// Doc is a Markdown file parsed. Every node's offsets index Src.
type Doc struct {
	// Src is the file with every line end read as LF, a CRLF and a lone CR
	// alike (core.LineFeeds), so its lines are the file's, with its front matter, if any, written over with spaces.
	Src  []byte
	Root ast.Node
	// Front is the front matter's text between its fences, from offset
	// FrontAt of Src; nil when the file has none.
	Front   []byte
	FrontAt int
	// maxPieces is the most pieces its prose may hold, and maxTokens the
	// most tokens the HTML parse may read of its HTML blocks; html is what
	// that parse found, once it has run.
	maxPieces int
	maxTokens int64
	html      *htmlBlocks
	// nodes is the guard's count of the nodes the parse made, an upper
	// bound on those in the tree, and goOn its count of continuations.
	nodes, goOn int64
}

// Parse parses data, which it may change, as CommonMark, past a UTF-8 byte
// order mark at its start, for the file named file, which its refusals name.
// A file that starts with a line of --- or +++ and has a line of the same,
// or of ... after ---, further on, starts with front matter, which the parse
// skips. The parse
// takes at most timeLimit and makes at most limit/markdownBytesPerNode nodes.
// Its tree nests at most maxDepth levels below the document: the text of a
// paragraph in 998 block quotes is at the limit. Past any bound the parse is
// refused.
func Parse(file string, data []byte, limit int64, timeLimit time.Duration) (*Doc, error) {
	return parse(markdownParser, file, data, limit, timeLimit)
}

// ParseTables is Parse with GitHub's tables, as goldmark's table extension
// reads them: a paragraph whose second line, or a later one, is a delimiter
// row such as |---|:-:| becomes a table from the line before it. The guard
// counts, before goldmark reads a table, the most nodes the table may make.
func ParseTables(file string, data []byte, limit int64, timeLimit time.Duration) (*Doc, error) {
	return parse(tablesParser, file, data, limit, timeLimit)
}

// parse is Parse with the parser md.
func parse(md parser.Parser, file string, data []byte, limit int64, timeLimit time.Duration) (doc *Doc, err error) {
	data = core.DropBOM(core.LineFeeds(data)) // three bytes on the first line: the lines stay the file's
	lines := int64(bytes.Count(data, newline))
	doc = &Doc{Src: data, maxPieces: int(limit / markdownBytesPerPiece), maxTokens: htmlparse.MaxTokens(limit)}
	if at, end, ok := frontMatter(data); ok {
		doc.Front, doc.FrontAt = bytes.Clone(data[at:end]), at
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
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stopParse); !ok {
				panic(r)
			}
			doc, err = nil, g.err
		}
	}()
	// A line holds at most two text nodes that the guard does not count with
	// an inline node: the one that ends it, and the one before that, after
	// the line's last inline node, which goldmark starts at a < or another
	// character for an inline parser that then makes no node. The document is
	// one node more.
	g.add(2*lines + 3)
	pc := parser.NewContext()
	pc.Set(guardKey, g)
	doc.Root = md.Parse(data, parser.WithContext(pc))
	doc.nodes, doc.goOn = g.nodes, g.goOn
	if err := checkDepth(doc.Root, data); err != nil {
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
			if depth++; depth > core.MaxDepth {
				return core.TooDeep(LineOf(src, NodeStart(c)))
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

// NodeStart is the offset where n or the nearest node above it starts, or 0.
func NodeStart(n ast.Node) int {
	for ; n != nil; n = n.Parent() {
		if p := n.Pos(); p >= 0 {
			return p
		}
	}
	return 0
}

// LineOf is the line, counted from 1, that holds offset off of src.
func LineOf(src []byte, off int) int {
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
	err        error
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
		g.stop(fmt.Errorf("%s: parsing the Markdown took more than %s, the limit; %s", g.file, g.timeLimit, core.OtherWay))
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
// refuses a line longer than MaxHTMLLine too.
type guardedBlock struct {
	parser.BlockParser
	html bool
}

func (b guardedBlock) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	g := guardOf(pc)
	g.tick()
	if len(pc.OpenedBlocks()) >= core.MaxDepth {
		_, seg := reader.Position()
		g.stop(fmt.Errorf("%s: %w", g.file, core.TooDeep(LineOf(g.src, seg.Start))))
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
				g.file, LineOf(g.src, seg.Start), g.maxGoOn, markdownBytesPerContinuation))
		}
	}
	return b.BlockParser.Continue(n, reader, pc)
}

// checkLine refuses a line longer than MaxHTMLLine that the HTML block
// parser is about to read.
func (b guardedBlock) checkLine(g *guard, reader text.Reader) {
	if !b.html {
		return
	}
	if line, seg := reader.PeekLine(); len(line) > MaxHTMLLine {
		g.stop(fmt.Errorf("%s: line %d: a line of HTML of %d bytes, over the limit of %d; %s",
			g.file, LineOf(g.src, seg.Start), len(line), MaxHTMLLine, core.OtherWay))
	}
}

func (b guardedBlock) Close(n ast.Node, reader text.Reader, pc parser.Context) {
	guardOf(pc).tick()
	b.BlockParser.Close(n, reader, pc)
}

// guardedInline is an inline parser that looks at the guard on every call. A
// node it returns counts three: itself, the text before it, and the text of
// a link's label that goldmark reads with the link in one call. A run of * or
// _ it returns counts against maxDelimiters, and gets a processor that looks
// at the guard each time goldmark tries a pair of runs and each time it
// matches one (guardedProcessor).
type guardedInline struct{ parser.InlineParser }

func (p guardedInline) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	g := guardOf(pc)
	g.tick()
	n := p.InlineParser.Parse(parent, block, pc)
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
				g.file, LineOf(g.src, d.Value.Index().Start), maxDelimiters, core.OtherWay))
		}
		d.Processor = guardedProcessor{d.Processor, g}
	}
	return n
}

// parseRun reads a run of * or _ for goldmark's emphasis parser, in place of
// goldmark's ParseDelimiter. It reads the run as a delimiter run of
// CommonMark (canOpenClose) and leaves every match to goldmark's
// ProcessDelimiters, which tries each pair of runs through the run's
// processor when the paragraph closes. ParseDelimiter first tries to match a
// run on its line at once: in the one call, it scans the line ahead for a
// closing run and walks the paragraph's runs for an opener. No wrapper sees
// that work, so a guard could bound it only through a model of goldmark's
// code, and goldmark 2.1.8 changed the code under errata's model. Read this
// way, all the work emphasis costs is calls the guard sees.
func parseRun(block text.Reader, minimum int, processor parser.DelimiterProcessor, pc parser.Context) ast.Node {
	before := block.PrecedingCharacter()
	line, seg := block.PeekLine()
	if len(line) == 0 || !processor.IsDelimiter(line[0]) {
		return nil
	}
	c, n := line[0], 0
	for n < len(line) && line[n] == c {
		n++
	}
	if n < minimum {
		return nil
	}
	after := ' ' // a line's end counts as a space
	if n < len(line) {
		after = util.ToRune(line, n)
	}
	run := text.NewSingleLineValueFromSegment(seg.WithStop(seg.Start+n), block.Decoder())
	block.Advance(n)
	canOpen, canClose := canOpenClose(before, after, c)
	if !canOpen && !canClose {
		return ast.NewText(run)
	}
	d := parser.NewDelimiter(canOpen, canClose, n, c, processor)
	d.Value = run
	pc.PushDelimiter(d)
	return d
}

// canOpenClose reports whether a run of c between the characters before and
// after it may open emphasis and may close it, by the rules of CommonMark
// 0.31.2, section 6.2: a left-flanking run of * opens and a right-flanking
// one closes, and a run of _ that is both opens only after punctuation and
// closes only before it.
func canOpenClose(before, after rune, c byte) (canOpen, canClose bool) {
	beforeSpace, beforePunct := markdownSpace(before), markdownPunct(before)
	afterSpace, afterPunct := markdownSpace(after), markdownPunct(after)
	left := !afterSpace && (!afterPunct || beforeSpace || beforePunct)
	right := !beforeSpace && (!beforePunct || afterSpace || afterPunct)
	if c == '_' {
		return left && (!right || beforePunct), right && (!left || afterPunct)
	}
	return left, right
}

// markdownSpace is CommonMark's Unicode whitespace character: a space
// separator (Zs), a tab, a line feed, a form feed or a carriage return.
func markdownSpace(r rune) bool {
	return r == '\t' || r == '\n' || r == '\f' || r == '\r' || unicode.Is(unicode.Zs, r)
}

// markdownPunct is CommonMark's Unicode punctuation character: one in the
// general category P or S.
func markdownPunct(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

// guardedCloser is guardedInline for a parser that goldmark also calls when
// a block closes.
type guardedCloser struct{ guardedInline }

func (p guardedCloser) CloseBlock(parent ast.Node, block text.Reader, pc parser.Context) {
	guardOf(pc).tick()
	p.InlineParser.(parser.CloseBlocker).CloseBlock(parent, block, pc)
}

// guardedProcessor matches runs of * and _ as goldmark does, looking at the
// guard on every call: each pair goldmark tries and each match, which makes
// a node. With parseRun reading the runs, these calls are all the work
// emphasis costs.
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
	lines := n.Source()
	if defs := definitions(lines, g.src); defs*len(lines) > maxDefinitionCopies {
		g.stop(fmt.Errorf("%s: line %d: a paragraph of %d lines may hold %d link reference definitions, and the two multiplied are over the limit of %d; put blank lines between the definitions",
			g.file, LineOf(g.src, lines[0].Start), len(lines), defs, maxDefinitionCopies))
	}
	t.ParagraphTransformer.Transform(n, reader, pc)
}

// definitions is an upper bound on the link reference definitions at the
// start of a paragraph of these lines: none unless the first line starts
// with [, and otherwise the lines that do, after an indent of up to three
// spaces.
func definitions(lines []text.Segment, src []byte) int {
	n := 0
	for i, s := range lines {
		v := s.Bytes(src)
		line := bytes.TrimLeft(v, " ")
		if len(line) > 0 && line[0] == '[' && len(v)-len(line) <= 3 {
			n++
		} else if i == 0 {
			return 0
		}
	}
	return n
}

// tableGuard looks at the guard before goldmark's table transformer reads a
// paragraph, refuses a table that pads its short rows with more empty cells
// than it has bytes (tablePadding), and counts the most nodes a table of it
// may make (tableNodes). goldmark's reading of the lines is one pass over
// them.
type tableGuard struct{}

func (tableGuard) Transform(n *ast.Paragraph, _ text.Reader, pc parser.Context) {
	g := guardOf(pc)
	g.tick()
	lines := n.Source()
	if padded, size, at := tablePadding(lines, g.src); padded > size {
		g.stop(fmt.Errorf("%s: line %d: a table pads its short rows with at least %d empty cells, more than its %d bytes, the limit; %s",
			g.file, LineOf(g.src, at), padded, size, core.OtherWay))
	}
	g.add(tableNodes(lines, g.src))
}

// tablePadding is a lower bound on the empty cells goldmark adds to the rows
// of the table it reads from a paragraph of these lines, the table's bytes,
// and the offset where the table starts; 0, 0 and 0 for a paragraph that
// holds no table. goldmark gives every row a cell for each column of the
// delimiter row, so a header of n columns over n rows of one byte makes n
// times n cells from a file of about 6n bytes: quadratic. A row of p pipes
// holds at most p+1 cells, so it is padded with at least the columns less
// p+1. Bounding the padding by the table's bytes, as cmark-gfm bounds the
// cells it completes, keeps the cells of a table linear in its bytes: a row
// holds at most a cell for each of its pipes, one more, and the padding.
func tablePadding(lines []text.Segment, src []byte) (padded, size, at int) {
	i, cols := tableAt(lines, src)
	if i == 0 {
		return 0, 0, 0
	}
	for _, l := range lines[i+1:] {
		padded += max(0, cols-bytes.Count(l.Bytes(src), []byte("|"))-1)
	}
	return padded, lines[len(lines)-1].Stop - lines[i-1].Start, lines[i-1].Start
}

// tableAt is where goldmark's table extension reads a table from a
// paragraph of these lines: the index of its delimiter row and the row's
// columns, or 0 and 0 for no table. goldmark takes the first line after the
// first that is a delimiter row (delimiterColumns), and makes a table there
// unless the line before it, the header, has more cells than the row has
// columns (headerCells); a header of fewer it pads, as it pads any row.
// Either way it reads no further line. TestTableDecisionIsGoldmarks holds
// this to goldmark's own parse.
func tableAt(lines []text.Segment, src []byte) (delimiter, cols int) {
	for i := 1; i < len(lines); i++ {
		cols := delimiterColumns(lines[i].Bytes(src))
		if cols == 0 {
			continue
		}
		if headerCells(lines[i-1].Bytes(src)) > cols {
			return 0, 0
		}
		return i, cols
	}
	return 0, 0
}

// headerCells is the cells of a header row, counted by the loop of goldmark's
// parseRow: the line without the blanks at either end; a | at its start
// skipped and one at its end left out; then a cell for each run up to a |
// that no backslash escapes, or up to the end, while any of the line is
// left, so a | left at the end of what remains, as in |a|a||, starts no
// cell. An empty line has none.
func headerCells(line []byte) int {
	line = util.TrimRightSpace(util.TrimLeftSpace(line))
	pos, limit := 0, len(line)
	if len(line) > 0 && line[0] == '|' {
		pos++
	}
	if len(line) > 0 && line[limit-1] == '|' {
		limit--
	}
	cells := 0
	for pos < limit {
		closure := pos
		for ; closure < limit; closure++ {
			if line[closure] == '|' && (closure == 0 || line[closure-1] != '\\') {
				break
			}
		}
		cells++
		pos = closure + 1
	}
	return cells
}

// delimiterColumns is the columns of line read as goldmark's table
// extension reads a delimiter row, 0 for a line that is none: at most three
// spaces of indent, only blanks, -, | and :, not only -, and between the
// pipes, the blank ends left out, cells of :?-+:? each.
func delimiterColumns(line []byte) int {
	if w, _ := util.IndentWidth(line, 0); w > 3 {
		return 0
	}
	if len(bytes.Trim(line, "-")) == 0 || len(bytes.Trim(line, " \t\n\v\f\r-|:")) > 0 {
		return 0
	}
	cols := bytes.Split(line, []byte("|"))
	if util.IsBlank(cols[0]) {
		cols = cols[1:]
	}
	if len(cols) > 0 && util.IsBlank(cols[len(cols)-1]) {
		cols = cols[:len(cols)-1]
	}
	for _, c := range cols {
		c = bytes.TrimSuffix(bytes.TrimPrefix(bytes.TrimSpace(c), []byte(":")), []byte(":"))
		if len(c) == 0 || len(bytes.Trim(c, "-")) > 0 {
			return 0
		}
	}
	return len(cols)
}

// tableNodes is an upper bound on the nodes goldmark's table transformer
// makes of a paragraph of these lines. It reads a table from the first line
// after the first that is a delimiter row: a row for each line from the one
// before it, and in each row a cell for each column of the delimiter row,
// which has at most one more than its pipes. A cell may hold two text nodes
// that no inline parser makes, so it counts three, and the table, its header
// and its body three more. A line of nothing but blanks, -, | and : may be
// that row, and the bound is the largest any of them gives.
func tableNodes(lines []text.Segment, src []byte) int64 {
	var most int64
	for i := 1; i < len(lines); i++ {
		line := lines[i].Bytes(src)
		if len(bytes.Trim(line, " \t\n\v\f\r-|:")) > 0 {
			continue
		}
		rows := int64(len(lines) - i + 1)
		most = max(most, rows*(3*int64(bytes.Count(line, []byte("|"))+1)+1)+3)
	}
	return most
}

// Prose is text joined from pieces of a file, with the offset in the file
// where each piece starts, so that an offset in the text maps back to one in
// the file. A piece is either bytes copied from the file or a byte errata
// wrote, a space or a newline, that maps to the offset it stands for.
type Prose struct {
	Text   []byte
	pieces []piece // in text order
	// maxPieces is the most pieces the prose may hold; zero, as in the zero
	// value, is no bound. over is whether it ran out, and overAt the offset
	// of the file where it would have taken one more.
	maxPieces int
	over      bool
	overAt    int
	// dropPipeEscapes is whether copyText drops the backslash of \|, as
	// GitHub reads a table cell (Cell).
	dropPipeEscapes bool
	// doc is the document the prose reads, whose HTML blocks it reads as
	// the HTML parse found them (htmlText).
	doc *Doc
}

// NewProse is empty prose for this document, bounded to its pieces.
func (d *Doc) NewProse() *Prose {
	return &Prose{maxPieces: d.maxPieces, doc: d}
}

// full reports whether the prose has as many pieces as it may hold, and
// marks where it ran out, at offset from of the file, the first time. Once
// full the prose takes nothing more.
func (p *Prose) full(from int) bool {
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

// CopyFrom appends b, which starts at offset from of the file, as a piece of
// its own unless it continues the piece before in the file.
func (p *Prose) CopyFrom(b []byte, from int) {
	if len(b) == 0 || p.over {
		return
	}
	if n := len(p.pieces); n == 0 || p.pieces[n-1].wrote || p.pieces[n-1].from+len(p.Text)-p.pieces[n-1].at != from {
		if p.full(from) {
			return
		}
		p.pieces = append(p.pieces, piece{at: len(p.Text), from: from})
	}
	p.Text = append(p.Text, b...)
}

// write appends b, which stands for offset from of the file.
func (p *Prose) write(b byte, from int) {
	if p.full(from) {
		return
	}
	p.pieces = append(p.pieces, piece{at: len(p.Text), from: from, wrote: true})
	p.Text = append(p.Text, b)
}

// Offset is the offset in the file of offset at of the text, or 0 when the
// text is empty.
func (p *Prose) Offset(at int) int {
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
func (p *Prose) atSpace() bool {
	n := len(p.Text)
	return n == 0 || p.Text[n-1] == ' ' || p.Text[n-1] == '\n'
}

// space appends a space that stands for offset from, unless atSpace.
func (p *Prose) space(from int) {
	if !p.atSpace() {
		p.write(' ', from)
	}
}

// Cell appends the prose of a table cell of GitHub's tables: Inline's, with
// the backslash of each \| dropped, in a code span too, since in a table
// the pipe is escaped for the table and not for the inline text.
func (p *Prose) Cell(src []byte, cell ast.Node) {
	p.dropPipeEscapes = true
	p.Inline(src, cell)
	p.dropPipeEscapes = false
}

// copyText appends b, which starts at offset from of the file, as a browser
// shows text (copyRun), and without the backslash of each \| when
// dropPipeEscapes is set.
func (p *Prose) copyText(b []byte, from int) {
	if !p.dropPipeEscapes {
		p.copyRun(b, from)
		return
	}
	start := 0
	for {
		i := bytes.Index(b[start:], []byte(`\|`))
		if i < 0 {
			p.copyRun(b[start:], from+start)
			return
		}
		p.copyRun(b[start:start+i], from+start)
		start += i + 1 // the pipe starts the next run
	}
}

// copyRun appends b, which starts at offset from of the file, as a browser
// shows text: each run of spaces, tabs and newlines as one space, and none
// where atSpace. A lone space is copied, so ordinary text stays one piece.
func (p *Prose) copyRun(b []byte, from int) {
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
		p.CopyFrom(b[start:i], from+start)
		switch {
		case p.atSpace():
		case j-i == 1 && b[i] == ' ':
			p.CopyFrom(b[i:j], from+i)
		default:
			p.write(' ', from+i)
		}
		start, i = j, j
	}
	p.CopyFrom(b[start:], from+start)
}

// isBlank reports whether b is HTML white space, a browser's blank: a
// space, a tab, a line feed, a form feed or a carriage return, which
// CommonMark leaves in the text when it stands alone.
func isBlank(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\f' || b == '\r' }

// EndBlock ends a line of prose: it drops a space at its end, then appends
// a newline that stands for the offset after the text's last byte, unless
// the text is empty or ends in one already.
func (p *Prose) EndBlock() {
	if n := len(p.Text); n > 0 && p.Text[n-1] == ' ' {
		p.Text = p.Text[:n-1]
		if last := len(p.pieces) - 1; p.pieces[last].at == n-1 {
			p.pieces = p.pieces[:last]
		}
	}
	if n := len(p.Text); n > 0 && p.Text[n-1] != '\n' {
		p.write('\n', p.Offset(n-1)+1)
	}
}

// Prose is the document's prose: the text of each paragraph, heading and
// list item, as inline writes it, each on a line of its own; the text a
// browser shows of an HTML block a blank line ends and of a pre or a
// textarea block (htmlText); and the text a browser shows after the end of
// any other HTML block on its last line (htmlTail). With code it
// holds instead each code block, HTML block and the front matter, line for
// line as the block holds them. A block of a kind it does not know is an
// error, so that a goldmark extension cannot make text disappear.
func (d *Doc) Prose(code bool) (*Prose, error) {
	p := d.NewProse()
	if code && d.Front != nil {
		p.CopyFrom(d.Front, d.FrontAt)
	}
	if err := p.Blocks(d.Src, d.Root.FirstChild(), nil, code); err != nil {
		return nil, err
	}
	if err := d.Err(p); err != nil {
		return nil, err
	}
	// The last block's newline ends nothing, and would give ^$ a match.
	if n := len(p.Text); n > 0 && p.Text[n-1] == '\n' {
		p.Text = p.Text[:n-1]
		if last := len(p.pieces) - 1; p.pieces[last].at == n-1 {
			p.pieces = p.pieces[:last]
		}
	}
	return p, nil
}

// Err refuses prose of this document that ran out of pieces: its text is
// cut short.
func (d *Doc) Err(p *Prose) error {
	if p.over {
		return fmt.Errorf("line %d: the prose is made of more than %d pieces of the file, the limit, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this broken up",
			LineOf(d.Src, p.overAt), p.maxPieces, markdownBytesPerPiece)
	}
	return nil
}

// Blocks appends the prose of the blocks from first up to stop, its later
// sibling, or to the last sibling when stop is nil, as Prose reads them.
func (p *Prose) Blocks(src []byte, first, stop ast.Node, code bool) error {
	for c := first; c != nil && c != stop; c = c.NextSibling() {
		switch c.Kind() {
		case ast.KindParagraph, ast.KindHeading:
			p.Inline(src, c)
			p.EndBlock()
		case ast.KindCodeBlock:
			if code {
				p.Lines(src, c.(*ast.CodeBlock).Value.Segments())
			}
		case ast.KindHTMLBlock:
			h := c.(*ast.HTMLBlock)
			switch {
			case code:
				p.Lines(src, h.Value.Segments())
			case shownAsHTML(h, src):
				if err := p.htmlText(src, h); err != nil {
					return err
				}
			default:
				p.htmlTail(src, h)
			}
		case ast.KindBlockquote, ast.KindList, ast.KindListItem:
			if err := p.Blocks(src, c.FirstChild(), nil, code); err != nil {
				return err
			}
		case ast.KindThematicBreak, ast.KindLinkReferenceDefinition:
		default:
			return fmt.Errorf("line %d: a block of kind %s, which the text extractor does not read", LineOf(src, FirstOffset(c)), c.Kind())
		}
	}
	return nil
}

// FirstOffset is the first offset in the file that n or a node below it
// holds, or where the nearest node above it starts.
func FirstOffset(n ast.Node) int {
	for c := n; c != nil; c = c.FirstChild() {
		if b, ok := c.(ast.BlockNode); ok && len(b.Source()) > 0 {
			return b.Source()[0].Start
		}
		if t, ok := c.(*ast.Text); ok {
			return t.Value.Index().Start
		}
	}
	return NodeStart(n)
}

// Lines appends the lines of a code or HTML block as the block holds them,
// without fences or the markers and indent of the blocks around it, a tab
// that the block's indent splits as the spaces left of it.
func (p *Prose) Lines(src []byte, segs []text.Segment) {
	for _, s := range segs {
		for range s.Padding {
			p.write(' ', s.Start)
		}
		p.CopyFrom(src[s.Start:s.Stop], s.Start)
	}
	p.EndBlock()
}

// htmlEnds is, for each HTML block that ends on the line holding its end,
// what ends it, in lower case. CommonMark's types 1 to 5: a script, pre,
// style or textarea element, a comment, a processing instruction, a
// declaration and CDATA.
var htmlEnds = map[ast.HTMLBlockKind][]string{
	ast.HTMLBlockKind1: {"</script>", "</pre>", "</style>", "</textarea>"},
	ast.HTMLBlockKind2: {"-->"}, // <!--> and <!---> end at once
	ast.HTMLBlockKind3: {"?>"},  // and <?>, as in goldmark and a browser
	ast.HTMLBlockKind4: {">"},
	ast.HTMLBlockKind5: {"]]>"},
}

// shownAsHTML reports whether the prose reads HTML block h through the HTML
// parse (htmlText): a block of CommonMark's type 6 or 7, which a blank
// line ends, such as <div> or <p align=center>, and a pre or textarea
// element, whose text a browser shows. The other blocks of type 1, a
// script or a style, and the comments, processing instructions,
// declarations and CDATA of types 2 to 5 show only the text after their
// end (htmlTail).
func shownAsHTML(h *ast.HTMLBlock, src []byte) bool {
	switch h.HTMLBlockKind {
	case ast.HTMLBlockKind6, ast.HTMLBlockKind7:
		return true
	case ast.HTMLBlockKind1:
		first := h.Value.Segments()[0] // a block of type 1 starts on its first line
		open := asciiLower(bytes.TrimLeft(src[first.Start:first.Stop], " \t"))
		return bytes.HasPrefix(open, []byte("<pre")) || bytes.HasPrefix(open, []byte("<textarea"))
	case ast.HTMLBlockKind2, ast.HTMLBlockKind3, ast.HTMLBlockKind4, ast.HTMLBlockKind5:
	}
	return false
}

// htmlTail appends what a browser shows after the end of an HTML block of
// types 1 to 5, on the line that holds the end, as a line of prose of its
// own. A block of type 6 or 7, which a blank line ends, gives nothing; the
// prose reads those with htmlText.
func (p *Prose) htmlTail(src []byte, h *ast.HTMLBlock) {
	ends, ok := htmlEnds[h.HTMLBlockKind]
	if !ok {
		return
	}
	// The end is on the block's last line. A block of one line holds it
	// there or nowhere, and may share the opening's bytes, as <?> does. A
	// block of more lines ends at the first line after its first that holds
	// an end; a last line without one ran to the end of its container
	// unclosed.
	segs := h.Value.Segments()
	last := segs[len(segs)-1]
	line := asciiLower(src[last.Start:last.Stop]) // as long as the line: end indexes it
	end := -1
	for _, m := range ends {
		if i := bytes.Index(line, []byte(m)); i >= 0 && (end < 0 || i+len(m) < end) {
			end = i + len(m)
		}
	}
	if end < 0 {
		return
	}
	p.visible(src[last.Start+end:last.Stop], last.Start+end)
	p.EndBlock()
}

// visible appends the text a browser shows of HTML b, which starts at offset
// from of the file: a tag of htmlparse.LineTag stands for a space, any other tag and
// a comment for nothing, and the rest is text, an entity as written.
func (p *Prose) visible(b []byte, from int) {
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

// breaksLine reports whether b starts with the tag, opening or closing, of
// a line element (htmlparse.LineTag): the name ends at a blank, a / or a
// >, so a custom element such as <h1-x> is not h1.
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
	return j < len(b) && htmlparse.LineTag(string(asciiLower(b[i:j])))
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

// Inline appends the prose of n's inline children: their text, spaced as
// copyText spaces it, with a line break and a tag of a line element
// (htmlparse.LineTag) as one space, any other tag or comment as nothing,
// and without code span backticks, emphasis markers or link destinations.
// A link or an image keeps its text, and an autolink its address.
func (p *Prose) Inline(src []byte, n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			s := c.Value.Index()
			p.copyText(src[s.Start:s.Stop], s.Start)
			if c.SoftLineBreak() || c.HardLineBreak() {
				p.space(s.Stop)
			}
		case *ast.CodeSpan:
			for _, s := range codeSpanText(c, src) {
				p.copyText(src[s.Start:s.Stop], s.Start)
			}
		case *ast.AutoLink:
			s := c.Label.Index()
			p.CopyFrom(src[s.Start:s.Stop], s.Start)
		case *ast.RawHTML:
			if s := c.Value.Index(); breaksLine(src[s.Start:s.Stop]) {
				p.space(NodeStart(c))
			}
		default:
			p.Inline(src, c)
		}
	}
}

// codeSpanText is the text of a code span: its lines between the backtick
// strings, less the space or newline that CommonMark strips from each end
// when both ends have one and the text is not all blanks. The rule is read
// from the indices, which hold the unstripped text in every goldmark: up to
// v2.1.14 Value.Bytes gave the stripped text, and from v2.1.15 it gives the
// raw bytes, so a test of its length stopped seeing the strip.
func codeSpanText(c *ast.CodeSpan, src []byte) []text.Index {
	ix := slices.Clone(c.Value.Indices())
	if stripsEnds(ix, src) {
		ix[0].Start++
		ix[len(ix)-1].Stop--
	}
	return ix
}

// stripsEnds is whether CommonMark strips a code span's ends, as goldmark
// decides it: the text starts and ends with a space or a newline, and is not
// all blanks. goldmark also refuses a one line span under two bytes, which
// those two already refuse.
func stripsEnds(ix []text.Index, src []byte) bool {
	if len(ix) == 0 {
		return false
	}
	first, last := src[ix[0].Start:ix[0].Stop], src[ix[len(ix)-1].Start:ix[len(ix)-1].Stop]
	if len(first) == 0 || len(last) == 0 {
		return false
	}
	return spaceOrNewline(first[0]) && spaceOrNewline(last[len(last)-1]) &&
		slices.ContainsFunc(ix, func(s text.Index) bool { return !util.IsBlank(src[s.Start:s.Stop]) })
}

// spaceOrNewline is a byte a code span's ends may lose.
func spaceOrNewline(b byte) bool { return b == ' ' || b == '\n' }

// newline is a line break, as bytes.Count takes it.
var newline = []byte("\n")
