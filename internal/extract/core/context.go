package core

import (
	"bytes"
	"fmt"
	"math/bits"
	"regexp"
	"regexp/syntax"
	"strings"
	"time"
	"unicode"
)

// Context is the lines around a match that decide whether a regex keeps it
// (ADR 0012): the lines of the match, Before lines before its first and
// After lines after its last. A match is kept when With matches in one of
// those lines, and Without matches in none. Each of the two is searched
// in each line alone, as though the line were the whole text, so it never
// matches across a line break.
type Context struct {
	Before  int    `json:"before"`
	After   int    `json:"after"`
	With    string `json:"with,omitempty"`
	Without string `json:"without,omitempty"`
}

// Around is a Context checked and compiled, as a Search or an Each applies
// it to its matches.
type Around struct {
	before, after int
	with, without *Search
}

// CompileContext checks c, of an extractor of kind, and compiles its
// patterns. Every refusal starts with kind.
func CompileContext(kind string, c Context) (*Around, error) {
	if c.With == "" && c.Without == "" {
		return nil, fmt.Errorf("%s: context needs with, without or both: a pattern that must match in the lines around a match, or one that must not", kind)
	}
	if c.Before < 0 || c.After < 0 {
		return nil, fmt.Errorf("%s: context before %d and after %d count lines and cannot be below 0", kind, c.Before, c.After)
	}
	a := &Around{before: c.Before, after: c.After}
	var err error
	if a.with, err = compileLine(kind, "with", c.With); err != nil {
		return nil, err
	}
	if a.without, err = compileLine(kind, "without", c.Without); err != nil {
		return nil, err
	}
	return a, nil
}

// compileLine is the search of a context pattern, "" giving none: the
// pattern with the m flag, as any regex's, rewritten by oneLine so that a
// search of the whole text finds a match in each line where a search of
// that line alone finds one.
func compileLine(kind, key, pattern string) (*Search, error) {
	if pattern == "" {
		return nil, nil
	}
	if strings.Contains(pattern, "(?{") {
		return nil, fmt.Errorf("%s: context %s %s references an extractor's values; only the pattern itself can", kind, key, Quote(pattern))
	}
	// RE2's message quotes the pattern as parsed, so the pattern is
	// parsed as written first, for a message that quotes what the config
	// says.
	if _, err := syntax.Parse(pattern, syntax.Perl); err != nil {
		return nil, fmt.Errorf("%s: context %s %s: %w", kind, key, Quote(pattern), ClipError(err))
	}
	parsed, err := syntax.Parse("(?m)"+pattern, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("%s: context %s %s: %w", kind, key, Quote(pattern), ClipError(err))
	}
	rewritten := oneLine(parsed)
	if emptyAtEveryLine(rewritten) {
		return nil, fmt.Errorf("%s: context %s %s matches the empty text at the start or the end of every line, so it holds in every line; make it need a character, as ^\\s*# needs the #",
			kind, key, Quote(pattern))
	}
	line := rewritten.String()
	re, err := regexp.Compile(line)
	if err != nil {
		return nil, fmt.Errorf("%s: context %s %s: %w", kind, key, Quote(pattern), ClipError(err))
	}
	s, err := searchOf(line, re)
	if err != nil {
		return nil, fmt.Errorf("%s: context %s %s: %w", kind, key, Quote(pattern), ClipError(err))
	}
	return &s, nil
}

// oneLine is re rewritten to match, in a text, what re matches in a line
// of it alone: nothing in it matches a newline, which a line alone does
// not hold, \A and \z match at the start and end of each line, as they do
// at those of a line alone, and every capture group is a plain group,
// since nothing reads a context pattern's groups. ^, $ and \b already see
// a newline as they see either end of the text, so a match at a place in
// a line alone is a match at that place in the text, and one in the text
// stays in its line.
func oneLine(re *syntax.Regexp) *syntax.Regexp {
	for i, sub := range re.Sub {
		re.Sub[i] = oneLine(sub)
	}
	switch re.Op {
	case syntax.OpCapture:
		return re.Sub[0]
	case syntax.OpAnyChar:
		re.Op = syntax.OpAnyCharNotNL
	case syntax.OpBeginText:
		re.Op = syntax.OpBeginLine
	case syntax.OpEndText:
		re.Op, re.Flags = syntax.OpEndLine, re.Flags&^syntax.WasDollar
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if r == '\n' || re.Flags&syntax.FoldCase != 0 && unicode.SimpleFold(r) == '\n' {
				return &syntax.Regexp{Op: syntax.OpNoMatch}
			}
		}
	case syntax.OpCharClass:
		re.Rune = withoutNewline(re.Rune)
		if len(re.Rune) == 0 {
			return &syntax.Regexp{Op: syntax.OpNoMatch}
		}
	default: // every other op matches no newline of its own
	}
	return re
}

// emptyAtEveryLine reports whether re, rewritten by oneLine, matches the
// empty text at the start of every line, such as x*, ^ or (foo)?, or at
// the end of every line, such as $. With would hold in every line and
// without would drop every match, so a stray ? would turn a guard into one
// that always passes. ^$ matches only an empty line, and \b only beside a
// word character, so neither is refused.
func emptyAtEveryLine(re *syntax.Regexp) bool {
	return emptyAt(re, true) || emptyAt(re, false)
}

// emptyAt reports whether re matches the empty text at the start of any
// line, for start, or at the end of any line: ^ holds only at a start and
// $ only at an end, and \b and \B depend on the line, so they hold at
// neither.
func emptyAt(re *syntax.Regexp, start bool) bool {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpStar, syntax.OpQuest:
		return true
	case syntax.OpBeginLine, syntax.OpBeginText:
		return start
	case syntax.OpEndLine, syntax.OpEndText:
		return !start
	case syntax.OpCapture, syntax.OpPlus:
		return emptyAt(re.Sub[0], start)
	case syntax.OpRepeat:
		return re.Min == 0 || emptyAt(re.Sub[0], start)
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !emptyAt(sub, start) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if emptyAt(sub, start) {
				return true
			}
		}
		return false
	default: // a character, a class, \b, \B or no match needs what the line holds
		return false
	}
}

// withoutNewline is the ranges of a character class, lo and hi pairs, with
// '\n' taken out.
func withoutNewline(ranges []rune) []rune {
	out := make([]rune, 0, len(ranges)+2)
	for i := 0; i+1 < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		if lo > '\n' || hi < '\n' {
			out = append(out, lo, hi)
			continue
		}
		if lo < '\n' {
			out = append(out, lo, '\n'-1)
		}
		if hi > '\n' {
			out = append(out, '\n'+1, hi)
		}
	}
	return out
}

// keep is the matches of data whose lines a has the context of, through
// the deadline the search of them ran under: holds of each, in order.
func (a *Around) keep(data []byte, matches []Match, deadline time.Time, file string, limit time.Duration, whose string) ([]Match, error) {
	in := a.of(data, deadline, file, limit, whose)
	out := matches[:0]
	for _, m := range matches {
		ok, err := in.holds(m[0], m[1])
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// aroundFile is a context over one file: the lines each of its patterns
// matches in, found on the first match asked about, so a file the search
// finds nothing in is never searched for a context.
type aroundFile struct {
	*Around
	data          []byte
	deadline      time.Time
	file, whose   string
	limit         time.Duration
	found         bool
	with, without *lineSet
	cursor        *LineCursor
}

// of is a over data, under the deadline the search of data runs under;
// file, limit and whose name the file and its time limit in a refusal on
// time. For a nil a, of is nil, whose holds keeps every match, so a
// caller that hands each match on as it finds it, such as one building a
// record of every group, asks holds and needs no list of matches.
func (a *Around) of(data []byte, deadline time.Time, file string, limit time.Duration, whose string) *aroundFile {
	if a == nil {
		return nil
	}
	return &aroundFile{Around: a, data: data, deadline: deadline, file: file, limit: limit, whose: whose, cursor: NewLineCursor(data)}
}

// holds reports whether the match from start to end has the context. The
// first call searches the file for each context pattern; each one after
// costs two lookups and the newlines between, so asking of every match in
// the order a search finds them is linear in the file and the matches.
func (f *aroundFile) holds(start, end int) (bool, error) {
	if f == nil {
		return true, nil
	}
	if !f.found {
		var err error
		if f.with, err = f.lines(f.Around.with, "with", f.data, f.deadline, f.file, f.limit, f.whose); err != nil {
			return false, err
		}
		if f.without, err = f.lines(f.Around.without, "without", f.data, f.deadline, f.file, f.limit, f.whose); err != nil {
			return false, err
		}
		f.found = true
	}
	first := f.cursor.At(start)
	last := first
	if end > start {
		last += bytes.Count(f.data[start:end-1], newline)
	}
	lo, hi := first-min(f.before, first), last+min(f.after, len(f.data))
	return (f.with == nil || f.with.any(lo, hi)) && (f.without == nil || !f.without.any(lo, hi)), nil
}

// fileLines is the number of lines of data: one for each newline, and one
// for the text after the last, unless data ends with a newline, which ends
// its last line. An empty file has one empty line. An empty match past a
// final newline, as ^$ finds, marks the line after the last, which any
// cuts off, so it counts in no context.
func fileLines(data []byte) int {
	n := bytes.Count(data, newline)
	if len(data) == 0 || data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// lines is the set of lines of data in which s finds a match, nil for no
// s. Once it finds one in a line it searches on from the next, so it reads
// each line about once whatever the pattern matches.
func (a *Around) lines(s *Search, key string, data []byte, deadline time.Time, file string, limit time.Duration, whose string) (*lineSet, error) {
	if s == nil {
		return nil, nil
	}
	set := newLineSet(fileLines(data))
	clock := &clockedReader{deadline: deadline, spare: maxTry}
	cursor := NewLineCursor(data)
	for pos := 0; pos <= len(data); {
		m := s.find(data, pos, clock)
		if clock.stopped || clock.late() {
			return nil, fmt.Errorf("%s: matching took more than %s, %s, and stopped in the search of the context's %s pattern; shorten a counted repeat in it, such as {0,1000}",
				file, limit, whose, key)
		}
		if m == nil {
			break
		}
		set.add(cursor.At(m[0]))
		nl := bytes.IndexByte(data[m[0]:], '\n')
		if nl < 0 {
			break
		}
		pos = m[0] + nl + 1
	}
	set.count()
	return set, nil
}

// lineSet is a set of the lines of a text, counted from 1: one bit for each
// line, and the bits set before each word, so whether a range holds one is
// two lookups. A text of 32 MiB of empty lines takes 8 MB.
type lineSet struct {
	bits   []uint64
	before []int // bits set in the words before word i; len(bits)+1 of them
	lines  int
}

func newLineSet(lines int) *lineSet {
	return &lineSet{bits: make([]uint64, lines/64+1), lines: lines}
}

func (s *lineSet) add(line int) {
	i := line - 1
	s.bits[i/64] |= 1 << (i % 64)
}

// count fills before, once every line is added.
func (s *lineSet) count() {
	s.before = make([]int, len(s.bits)+1)
	for i, w := range s.bits {
		s.before[i+1] = s.before[i] + bits.OnesCount64(w)
	}
}

// below is how many of the lines before line i, counted from 0, are set.
func (s *lineSet) below(i int) int {
	return s.before[i/64] + bits.OnesCount64(s.bits[i/64]&(1<<(i%64)-1))
}

// any reports whether a line from lo to hi, both counted from 1, is set;
// the range is cut to the lines of the text.
func (s *lineSet) any(lo, hi int) bool {
	lo, hi = max(lo, 1), min(hi, s.lines)
	if lo > hi {
		return false
	}
	return s.below(hi) > s.below(lo-1)
}
