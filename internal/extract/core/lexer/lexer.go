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
		blank := true // only blanks since the start of the line
		var hd heredocs
		start := 0
		if l.Begin != "" {
			if s, ok := l.before(data); ok {
				if !yield(s) {
					return
				}
				start = s.End
			}
		}
	scan:
		for i := start; i < len(data); {
			if hd.open && i == hd.eol {
				i, hd.open, blank = hd.resume, false, false
				continue
			}
			b := data[i]
			if l.Escape != 0 && b == l.Escape {
				blank = false
				i += 2
				continue
			}
			for _, f := range c.first[b] {
				s, ok := f.at(data, i, blank, c, 0)
				if !ok {
					continue
				}
				s.Form = f.i
				if f.Heredoc && s.Closed {
					s = f.here(data, s, &hd, c)
				}
				if hd.open && s.End > hd.eol && !f.Heredoc {
					yield(Span{Kind: s.Kind, Start: s.Start, BodyStart: len(data), BodyEnd: len(data), End: len(data), Form: f.i})
					return
				}
				if !yield(s) {
					return
				}
				if !s.Closed && s.End == len(data) {
					return
				}
				i, blank = s.End, data[s.End-1] == '\n' // a span is never empty
				continue scan
			}
			switch b {
			case '\n':
				blank = true
			case ' ', '\t', '\r', '\f', '\v':
			default:
				blank = false
			}
			i++
		}
	}
}

// at is the span of f opening at i in data, if it opens there; blank is
// whether only blanks stand before i on its line. c is f's row, whose forms
// the code inside f's body is lexed with, level deep in such code.
func (f *Form) at(data []byte, i int, blank bool, c *compiled, level int) (Span, bool) {
	if !bytes.HasPrefix(data[i:], []byte(f.Open)) {
		return Span{}, false
	}
	if f.LineStart && !blank {
		return Span{}, false
	}
	if f.After != "" && i > 0 && data[i-1] != '\n' && strings.IndexByte(f.After, data[i-1]) < 0 {
		return Span{}, false
	}
	if f.Before != "" && !f.valueStarts(data, i) {
		return Span{}, false
	}
	j := i + len(f.Open)
	n := 0
	if f.Fence != "" {
		for j+n < len(data) && f.inFence[data[j+n]] && (f.FenceMax == 0 || n < f.FenceMax) {
			n++
		}
		if n < f.FenceMin {
			return Span{}, false
		}
		if n == f.FenceMax && j+n < len(data) && f.inFence[data[j+n]] {
			// A fence past its bound: the lexer cannot know where the
			// string ends, so the span runs off the end and the file's
			// rule is unknown (Span.RunsOff), never read on as code.
			return Span{Kind: f.Kind, Start: i, BodyStart: len(data), BodyEnd: len(data), End: len(data)}, true
		}
	}
	fence := data[j : j+n]
	if f.Tag != "" { // a form with a tag has no fence, so n is 0
		t := j
		for t < len(data) && f.inTag[data[t]] && t-j < f.TagMax {
			t++
		}
		if t == j {
			return Span{}, false
		}
		j = t
	}
	if f.Heredoc {
		return f.tag(data, i, j)
	}
	if !bytes.HasPrefix(data[j+n:], []byte(f.Inner)) {
		return Span{}, false
	}
	s := Span{Kind: f.Kind, Start: i, BodyStart: j + n + len(f.Inner)}
	if f.Indented {
		return indented(data, i, s)
	}
	if f.Short {
		return f.short(data, s)
	}
	if f.ToEnd && f.Close == "" {
		// Alone on its line: the rest of the file is the span.
		if k := s.BodyStart; k < len(data) && data[k] != '\n' && data[k] != '\r' {
			return Span{}, false
		}
		s.BodyEnd, s.End, s.Closed = len(data), len(data), true
		return s, true
	}
	if f.Close == "" {
		end := len(data)
		if k := bytes.IndexByte(data[s.BodyStart:], '\n'); k >= 0 {
			end = s.BodyStart + k
		}
		if f.Stop != "" {
			if k := bytes.Index(data[s.BodyStart:end], []byte(f.Stop)); k >= 0 {
				end = s.BodyStart + k
				s.End, s.BodyEnd, s.Closed = end, end, true
				return s, true
			}
		}
		s.End, s.Closed = end, true
		if data[end-1] == '\r' { // end is past the opener, so end-1 is in data
			end--
		}
		s.BodyEnd = end
		return s, true
	}
	closer := slices.Concat([]byte(f.Close), fence, []byte(f.Outer))
	depth := 1
	blank = false
	class := false // inside a regular expression's [...]
	for k := s.BodyStart; k < len(data); {
		b := data[k]
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
		switch {
		case f.Line && b == '\n':
			s.BodyEnd, s.End = k, k
			return s, true
		case f.Escape != 0 && b == f.Escape:
			// In a form whose code opens with a lone {, an escape takes no
			// brace: Python's rf'\{{' is a backslash and a brace.
			if f.braceCode() && k+1 < len(data) && (data[k+1] == '{' || data[k+1] == '}') {
				k++
			} else {
				k += 2
			}
			continue
		case f.Pair != 0 && b == f.Pair:
			depth++ // a bracket the closer's byte closes, nested in the body
			k++
			continue
		case f.Nests && bytes.HasPrefix(data[k:], []byte(f.Open)):
			depth++
			k += len(f.Open)
			blank = false
			continue
		case f.Classes && (b == '[' || b == ']'):
			class = b == '['
			k++
			continue
		case !class && bytes.HasPrefix(data[k:], closer) && (!f.LineStart || blank) && f.then(data, k+len(closer)):
			if f.Doubled && bytes.HasPrefix(data[k+len(closer):], closer) {
				k += 2 * len(closer)
				blank = false
				continue
			}
			if depth--; depth == 0 {
				end := k + len(closer)
				for f.Greedy && end < len(data) && data[end] == closer[len(closer)-1] && end-k-len(closer) < maxRun {
					end++
				}
				s.BodyEnd, s.End, s.Closed = end-len(closer), end, true
				return s, true
			}
			k += len(closer)
			blank = false
			continue
		}
		switch b {
		case '\n':
			blank = true
		case ' ', '\t', '\r', '\f', '\v':
		default:
			blank = false
		}
		k++
	}
	s.BodyEnd, s.End, s.Closed = len(data), len(data), f.ToEnd
	return s, true
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
	depth := 1 // the brackets of every kind open, open's included
scan:
	for k < len(data) {
		b := data[k]
		if depth == 1 && spec != "" && strings.IndexByte(spec, b) >= 0 && (k+1 == len(data) || data[k+1] != '=') {
			return c.spec(data, k+1, level, spec) // not := or !=, which are code
		}
		for _, f := range c.first[b] {
			if s, ok := f.at(data, k, false, c, level); ok {
				if !s.Closed || f.Heredoc {
					return 0, false
				}
				k = s.End
				continue scan
			}
		}
		switch b {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth--; depth == 0 {
				return k + 1, b == shut
			}
		}
		k++
	}
	return 0, false
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
	k := i - 1
	for k >= 0 && (data[k] == ' ' || data[k] == '\t' || data[k] == '\n' || data[k] == '\r') {
		if data[k] != ' ' && data[k] != '\t' && f.LineStarts {
			// A line end ends the statement before it, unless a backslash
			// joins the lines.
			j := k - 1
			if j >= 0 && data[k] == '\n' && data[j] == '\r' {
				j--
			}
			if j < 0 || data[j] != '\\' {
				return true
			}
			k = j
		}
		k--
	}
	if k < 0 || strings.IndexByte(f.Before, data[k]) >= 0 {
		return true
	}
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
	var q byte
	if j < len(data) && (data[j] == '\'' || data[j] == '"' || data[j] == '`') {
		q = data[j]
		j++
	}
	k := j
	for k < len(data) && k-j < maxTag && identByte(data[k]) {
		k++
	}
	if k == j || data[j] >= '0' && data[j] <= '9' || f.TagFirst != "" && strings.IndexByte(f.TagFirst, data[j]) < 0 {
		return Span{}, false
	}
	end := k
	if q != 0 {
		if k >= len(data) || data[k] != q {
			return Span{}, false
		}
		end++
	} else if k < len(data) && identByte(data[k]) {
		return Span{}, false // a tag past maxTag
	}
	return Span{Kind: f.Kind, Start: i, BodyStart: end, BodyEnd: end, End: end, Closed: true,
		Here: Here{Start: j, End: k, Quote: q}}, true
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
	tag := data[s.Here.Start:s.Here.End]
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
	for p := from; ; {
		q := p
		for f.Indent && q < len(data) && (data[q] == ' ' || data[q] == '\t') {
			q++
		}
		end := q + len(tag)
		if bytes.HasPrefix(data[q:], tag) && (end == len(data) || data[end] == '\n' || data[end] == '\r' ||
			f.Trail && !identByte(data[end])) {
			if s.Here.Quote != '\'' && !f.codeCloses(data, from, p, c) {
				return runsOff
			}
			s.Here = Here{Start: from, End: p, Close: end, Quote: s.Here.Quote}
			hd.resume = end
			return s
		}
		k := bytes.IndexByte(data[p:], '\n')
		if k < 0 {
			return runsOff
		}
		p += k + 1
	}
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
	k := s.BodyStart
	indicator := 0 // an explicit indentation indicator, 0 for none
	for n := 0; n < 2 && k < len(data) && strings.IndexByte("-+123456789", data[k]) >= 0; n++ {
		if data[k] >= '1' && data[k] <= '9' {
			indicator = int(data[k] - '0')
		}
		k++
	}
	for k < len(data) && (data[k] == ' ' || data[k] == '\t') {
		k++
	}
	switch {
	case k == len(data) || data[k] == '\n' || data[k] == '\r':
	case data[k] == '#' && (data[k-1] == ' ' || data[k-1] == '\t'):
	default:
		return Span{}, false
	}
	nl := bytes.IndexByte(data[k:], '\n')
	if nl < 0 {
		s.BodyStart, s.BodyEnd, s.End, s.Closed = len(data), len(data), len(data), true
		return s, true
	}
	parent := parentColumn(data, i)
	p := k + nl + 1
	s.BodyStart = p
	// content is the body's indentation: the node's column plus an explicit
	// indicator, else that of the body's first line that is not blank.
	content := -1
	if indicator > 0 {
		content = max(parent, 0) + indicator
	}
	for p < len(data) {
		col := 0
		for p+col < len(data) && data[p+col] == ' ' {
			col++
		}
		end := len(data)
		if e := bytes.IndexByte(data[p:], '\n'); e >= 0 {
			end = p + e + 1
		}
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
	s.BodyEnd, s.End, s.Closed = p, p, true
	return s, true
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
