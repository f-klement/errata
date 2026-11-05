// Package htmlparse is the HTML parse that the readers of HTML share: the
// html kind reads a file with it, and the text and markdown kinds read a
// Markdown file's HTML blocks with it. Scanner runs golang.org/x/net/html's
// tokenizer over the bytes and gives each token's offsets, from the
// tokenizer's raw bytes, so a line is always the file's; it leaves out what
// a browser does not show as text. It lives below core, as mdparse does,
// since no reader imports another.
package htmlparse

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// BytesPerToken is how many bytes of max_bytes allow one token of the
// tokenizer: a tag, a comment, a doctype or a run of text. The tokenizer
// reads each byte once, so a file's tokens are at most its bytes; the bound
// is what a reader spends on each token it keeps, a piece of text, a cell
// or an item. The densest of the real pages measured, RE2's syntax page,
// has a token for every 6.4 bytes (docs/reference.md#limits).
const BytesPerToken = 4

// MaxTokens is the most tokens a file of up to limit bytes may hold: one
// for every BytesPerToken bytes, and one more, so that a limit below
// BytesPerToken still reads a token.
func MaxTokens(limit int64) int64 { return limit/BytesPerToken + 1 }

// TooManyTokensError is the refusal of HTML with more tokens than its limit.
// At is the offset where the token past the limit starts.
type TooManyTokensError struct {
	At  int
	Max int64
}

func (e *TooManyTokensError) Error() string {
	return fmt.Sprintf("the HTML holds more than %d tokens, the limit, one for every %d bytes of max_bytes; set max_bytes: higher to read a file this broken up",
		e.Max, BytesPerToken)
}

// errNoProgress stops a scan whose loop runs past what its input allows,
// which only a broken scanner does.
var errNoProgress = errors.New("the HTML scan ran past the steps its input allows")

// errOffsets is the refusal of a tokenizer whose raw bytes are not the
// input's, which would put every line after it wrong.
var errOffsets = errors.New("the HTML tokenizer's bytes are not the file's, so no line would be right")

// Kind is what a token Scanner gives is.
type Kind int

const (
	// Text is text a browser shows, its blanks folded.
	Text Kind = iota
	// Pre is text a browser shows as written: in a pre, a listing or a
	// textarea element.
	Pre
	// StartTag is a start tag, self closing or not.
	StartTag
	// EndTag is an end tag.
	EndTag
	// Comment is a comment, <!-- to --> or --!>, or one with no end.
	Comment
)

// hidden are the elements a browser shows no text of, or whose text it
// reads as no HTML: the tokenizer reads the text of each as one token,
// which the scanner leaves out. A browser shows the text of xmp and
// plaintext, both obsolete; errata leaves it out. A template element, which
// the tokenizer does not know, is left out whole by the scanner.
var hidden = map[string]bool{"script": true, "style": true, "title": true, "iframe": true,
	"noscript": true, "noembed": true, "noframes": true, "xmp": true, "plaintext": true}

// restartTokens is what a scan of several blocks counts for each tokenizer
// it starts after the first, as tokens: a tokenizer allocates a buffer of
// 4096 bytes, so a fresh one costs what a token for every 4 bytes of it
// would.
const restartTokens = 4096 / BytesPerToken

// Scanner reads the tokens of HTML one at a time, with the offsets of their
// bytes. It gives the start and end tags, the comments and the text a
// browser shows; it leaves out a doctype, a processing instruction and
// other bogus comments, the text of the hidden elements, and everything
// from a <template> to its </template>. Inside an svg or a math element it
// reads CDATA as text and every element as foreign content (foreign.go),
// as a browser does, and leaves out the text of the veiled ones.
//
// src may hold several blocks of HTML, one after another, each read as a
// file of its own (Blocks): the Markdown readers scan every HTML block of a
// file with one scanner. One tokenizer reads them all while each block
// ends between two tokens, or inside text the tokenizer reads as plain
// text, which the scanner splits at the block's end. A block that ends in
// the middle of a tag, a comment or the text of a raw element leaves the
// tokenizer in a state the next block must not inherit, so the scanner
// starts a fresh tokenizer at the next block, at the cost of restartTokens.
type Scanner struct {
	z          *html.Tokenizer
	src        []byte
	start, end int
	kind       Kind
	name       string
	closing    bool
	attrs      bool
	class      string
	href       string
	attrsRead  bool
	fontAttr   bool   // whether the start tag has a color, a face or a size
	encoding   []byte // the start tag's encoding attribute
	cond       bool   // whether the start tag has a requiredExtensions or a systemLanguage
	tokens     int64
	maxTokens  int64
	err        error
	pos        int    // where the tokenizer's next token starts in src
	limit      int    // where the tokenizer's input ends in src
	ends       []int  // where each block ends in src
	block      int    // the block of the token
	split      [2]int // text the tokenizer read past a block's end, still to give
	tmpl       *nest  // the elements open in a template's content, nil outside one
	foreign    bool   // whether the tag is a foreign element's (Foreign)
	raw        string // the element whose text the next token is, if the tokenizer reads it raw
	rawOpen    string // the element whose text the tokenizer reads raw, until its end tag
	rawFrom    int    // where that element's start tag starts
	skipTo     int    // the tokens of a block read again end after this offset
	dropLF     bool   // whether a line feed that starts the next text is the start tag's, as after <pre>
	nest       nest   // the elements open in the block
	steps      int    // the passes of Next's loop
	modeSet    bool   // whether a token decided the page's mode
	maxSteps   int    // the bound on steps a test sets, 0 for stepLimit's
}

// NewScanner scans src, one block, and refuses it past maxTokens tokens.
// src has every line end read as LF (core.LineFeeds), as the HTML standard
// reads a CRLF and a lone CR before it tokenizes; the html kind and the
// Markdown parse fold it so.
func NewScanner(src []byte, maxTokens int64) *Scanner {
	s := &Scanner{z: html.NewTokenizer(bytes.NewReader(src)), src: src, maxTokens: maxTokens, ends: []int{len(src)}, limit: len(src)}
	s.nest.quirks = true // until the first doctype says otherwise
	return s
}

// Blocks makes src a run of blocks, each ending where ends says, in order,
// the last at the end of src, each read as alone, with elements perhaps
// open before it (nest). Call it before the first Next.
func (s *Scanner) Blocks(ends []int) {
	// A Markdown file's page is in standards mode, as a site renders it.
	s.ends, s.nest.outside, s.nest.quirks = ends, true, false
}

// Headings has the scanner follow headings as it follows pre elements:
// HeadingOpen says whether one is open, and a heading's end that errata
// cannot place is refused (nest).
func (s *Scanner) Headings() { s.nest.headings = true }

// Block is the index of the block that holds the token.
func (s *Scanner) Block() int { return s.block }

// Next moves to the next token and reports whether there is one. At the
// end of src, or on a refusal, it is false; Err says which.
func (s *Scanner) Next() bool {
	for s.err == nil {
		// Every pass of the loop reads a token, gives a piece of text the
		// tokenizer read past a block's end, moves to a block or starts a
		// tokenizer, each at most twice for a byte or a block of src, so
		// the passes are bounded by its size; past the bound the scan is
		// broken, and stops rather than loop.
		if s.steps++; s.steps > s.stepLimit() {
			s.err = errNoProgress
			return false
		}
		if s.split[0] < s.split[1] {
			if s.textSplit() {
				return true
			}
			continue
		}
		for s.pos >= s.ends[s.block] {
			if !s.nextBlock(s.raw != "") {
				return false
			}
		}
		s.z.AllowCDATA(namespace(s.active().top()) != "") // CDATA is text in foreign content
		tt := s.z.Next()
		if tt == html.ErrorToken {
			if err := s.z.Err(); !errors.Is(err, io.EOF) {
				s.err = err
				return false
			}
			switch {
			case s.limit < len(s.src):
				s.pos = s.limit // the block is read; the next starts afresh
			case s.block+1 == len(s.ends):
				return false
			default:
				// A token left open ran to the end of the last block: the
				// block is read again from it, as alone, or from the start
				// tag of a raw element it is in, as for a token cut by the
				// block's end. What the tokenizer read past the block's end
				// counts.
				s.charge(len(s.src)-s.ends[s.block], s.pos)
				from := s.pos
				if s.rawOpen != "" {
					s.raw, s.skipTo, from = s.rawOpen, s.pos, s.rawFrom
				}
				s.restart(from, s.ends[s.block])
			}
			continue
		}
		raw := s.z.Raw()
		start, end := s.pos, s.pos+len(raw)
		s.pos = end
		if end > len(s.src) || !bytes.Equal(raw, s.src[start:end]) {
			s.err = errOffsets
			return false
		}
		if s.tokens++; s.tokens > s.maxTokens {
			s.err = &TooManyTokensError{At: start, Max: s.maxTokens}
			return false
		}
		if end <= s.skipTo {
			continue // read again from a raw element's start tag: given before
		}
		start = max(start, s.skipTo) // a raw text read again gives only its rest
		after, dropLF := s.raw, s.dropLF
		s.raw, s.dropLF = "", false
		if blockEnd := s.ends[s.block]; end > blockEnd {
			if tt != html.TextToken || after != "" || bytes.HasPrefix(raw, []byte("<![CDATA[")) {
				// The tokenizer read this token past the block's end for
				// nothing, a comment or a script's text to the end of the
				// HTML at worst: those bytes count, so that blocks left open
				// cost no more than max_bytes in all.
				s.charge(end-blockEnd, start)
			}
			switch {
			case s.rawOpen != "" && (tt != html.TextToken || after != ""):
				// The text of a raw element, or its end tag, cut by the
				// block's end: the block is read again from the element's
				// start tag, which a fresh tokenizer needs to read the text
				// raw, by one that ends with the block, as alone; what the
				// scan gave before start it gives no more.
				s.raw, s.dropLF, s.skipTo = s.rawOpen, dropLF, start
				s.restart(s.rawFrom, blockEnd)
				continue
			case tt == html.TextToken && !bytes.HasPrefix(raw, []byte("<![CDATA[")):
				s.split = [2]int{blockEnd, end} // plain text: the next block's own
			default:
				// A tag, a comment or a CDATA the block leaves open reads as
				// the block alone reads it: the rest of the block again, by
				// a tokenizer that ends where the block does.
				s.raw, s.dropLF = after, dropLF
				s.restart(start, blockEnd)
				continue
			}
			end = blockEnd
		}
		s.start, s.end = start, end
		if s.keep(tt, after, dropLF) {
			return true
		}
	}
	return false
}

// textSplit gives the part in the block of the text the tokenizer read
// past a block's end, and reports whether there is one to give.
func (s *Scanner) textSplit() bool {
	for s.split[0] >= s.ends[s.block] {
		if !s.nextBlock(false) {
			s.split = [2]int{}
			return false
		}
	}
	s.start, s.end = s.split[0], min(s.split[1], s.ends[s.block])
	s.split[0] = s.end
	return s.keep(html.TextToken, "", false)
}

// nextBlock moves to the next block, with none of the last one's elements
// open, and reports whether there is one. With restart, the tokenizer is in
// the raw text of an element the last block opened, and a fresh one starts
// at the block.
func (s *Scanner) nextBlock(restart bool) bool {
	if s.block+1 >= len(s.ends) {
		return false
	}
	s.block++
	s.tmpl, s.raw, s.rawOpen, s.dropLF = nil, "", "", false
	s.nest.reset()
	if restart || s.limit < len(s.src) {
		s.restart(s.ends[s.block-1], len(s.src))
	}
	return true
}

// stepLimit is the most passes Next's loop may make: 4 for each byte and
// each block of src, and 16 more, unless a test set maxSteps.
func (s *Scanner) stepLimit() int {
	if s.maxSteps > 0 {
		return s.maxSteps
	}
	return 4*(len(s.src)+len(s.ends)) + 16
}

// charge counts n bytes a tokenizer read for nothing as tokens, one for
// every BytesPerToken bytes or part of them, and refuses the scan past its
// limit at offset at.
func (s *Scanner) charge(n, at int) {
	if s.tokens += int64((n + BytesPerToken - 1) / BytesPerToken); s.tokens > s.maxTokens {
		s.err = &TooManyTokensError{At: at, Max: s.maxTokens}
	}
}

// restart starts a fresh tokenizer that reads src from at to end, and
// counts it.
func (s *Scanner) restart(at, end int) {
	s.z, s.pos, s.limit, s.split = html.NewTokenizer(bytes.NewReader(s.src[at:end])), at, end, [2]int{}
	if s.tokens += restartTokens; s.tokens > s.maxTokens {
		s.err = &TooManyTokensError{At: at, Max: s.maxTokens}
	}
}

// lineFeed is the length of the line feed text starts with, 0 for none,
// and whether text is that line feed alone. src's line ends are folded, so
// a line feed is a LF.
func lineFeed(text []byte) (n int, alone bool) {
	if bytes.HasPrefix(text, []byte("\n")) {
		return 1, len(text) == 1
	}
	return 0, false
}

// keep classifies the token the tokenizer is at, of type tt, after the raw
// element raw and with dropLF, and reports whether Next gives it.
func (s *Scanner) keep(tt html.TokenType, raw string, dropLF bool) bool {
	s.name, s.foreign = "", false
	switch tt {
	case html.TextToken:
		s.modeSet = s.modeSet || len(bytes.Trim(s.src[s.start:s.end], " \t\r\n\f")) > 0
		if s.tmpl != nil || hidden[raw] {
			return false
		}
		if bytes.HasPrefix(s.src[s.start:s.end], []byte("<![CDATA[")) {
			s.start += len("<![CDATA[")
			if bytes.HasSuffix(s.src[s.start:s.end], []byte("]]>")) {
				s.end -= len("]]>")
			}
		}
		if dropLF {
			n, alone := lineFeed(s.src[s.start:s.end])
			s.start += n
			s.dropLF = alone // text of the line feed alone leaves the pre empty
		}
		// A browser reopens formatting elements before text it inserts as
		// HTML's: not a raw element's, nor NULs alone or a pre's first line
		// feed, which it drops, nor text in foreign content.
		if raw == "" && len(bytes.Trim(s.src[s.start:s.end], "\x00")) > 0 && !s.foreignToken(&s.nest, false, false) {
			if err := s.nest.reconstruct(); err != "" {
				s.err = &NestError{At: s.start, msg: err}
				return false
			}
		}
		if !s.nest.textDrawn() {
			return false // inside an svg title, say, or right inside an svg (drawn.go)
		}
		s.kind = Text
		if s.nest.pre > 0 || raw != "" {
			s.kind = Pre
		}
		return s.start < s.end
	case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
		s.modeSet = true // no doctype first: quirks mode
		name, attrs := s.z.TagName()
		s.name, s.attrs, s.class, s.href, s.attrsRead = string(name), attrs, "", "", false
		s.fontAttr, s.encoding, s.cond = false, nil, false
		s.closing = tt == html.EndTagToken
		kept := s.tag(tt == html.SelfClosingTagToken)
		for _, n := range []*nest{&s.nest, s.tmpl} {
			if n != nil && n.work > 0 {
				// A rebuild's work counts as tokens, one for each element
				// or entry it touched (adopt).
				s.charge(n.work*BytesPerToken, s.start)
				n.work = 0
			}
		}
		if s.err != nil {
			return false
		}
		if dropLF && tt == html.EndTagToken && s.nest.ignored {
			s.dropLF = true // a browser ignores the tag: the pre is empty, its first line feed the tag's
		}
		return kept
	case html.CommentToken:
		s.kind = Comment
		return s.tmpl == nil && bytes.HasPrefix(s.src[s.start:s.end], []byte("<!--"))
	case html.DoctypeToken:
		// The first doctype, before any other token but blanks and
		// comments, sets the page's mode, by the standard's rules; any later
		// one, and any in a Markdown block, changes nothing, as in a browser.
		if !s.modeSet && !s.nest.outside {
			s.nest.quirks = doctypeQuirks(string(s.z.Text()))
		}
		s.modeSet = true
	case html.ErrorToken: // Next stops at an error before keep
	}
	return false
}

// tag follows the elements a tag opens or closes that change what the
// scanner gives, and reports whether Next gives the tag. A tag in foreign
// content is foreignTag's, unless it is read as HTML's. A self closing tag
// opens its element, as a browser reads <pre/> as <pre>, but an svg or a
// math, and any foreign element.
func (s *Scanner) tag(selfClosing bool) bool {
	n := s.active()
	if s.foreignToken(n, true, !s.closing) {
		if kept, html := s.foreignTag(n, selfClosing); !html {
			return kept && s.tmpl == nil
		}
	}
	// The tokenizer reads the text after a raw element's start tag raw,
	// inside a template too, and a block that ends in it needs a fresh one.
	switch {
	case s.closing:
		s.rawOpen = ""
	case s.name == "textarea" || hidden[s.name]:
		s.raw, s.rawOpen, s.rawFrom = s.name, s.name, s.start
	}
	if s.tmpl != nil {
		return s.templateTag(selfClosing)
	}
	if s.name == "template" {
		if !s.closing {
			s.tmpl = &nest{inert: true, quirks: s.nest.quirks}
		}
		return false
	}
	s.kind = StartTag
	if s.closing {
		s.kind = EndTag
		if err := s.nest.end(s.name); err != "" {
			s.err = &NestError{At: s.start, msg: err}
			return false
		}
		return true
	}
	// An svg or a math opens foreign content, its own element in its
	// namespace, unless it is self closing: a browser takes any start tag
	// in their namespace as written.
	name, opens := s.name, !selfClosing
	if s.name == "svg" || s.name == "math" {
		name, s.foreign = foreignKey(s.name, s.name), true
	} else {
		opens = true
	}
	if err := s.nest.start(name, opens && !void[name]); err != "" {
		s.err = &NestError{At: s.start, msg: err}
		return false
	}
	// A line feed right after these start tags is the tag's.
	s.dropLF = s.name == "pre" || s.name == "listing" || s.name == "textarea"
	return true
}

// void are the elements that have no end tag and hold nothing.
var void = set(`area base br col embed hr img input keygen link meta param source track wbr`)

// set is the words of s as a set.
func set(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		out[w] = true
	}
	return out
}

// NestError is the refusal of an end tag a browser may read two ways
// inside a pre or a heading, or of elements nested past the limit. At is
// the offset of the tag.
type NestError struct {
	At  int
	msg string
}

func (e *NestError) Error() string { return e.msg }

// Kind is the kind of the token.
func (s *Scanner) Kind() Kind { return s.kind }

// Start is the offset in src where the token starts. For text after the
// start tag of a pre, a listing or a textarea, the line feed that starts it
// is the tag's, as a browser reads it, and not the text's.
func (s *Scanner) Start() int { return s.start }

// End is the offset in src after the token.
func (s *Scanner) End() int { return s.end }

// Bytes is the token's bytes in src, from Start to End.
func (s *Scanner) Bytes() []byte { return s.src[s.start:s.end] }

// Name is a tag's name in lower case.
func (s *Scanner) Name() string { return s.name }

// InPre reports whether the token is inside a pre or a listing element.
func (s *Scanner) InPre() bool { return s.nest.pre > 0 }

// HeadingOpen reports whether a heading is open after the token, when the
// scanner follows headings (Headings).
func (s *Scanner) HeadingOpen() bool { return s.nest.heads > 0 }

// Class is the class attribute of a start tag, "" for none.
func (s *Scanner) Class() string {
	s.readAttrs()
	return s.class
}

// Href is the href attribute of a start tag, "" for none.
func (s *Scanner) Href() string {
	s.readAttrs()
	return s.href
}

// readAttrs reads a start tag's class and href once: the tokenizer gives
// a tag's attributes one pass only.
func (s *Scanner) readAttrs() {
	if s.attrsRead || s.kind != StartTag {
		return
	}
	s.attrsRead = true
	for more := s.attrs; more; {
		var key, val []byte
		key, val, more = s.z.TagAttr()
		switch string(key) {
		case "class":
			s.class = string(val)
		case "href":
			s.href = string(val)
		case "color", "face", "size":
			s.fontAttr = true
		case "encoding":
			s.encoding = val
		case "requiredextensions", "systemlanguage":
			s.cond = true
		}
	}
}

// CommentText is the offsets of a comment's text: from after its <!-- to
// before its --> or --!>. <!--> and <!---> are empty comments, as in a
// browser. ok is false for a comment that runs to the end of the file with
// no end, which errata reads as none, as CommonMark does.
func (s *Scanner) CommentText() (start, end int, ok bool) {
	b := s.Bytes()
	start = s.start + len("<!--")
	switch {
	case bytes.HasSuffix(b, []byte("--!>")) && len(b) >= len("<!----!>"):
		return start, s.end - len("--!>"), true
	case bytes.HasSuffix(b, []byte("-->")):
		return start, max(start, s.end-len("-->")), true
	}
	return 0, 0, false
}

// Tokens is how many tokens the scan has read.
func (s *Scanner) Tokens() int64 { return s.tokens }

// Err is the refusal that stopped the scan, or nil at the end of src.
func (s *Scanner) Err() error { return s.err }

// Ignored reports whether the tag opened and closed nothing, as a browser
// ignores a stray end tag, or a cell's start tag outside a table.
func (s *Scanner) Ignored() bool { return s.nest.ignored }

// BreaksLine reports whether the token is a start or end tag of one of
// the elements a browser starts a new line at (LineTag) that opens or
// closes one, as a browser ignores a stray end tag such as </li> with no
// li open, or a tag that closes one open, as </listing> closes a div.
func (s *Scanner) BreaksLine() bool {
	return (s.kind == StartTag || s.kind == EndTag) && (LineTag(s.name) && !s.foreign && !s.nest.ignored || s.nest.closedLine)
}

// StandardOnly reports whether the scan has followed a rule of the HTML
// standard that golang.org/x/net/html v0.62.0, the browser tests' oracle,
// does not: the adoption agency took a fourth formatting element below a
// block off the stack (adopt). The browser tests compare no such page
// with the oracle.
func (s *Scanner) StandardOnly() bool { return s.nest.standardOnly }

// Foreign reports whether the tag is an element's of svg or math, an svg
// or a math among them, which no reader takes for the HTML element of its
// name: a td inside an svg is no cell.
func (s *Scanner) Foreign() bool { return s.foreign }

// lineTags are the elements a browser starts a new line at, or puts a cell
// or a rule between. Any other tag, a comment or an inline element such as
// <code>, <a>, <b> or <sub>, joins the text on either side of it.
var lineTags = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`address article aside blockquote br caption center dd details dialog div
		dl dt fieldset figcaption figure footer form h1 h2 h3 h4 h5 h6 header hr legend li main nav
		dir hgroup menu noscript ol optgroup option p pre search section summary table tbody td
		tfoot th thead tr ul`) {
		lineTags[t] = true
	}
}

// LineTag reports whether name, in lower case, is an element a browser
// starts a new line at.
func LineTag(name string) bool { return lineTags[name] }

// Unescape calls put for each piece of b, text of HTML that starts at
// offset from: a run of b as it is, with wrote false, or the text a
// character reference such as &amp; stands for, with wrote true and from
// the offset of its &. Each reference is decoded as html.UnescapeString
// decodes it, on the bytes from its & to the next, so the pass is linear. A
// piece may be empty.
func Unescape(b []byte, from int, put func(piece []byte, at int, wrote bool)) {
	first := bytes.IndexByte(b, '&')
	if first < 0 {
		put(b, from, false)
		return
	}
	put(b[:first], from, false)
	// b[i] is a &, and each pass moves i to the next one, at most once for
	// each byte of b.
	for i, passes := first, 0; i < len(b) && passes < len(b); passes++ {
		j := len(b)
		if next := bytes.IndexByte(b[i+1:], '&'); next >= 0 {
			j = i + 1 + next
		}
		seg := string(b[i:j])
		dec := html.UnescapeString(seg)
		if dec == seg {
			put(b[i:j], from+i, false)
			i = j
			continue
		}
		// The text after the reference is the same in both: what is left
		// differs, and is the reference.
		k := 0
		for k < len(seg) && k < len(dec) && seg[len(seg)-1-k] == dec[len(dec)-1-k] {
			k++
		}
		put([]byte(dec[:len(dec)-k]), from+i, true)
		put(b[j-k:j], from+j-k, false)
		i = j
	}
}
