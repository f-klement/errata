package text

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"

	"github.com/f-klement/errata/internal/extract/core"
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
// and look at a guard on every call. The guard stops the parse, by a panic
// that parseMarkdown recovers, once it has run past its time limit or past
// one of the bounds below. Between two looks at the guard, goldmark does at
// most one pass over a paragraph or one of the bounded costs the bounds below
// name: it scans HTML and link destinations by hand, a byte at a time.
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
	// hold. goldmark matches each closing run against the runs open before
	// it, so a paragraph costs up to the square of its runs, some of it
	// between two looks at the guard. On goldmark 2.1.8, of ten shapes of
	// 5000 runs on one line with 300 bytes after them, the slowest, such as
	// "****_a*a" 1663 times, takes 0.062 s on the build node and up to
	// 0.09 s on the slowest worker node. From 256 bytes or more before a
	// line's end goldmark matches no emphasis at once, so a long line of
	// closed emphasis counts two runs for each. The most in one paragraph
	// of the real files below, under 2.1.8, is 286.
	maxDelimiters = 5000

	// markdownScanPerByte is how many bytes goldmark may scan for runs of *
	// and _ that close emphasis, for each byte of max_bytes. From a run that
	// opens with fewer than goldmarkScanLine bytes left on its line,
	// goldmark looks to the end of the line for a run to close it, so runs
	// that close nothing cost up to 255 bytes each. Without this limit, 32
	// MiB of lines of 85 such runs scanned 330 MB, ten bytes for each of the
	// file's, in 2.2 s on the build node before the node limit stopped them.
	// The guard counts the rest of the line for each run that may open there
	// and is left open, and the bytes up to the closing run for each
	// emphasis matched at once: the bytes goldmark's hop to the closing run
	// reads. goldmark then reads the same bytes once more to check them, so
	// it reads at most twice what the guard counts.
	//
	// From a run there that may also close, goldmark first walks the runs
	// of the paragraph before it for one of the same character that opens.
	// An emphasis it then matches at once adds no run, so maxDelimiters
	// alone does not bound the walk: 4990 runs and then lines of a*b*c took
	// 3.99 s at 1 MiB and 16.6 s at 4 MiB on the build node, and ran to the
	// 30 s time limit at 32 MiB. The guard counts a byte for each run of the
	// paragraph so far: before an unmatched run that may close, and before
	// an emphasis matched at once whose opening run follows a character
	// that goldmark does not read as a space (goldmarkSpace), since only
	// such a run may close. The build node refuses that shape at 32 MiB in
	// at most 0.39 s, and the same with a vertical tab or a form feed before
	// each run.
	//
	// On goldmark 2.1.8, with its hop and its walk counted in a copy of its
	// source, the guard counts at least both on each of 5341 real files, on
	// 100035 random documents of runs, words, links, blocks and spaces, and
	// on ten shapes of runs at 32 MiB but for the one call in which the node
	// limit stopped three of them, before the guard counted its scan. Six
	// of those shapes reach this limit without a walk, and the build node
	// refuses each in at most 0.90 s. Of the real files, the most is 140541
	// bytes, from a changelog of 2.9 MB, and the densest, a file of 82
	// bytes, counts 0.99 for each of its bytes. The real files are the
	// Markdown files of a Go module cache and of npm packages' node_modules
	// on a development machine on 2026-10-25, 42 MB once duplicates were
	// removed; they are not kept.
	markdownScanPerByte = 2

	// goldmarkScanLine is goldmark's fastPathMax, 256 since 2.1.8: from a
	// run with this many bytes or more left on its line, goldmark scans
	// nothing and matches the run when the paragraph closes, so the guard
	// counts no scan for it. goldmark 2.1.6 scanned from every run to the
	// end of its line, so a line of runs cost the square of its length:
	// 100000 runs that close nothing, on one line of 400 KB, scanned 988 MB
	// in 2.2 s before the run limit refused them, where 2.1.8 refuses them
	// at once. TestGuardScansWhereGoldmarkDoes fails when goldmark's length
	// and this one differ.
	goldmarkScanLine = 256

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
	// block. goldmark scans such a line in calls the guard cannot stop: a
	// tag of 32 MiB took 0.59 s, and 0.02 s at the limit. The limit dates
	// from goldmark v1, whose regular expressions took 11 s over the same
	// line. The longest line of HTML in the real files measured is 1801
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
		util.Prioritized(parser.NewEmphasisParser(), 500),
	}
	commonMarkTransformers = []util.PrioritizedValue[parser.ParagraphTransformer]{
		util.Prioritized(parser.LinkReferenceParagraphTransformer, 100),
	}
)

// markdownParser is goldmark's CommonMark parser with every block parser,
// inline parser and paragraph transformer wrapped to look at the guard, in
// place of goldmark's own, which WithDefaultParsers(false) leaves out.
var markdownParser = func() parser.Parser {
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
	return parser.New(parser.WithDefaultParsers(false), parser.WithBlockParsers(blocks...),
		parser.WithInlineParsers(inlines...), parser.WithParagraphTransformers(transformers...))
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
	// bound on those in the tree, goOn its count of continuations, and
	// scanned its count of the bytes goldmark scanned for closing runs.
	nodes, goOn, scanned int64
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
		maxScan:   limit * markdownScanPerByte,
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
	g.add(2*int64(bytes.Count(data, newline)) + 3)
	pc := parser.NewContext()
	pc.Set(guardKey, g)
	doc.root = markdownParser.Parse(data, parser.WithContext(pc))
	doc.nodes, doc.goOn, doc.scanned = g.nodes, g.goOn, g.scanned
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
			if depth++; depth > core.MaxDepth {
				return core.TooDeep(lineOf(src, nodeStart(c)))
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
	// scanned counts the bytes goldmark scans for runs that close
	// emphasis, up to maxScan.
	scanned int64
	maxScan int64
	err     error
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
// refuses a line longer than maxHTMLLine too.
type guardedBlock struct {
	parser.BlockParser
	html bool
}

func (b guardedBlock) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	g := guardOf(pc)
	g.tick()
	if len(pc.OpenedBlocks()) >= core.MaxDepth {
		_, seg := reader.Position()
		g.stop(fmt.Errorf("%s: %w", g.file, core.TooDeep(lineOf(g.src, seg.Start))))
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
			g.file, lineOf(g.src, seg.Start), len(line), maxHTMLLine, core.OtherWay))
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
// at the guard when goldmark matches runs. goldmark matches a pair of runs on
// one line at once when nothing between them is for another parser and the
// opening run has fewer than goldmarkScanLine bytes left on its line, and
// returns the emphasis whole: one node for every two characters of the
// closing run, nested around a text. Each node below the first counts one
// more. Before either, goldmark scans that short rest of the line for the
// closing run, and for a run that may also close walks the paragraph's runs
// for an opener; the guard counts both against maxScan.
type guardedInline struct{ parser.InlineParser }

func (p guardedInline) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	g := guardOf(pc)
	g.tick()
	line, seg := block.PeekLine()
	before := block.PrecedingCharacter()
	n := p.InlineParser.Parse(parent, block, pc)
	if n == nil {
		return nil
	}
	g.add(3)
	switch d := n.(type) {
	case *parser.Delimiter:
		runs := g.runsIn(parent)
		if d.CanOpen && len(line) < goldmarkScanLine {
			g.scan(len(line), seg.Start)
			if d.CanClose {
				g.scan(runs, seg.Start)
			}
		}
		if g.delimiters++; g.delimiters > maxDelimiters {
			g.stop(fmt.Errorf("%s: line %d: a paragraph holds more than %d runs of * and _, the limit; %s",
				g.file, lineOf(g.src, d.Value.Index().Start), maxDelimiters, core.OtherWay))
		}
		d.Processor = guardedProcessor{d.Processor, g}
	case *ast.Emphasis, *ast.Strong:
		rest, _ := block.PeekLine()
		runs := g.runsIn(parent)
		if goldmarkSpace(before) {
			runs = 0 // a run after a space cannot close, so goldmark walked nothing
		}
		g.scan(len(line)-len(rest)+runs, seg.Start)
		for c := n.FirstChild(); c != nil; c = c.FirstChild() {
			g.add(1)
		}
	}
	return n
}

// goldmarkSpace reports whether goldmark reads r, the character before a
// run, as a space when it decides whether the run may close: runeClass in
// goldmark 2.1.8's parser/delimiter.go. Below 0x80 that is its charFlags
// space, util.IsSpace less CR and LF, or a CR or an LF, which is util.IsSpace
// itself: tab, LF, CR and space, and not the vertical tab or the form feed
// that unicode.IsSpace also takes. From 0x80 it is util.IsSpaceRune.
func goldmarkSpace(r rune) bool {
	if r < utf8.RuneSelf {
		return util.IsSpace(byte(r)) //nolint:gosec // r comes from PrecedingCharacter, never negative, so here it is in [0, 0x80)
	}
	return util.IsSpaceRune(r)
}

// runsIn is how many runs of * and _ the guard has counted in block so far,
// none when the parse has moved on to a block other than the last one.
func (g *guard) runsIn(block ast.Node) int {
	if block != g.block {
		g.block, g.delimiters = block, 0
	}
	return g.delimiters
}

// scan counts n bytes that goldmark scanned from offset at for a run that
// closes emphasis, and stops the parse past maxScan.
func (g *guard) scan(n, at int) {
	if g.scanned += int64(n); g.scanned > g.maxScan {
		g.stop(fmt.Errorf("%s: line %d: the Markdown parse scans more than %d bytes for runs of * and _ that close emphasis, the limit, %d for every byte of max_bytes; %s",
			g.file, lineOf(g.src, at), g.maxScan, markdownScanPerByte, core.OtherWay))
	}
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
	lines := n.Source()
	if defs := definitions(lines, g.src); defs*len(lines) > maxDefinitionCopies {
		g.stop(fmt.Errorf("%s: line %d: a paragraph of %d lines may hold %d link reference definitions, and the two multiplied are over the limit of %d; put blank lines between the definitions",
			g.file, lineOf(g.src, lines[0].Start), len(lines), defs, maxDefinitionCopies))
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
		case ast.KindParagraph, ast.KindHeading:
			p.inline(src, c)
			p.endBlock()
		case ast.KindCodeBlock:
			if code {
				p.lines(src, c.(*ast.CodeBlock).Value.Segments())
			}
		case ast.KindHTMLBlock:
			if code {
				p.lines(src, c.(*ast.HTMLBlock).Value.Segments())
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
		if b, ok := c.(ast.BlockNode); ok && len(b.Source()) > 0 {
			return b.Source()[0].Start
		}
		if t, ok := c.(*ast.Text); ok {
			return t.Value.Index().Start
		}
	}
	return nodeStart(n)
}

// lines appends the lines of a code or HTML block as the block holds them,
// without fences or the markers and indent of the blocks around it, a tab
// that the block's indent splits as the spaces left of it.
func (p *prose) lines(src []byte, segs []text.Segment) {
	for _, s := range segs {
		for range s.Padding {
			p.write(' ', s.Start)
		}
		p.copyFrom(src[s.Start:s.Stop], s.Start)
	}
	p.endBlock()
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

// htmlTail appends what a browser shows after the end of an HTML block of
// types 1 to 5, on the line that holds the end, as a line of prose of its
// own. A block of type 6 or 7, which a blank line ends, gives nothing.
func (p *prose) htmlTail(src []byte, h *ast.HTMLBlock) {
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
			p.copyFrom(src[s.Start:s.Stop], s.Start)
		case *ast.RawHTML:
			if s := c.Value.Index(); breaksLine(src[s.Start:s.Stop]) {
				p.space(nodeStart(c))
			}
		default:
			p.inline(src, c)
		}
	}
}

// codeSpanText is the text of a code span: its lines between the backtick
// strings, less the space or newline that CommonMark strips from each end
// when both ends have one and the text is not all blanks. goldmark's Value
// strips them in what it gives, two bytes fewer than its indices hold.
func codeSpanText(c *ast.CodeSpan, src []byte) []text.Index {
	ix := slices.Clone(c.Value.Indices())
	n := 0
	for _, s := range ix {
		n += s.Stop - s.Start
	}
	if len(c.Value.Bytes(src)) == n-2 {
		ix[0].Start++
		ix[len(ix)-1].Stop--
	}
	return ix
}

// newline is a line break, as bytes.Count takes it.
var newline = []byte("\n")
