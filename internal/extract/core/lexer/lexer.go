// Package lexer finds the comments and the string literals of a source file,
// driven by a table of languages with one row each (table.go). A row lists a
// language's file names and the forms its comments and literals take: the
// bytes that open and close each, its escape, whether it ends at a line's end,
// nests or is one character long. The lexer reads a file once, left to right,
// and skips a string literal whole, so a comment marker inside one is no
// comment. It parses nothing else: what a row cannot state, such as a shell
// heredoc, it does not understand (README, Code comments).
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
)

// Span is one comment, string literal or quoted name of a file: the bytes
// from its opener to the end of its closer, and its body, the bytes between
// them. A line comment's span ends before the line feed, and its body before
// a carriage return ahead of it too. A span with no closer runs to the end of the
// file, or of the line for a form that ends at one, and is not Closed. A line
// comment is always Closed: the end of the file ends its line.
type Span struct {
	Kind               Kind
	Start, End         int
	BodyStart, BodyEnd int
	Closed             bool
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

	// inFence is Fence as a set, which compile fills in.
	inFence *[256]bool
}

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

	// index is the row's forms indexed, set for each row of the table and
	// shared by every copy of it.
	index *compiled
}

// compiled is a language's forms indexed by the first byte of their opener,
// the longest opener and inner first, so --[[ is tried before -- and
// Swift's #""" before #".
type compiled struct {
	first [256][]*Form
}

func compile(forms []Form) *compiled {
	c := &compiled{}
	for i := range forms {
		f := &forms[i]
		c.first[f.Open[0]] = append(c.first[f.Open[0]], f)
		if f.Fence != "" {
			f.inFence = new([256]bool)
			for j := range len(f.Fence) {
				f.inFence[f.Fence[j]] = true
			}
		}
	}
	for b := range c.first {
		slices.SortStableFunc(c.first[b], func(x, y *Form) int { return cmp.Compare(opener(y), opener(x)) })
	}
	return c
}

// opener is how long f's opener is without its fence, which compile sorts by.
func opener(f *Form) int { return len(f.Open) + len(f.Inner) }

// Spans are the comments, string literals and quoted names of data, in the
// order they start. A span does not overlap the one before it. The walk is
// linear in data: every fence and every run a closer takes is bounded.
func (l Language) Spans(data []byte) iter.Seq[Span] {
	c := l.index
	if c == nil {
		l.Forms = slices.Clone(l.Forms) // compile fills in each form's fence set
		c = compile(l.Forms)
	}
	return func(yield func(Span) bool) {
		blank := true // only blanks since the start of the line
	scan:
		for i := 0; i < len(data); {
			b := data[i]
			if l.Escape != 0 && b == l.Escape {
				blank = false
				i += 2
				continue
			}
			for _, f := range c.first[b] {
				if s, ok := f.at(data, i, blank); ok {
					if !yield(s) {
						return
					}
					i, blank = s.End, data[s.End-1] == '\n' // a span is never empty
					continue scan
				}
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
// whether only blanks stand before i on its line.
func (f *Form) at(data []byte, i int, blank bool) (Span, bool) {
	if !bytes.HasPrefix(data[i:], []byte(f.Open)) {
		return Span{}, false
	}
	if f.LineStart && !blank {
		return Span{}, false
	}
	if f.After != "" && i > 0 && data[i-1] != '\n' && strings.IndexByte(f.After, data[i-1]) < 0 {
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
	if f.Close == "" {
		end := len(data)
		if k := bytes.IndexByte(data[s.BodyStart:], '\n'); k >= 0 {
			end = s.BodyStart + k
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
	for k := s.BodyStart; k < len(data); {
		b := data[k]
		switch {
		case f.Line && b == '\n':
			s.BodyEnd, s.End = k, k
			return s, true
		case f.Escape != 0 && b == f.Escape:
			k += 2
			continue
		case f.Nests && bytes.HasPrefix(data[k:], []byte(f.Open)):
			depth++
			k += len(f.Open)
			blank = false
			continue
		case bytes.HasPrefix(data[k:], closer) && (!f.LineStart || blank):
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
	s.BodyEnd, s.End = len(data), len(data)
	return s, true
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
