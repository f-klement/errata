// Package lexer finds the comments and the string literals of a source file,
// driven by a table of languages with one row each (table.go). A row lists a
// language's file names and the forms its comments and literals take: the
// bytes that open and close each, its escape, whether it ends at a line's end,
// nests or is one character long. The lexer reads a file once, left to right,
// and skips a string literal whole, so a comment marker inside one is no
// comment. It parses nothing else: what a row cannot state, such as a shell
// heredoc, it does not understand (docs/reference.md#comment).
//
// A reader reads the table through core (core.Language), as it reads every
// package below core. The comment extractor reads the comments of a row; the
// markers and the literal reader read the same rows, so a row holds no rule of
// either.
package lexer

import (
	"bytes"
	"cmp"
	"iter"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// Kind is what a Span is.
type Kind uint8

// The kinds of span.
const (
	// Comment is a comment, which the language ignores.
	Comment Kind = iota + 1
	// String is a string or character literal: a value the program holds.
	String
	// Name is a quoted name, such as a quoted identifier in SQL: neither a
	// comment nor a value.
	Name
	// Text is text outside a file's code, such as the HTML of a PHP
	// template around its <?php ... ?>: neither a comment nor code.
	Text
)

// Span is one comment, string literal or quoted name of a file: the bytes
// from its opener to the end of its closer, and its body, the bytes between
// them. A line comment's span ends before the line feed, and its body before
// a carriage return ahead of it too. A span with no closer runs to the end of the
// file, or of the line for a form that ends at one, and is not Closed. A line
// comment is always Closed: the end of the file ends its line.
//
// A heredoc's span is its opener alone, such as <<~EOS, with an empty body:
// the rest of its line is code, and its body is the lines after it, from
// Here.Start to Here.End, which no span overlaps (Heredoc).
type Span struct {
	Kind               Kind
	Start, End         int
	BodyStart, BodyEnd int
	Closed             bool
	// Form is the index in the row's Forms of the form the span is.
	Form int
	// Here is a heredoc's body, the zero Here for any other span.
	Here Here
}

// Here is the body of a heredoc: the lines from Start to End, the start of
// its closer's line, and Close, the end of the tag that closes it. Quote is
// the quote the opener wrote around its tag, 0 for none.
type Here struct {
	Start, End, Close int
	Quote             byte
}

// Form is one form of comment or literal a language writes: Open, then a
// fence and Inner, the body, and Close, then the same fence and Outer. The
// fence is a run of the bytes Fence holds, at least FenceMin and at most
// FenceMax of them, such as the # of Rust's r#"..."# or the = of Lua's
// [==[...]==]. A form with no Fence has an empty fence, and Inner and Outer
// are simply part of its opener and closer. A Close of "" ends the form at
// the end of its line: a line comment.
type Form struct {
	Kind  Kind
	Open  string
	Close string
	// Fence are the bytes the fence may hold; FenceMax 0 sets no maximum.
	// A fence of FenceMax bytes followed by one more of them opens a span
	// that runs off the end of the file, so its reader refuses the file.
	Fence              string
	FenceMin, FenceMax int
	Inner, Outer       string
	// Escape, when set, makes the byte after it part of the body, so it
	// neither closes nor nests.
	Escape byte
	// Doubled reads the closer written twice as one closer in the body, as
	// SQL reads '' inside '...'.
	Doubled bool
	// Line ends the form unclosed at the end of its line, as a C string
	// that lacks its closing quote.
	Line bool
	// Short makes the form a character literal: its body is one character
	// or one escape, and Open that is not followed by one and Close opens
	// nothing, as Rust's ' of a lifetime.
	Short bool
	// Nests counts Open inside the body as one more level, which needs one
	// more Close, as Rust's block comments do.
	Nests bool
	// After, when set, are the bytes that may stand just before Open; Open
	// at the start of a line is always one. A shell # starts a comment only
	// at a word's start.
	After string
	// LineStart opens the form only after blanks alone on its line, and
	// closes it only there, as Ruby's =begin and =end.
	LineStart bool
	// Tag, when set, are the bytes of a name between Open and Inner, one
	// to TagMax of them, which the closer does not repeat: the letter of
	// Elixir's sigil ~s(...).
	Tag    string
	TagMax int
	// Heredoc makes the form a heredoc: Open, then a tag, an identifier
	// that may stand in quotes, whose first byte TagFirst holds when set.
	// Its body is the lines after the opener's line, up to a line that holds
	// the tag alone, after blanks when Indent is set, or followed by a byte
	// that is no identifier's when Trail is set. A second heredoc opened on
	// the same line has its body after the first one's closer.
	Heredoc  bool
	TagFirst string
	Indent   bool
	Trail    bool
	// Code are the openers of code inside the body, such as ${ or \(, which
	// the walk reads as the row's code up to the bracket that closes the
	// opener's ( or {, so a string or a comment inside it is lexed as one
	// and its closer does not close the body. An opener of one { that is
	// written twice, as Python's {{, is the brace itself.
	Code []string
	// Spec are the bytes that end the code inside a replacement field and
	// start its format spec, where they stand with no other bracket open
	// and no = after them, as Python's : and ! do in f"{n:#x}".
	Spec string
	// Before, when set, are the bytes one of which stands before Open,
	// past the blanks and line ends before it, unless Open starts the file:
	// a regular expression's / opens only where a value starts.
	Before string
	// Words are the words after which Open starts a value too, as Ruby's
	// if /re/ and JavaScript's return /re/; a member named so is no word.
	// Glued are the bytes that may not stand straight before Open, as the
	// : of Ruby's symbol :/.
	Words []string
	Glued string
	// Calls are words that may be a method's name, after which Open starts
	// a value only with a blank before it and none after it, as Ruby reads
	// p /x/ as a call with a regex and p / 2 as a division.
	Calls []string
	// LineStarts makes the start of a line one where a value starts, as in
	// Ruby, whose statements end at a line's end.
	LineStarts bool
	// Classes reads [...] in the body as a class, inside which the closer
	// closes nothing, as a regular expression's [/] holds a /.
	Classes bool
	// Pair is the bracket that opens what the closer's byte closes inside
	// the body, which nests, as Ruby's %w(a (b)) holds a (b).
	Pair byte
	// Stop ends a line comment before it, as PHP's ?> ends a // comment.
	Stop string
	// Then, when set, are what one of must follow Close for it to close,
	// as PHP's <? opens code only as <?php, <?= or <? and a blank, so a
	// template's <?xml stays text. A row's Begin follows the same rule.
	Then []string
	// ToEnd makes a span that meets no closer end, closed, at the end of
	// the file: Ruby's __END__, alone on its line, and a PHP template's
	// text after its last ?>.
	ToEnd bool
	// Greedy makes the closer take the run of its last byte that follows it,
	// up to maxRun more, and leaves the extra bytes in the body, as Kotlin
	// reads """a"""" as the string a".
	Greedy bool
	// Indented makes the form a YAML block scalar: Open, then at most two
	// indicators of chomping and indentation, then only blanks or a comment
	// to the end of its line. The body is the lines after it that are blank
	// or indented as far as its first line that is not, which must be
	// further than the node the scalar is the value of; a document marker at
	// column 0 ends it. A comment on the opener's line is part of the span.
	Indented bool

	// The fields below say how the literal reader reads a string of the
	// form's body; the lexer does not read them.

	// Interp opens code inside the body, such as #{ or ${, which makes the
	// string computed. A $ opens code only before a letter, _ or {.
	Interp string
	// QuoteEscapes makes the escape stand only before itself and before
	// the closer's first byte, as in a single quoted string of Ruby or PHP;
	// any other escape is the escape byte itself. Without it, a form with
	// an Escape reads the escapes of C.
	QuoteEscapes bool
	// Computed makes every string of the form a computed one, as Ruby's
	// backticks run a command.
	Computed bool
	// Dedent takes indentation off each line of the body: DedentCloser the
	// blanks before the closer on its line, DedentCommon the blanks every
	// line that is not blank starts with.
	Dedent Dedent
	// TrimFirst leaves out a line feed that starts the body, and TrimLast
	// one that ends it.
	TrimFirst, TrimLast bool

	// inFence is Fence as a set, and inTag Tag, which compile fills in.
	inFence, inTag *[256]bool
}

// Dedent is how a string form takes indentation off the lines of its body.
type Dedent uint8

// The ways of taking off indentation.
const (
	DedentNone Dedent = iota
	DedentCloser
	DedentCommon
)

// maxRun is the most bytes a Greedy closer takes past its own.
const maxRun = 32

// Language is one row of the table: a language, its family, the file names it
// is chosen for and the forms its comments and literals take.
type Language struct {
	// Name is what a spec's language: names it by.
	Name string
	// Family is the group the docs list it under.
	Family string
	// Extensions are the endings of the file names it reads, such as .go;
	// Files the whole names, such as Dockerfile.
	Extensions []string
	Files      []string
	Forms      []Form
	// Escape, when set, makes the byte after it plain outside any form, so
	// it opens nothing: the shell's \#.
	Escape byte
	// Begin, when set, is what the file's code starts after: the text
	// before it is a span of kind Text, as a PHP template's HTML before
	// its first <?php.
	Begin string
	// Syntax is how the language writes literal data, which the literal
	// reader reads; nil for a row it does not read.
	Syntax *Syntax

	// index is the row's forms indexed, set for each row of the table and
	// shared by every copy of it.
	index *compiled
}

// compiled is a language's forms indexed by the first byte of their opener,
// the longest opener and inner first, so --[[ is tried before -- and
// Swift's #""" before #".
type compiled struct {
	first [256][]indexed
}

// indexed is a form and its index in its row's Forms.
type indexed struct {
	*Form
	i int
}

func compile(forms []Form) *compiled {
	c := &compiled{}
	for i := range forms {
		f := &forms[i]
		c.first[f.Open[0]] = append(c.first[f.Open[0]], indexed{f, i})
		f.inFence, f.inTag = set(f.Fence), set(f.Tag)
		if f.Code == nil {
			f.Code = codeOf(f.Interp)
		}
	}
	for b := range c.first {
		slices.SortStableFunc(c.first[b], func(x, y indexed) int { return cmp.Compare(opener(y.Form), opener(x.Form)) })
	}
	return c
}

// codeOf are the openers of code a form whose Interp is interp walks: the
// bracket that interp opens, and ${ for a $, which opens code only there,
// since $name holds no string.
func codeOf(interp string) []string {
	switch interp {
	case "":
		return nil
	case "$":
		return []string{"${"}
	case `\#`:
		return []string{`\#(`}
	}
	return []string{interp}
}

// set is the bytes of s as a set, nil for none.
func set(s string) *[256]bool {
	if s == "" {
		return nil
	}
	out := new([256]bool)
	for j := range len(s) {
		out[s[j]] = true
	}
	return out
}

// opener is how long f's opener is without its fence, which compile sorts by.
func opener(f *Form) int { return len(f.Open) + len(f.Inner) }

// Spans are the comments, string literals and quoted names of data, in the
// order they start. A span does not overlap the one before it. The walk is
// linear in data: every fence and every run a closer takes is bounded.
//
// A heredoc's body is skipped at the end of its opener's line. A span on
// that line that runs past its end, such as a block comment, makes the walk
// lose its place: it yields one span from there that runs off the end of the
// file (Span.RunsOff) and stops.
func (l Language) Spans(data []byte) iter.Seq[Span] {
	c := l.index
	if c == nil {
		l.Forms = slices.Clone(l.Forms) // compile fills in each form's fence set
		c = compile(l.Forms)
	}
	return func(yield func(Span) bool) {
		w := spanWalk{l: l, c: c, data: data, blank: true}
		i, ok := l.text(data, yield)
		for ok && i < len(data) {
			i, ok = w.step(i, yield)
		}
	}
}

// text yields the span of the text before the file's code, for a row with
// a Begin, and returns where the code starts; false when yield stopped.
func (l Language) text(data []byte, yield func(Span) bool) (int, bool) {
	if l.Begin == "" {
		return 0, true
	}
	s, ok := l.before(data)
	if !ok {
		return 0, true
	}
	return s.End, yield(s)
}

// spanWalk is what Spans knows as it reads a file: the row and its index,
// whether only blanks stand before the byte on its line, and the heredocs
// opened on that line.
type spanWalk struct {
	l     Language
	c     *compiled
	data  []byte
	blank bool
	hd    heredocs
}

// step reads the file at i, yielding the span that opens there, and returns
// where the walk goes on; false when it stops: yield stopped, or the span
// runs off the end of the file.
func (w *spanWalk) step(i int, yield func(Span) bool) (int, bool) {
	if w.hd.open && i == w.hd.eol {
		w.hd.open, w.blank = false, false
		return w.hd.resume, true
	}
	b := w.data[i]
	if w.l.Escape != 0 && b == w.l.Escape {
		w.blank = false
		return i + 2, true
	}
	s, ok := w.open(i)
	if !ok {
		w.blank = stillBlank(w.blank, b)
		return i + 1, true
	}
	w.blank = w.data[s.End-1] == '\n' // a span is never empty
	return s.End, yield(s) && (s.Closed || s.End != len(w.data))
}

// open is the span of the first of the row's forms that opens at i, with a
// heredoc's body found. A span other than a heredoc that runs past the end
// of a line whose heredocs are open makes the walk lose its place, so the
// span runs off the end of the file from its start.
func (w *spanWalk) open(i int) (Span, bool) {
	for _, f := range w.c.first[w.data[i]] {
		s, ok := f.at(w.data, i, w.blank, w.c, 0)
		if !ok {
			continue
		}
		s.Form = f.i
		if f.Heredoc && s.Closed {
			s = f.here(w.data, s, &w.hd, w.c)
		}
		if w.hd.open && s.End > w.hd.eol && !f.Heredoc {
			n := len(w.data)
			return Span{Kind: s.Kind, Start: s.Start, BodyStart: n, BodyEnd: n, End: n, Form: f.i}, true
		}
		return s, true
	}
	return Span{}, false
}

// at is the span of f opening at i in data, if it opens there; blank is
// whether only blanks stand before i on its line. c is f's row, whose forms
// the code inside f's body is lexed with, level deep in such code.
func (f *Form) at(data []byte, i int, blank bool, c *compiled, level int) (Span, bool) {
	if !f.opens(data, i, blank) {
		return Span{}, false
	}
	j := i + len(f.Open)
	n, ok, runsOff := f.fence(data, j)
	if !ok {
		return Span{}, false
	}
	if runsOff {
		// A fence past its bound: the lexer cannot know where the
		// string ends, so the span runs off the end and the file's
		// rule is unknown (Span.RunsOff), never read on as code.
		return Span{Kind: f.Kind, Start: i, BodyStart: len(data), BodyEnd: len(data), End: len(data)}, true
	}
	fence := data[j : j+n]
	if f.Tag != "" { // a form with a tag has no fence, so n is 0
		if j, ok = f.tagEnd(data, j); !ok {
			return Span{}, false
		}
	}
	if f.Heredoc {
		return f.tag(data, i, j)
	}
	if !bytes.HasPrefix(data[j+n:], []byte(f.Inner)) {
		return Span{}, false
	}
	return f.body(data, Span{Kind: f.Kind, Start: i, BodyStart: j + n + len(f.Inner)}, fence, c, level)
}

// opens reports whether f's opener stands at i in data where it may open:
// at a line's start when f needs one, after a byte of After, and where a
// value starts when f has a Before.
func (f *Form) opens(data []byte, i int, blank bool) bool {
	return bytes.HasPrefix(data[i:], []byte(f.Open)) && (!f.LineStart || blank) && f.after(data, i) &&
		(f.Before == "" || f.valueStarts(data, i))
}

// after reports whether the byte before i in data may stand before f's
// opener: f has no After, i starts the file or a line, or After holds it.
func (f *Form) after(data []byte, i int) bool {
	return f.After == "" || i == 0 || data[i-1] == '\n' || strings.IndexByte(f.After, data[i-1]) >= 0
}

// fence is the length of the fence that starts at j in data; false when it
// is shorter than FenceMin. runsOff reports a fence of FenceMax bytes that
// one more of them follows. A form with no Fence has an empty fence.
func (f *Form) fence(data []byte, j int) (n int, ok, runsOff bool) {
	if f.Fence == "" {
		return 0, true, false
	}
	n = f.fenceRun(data, j)
	if n < f.FenceMin {
		return n, false, false
	}
	return n, true, n == f.FenceMax && j+n < len(data) && f.inFence[data[j+n]]
}

// fenceRun is how many bytes of Fence stand from j in data, at most
// FenceMax when it is set.
func (f *Form) fenceRun(data []byte, j int) int {
	n := 0
	for j+n < len(data) && f.inFence[data[j+n]] && (f.FenceMax == 0 || n < f.FenceMax) {
		n++
	}
	return n
}

// tagEnd is the end of the tag that starts at j in data, one to TagMax
// bytes of Tag; false when no byte of Tag stands at j.
func (f *Form) tagEnd(data []byte, j int) (int, bool) {
	t := j
	for t < len(data) && f.inTag[data[t]] && t-j < f.TagMax {
		t++
	}
	return t, t > j
}

// body is s, whose body starts at s.BodyStart, read to its end the way f's
// kind of form ends: a block scalar, a character literal, the rest of the
// file, the end of the line, or a closer after fence.
func (f *Form) body(data []byte, s Span, fence []byte, c *compiled, level int) (Span, bool) {
	switch {
	case f.Indented:
		return indented(data, s.Start, s)
	case f.Short:
		return f.short(data, s)
	case f.ToEnd && f.Close == "":
		return toEnd(data, s)
	case f.Close == "":
		return f.lineEnd(data, s), true
	}
	return f.closed(data, s, fence, c, level)
}

// toEnd is s, which runs to the end of the file, closed, when its opener
// stands alone on its line; no span otherwise.
func toEnd(data []byte, s Span) (Span, bool) {
	if k := s.BodyStart; k < len(data) && data[k] != '\n' && data[k] != '\r' {
		return Span{}, false
	}
	s.BodyEnd, s.End, s.Closed = len(data), len(data), true
	return s, true
}

// lineEnd is s, a line comment, closed at the end of its line or before
// f's Stop on it. Its body ends before a carriage return that ends the
// line.
func (f *Form) lineEnd(data []byte, s Span) Span {
	end := len(data)
	if k := bytes.IndexByte(data[s.BodyStart:], '\n'); k >= 0 {
		end = s.BodyStart + k
	}
	if f.Stop != "" {
		if k := bytes.Index(data[s.BodyStart:end], []byte(f.Stop)); k >= 0 {
			end = s.BodyStart + k
			s.End, s.BodyEnd, s.Closed = end, end, true
			return s
		}
	}
	s.End, s.Closed = end, true
	if data[end-1] == '\r' { // end is past the opener, so end-1 is in data
		end--
	}
	s.BodyEnd = end
	return s
}

// closed is s read up to the closer that ends it, f's Close, then fence
// and Outer. Code inside the body is walked as c's code, level+1 deep.
// A span that meets no closer runs to the end of the file, Closed only
// when f is ToEnd.
func (f *Form) closed(data []byte, s Span, fence []byte, c *compiled, level int) (Span, bool) {
	w := bodyWalk{f: f, data: data, closer: slices.Concat([]byte(f.Close), fence, []byte(f.Outer)), depth: 1}
	for k := s.BodyStart; k < len(data); {
		if f.braces(data, k) {
			k += 2 // {{ is a brace
			continue
		}
		if at, open, ok := f.code(data, k); ok {
			end, closed := c.walk(data, at, open, level+1, f.Spec)
			if !closed {
				// Code the walk cannot close: the lexer lost its place,
				// so the span runs off and the file's rule is unknown.
				s.BodyEnd, s.End = len(data), len(data)
				return s, true
			}
			k = end
			continue
		}
		next, done := w.step(&s, k)
		if done {
			return s, true
		}
		k = next
	}
	s.BodyEnd, s.End, s.Closed = len(data), len(data), f.ToEnd
	return s, true
}

// bodyWalk is what closed knows of a body as it reads it: the closer, how
// many levels deep the body nests, whether only blanks stand before the
// byte on its line, and whether it is inside a regular expression's [...].
type bodyWalk struct {
	f      *Form
	data   []byte
	closer []byte
	depth  int
	blank  bool
	class  bool
}

// step reads the byte at k of the body and returns where the walk goes on;
// true when it ends the span, which it sets in s.
func (w *bodyWalk) step(s *Span, k int) (int, bool) {
	f, b := w.f, w.data[k]
	if f.Line && b == '\n' {
		s.BodyEnd, s.End = k, k
		return k, true
	}
	if f.Escape != 0 && b == f.Escape {
		return k + f.escaped(w.data, k), false
	}
	if next, ok := w.bracket(k); ok {
		return next, false
	}
	if w.closes(k) {
		return w.close(s, k)
	}
	w.blank = stillBlank(w.blank, b)
	return k + 1, false
}

// escaped is how many bytes the escape at k in data takes: two, or one in
// a form whose code opens with a lone { when a brace follows, since there
// an escape takes no brace: Python's rf'\{{' is a backslash and a brace.
func (f *Form) escaped(data []byte, k int) int {
	if f.braceCode() && k+1 < len(data) && (data[k+1] == '{' || data[k+1] == '}') {
		return 1
	}
	return 2
}

// bracket reads, at k, a bracket the body counts: one the closer's byte
// closes, f's opener in a form that nests, or a regular expression's [ or ].
// It returns where the walk goes on past it; false when none stands at k.
func (w *bodyWalk) bracket(k int) (int, bool) {
	f, b := w.f, w.data[k]
	switch {
	case f.Pair != 0 && b == f.Pair:
		w.depth++ // a bracket the closer's byte closes, nested in the body
		return k + 1, true
	case f.Nests && bytes.HasPrefix(w.data[k:], []byte(f.Open)):
		w.depth++
		w.blank = false
		return k + len(f.Open), true
	case f.Classes && (b == '[' || b == ']'):
		w.class = b == '['
		return k + 1, true
	}
	return k, false
}

// closes reports whether the closer at k may close: outside a class, at a
// line's start for a LineStart form, and followed by one of f's Then.
func (w *bodyWalk) closes(k int) bool {
	f := w.f
	return !w.class && bytes.HasPrefix(w.data[k:], w.closer) && (!f.LineStart || w.blank) && f.then(w.data, k+len(w.closer))
}

// close reads the closer at k: a doubled closer is part of the body, a
// nested one closes a level, and the last closes the span, which it sets
// in s, true.
func (w *bodyWalk) close(s *Span, k int) (int, bool) {
	n := len(w.closer)
	if w.f.Doubled && bytes.HasPrefix(w.data[k+n:], w.closer) {
		w.blank = false
		return k + 2*n, false
	}
	if w.depth--; w.depth == 0 {
		end := w.greedy(k + n)
		s.BodyEnd, s.End, s.Closed = end-n, end, true
		return end, true
	}
	w.blank = false
	return k + n, false
}

// greedy is end, the end of a closer, moved past the run of the closer's
// last byte that follows it when f is Greedy, at most maxRun bytes.
func (w *bodyWalk) greedy(end int) int {
	last, from := w.closer[len(w.closer)-1], end
	for w.f.Greedy && end < len(w.data) && w.data[end] == last && end-from < maxRun {
		end++
	}
	return end
}

// stillBlank is whether only blanks stand on a line after b, given blank
// for the bytes before it: a line feed starts a blank line, a blank keeps
// it as it was, and any other byte ends it.
func stillBlank(blank bool, b byte) bool {
	switch b {
	case '\n':
		return true
	case ' ', '\t', '\r', '\f', '\v':
		return blank
	}
	return false
}

// maxNest is how deeply code inside a string may hold strings that hold
// code, past which the span runs off and the file's rule is unknown. The
// walk is linear at any depth; the bound keeps its stack small.
const maxNest = 256

// code reports whether one of f's Code openers starts at k in data, and if
// it does, where the code inside it starts and the bracket it opened.
func (f *Form) code(data []byte, k int) (int, byte, bool) {
	for _, o := range f.Code {
		if !bytes.HasPrefix(data[k:], []byte(o)) {
			continue
		}
		b := strings.IndexAny(o, "({")
		return k + b + 1, o[b], true
	}
	return 0, 0, false
}

// codeCloses reports whether each opener of code in data[from:to], a
// heredoc's body, closes before to.
func (f *Form) codeCloses(data []byte, from, to int, c *compiled) bool {
	for k := from; k < to; {
		at, open, ok := f.code(data, k)
		if !ok {
			k++
			if f.Escape != 0 && data[k-1] == f.Escape {
				k++
			}
			continue
		}
		end, closed := c.walk(data, at, open, 1, f.Spec)
		if !closed || end > to {
			return false
		}
		k = end
	}
	return true
}

// then reports whether the closer that ends at k may close: f has no Then,
// or one of Then follows it.
func (f *Form) then(data []byte, k int) bool {
	if len(f.Then) == 0 {
		return true
	}
	for _, t := range f.Then {
		if bytes.HasPrefix(data[k:], []byte(t)) {
			return true
		}
	}
	return false
}

// braces reports whether k in data starts {{ in a form whose code opens
// with a lone {, where it is the brace itself.
func (f *Form) braces(data []byte, k int) bool {
	return f.braceCode() && k+1 < len(data) && data[k] == '{' && data[k+1] == '{'
}

// braceCode reports whether f's code opens with a lone {, as an f-string's.
func (f *Form) braceCode() bool { return len(f.Code) == 1 && f.Code[0] == "{" }

// walk reads code from k up to the bracket that closes open, lexing each
// comment and string of the row as a span, and returns where the code ends,
// past that bracket; false when it runs off the end of the file, nests past
// maxNest, closes a bracket of another kind, or holds a span that runs off,
// ends unclosed at its line or is a heredoc, whose body the walk cannot
// place. Every bracket counts, so a byte of spec found with none open but
// open's starts a format spec, which spec reads: Python's f"{n:#x}".
func (c *compiled) walk(data []byte, k int, open byte, level int, spec string) (int, bool) {
	if level > maxNest {
		return 0, false
	}
	shut := byte(')')
	if open == '{' {
		shut = '}'
	}
	return c.walkCode(data, k, shut, level, spec)
}

// walkCode is walk past its bound and its opener: shut is the bracket that
// closes the code.
func (c *compiled) walkCode(data []byte, k int, shut byte, level int, spec string) (int, bool) {
	depth := 1 // the brackets of every kind open, open's included
	for k < len(data) {
		b := data[k]
		if depth == 1 && specStarts(data, k, spec) {
			return c.spec(data, k+1, level, spec)
		}
		if end, ok, lexed := c.lexed(data, k, level); lexed {
			if !ok {
				return 0, false
			}
			k = end
			continue
		}
		if depth = nest(b, depth); depth == 0 {
			return k + 1, b == shut
		}
		k++
	}
	return 0, false
}

// specStarts reports whether a byte of spec at k in data starts a format
// spec: not := or !=, which are code.
func specStarts(data []byte, k int, spec string) bool {
	return spec != "" && strings.IndexByte(spec, data[k]) >= 0 && (k+1 == len(data) || data[k+1] != '=')
}

// lexed reports whether a comment or string of the row opens at k in data,
// level deep in code, and where it ends; false for one that runs off, ends
// unclosed at its line or is a heredoc.
func (c *compiled) lexed(data []byte, k, level int) (end int, ok, lexed bool) {
	for _, f := range c.first[data[k]] {
		if s, opens := f.at(data, k, false, c, level); opens {
			return s.End, s.Closed && !f.Heredoc, true
		}
	}
	return 0, false, false
}

// nest is depth after b: one more for an opening bracket, one fewer for a
// closing one.
func nest(b byte, depth int) int {
	switch b {
	case '(', '[', '{':
		return depth + 1
	case ')', ']', '}':
		return depth - 1
	}
	return depth
}

// spec reads a format spec from k to the } that ends its replacement field:
// no string or comment opens in it, and a {...} in it is a field of its own,
// whose code walk reads, as Python's f"{x:{w}}". It returns where the field
// ends, past its }; false when no } ends it.
func (c *compiled) spec(data []byte, k, level int, spec string) (int, bool) {
	for ; k < len(data); k++ {
		switch data[k] {
		case '{':
			end, ok := c.walk(data, k+1, '{', level+1, spec)
			if !ok {
				return 0, false
			}
			k = end - 1
		case '}':
			return k + 1, true
		}
	}
	return 0, false
}

// valueStarts reports whether f's value may start at i in data: past the
// blanks and line ends before it, the file starts, a byte of Before stands,
// or a word of Words; never straight after a byte of Glued. A line end
// alone is not enough, since a statement may go on over it, as a division
// does in (1\n  / 2).
func (f *Form) valueStarts(data []byte, i int) bool {
	if i > 0 && strings.IndexByte(f.Glued, data[i-1]) >= 0 {
		return false
	}
	k, ended := f.back(data, i-1)
	if ended || k < 0 || strings.IndexByte(f.Before, data[k]) >= 0 {
		return true
	}
	return f.word(data, i, k)
}

// back is k moved back past the blanks and line ends at and before it in
// data, to the byte before them, -1 at the start of the file; true when f
// is LineStarts and a line end there ends a statement.
func (f *Form) back(data []byte, k int) (int, bool) {
	for k >= 0 && gap(data[k]) {
		if f.LineStarts && data[k] != ' ' && data[k] != '\t' {
			j, joined := joined(data, k)
			if !joined {
				return k, true
			}
			k = j
		}
		k--
	}
	return k, false
}

// gap reports whether b is a blank or a line end.
func gap(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// joined reports whether a backslash joins the line that the line end at k
// in data ends to the next, and where that backslash stands. A line end
// ends the statement before it unless a backslash joins the lines.
func joined(data []byte, k int) (int, bool) {
	j := k - 1
	if j >= 0 && data[k] == '\n' && data[j] == '\r' {
		j--
	}
	return j, j >= 0 && data[j] == '\\'
}

// word reports whether the word that ends at k in data is one after which
// f's value starts at i: one of Words, or one of Calls with a blank before
// i and none after Open. A member named so is no word.
func (f *Form) word(data []byte, i, k int) bool {
	w := k + 1
	for w > 0 && identByte(data[w-1]) {
		w--
	}
	if w > 0 && data[w-1] == '.' {
		return false // a member named so is no word
	}
	word := string(data[w : k+1])
	if slices.Contains(f.Calls, word) {
		// p /x/ calls p with a regex; p / 2 divides, as Ruby decides it.
		after := i + len(f.Open)
		return k+1 < i && after < len(data) && strings.IndexByte(" \t\n\r", data[after]) < 0
	}
	return slices.Contains(f.Words, word)
}

// before is the span of the text before the file's first Begin: kind Text,
// up to and with Begin, which is all of it when the file starts with Begin,
// or the whole file when it holds none; false for an empty file. It names
// the row's first Text form.
func (l Language) before(data []byte) (Span, bool) {
	if len(data) == 0 {
		return Span{}, false
	}
	form := 0
	for i, f := range l.Forms {
		if f.Kind == Text {
			form = i
			break
		}
	}
	k := -1
	for at := 0; at < len(data); {
		n := bytes.Index(data[at:], []byte(l.Begin))
		if n < 0 {
			break
		}
		if l.Forms[form].then(data, at+n+len(l.Begin)) {
			k = at + n
			break
		}
		at += n + 1
	}
	end, body := len(data), len(data)
	if k >= 0 {
		end, body = k+len(l.Begin), k
	}
	return Span{Kind: Text, Start: 0, End: end, BodyStart: 0, BodyEnd: body, Closed: true, Form: form}, true
}

// identByte reports whether b may stand in an identifier: an ASCII letter,
// a digit, _ or any byte of a character past ASCII.
func identByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b >= 0x80
}

// tag is the opener of a heredoc whose tag starts at j, its Open being at
// i: the tag, in quotes or not, at most maxTag bytes. The span is the
// opener, Closed, and Here.Quote its quote; its body is found by here.
func (f *Form) tag(data []byte, i, j int) (Span, bool) {
	q, j := quoted(data, j)
	k := j
	for k < len(data) && k-j < maxTag && identByte(data[k]) {
		k++
	}
	if !f.tagStarts(data, j, k) {
		return Span{}, false
	}
	end, ok := tagEnds(data, k, q)
	if !ok {
		return Span{}, false
	}
	return Span{Kind: f.Kind, Start: i, BodyStart: end, BodyEnd: end, End: end, Closed: true,
		Here: Here{Start: j, End: k, Quote: q}}, true
}

// quoted is the quote that stands at j in data before a heredoc's tag, 0
// for none, and where the tag starts.
func quoted(data []byte, j int) (byte, int) {
	if j < len(data) && (data[j] == '\'' || data[j] == '"' || data[j] == '`') {
		return data[j], j + 1
	}
	return 0, j
}

// tagStarts reports whether data[j:k] may be a heredoc's tag: not empty,
// not starting with a digit, and starting with a byte of TagFirst when f
// has one.
func (f *Form) tagStarts(data []byte, j, k int) bool {
	return k != j && (data[j] < '0' || data[j] > '9') && (f.TagFirst == "" || strings.IndexByte(f.TagFirst, data[j]) >= 0)
}

// tagEnds is the end of a heredoc's opener whose tag ends at k in data,
// past its closing quote q when it has one; false when that quote is not
// there, or, for a tag with no quotes, when the tag runs past maxTag.
func tagEnds(data []byte, k int, q byte) (int, bool) {
	if q != 0 {
		if k >= len(data) || data[k] != q {
			return 0, false
		}
		return k + 1, true
	}
	if k < len(data) && identByte(data[k]) {
		return 0, false // a tag past maxTag
	}
	return k, true
}

// heredocs is what the walk knows of the heredocs opened on the line it is
// on: whether one is open, the line feed that ends that line, and where
// the walk goes on after it, past the last closer.
type heredocs struct {
	open        bool
	eol, resume int
}

// here is s, the opener of a heredoc whose tag is data[s.Here.Start:s.Here.End],
// with its body found: the lines after the line feed that ends the opener's
// line, or after the line of the closer of a heredoc hd says was opened
// before it on that line, up to the line that closes it, which hd then
// resumes after. A heredoc with no closer, or on the last line, runs off
// the end of the file, and the walk stops there; so does one whose body
// holds code that does not close before the closer's line, where the lexer
// cannot tell which line closes it. c is f's row.
func (f *Form) here(data []byte, s Span, hd *heredocs, c *compiled) Span {
	runsOff := Span{Kind: s.Kind, Start: s.Start, BodyStart: len(data), BodyEnd: len(data), End: len(data), Form: s.Form}
	at := s.End // the body starts on the line after at
	if hd.open {
		at = hd.resume // the line after the closer before
	}
	k := bytes.IndexByte(data[at:], '\n')
	if k < 0 {
		return runsOff
	}
	if !hd.open {
		hd.open, hd.eol = true, at+k
	}
	from := at + k + 1
	p, end, ok := f.closerLine(data, from, data[s.Here.Start:s.Here.End])
	if !ok || s.Here.Quote != '\'' && !f.codeCloses(data, from, p, c) {
		return runsOff
	}
	s.Here = Here{Start: from, End: p, Close: end, Quote: s.Here.Quote}
	hd.resume = end
	return s
}

// closerLine is the start of the first line from p in data that closes a
// heredoc whose tag is tag, and the end of the tag on it; false when no
// line does.
func (f *Form) closerLine(data []byte, p int, tag []byte) (int, int, bool) {
	for {
		if end, ok := f.closesAt(data, p, tag); ok {
			return p, end, true
		}
		k := bytes.IndexByte(data[p:], '\n')
		if k < 0 {
			return 0, 0, false
		}
		p += k + 1
	}
}

// closesAt reports whether the line at p in data closes a heredoc whose tag
// is tag, and where the tag ends on it: the tag, after blanks when f is
// Indent, then the end of the line or the file, or for a Trail form a byte
// that is no identifier's.
func (f *Form) closesAt(data []byte, p int, tag []byte) (int, bool) {
	if f.Indent {
		p = blanks(data, p)
	}
	end := p + len(tag)
	return end, bytes.HasPrefix(data[p:], tag) && (end == len(data) || data[end] == '\n' || data[end] == '\r' ||
		f.Trail && !identByte(data[end]))
}

// maxShortEscape is the most bytes a character literal's escape may run to
// before its closer, as \u{10FFFF} does.
const maxShortEscape = 10

// short is s, a character literal, closed after one character or one escape,
// or no span when its closer is not there.
func (f *Form) short(data []byte, s Span) (Span, bool) {
	k := s.BodyStart
	if k >= len(data) {
		return Span{}, false
	}
	if f.Escape != 0 && data[k] == f.Escape {
		limit := min(len(data), k+2+maxShortEscape)
		for e := k + 2; e < limit && data[e] != '\n'; e++ {
			if bytes.HasPrefix(data[e:], []byte(f.Close)) {
				s.BodyEnd, s.End, s.Closed = e, e+len(f.Close), true
				return s, true
			}
		}
		return Span{}, false
	}
	_, n := utf8.DecodeRune(data[k:])
	if data[k] == '\n' || !bytes.HasPrefix(data[k+n:], []byte(f.Close)) {
		return Span{}, false
	}
	s.BodyEnd, s.End, s.Closed = k+n, k+n+len(f.Close), true
	return s, true
}

// RunsOff reports whether s has a closer it never reached and runs to the end
// of a file of size bytes. A file its compiler takes ends no comment or
// string that way, so such a span means the lexer lost its place, as a quote
// in a construct it does not understand makes it do, and from there it may
// read code as comment. A reader refuses a file that has one.
func (s Span) RunsOff(size int) bool { return !s.Closed && s.End == size }

// indented is s, a YAML block scalar opening at i, or no span when what
// follows Open on its line is no block scalar's header.
func indented(data []byte, i int, s Span) (Span, bool) {
	k, indicator := indicators(data, s.BodyStart)
	k = blanks(data, k)
	if !headerEnds(data, k) {
		return Span{}, false
	}
	nl := bytes.IndexByte(data[k:], '\n')
	if nl < 0 {
		s.BodyStart, s.BodyEnd, s.End, s.Closed = len(data), len(data), len(data), true
		return s, true
	}
	parent := parentColumn(data, i)
	// content is the body's indentation: the node's column plus an explicit
	// indicator, else that of the body's first line that is not blank.
	content := -1
	if indicator > 0 {
		content = max(parent, 0) + indicator
	}
	s.BodyStart = k + nl + 1
	end := blockEnd(data, s.BodyStart, parent, content)
	s.BodyEnd, s.End, s.Closed = end, end, true
	return s, true
}

// indicators reads at most two indicators of chomping and indentation from
// k in data, and returns where they end and the explicit indentation
// indicator, 0 for none.
func indicators(data []byte, k int) (int, int) {
	indicator := 0
	for n := 0; n < 2 && k < len(data) && strings.IndexByte("-+123456789", data[k]) >= 0; n++ {
		if data[k] >= '1' && data[k] <= '9' {
			indicator = int(data[k] - '0')
		}
		k++
	}
	return k, indicator
}

// blanks is where the run of spaces and tabs from k in data ends.
func blanks(data []byte, k int) int {
	for k < len(data) && (data[k] == ' ' || data[k] == '\t') {
		k++
	}
	return k
}

// headerEnds reports whether a block scalar's header may end at k in data,
// k past its opener: at the end of the line or the file, or where a comment
// starts after a blank.
func headerEnds(data []byte, k int) bool {
	return k == len(data) || data[k] == '\n' || data[k] == '\r' ||
		data[k] == '#' && (data[k-1] == ' ' || data[k-1] == '\t')
}

// blockEnd is where the body of a block scalar that starts at p in data
// ends: before its first line that is not blank and is indented no further
// than parent, the node's column, or less than content, the body's
// indentation (-1 for that of its first line that is not blank), or that is
// a document marker at column 0.
func blockEnd(data []byte, p, parent, content int) int {
	for p < len(data) {
		col := spaces(data, p)
		end := nextLine(data, p)
		if len(bytes.TrimSpace(data[p:end])) > 0 {
			if content < 0 {
				content = col
			}
			if col <= parent || col < content || col == 0 && documentMarker(data[p:end]) {
				break
			}
		}
		p = end
	}
	return p
}

// spaces is how many spaces stand from p in data.
func spaces(data []byte, p int) int {
	col := 0
	for p+col < len(data) && data[p+col] == ' ' {
		col++
	}
	return col
}

// nextLine is the start of the line after the one at p in data, past its
// line feed, or the end of the file.
func nextLine(data []byte, p int) int {
	if e := bytes.IndexByte(data[p:], '\n'); e >= 0 {
		return p + e + 1
	}
	return len(data)
}

// documentMarker reports whether line starts with a YAML document marker,
// --- or ..., followed by a blank or the end of the line.
func documentMarker(line []byte) bool {
	if !bytes.HasPrefix(line, []byte("---")) && !bytes.HasPrefix(line, []byte("...")) {
		return false
	}
	return len(line) == 3 || line[3] == ' ' || line[3] == '\t' || line[3] == '\n' || line[3] == '\r'
}

// parentColumn is the column of the node whose value is the block scalar
// opening at i: the first byte of its line past blanks and "- " entries, or
// the last entry's dash when the scalar is the entry itself. A scalar alone
// on its line, after blanks only, is the value of nothing on it, and every
// line indented at least as far is its body.
func parentColumn(data []byte, i int) int {
	start := bytes.LastIndexByte(data[:i], '\n') + 1
	if documentMarker(data[start:i]) {
		return -1 // --- |: the document's own scalar, whose body may start at column 0
	}
	c, dash := start, -1
	for c < i {
		switch {
		case data[c] == ' ':
			c++
		case data[c] == '-' && (data[c+1] == ' ' || data[c+1] == '\t'): // c < i, so c+1 is in data
			dash = c
			c += 2
		default:
			return c - start
		}
	}
	if dash >= 0 {
		return dash - start
	}
	return c - start - 1
}

// Languages are copies of the rows of the table, in its order. A row is the
// table's own; a copy changed by its caller changes neither the table nor
// the forms its Spans reads.
func Languages() []Language {
	out := make([]Language, len(table))
	for i := range table {
		out[i] = table[i].copy()
	}
	return out
}

// copy is l with its slices copied.
func (l Language) copy() Language {
	l.Extensions = slices.Clone(l.Extensions)
	l.Files = slices.Clone(l.Files)
	l.Forms = slices.Clone(l.Forms)
	return l
}

// Named is a copy of the row a spec's language: names, by its Name.
func Named(name string) (Language, bool) {
	for i := range table {
		if table[i].Name == name {
			return table[i].copy(), true
		}
	}
	return Language{}, false
}

// For is a copy of the row of the file named file, chosen by its base name,
// then by the longest extension a row lists, in any case.
func For(file string) (Language, bool) {
	base := path.Base(strings.ReplaceAll(file, `\`, "/"))
	for i := range table {
		if slices.Contains(table[i].Files, base) {
			return table[i].copy(), true
		}
	}
	lower := strings.ToLower(base)
	// Each dot past the first byte starts an extension, the longest first;
	// a name that is all extension, such as .go, has none.
	for dot := 1; dot < len(lower); dot++ {
		if lower[dot] != '.' {
			continue
		}
		if row, ok := byExtension[lower[dot:]]; ok {
			return table[row].copy(), true
		}
	}
	return Language{}, false
}

// byExtension is the row of each extension of the table; no two rows list
// one (TestTheTableIsWellFormed).
var byExtension = map[string]int{}

// Names are the names of every row, for a message.
func Names() string {
	names := make([]string, len(table))
	for i := range table {
		names[i] = table[i].Name
	}
	return strings.Join(names, ", ")
}

// The rows of the table are indexed once, when the program starts.
func init() {
	for i := range table {
		table[i].index = compile(table[i].Forms)
		for _, ext := range table[i].Extensions {
			byExtension[ext] = i
		}
	}
}
