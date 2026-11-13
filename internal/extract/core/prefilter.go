package core

import (
	"bytes"
	"regexp/syntax"
	"slices"
	"unicode"
	"unicode/utf8"
)

// A pattern with no literal prefix runs regexp over every byte of a file,
// about 20 MB/s through the clocked reader. Most such patterns still need a
// literal somewhere: ([\x{2013}\x{2014}]) needs one of two, and
// (\w*[Gg]etFeatureFlags\w*) needs GetFeatureFlags or getFeatureFlags. A
// seeker holds the literals one of which every match holds, read from the
// parsed pattern, and find runs the pattern only on the lines around each
// place one of them occurs (findNear).
//
// A seeker only narrows where find searches, so a literal set that holds
// more than it must costs time and never a match. What it may never do is
// leave out a literal some match holds; litsOf errs toward knowing nothing.

const (
	// maxLits is the most texts a node's set may hold: a class or an
	// alternation of more, or a product that would grow past it, gives up.
	maxLits = 16
	// maxSeek is the most literals a seeker looks for, each a scan of the
	// text; a set of more is cut to shorter prefixes until it fits.
	maxSeek = 8
	// maxSpan is the most newlines a match may hold for find to search near
	// a literal: it searches that many lines either side of the literal's.
	maxSpan = 3
)

// seeker is the literals one of which every match of a pattern holds, and
// span, the most newlines a match holds.
type seeker struct {
	lits [][]byte
	span int
}

// seekerOf returns the seeker of the pattern regexp compiled, nil when there
// is none: no literal every match holds, a match that may hold more than
// maxSpan newlines, or a \A or \z, which a search of a few lines cut from the
// text would see at their ends.
func seekerOf(pattern string) *seeker {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil || holdsOp(parsed, syntax.OpBeginText, syntax.OpEndText) {
		return nil
	}
	span := newlines(parsed)
	lits := shorten(litsOf(parsed).need)
	if span > maxSpan || lits == nil {
		return nil
	}
	return &seeker{lits: lits, span: span}
}

// holdsOp reports whether re or a node below it is one of ops.
func holdsOp(re *syntax.Regexp, ops ...syntax.Op) bool {
	if slices.Contains(ops, re.Op) {
		return true
	}
	for _, sub := range re.Sub {
		if holdsOp(sub, ops...) {
			return true
		}
	}
	return false
}

// newlines is the most newlines a match of re holds, maxSpan+1 for more
// than maxSpan or no bound.
func newlines(re *syntax.Regexp) int {
	switch re.Op {
	case syntax.OpLiteral:
		return min(maxSpan+1, bytes.Count([]byte(string(re.Rune)), newline))
	case syntax.OpCharClass:
		return classNewlines(re.Rune)
	case syntax.OpAnyChar:
		return 1
	case syntax.OpConcat:
		return sumNewlines(re.Sub)
	case syntax.OpAlternate:
		return maxNewlines(re.Sub)
	case syntax.OpCapture, syntax.OpQuest:
		return newlines(re.Sub[0])
	case syntax.OpStar, syntax.OpPlus, syntax.OpRepeat:
		return repeatNewlines(re)
	default: // the empty-width ops, OpAnyCharNotNL and OpNoMatch match no newline
		return 0
	}
}

// classNewlines is 1 when the class ranges holds a newline, else 0.
func classNewlines(ranges []rune) int {
	for i := 0; i < len(ranges); i += 2 {
		if ranges[i] <= '\n' && '\n' <= ranges[i+1] {
			return 1
		}
	}
	return 0
}

// sumNewlines is the newlines of a concatenation of subs.
func sumNewlines(subs []*syntax.Regexp) int {
	n := 0
	for _, sub := range subs {
		n = min(maxSpan+1, n+newlines(sub))
	}
	return n
}

// maxNewlines is the newlines of an alternation of subs.
func maxNewlines(subs []*syntax.Regexp) int {
	n := 0
	for _, sub := range subs {
		n = max(n, newlines(sub))
	}
	return n
}

// repeatNewlines is the newlines of a repeat: none when its node matches
// none, else the node's times the most repeats, past maxSpan for no most.
func repeatNewlines(re *syntax.Regexp) int {
	n := newlines(re.Sub[0])
	_, most := bounds(re)
	switch {
	case n == 0:
		return 0
	case most < 0:
		return maxSpan + 1
	default:
		return min(maxSpan+1, n*most)
	}
}

// bounds is the fewest and the most times a repeat matches its node, -1
// for no most.
func bounds(re *syntax.Regexp) (fewest, most int) {
	switch re.Op {
	case syntax.OpQuest:
		return 0, 1
	case syntax.OpStar:
		return 0, -1
	case syntax.OpPlus:
		return 1, -1
	default:
		return re.Min, re.Max
	}
}

// litSet is what the matches of a node are known to be or to hold.
type litSet struct {
	// exact is the texts every match is one of, when isExact; "" for an
	// empty match.
	exact   []string
	isExact bool
	// need is texts one of which every match holds, none of them empty;
	// nil when none is known.
	need []string
}

// litsOf is what the matches of re are known to be or to hold.
func litsOf(re *syntax.Regexp) litSet {
	switch re.Op {
	case syntax.OpLiteral:
		return literalLits(re)
	case syntax.OpCharClass:
		return exactly(classLits(re.Rune))
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return exactly([]string{""})
	case syntax.OpCapture:
		return litsOf(re.Sub[0])
	case syntax.OpConcat:
		return concatLits(re.Sub)
	case syntax.OpAlternate:
		return alternateLits(re.Sub)
	case syntax.OpQuest, syntax.OpPlus, syntax.OpRepeat:
		return repeatLits(re)
	default: // OpAnyChar, OpAnyCharNotNL, OpStar and OpNoMatch are known to hold nothing
		return litSet{}
	}
}

// exactly is the litSet of a node whose matches are one of set, or of one
// known to hold nothing when set is nil.
func exactly(set []string) litSet {
	if set == nil {
		return litSet{}
	}
	return litSet{exact: set, isExact: true, need: needOf(set)}
}

// needOf is set as a need: nil when it holds the empty text, which any
// match holds.
func needOf(set []string) []string {
	if slices.Contains(set, "") {
		return nil
	}
	return set
}

// plain reports whether a literal r in a pattern matches only its UTF-8
// encoding. regexp reads a byte that is not UTF-8 as U+FFFD, so a pattern's
// U+FFFD also matches such a byte, which no scan for EF BF BD finds.
func plain(r rune) bool {
	return utf8.ValidRune(r) && r != utf8.RuneError
}

// classLits is the texts of the runes of a class, nil for more than
// maxLits or one that is not plain.
func classLits(ranges []rune) []string {
	var out []string
	for i := 0; i < len(ranges); i += 2 {
		lo, hi := ranges[i], ranges[i+1]
		if len(out)+int(hi-lo)+1 > maxLits {
			return nil
		}
		for r := lo; r <= hi; r++ {
			if !plain(r) {
				return nil
			}
			out = append(out, string(r))
		}
	}
	return out
}

// literalLits is the texts of a literal: with (?i), every way of writing
// each rune that regexp takes for it. Where they grow past maxLits, or at a
// rune that is not plain, the texts of the runes before are a need.
func literalLits(re *syntax.Regexp) litSet {
	out := []string{""}
	for _, r := range re.Rune {
		folds := []rune{r}
		if re.Flags&syntax.FoldCase != 0 {
			folds = orbit(r)
		}
		if !plain(r) || len(out)*len(folds) > maxLits {
			return litSet{need: needOf(out)}
		}
		out = product(out, runeTexts(folds))
	}
	return exactly(out)
}

// orbit is r and every rune that folds to it, as regexp matches r under
// (?i).
func orbit(r rune) []rune {
	out := []rune{r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		out = append(out, f)
	}
	return out
}

// runeTexts is the text of each of runes.
func runeTexts(runes []rune) []string {
	out := make([]string, len(runes))
	for i, r := range runes {
		out[i] = string(r)
	}
	return out
}

// product is every text of a followed by one of b, each once.
func product(a, b []string) []string {
	out := []string{}
	for _, x := range a {
		for _, y := range b {
			out = append(out, x+y)
		}
	}
	return dedupe(out)
}

// dedupe is set sorted with each text once, nil for nil.
func dedupe(set []string) []string {
	if set == nil {
		return nil
	}
	set = slices.Clone(set)
	slices.Sort(set)
	return slices.Compact(set)
}

// concatLits is what a concatenation's matches are or hold: the texts of
// a run of exact nodes, joined, are a need, as is any node's need, and the
// best of them is the concatenation's.
func concatLits(subs []*syntax.Regexp) litSet {
	run, best, whole := []string{""}, []string(nil), true
	for _, sub := range subs {
		s := litsOf(sub)
		best = better(best, s.need)
		switch {
		case !s.isExact:
			best, run, whole = better(best, needOf(run)), []string{""}, false
		case len(run)*len(s.exact) > maxLits:
			best, run, whole = better(best, needOf(run)), s.exact, false
		default:
			run = product(run, s.exact)
		}
	}
	best = better(best, needOf(run))
	if whole {
		return litSet{exact: run, isExact: true, need: best}
	}
	return litSet{need: best}
}

// alternateLits is what an alternation's matches are or hold: one of its
// branches' texts when every branch is exact, and one of its branches'
// needs when every branch has one.
func alternateLits(subs []*syntax.Regexp) litSet {
	exact, need, isExact := []string{}, []string{}, true
	for _, sub := range subs {
		s := litsOf(sub)
		isExact = isExact && s.isExact
		exact = append(exact, s.exact...)
		if need != nil && s.need != nil {
			need = append(need, s.need...)
		} else {
			need = nil
		}
	}
	out := litSet{need: dedupe(need)}
	if exact = dedupe(exact); isExact && len(exact) <= maxLits {
		out.exact, out.isExact = exact, true
	}
	if len(out.need) > maxLits {
		out.need = nil
	}
	return out
}

// repeatLits is what a repeat's matches are or hold: its node's need when
// it repeats at least once, and its texts when it repeats a fixed number of
// times or is optional and its node is exact.
func repeatLits(re *syntax.Regexp) litSet {
	s := litsOf(re.Sub[0])
	fewest, most := bounds(re)
	out := litSet{}
	if fewest > 0 {
		out.need = s.need
	}
	if !s.isExact {
		return out
	}
	var set []string
	switch {
	case fewest == most:
		set = power(s.exact, fewest)
	case fewest == 0 && most == 1:
		set = dedupe(append([]string{""}, s.exact...))
	default: // a range of counts gives up its texts
	}
	if set == nil || len(set) > maxLits {
		return out
	}
	exact := exactly(set)
	exact.need = better(exact.need, out.need)
	return exact
}

// power is set joined to itself n times, nil once that holds more than
// maxLits texts.
func power(set []string, n int) []string {
	out := []string{""}
	for range n {
		if len(out)*len(set) > maxLits {
			return nil
		}
		out = product(out, set)
	}
	return out
}

// better is the need of a and b that narrows a search more, as the
// seeker would look for it (shorten): one it can look for at all, then the
// one whose shortest literal is longer, then the one with fewer. nil is no
// need.
func better(a, b []string) []string {
	switch {
	case b == nil:
		return a
	case a == nil:
		return b
	}
	la, na := score(a)
	lb, nb := score(b)
	if lb > la || lb == la && nb < na {
		return b
	}
	return a
}

// score is the length of the shortest literal the seeker of need looks
// for and how many it looks for; 0 for a need it cannot look for.
func score(need []string) (length, count int) {
	lits := shorten(need)
	if lits == nil {
		return 0, 0
	}
	length = len(lits[0])
	for _, l := range lits[1:] {
		length = min(length, len(l))
	}
	return length, len(lits)
}

// shorten is need as at most maxSeek literals: the texts cut to the
// longest length at which their distinct prefixes are that few. A match
// holding a text holds its prefix. nil for no need.
func shorten(need []string) [][]byte {
	n := 0
	for _, s := range need {
		n = max(n, len(s))
	}
	for ; n > 0; n-- {
		cut := make([]string, len(need))
		for i, s := range need {
			cut[i] = s[:min(n, len(s))]
		}
		if cut = dedupe(cut); len(cut) <= maxSeek {
			return asBytes(cut)
		}
	}
	return nil
}

// asBytes is each text of set as bytes.
func asBytes(set []string) [][]byte {
	out := make([][]byte, len(set))
	for i, s := range set {
		out[i] = []byte(s)
	}
	return out
}

// found is where each literal of a seeker next occurs in one text, so a
// literal is looked for again only once the search has passed it.
type found struct {
	text []byte
	from int    // the position of the last look; find looks on from it
	at   []int  // where literal i next occurs at or after from, -1 for nowhere, -2 before the first look
	win  window // the lines findNear last searched around a literal
}

// window is the lines findNear searches for a literal: the line holding
// it, from start to its end, and the lines from first to last around it.
type window struct{ start, end, first, last int }

// foundFor is the clock's record of where the n literals occur in data,
// started afresh for another text, or a position before the last look.
func (r *clockedReader) foundFor(data []byte, pos, n int) *found {
	f := r.found
	if f == nil || pos < f.from || len(f.text) != len(data) || len(data) > 0 && &f.text[0] != &data[0] {
		f = &found{text: data, at: make([]int, n), win: window{start: 1}}
		for i := range f.at {
			f.at[i] = -2
		}
		r.found = f
	}
	f.from = pos
	return f
}

// next returns where the first literal at or after pos starts in data, -1
// when none does.
func (s *seeker) next(data []byte, pos int, clock *clockedReader) int {
	f := clock.foundFor(data, pos, len(s.lits))
	first := -1
	for i, lit := range s.lits {
		if f.at[i] != -1 && f.at[i] < pos {
			f.at[i] = indexFrom(data, pos, lit)
		}
		if at := f.at[i]; at >= 0 && (first < 0 || at < first) {
			first = at
		}
	}
	return first
}

// indexFrom is where lit first occurs in data at or after pos, -1 for
// nowhere.
func indexFrom(data []byte, pos int, lit []byte) int {
	if i := bytes.Index(data[pos:], lit); i >= 0 {
		return pos + i
	}
	return -1
}

// findNear is find for a pattern with a seeker: the first match at or after
// start. A match holds one of the literals, so it starts no earlier than
// span lines before the first literal at or after start, c, and one that
// starts by the end of c's line ends no later than span lines after it. So
// find searches those lines from start, and the first match it finds there
// is the first in the text if it starts by the end of c's line. If none
// does, no match starts before the next line, and find looks on from there.
//
// The lines are cut from the text at newlines, where ^, $, \b and \B see
// the end of the cut as they see the newline; a pattern with \A or \z,
// which would not, has no seeker. A search from past the start of the
// window looks back from there as Search.search does.
func (e Search) findNear(data []byte, start int, clock *clockedReader) []int {
	for pos := start; ; {
		c := e.seek.next(data, pos, clock)
		if c < 0 {
			return nil
		}
		w := e.windowOf(data, c, clock)
		if clock.stopped {
			return nil
		}
		m, _ := e.search(data[w.first:w.last], max(w.first, pos)-w.first, e.re, e.after, clock)
		if m != nil && m[0]+w.first <= w.end || clock.stopped {
			return shifted(m, w.first)
		}
		pos = min(w.end+1, len(data))
	}
}

// windowOf is the window of the literal at c. The record keeps the last
// one, and c is in its line while the search finds more matches in that
// line, so the matches of a line cost one scan for its bounds, not one each:
// 80000 matches on one line of 1 MiB took 37 s without it. The scan is not
// a read of the clock's, so a new window looks at the clock as a read does
// every ClockEvery runes, and stops it once the deadline has passed.
func (e Search) windowOf(data []byte, c int, clock *clockedReader) window {
	f := clock.found
	if w := f.win; w.start <= c && c <= w.end {
		return w
	}
	w := window{start: lineStart(data, c), end: lineEnd(data, c)}
	w.first, w.last = linesAround(data, w.start, w.end, e.seek.span)
	f.win = w
	if clock.late() {
		clock.stopped = true
	}
	return w
}

// shifted is m with each position in it moved on by n.
func shifted(m []int, n int) []int {
	for i := range m {
		if m[i] >= 0 {
			m[i] += n
		}
	}
	return m
}

// linesAround is the start of the line span lines before the line from
// start to end, and the end of the line span lines after it, both lines in
// data where it has fewer.
func linesAround(data []byte, start, end, span int) (first, last int) {
	first, last = start, end
	for range span {
		if first > 0 {
			first = lineStart(data, first-1)
		}
		if last < len(data) {
			last = lineEnd(data, last+1)
		}
	}
	return first, last
}

// lineStart is where the line holding position i starts.
func lineStart(data []byte, i int) int {
	return bytes.LastIndexByte(data[:i], '\n') + 1
}

// lineEnd is where the line holding position i ends: its newline, or the
// end of data.
func lineEnd(data []byte, i int) int {
	if nl := bytes.IndexByte(data[i:], '\n'); nl >= 0 {
		return i + nl
	}
	return len(data)
}
