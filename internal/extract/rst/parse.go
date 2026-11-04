package rst

import (
	"bytes"
	"math"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// doc is a file cut into lines.
type doc struct {
	data   []byte
	starts []int // the offset where each line starts; a byte order mark is no part of the first
}

// newDoc is data cut at its newlines. A CR before a newline is no part of
// its line, so the lines are the file's whether it ends them with LF or
// CRLF. A file that ends in a newline has an empty line after it, which
// is blank and so reads as nothing.
func newDoc(data []byte) *doc {
	d := &doc{data: data, starts: []int{len(data) - len(core.DropBOM(data))}}
	for i, c := range data {
		if c == '\n' {
			d.starts = append(d.starts, i+1)
		}
	}
	return d
}

// lines is how many lines d has.
func (d *doc) lines() int { return len(d.starts) }

// line is line i, counted from 0, without its line end.
func (d *doc) line(i int) []byte {
	end := len(d.data)
	if i+1 < len(d.starts) {
		end = d.starts[i+1]
	}
	l := d.data[d.starts[i]:end]
	l = bytes.TrimSuffix(l, []byte("\n"))
	return bytes.TrimSuffix(l, []byte("\r"))
}

// indent is the column of the first character of line i that is no
// blank, a tab moving to the next multiple of 8 as docutils reads it, and
// its offset in the line. A blank line's offset is its length.
func (d *doc) indent(i int) (col, off int) {
	l := d.line(i)
	for off < len(l) && isSpace(l[off]) {
		col = advance(col, l[off])
		off++
	}
	return col, off
}

// advance is the column after character c at column col: the next
// multiple of 8 for a tab, as docutils reads it, and the next column for
// any other.
func advance(col int, c byte) int {
	if c == '\t' {
		return col/8*8 + 8
	}
	return col + 1
}

// blank reports whether line i holds only blanks.
func (d *doc) blank(i int) bool {
	_, off := d.indent(i)
	return off == len(d.line(i))
}

// isSpace reports whether c is a blank inside a line.
func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\f' || c == '\v' || c == '\r' }

// span is a piece of one line of the file, from start to end, at line,
// counted from 1.
type span struct{ start, end, line int }

// blanks are the characters isSpace reports.
const blanks = " \t\f\v\r"

// piece is line i from offset start to offset end of it.
func (d *doc) piece(i, start, end int) span {
	return span{start: d.starts[i] + start, end: d.starts[i] + end, line: i + 1}
}

// from is line i from offset off of it, without the blanks at its end.
func (d *doc) from(i, off int) span {
	end := len(bytes.TrimRight(d.line(i), blanks))
	return d.piece(i, min(off, end), end)
}

// text is the bytes of s.
func (d *doc) text(s span) []byte { return d.data[s.start:s.end] }

// dedented is line i from column col, or from its first character that
// is no blank when that is before col, with the blanks at its end.
func (d *doc) dedented(i, col int) span {
	l := d.line(i)
	c, off := 0, 0
	for off < len(l) && c < col && isSpace(l[off]) {
		c = advance(c, l[off])
		off++
	}
	return d.piece(i, off, len(l))
}

// block is the lines from first up to end, without the blank lines at
// either end, dedented by the least indent of those that are not blank: an
// indented block, such as a directive's content or a literal block.
func (d *doc) block(first, end int) []span {
	for first < end && d.blank(first) {
		first++
	}
	for end > first && d.blank(end-1) {
		end--
	}
	least := math.MaxInt
	for i := first; i < end; i++ {
		if !d.blank(i) {
			col, _ := d.indent(i)
			least = min(least, col)
		}
	}
	out := make([]span, 0, end-first)
	for i := first; i < end; i++ {
		out = append(out, d.dedented(i, least))
	}
	return out
}

// indented is the end of the block of lines after line i indented more
// than ind, the blank lines inside it included.
func (d *doc) indented(i, ind int) int {
	end := i + 1
	for ; end < d.lines(); end++ {
		if col, _ := d.indent(end); col <= ind && !d.blank(end) {
			break
		}
	}
	return end
}

// content is the column of the lines of the content of the directive at
// line i, at column ind, after its first: the least indent of the lines of
// its block after the first that are not blank, as docutils'
// get_first_known_indented takes it, or three more than ind, as .. and a
// blank take, when the block has no such line (rst delta review 1, finding
// 2). Every line of the block is indented more than ind (indented).
func (d *doc) content(i, ind int) int {
	least := math.MaxInt
	for k, end := i+1, d.indented(i, ind); k < end; k++ {
		if !d.blank(k) {
			col, _ := d.indent(k)
			least = min(least, col)
		}
	}
	if least == math.MaxInt {
		return ind + 3
	}
	return least
}

// options is the line after the options of a directive whose body starts
// at line k, at column ind: the lines indented more than ind that start
// with a colon, each with the lines indented more than it that go on it,
// up to a blank line.
func (d *doc) options(k, ind int) int {
	opt := math.MaxInt // the column of the option before
	for ; k < d.lines() && !d.blank(k); k++ {
		col, off := d.indent(k)
		switch {
		case col > ind && d.line(k)[off] == ':':
			opt = col
		case col > opt: // goes on the option before
		default:
			return k
		}
	}
	return k
}

// parse walks d once, in file order, and hands r each title, block of
// prose, comment, code block and table it finds. Each line is read a fixed
// number of times, so the walk is linear in the file (ADR 0003).
func parse(d *doc, r *reading) {
	for i := 0; i < d.lines() && r.err == nil; {
		if d.blank(i) {
			i++
			continue
		}
		ind, off := d.indent(i)
		t := d.line(i)[off:]
		switch {
		case isExplicit(t):
			i = explicit(d, r, i, ind, off)
		case gridBorder(t):
			i = grid(d, r, i, ind)
		case simpleBorder(t) != nil:
			i = simple(d, r, i, ind)
		case ind == 0 && adornment(t) > 0:
			i = overline(d, r, i)
		case ind == 0 && i+1 < d.lines() && underlines(d.line(i+1), t) && !bullet.Match(t):
			r.title(style{char: d.line(i + 1)[0]}, plain(d, []span{d.from(i, 0)}), i+1)
			i += 2
		default:
			i = paragraph(d, r, i, ind, off)
		}
	}
}

// punctuation is the characters that may adorn a title: the printable
// ASCII characters that are neither letters nor digits, as docutils
// allows.
const punctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// adornment is the length of l when it is an adornment, one character
// that may adorn a title repeated from its first column, blanks after it
// allowed, and 0 when it is none.
func adornment(l []byte) int {
	l = bytes.TrimRight(l, " \t\f\v\r")
	if len(l) == 0 || strings.IndexByte(punctuation, l[0]) < 0 {
		return 0
	}
	for _, c := range l {
		if c != l[0] {
			return 0
		}
	}
	return len(l)
}

// underlines reports whether l is an adornment that underlines a title t,
// which is not empty: at least four characters long, or as wide as the
// title. docutils reads a shorter one under a wider title as text.
func underlines(l, t []byte) bool {
	n := adornment(l)
	return n >= 4 || n >= width(bytes.TrimRight(t, " \t"))
}

// width is the columns t takes, as docutils counts them: two for a wide
// East Asian character, one for any other.
func width(t []byte) int {
	n := 0
	for _, c := range string(t) {
		n++
		if wide(c) {
			n++
		}
	}
	return n
}

// wide reports whether c is an East Asian character that takes two
// columns: Hangul Jamo, the CJK ideographs, syllables and symbols, Hangul
// syllables, the CJK compatibility forms, and the full width forms.
func wide(c rune) bool {
	for _, r := range wideRanges {
		if r[0] <= c && c <= r[1] {
			return true
		}
	}
	return false
}

// wideRanges are the ranges of characters wide reports, first and last.
var wideRanges = [][2]rune{
	{0x1100, 0x115F}, {0x2E80, 0x303E}, {0x3041, 0x4DBF}, {0x4E00, 0xA4CF}, {0xAC00, 0xD7A3},
	{0xF900, 0xFAFF}, {0xFE30, 0xFE4F}, {0xFF00, 0xFF60}, {0xFFE0, 0xFFE6}, {0x20000, 0x3FFFD},
}

// overline reads the lines from i, whose line i is an adornment: a title
// with an overline when the line after it is the title, and the line after
// that the same adornment as line i. Else an adornment shorter than four
// is text; a longer one over a line and another adornment is an error in
// docutils, three lines errata passes over, and alone a transition. It
// returns the line after.
func overline(d *doc, r *reading, i int) int {
	if i+2 < d.lines() && !d.blank(i+1) && bytes.Equal(bytes.TrimRight(d.line(i+2), " \t"), bytes.TrimRight(d.line(i), " \t")) {
		_, off := d.indent(i + 1)
		if underlines(d.line(i), d.line(i + 1)[off:]) {
			r.title(style{char: d.line(i)[0], over: true}, plain(d, []span{d.from(i+1, off)}), i+2)
			return i + 3
		}
	}
	if adornment(d.line(i)) < 4 {
		return paragraph(d, r, i, 0, 0) // docutils reads a short overline as text
	}
	if i+2 < d.lines() && !d.blank(i+1) && adornment(d.line(i+2)) > 0 {
		return i + 3 // an overline and an underline that differ: docutils reads no title, and errata no prose
	}
	return i + 1
}

// listMarker matches the bullet or enumerator that starts a list item,
// and the spaces after it: one of - * +, a number or # followed by . or ),
// or either of those in parentheses.
var listMarker = regexp.MustCompile(`^(?:[-*+]|(?:[0-9]+|#)[.)]|\((?:[0-9]+|#)\)) +`)

// bullet matches a bullet list item's start, which docutils reads before a
// title: a line of - foo over ----- is a list.
var bullet = regexp.MustCompile(`^[-*+](?: |$)`)

// paragraph reads the block of prose that starts at line i, whose first
// character is at column ind and offset off: its lines up to a blank line
// or a change of indent. A list item's marker is no part of its prose, and
// the lines after its first are indented to its text. A paragraph that
// ends in :: is followed by a literal block, the lines indented more than
// it, which is code with no language; the :: reads as one colon, or as
// none after a blank or alone. It returns the line after.
func paragraph(d *doc, r *reading, i, ind, off int) int {
	m := len(listMarker.Find(d.line(i)[off:]))
	return block(d, r, d.from(i, off+m), i+1, ind+m)
}

// block reads a block of prose whose first line is first, and whose lines
// after it, from line j, are at column col, as paragraph says.
func block(d *doc, r *reading, first span, j, col int) int {
	ls := []span{first}
	for ; j < d.lines() && !d.blank(j); j++ {
		c, o := d.indent(j)
		if c != col {
			break
		}
		ls = append(ls, d.from(j, o))
	}
	last := &ls[len(ls)-1]
	t := d.text(*last)
	if !bytes.HasSuffix(t, []byte("::")) {
		r.prose(d, ls)
		return j
	}
	switch {
	case len(t) == 2:
		ls = ls[:len(ls)-1]
	case isSpace(t[len(t)-3]):
		last.end = last.start + len(bytes.TrimRight(t[:len(t)-2], " \t"))
	default:
		last.end--
	}
	r.prose(d, ls)
	end := d.indented(j-1, col)
	if body := d.block(j, end); len(body) > 0 {
		r.code(d, "", body, body[0].line)
	}
	return end
}

// isExplicit reports whether t starts explicit markup: .. alone, or
// followed by a blank.
func isExplicit(t []byte) bool {
	return bytes.HasPrefix(t, []byte("..")) && (len(t) == 2 || isSpace(t[2]))
}

// directiveName matches the start of a directive: a name of letters and
// digits joined singly by - _ + : and ., then one space or none, :: and a
// blank or the end of the line.
var directiveName = regexp.MustCompile(`^([A-Za-z0-9]+(?:[-_+:.][A-Za-z0-9]+)*)( ?::)(?:[ \t]|$)`)

// directive is the name of the directive that rest, the text after .. and
// its blank, starts, in lower case, and the text after its ::, or ok false
// when rest starts none.
func directive(rest []byte) (name string, args []byte, ok bool) {
	m := directiveName.FindSubmatchIndex(rest)
	if m == nil {
		return "", nil, false
	}
	return strings.ToLower(string(rest[:m[3]])), rest[m[5]:], true
}

// codeDirectives are the directives whose content is code in the
// language of their argument: docutils' code and its sourcecode alias, and
// Sphinx's code-block.
var codeDirectives = []string{"code", "code-block", "sourcecode"}

// admonitions are the directives whose content is prose, the text after
// their :: included.
var admonitions = []string{"attention", "caution", "danger", "error", "hint", "important", "note", "tip", "warning", "seealso"}

// containers are the other directives whose content is rST errata reads,
// a table's included; the text after their :: is a title or a condition,
// and no prose.
var containers = []string{"admonition", "compound", "container", "only", "sidebar", "table", "topic"}

// explicit reads the explicit markup that starts line i, at column ind and
// offset off: a comment, a code directive, a directive whose content is
// rST, or a target, a footnote, a citation, a substitution or another
// directive, whose body it passes over unread. It returns the line after.
func explicit(d *doc, r *reading, i, ind, off int) int {
	l := d.line(i)
	o := off + 2
	for o < len(l) && isSpace(l[o]) {
		o++
	}
	first := d.from(i, o)
	rest := d.text(first)
	if len(rest) == 0 && (i+1 == d.lines() || d.blank(i+1)) {
		r.comment(d, nil, i+1) // an empty comment, which ends what is before it and holds nothing after
		return i + 1
	}
	name, args, isDirective := directive(rest)
	if isDirective && (contains(admonitions, name) || contains(containers, name)) {
		if a := bytes.TrimLeft(args, " \t"); len(a) > 0 && contains(admonitions, name) {
			// The text after :: and the lines under it are the content's first paragraph.
			return block(d, r, span{start: first.end - len(a), end: first.end, line: i + 1}, i+1, d.content(i, ind))
		}
		return d.options(i+1, ind) // the content is read as the rest of the file is
	}
	end := d.indented(i, ind)
	switch {
	case isDirective && contains(codeDirectives, name):
		k := d.options(i+1, ind)
		lang := ""
		if f := strings.Fields(string(args)); len(f) > 0 {
			lang = f[0]
		}
		body := d.block(k, end)
		line := i + 1
		if len(body) > 0 {
			line = body[0].line
		}
		r.code(d, lang, body, line)
		return end
	case isDirective, target.Match(rest), label.Match(rest), substitution.Match(rest):
		return end
	}
	r.comment(d, append([]span{first}, d.block(i+1, end)...), i+1)
	return end
}

// contains reports whether names holds name.
func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// label matches the start of a footnote or a citation, followed by a
// blank or the end of the line: a number, #, # and a name, or * between [
// and ] for a footnote, a name for a citation, a name being letters and
// digits joined singly by - _ . : and +.
var label = regexp.MustCompile(`^\[(?:[0-9]+|#|#?[A-Za-z0-9]+(?:[-_.:+][A-Za-z0-9]+)*|\*)\](?:[ \t]|$)`)

// target matches the start of a hyperlink target: _ and no blank.
var target = regexp.MustCompile(`^_[^ \t]`)

// substitution matches the start of a substitution definition: text
// between two |, followed by a blank.
var substitution = regexp.MustCompile(`^\|[^|]+\|[ \t]`)
