package core

import (
	"bytes"
	"math"
	"math/bits"
	"strings"
)

// A template's blocks. An engine renders a template before any tool reads
// it, and a value inside a block may not be in the rendered file at all, or
// be there more than once: {% if x %}, then a=1, then {% endif %} gives a
// only when x holds. TemplateRead marks every entry whose line lies in such
// a block (Entry.InBlock), and TemplateNotLiteral reads the mark, so a rule
// over the value is unknown rather than judging a file that may not exist in
// that form. A reader that binds one value from another, as shell and
// envfile do, asks TemplateBlocks while it reads, so a value that takes its
// text from a marked one is marked too. Every reader that calls
// TemplateNotLiteral does one or the other (TestTemplateReadersScanBlocks
// in package extract).
//
// Each engine's blocks are found by their tags alone, without parsing the
// expressions in them. Where errata cannot tell, it marks: a tag it does
// not know opens a block, and a closing tag no block opened (a block
// errata did not see), a block or a tag left open (a template the engine
// refuses) mark the whole file. A mark costs a rule only an unknown.

// templateLang is one engine's tag syntax, as a bit of a template file's
// set.
type templateLang uint8

const (
	langJinja     templateLang = 1 << iota // {% if %} ... {% endif %}, and {{ }} and {# #}
	langGo                                 // Go's text/template: {{ if }} ... {{ end }}
	langERB                                // <% if x %> ... <% end %>
	langTerraform                          // %{ if x } ... %{ endif }
	langCount     = iota
)

// templateLangs are the engines a template suffix names: .tmpl and .tpl are
// Jinja's and Go's (Helm and chezmoi write Go templates under them), .tpl
// and .tftpl Terraform's templatefile.
var templateLangs = map[string]templateLang{
	".j2": langJinja, ".jinja": langJinja, ".jinja2": langJinja,
	".tmpl": langJinja | langGo, ".tpl": langJinja | langGo | langTerraform,
	".tftpl": langTerraform, ".erb": langERB,
}

// templateLangsOf is the set of engines whose tags base's suffix names, 0
// for a file that is no template.
func templateLangsOf(base string) templateLang {
	for _, s := range templateSuffixes {
		if strings.HasSuffix(base, s) {
			return templateLangs[s]
		}
	}
	return 0
}

// TemplateRead is read for a template file, with every entry inside one of
// its engine's blocks marked (Entry.InBlock); for another file it is read.
// A reader that binds one value from another, as shell and envfile do,
// asks TemplateBlocks itself while it reads instead.
func TemplateRead(file string, read ReadFunc) ReadFunc {
	_, base := FileParts(file)
	if templateLangsOf(base) == 0 {
		return read
	}
	return func(data []byte, m *Meter, visit func(*Entry) error) error {
		blocks, err := TemplateBlocks(file, data, m)
		if err != nil {
			return err
		}
		return read(data, m, func(e *Entry) error {
			if e.InBlock, err = blocks.Holds(e.Line); err != nil {
				return err
			}
			return visit(e)
		})
	}
}

// Blocks reports which lines of a template lie inside one of its engine's
// blocks, for a reader to ask while it reads. A nil Blocks, a file that is
// no template's, holds no line.
//
// It keeps no list of blocks. TemplateBlocks scans the file once, to learn
// whether a tag marks every line (a stray closing tag, a tag left open,
// Jinja's extends); then Holds scans again as far as the line it is asked
// about, keeping only the block open there and the last one closed. Asked
// about lines in file order, as the readers ask, the second scan runs once;
// asked about a line before the one asked last, it starts again, charged
// again. Its memory is a few words whatever the file holds.
type Blocks struct {
	scan  *blockScan
	whole bool
	asked int
}

// TemplateBlocks is the blocks of data, the bytes of file, charged to m;
// nil for a file that is no template.
func TemplateBlocks(file string, data []byte, m *Meter) (*Blocks, error) {
	_, base := FileParts(file)
	langs := templateLangsOf(base)
	if langs == 0 {
		return nil, nil
	}
	first, err := newBlockScan(data, langs, m)
	if err != nil {
		return nil, err
	}
	if err := first.run(math.MaxInt); err != nil {
		return nil, err
	}
	// The engine refuses a template with a stray closing tag, a tag or a
	// block left open, and renders only the blocks of one that extends
	// another: no line of it is in the rendered file as written.
	if first.whole || first.unread || first.total() > 0 {
		return &Blocks{whole: true}, nil
	}
	second, err := newBlockScan(data, langs, m)
	return &Blocks{scan: second}, err
}

// Holds reports whether line lies inside a block.
func (b *Blocks) Holds(line int) (bool, error) {
	switch {
	case b == nil:
		return false, nil
	case b.whole:
		return true, nil
	case line < b.asked:
		again, err := newBlockScan(b.scan.data, b.scan.langs, b.scan.m)
		if err != nil {
			return false, err
		}
		b.scan = again
	}
	b.asked = line
	s := b.scan
	if err := s.run(line); err != nil {
		return false, err
	}
	return s.total() > 0 && s.openLine <= line || s.last.first <= line && line <= s.last.last, nil
}

// lineSpan is the lines from first to last, both held.
type lineSpan struct{ first, last int }

// tagWork is the fixed work of a template tag, besides its bytes. A byte
// that may start a tag costs one more, so a scan that went back over the
// same bytes would spend its budget rather than run on.
const tagWork = 64

// blockScan is one pass over a template's tags.
type blockScan struct {
	data     []byte
	langs    templateLang
	m        *Meter
	starts   string // the bytes a tag of langs starts with
	lines    *LineCursor
	i        int // where the scan goes on
	depth    [langCount]int
	openLine int      // the line the outermost open block starts on, while one is
	last     lineSpan // the last block closed, or a region the scan must mark
	whole    bool     // a closing tag no block opened, or extends
	unread   bool     // a tag left open
}

// newBlockScan is a scan of data from its start, charged its bytes.
func newBlockScan(data []byte, langs templateLang, m *Meter) (*blockScan, error) {
	s := &blockScan{data: data, langs: langs, m: m, lines: NewLineCursor(data), last: lineSpan{0, -1}}
	if langs&(langJinja|langGo) != 0 {
		s.starts += "{"
	}
	if langs&langERB != 0 {
		s.starts += "<"
	}
	if langs&langTerraform != 0 {
		s.starts += "%"
	}
	return s, m.Charge(len(data))
}

// run reads the tags that start on a line up to line.
func (s *blockScan) run(line int) error {
	for !s.whole && !s.unread {
		j := bytes.IndexAny(s.data[s.i:], s.starts)
		if j < 0 {
			s.i = len(s.data)
			return nil
		}
		p := s.i + j
		if s.lines.At(p) > line {
			return nil
		}
		if err := s.m.Charge(1); err != nil { // each byte a tag may start at
			return err
		}
		next, err := s.tag(p, s.langs, s.m)
		if err != nil {
			return err
		}
		s.i = next
	}
	return nil
}

// total is the depth of every engine's blocks together.
func (s *blockScan) total() int {
	n := 0
	for _, d := range s.depth {
		n += d
	}
	return n
}

// change applies a tag at off..end of engine lang that opens (+1), closes
// (-1) or does neither (0) a block.
func (s *blockScan) change(lang templateLang, off, end, delta int) {
	k := bits.TrailingZeros8(uint8(lang))
	switch {
	case delta > 0:
		if s.total() == 0 {
			s.openLine = s.lines.At(off)
		}
		s.depth[k]++
	case delta < 0 && s.depth[k] == 0:
		s.whole = true
	case delta < 0:
		s.depth[k]--
		if s.total() == 0 {
			s.last = lineSpan{s.openLine, s.lines.At(end)}
		}
	}
}

// hides reports whether the body of a tag of lang holds the start of a tag
// of another engine the file's suffix names, which that engine reads and
// the scan, passing over the body, does not: in a .tmpl, a Jinja comment
// {# {{ if .x }} #}, or a Go action that holds {%.
func (s *blockScan) hides(lang templateLang, body []byte) bool {
	if lang == langGo && s.langs&langGo == 0 {
		lang = langJinja // Jinja's own {{ }}, which reads its body as Jinja
	}
	other := s.langs &^ lang
	return other&langGo != 0 && bytes.Contains(body, []byte("{{")) ||
		other&langJinja != 0 && (bytes.Contains(body, []byte("{%")) || bytes.Contains(body, []byte("{#"))) ||
		other&langTerraform != 0 && bytes.Contains(body, []byte("%{")) ||
		other&langERB != 0 && bytes.Contains(body, []byte("<%"))
}

// hide marks the lines of a tag whose body hides another engine's tags.
// Inside an open block the block already holds them.
func (s *blockScan) hide(off, end int) {
	if s.total() == 0 {
		s.last = lineSpan{s.lines.At(off), s.lines.At(end)}
	}
}

// tag reads the tag, if any, at p, and returns where the scan goes on.
func (s *blockScan) tag(p int, langs templateLang, m *Meter) (int, error) {
	rest := s.data[p:]
	var lang templateLang
	var closer string
	switch {
	case langs&langJinja != 0 && bytes.HasPrefix(rest, []byte("{%")):
		lang, closer = langJinja, "%}"
	case langs&langJinja != 0 && bytes.HasPrefix(rest, []byte("{#")):
		lang, closer = langJinja, "#}"
	case langs&(langJinja|langGo) != 0 && bytes.HasPrefix(rest, []byte("{{")):
		lang, closer = langGo, "}}"
	case langs&langERB != 0 && bytes.HasPrefix(rest, []byte("<%%")):
		return p + 3, nil // <%% is a literal <%
	case langs&langERB != 0 && bytes.HasPrefix(rest, []byte("<%")):
		lang, closer = langERB, "%>"
	case langs&langTerraform != 0 && bytes.HasPrefix(rest, []byte("%%{")):
		return p + 3, nil // %%{ is a literal %{
	case langs&langTerraform != 0 && bytes.HasPrefix(rest, []byte("%{")):
		lang, closer = langTerraform, "}"
	default:
		return p + 1, nil
	}
	n := bytes.Index(rest[2:], []byte(closer))
	if n < 0 {
		s.unread = true // a tag the engine refuses
		return len(s.data), nil
	}
	end := p + 2 + n + len(closer)
	if err := m.Charge(tagWork); err != nil {
		return 0, err
	}
	body := rest[2 : 2+n]
	if s.hides(lang, body) {
		defer s.hide(p, end)
	}
	switch {
	case closer == "#}":
		return end, nil // a Jinja comment
	case lang == langGo && langs&langGo == 0:
		return end, nil // Jinja's {{ }} prints, and opens nothing
	case lang == langGo:
		s.change(langGo, p, end, goTag(body))
	case lang == langJinja:
		delta, raw := jinjaTag(body)
		if delta == wholeFile {
			s.whole = true
			return end, nil
		}
		s.change(langJinja, p, end, delta)
		if raw {
			return s.endRaw(end, m)
		}
	case lang == langERB:
		s.change(langERB, p, end, erbTag(body))
	case lang == langTerraform:
		s.change(langTerraform, p, end, terraformTag(body))
	}
	return end, nil
}

// endRaw passes over a Jinja raw block's text, whose tags are text, to the
// tag that ends it, and closes the block there.
func (s *blockScan) endRaw(i int, m *Meter) (int, error) {
	for {
		j := bytes.Index(s.data[i:], []byte("{%"))
		if j < 0 {
			return len(s.data), nil // the block stays open to the end
		}
		p := i + j
		n := bytes.Index(s.data[p+2:], []byte("%}"))
		if n < 0 {
			return len(s.data), nil
		}
		end := p + 2 + n + 2
		if err := m.Charge(tagWork); err != nil {
			return 0, err
		}
		if word, _ := tagWord(trimTag(s.data[p+2:p+2+n], "-+")); word == "endraw" {
			s.change(langJinja, p, end, -1)
			return end, nil
		}
		i = end
	}
}

// wholeFile is a tag's change that marks every line: Jinja's extends,
// whose template renders only the blocks it overrides.
const wholeFile = 2

// trimTag is a tag's body without the white space and the trim marks, such
// as Jinja's - and +, at its ends.
func trimTag(body []byte, marks string) []byte {
	return bytes.Trim(body, " \t\r\n\f\v"+marks)
}

// tagWord is the name a tag's body starts with, ASCII letters and _, and
// the rest after it. Every name the engines give a meaning to is such a
// word.
func tagWord(body []byte) (string, []byte) {
	n := 0
	for n < len(body) && strings.IndexByte(tagLetters, body[n]) >= 0 {
		n++
	}
	return string(body[:n]), body[n:]
}

// tagLetters are the bytes of a tag's name.
const tagLetters = "_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// jinjaTag is how a {% %} tag changes the depth, and whether it opens a
// raw block. end... closes; else, elif and the tags that hold no body
// (include, import, from, do, break, continue, print, and set with an =)
// change nothing; extends marks the whole file; every other tag, if, for,
// macro, call, filter, block, with, autoescape, trans, set with no =, an
// extension's own and one errata does not know, opens a block.
func jinjaTag(body []byte) (delta int, raw bool) {
	word, rest := tagWord(trimTag(body, "-+"))
	switch {
	case strings.HasPrefix(word, "end"):
		return -1, false
	case word == "extends":
		return wholeFile, false
	case word == "raw":
		return 1, true
	case word == "set" && bytes.Contains(rest, []byte("=")):
		return 0, false
	}
	switch word {
	case "else", "elif", "include", "import", "from", "do", "break", "continue", "print", "pluralize":
		return 0, false
	}
	return 1, false
}

// goTag is how a Go template action changes the depth: if, range, with,
// define and block open, end closes, and else and every other action, a
// comment too, change nothing.
func goTag(body []byte) int {
	word, _ := tagWord(trimTag(body, "-"))
	switch word {
	case "if", "range", "with", "define", "block":
		return 1
	case "end":
		return -1
	}
	return 0
}

// erbTag is how an ERB tag changes the depth, from the Ruby it holds: end
// or } closes; a statement that starts with if, unless, while, until,
// case, for or begin and does not end in end, or code that ends in do, do
// |x|, { or { |x|, opens; a comment, else, elsif, when and anything else
// change nothing.
func erbTag(body []byte) int {
	if bytes.HasPrefix(body, []byte("#")) {
		return 0
	}
	code := trimTag(bytes.TrimLeft(body, "=-"), "-")
	word, _ := tagWord(code)
	switch {
	case word == "end" || string(code) == "}":
		return -1
	case opensRubyBlock(code):
		return 1
	}
	switch word {
	case "if", "unless", "while", "until", "case", "for", "begin":
		if last := bytes.Fields(code); string(last[len(last)-1]) != "end" {
			return 1
		}
	}
	return 0
}

// opensRubyBlock reports whether code ends by opening a block: do or {,
// with |params| after it or not.
func opensRubyBlock(code []byte) bool {
	if bytes.HasSuffix(code, []byte("|")) {
		// Text before the |params|; with no | to open them, nothing opens.
		code = bytes.TrimRight(code[:max(0, bytes.LastIndexByte(code[:len(code)-1], '|'))], " \t")
	}
	if bytes.HasSuffix(code, []byte("{")) {
		return true
	}
	if !bytes.HasSuffix(code, []byte("do")) {
		return false
	}
	before := code[:len(code)-2]
	return len(before) == 0 || before[len(before)-1] == ' ' || before[len(before)-1] == '\t'
}

// terraformTag is how a %{ } directive changes the depth: if and for open,
// endif and endfor close, else changes nothing.
func terraformTag(body []byte) int {
	switch word, _ := tagWord(trimTag(body, "~")); word {
	case "if", "for":
		return 1
	case "endif", "endfor":
		return -1
	}
	return 0
}
