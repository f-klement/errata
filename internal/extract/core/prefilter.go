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
//
// A pattern whose best literal is one byte, as the : of
// ([0-9]{6,}:[A-Za-z0-9_-]{25,}), gains little from it: a : is on 64457 of
// the 269076 lines of Endpaper's backend Python, six digits in a row on
// 1501. When such a pattern cannot cross a line and requires minRun or more
// bytes in a row of one ASCII class of at most maxClass bytes, its seeker
// looks for that run as well (runOf), and find searches only a line that
// holds both the run and a literal (both). Neither the run nor the literal
// is known to be the rarer: on a CSV of numbers the digits are on every
// line and the : on none. So a line is searched only where both are, and
// a search is never slower than a search near the literal alone but for
// the scan for runs.

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
	// minRun is the fewest bytes of a class in a row a seeker looks for:
	// four digits are on 4202 of the lines of Endpaper's backend Python,
	// three on 12380. minLit is the shortest literal whose seeker looks
	// for no run as well.
	minRun = 4
	minLit = 2
	// maxClass is the most bytes a class of a run may hold. Four bytes of
	// \w, or of [A-Za-z0-9_-], in a row are on nearly every line of any
	// text, so a run of a wider class narrows no search.
	maxClass = 16
)

// seeker is the literals one of which every match of a pattern holds, the
// run of a class every match holds, or both, and span, the most newlines a
// match holds.
type seeker struct {
	lits [][]byte
	run  *classRun // nil for a seeker of literals alone
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
	need := litsOf(parsed).need
	if run := runSeeker(parsed, span, need); run != nil {
		return run
	}
	lits := shorten(need)
	if span > maxSpan || lits == nil {
		return nil
	}
	return &seeker{lits: lits, span: span}
}

// runSeeker is the seeker of the run of a class every match of re holds,
// with the literals of need when it has any: nil when a match may cross a
// line, the run is shorter than minRun, or the literals of need are minLit
// bytes long or longer.
func runSeeker(re *syntax.Regexp, span int, need []string) *seeker {
	run := runOf(re)
	if length, _ := score(need); span > 0 || run.k < minRun || length >= minLit {
		return nil
	}
	return &seeker{lits: shorten(need), run: &run}
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

// classRun is k or more bytes in a row, each in set: size is how many
// bytes set holds. A zero classRun is no run.
type classRun struct {
	set  [256]bool
	size int
	k    int
}

// runOf is the run of a class every match of re holds: the longest of
// the narrowest class that a node requires, as a class repeated at least
// k times. A class that holds a rune past ASCII or more than maxClass
// bytes, a run of fewer than minRun bytes and the subs of an alternation
// are no run; runOf errs toward knowing nothing, as litsOf does.
func runOf(re *syntax.Regexp) classRun {
	switch re.Op {
	case syntax.OpCharClass:
		return classRunOf(re.Rune, 1)
	case syntax.OpCapture:
		return runOf(re.Sub[0])
	case syntax.OpPlus, syntax.OpRepeat:
		return repeatRun(re)
	case syntax.OpConcat:
		return concatRun(re.Sub)
	default:
		return classRun{}
	}
}

// repeatRun is the run a repeat requires: its class's run of its fewest
// repeats, its node's run when the node is not a class, and none when it
// may repeat no times.
func repeatRun(re *syntax.Regexp) classRun {
	fewest, _ := bounds(re)
	switch sub := re.Sub[0]; {
	case fewest == 0:
		return classRun{}
	case sub.Op == syntax.OpCharClass:
		return classRunOf(sub.Rune, fewest)
	default:
		return runOf(sub)
	}
}

// concatRun is the better of the runs the nodes of a concatenation
// require.
func concatRun(subs []*syntax.Regexp) classRun {
	best := classRun{}
	for _, sub := range subs {
		best = betterRun(best, runOf(sub))
	}
	return best
}

// betterRun is the run of a and b that a seeker looks for: one of minRun
// bytes or more, then the narrower class, then the longer run. A narrower
// class is rarer in text: digits are fewer than the letters around them.
func betterRun(a, b classRun) classRun {
	switch {
	case b.k < minRun:
		return a
	case a.k < minRun, b.size < a.size, b.size == a.size && b.k > a.k:
		return b
	default:
		return a
	}
}

// classRunOf is the run of k of the class ranges, none when it holds a
// rune past ASCII or more than maxClass bytes. regexp reads an ASCII
// rune as its one byte, and any other byte as part of a longer rune or
// as U+FFFD, so a match holds k bytes of the set in a row.
func classRunOf(ranges []rune, k int) classRun {
	run := classRun{k: k}
	for pair := range slices.Chunk(ranges, 2) {
		lo, hi := pair[0], pair[len(pair)-1]
		if hi >= utf8.RuneSelf {
			return classRun{}
		}
		for r := lo; r <= hi; r++ {
			run.set[r] = true
			run.size++
		}
	}
	if run.size > maxClass {
		return classRun{}
	}
	return run
}

// index is where the first k bytes of the set in a row start in data at
// or after pos, -1 for nowhere: n counts the bytes of the set in a row,
// and the run ends at the byte that makes them k. Nowhere is
// slices.IndexFunc's own -1, so no fault in the count can report a place
// for a run that is not there.
func (r *classRun) index(data []byte, pos int) int {
	n := 0
	last := slices.IndexFunc(data[pos:], func(c byte) bool {
		if n++; !r.set[c] {
			n = 0
		}
		return n == r.k
	})
	if last < 0 {
		return last
	}
	return pos + last + 1 - r.k
}

// places is how many places the seeker looks for: its literals, then its
// run when it has one.
func (s *seeker) places() int {
	if s.run != nil {
		return len(s.lits) + 1
	}
	return len(s.lits)
}

// index is where place i of the seeker first occurs in data at or after
// pos, -1 for nowhere.
func (s *seeker) index(i int, data []byte, pos int) int {
	if i == len(s.lits) {
		return s.run.index(data, pos)
	}
	return indexFrom(data, pos, s.lits[i])
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

// next returns where the first literal or run at or after pos starts in
// data, -1 when none does. A seeker of both returns the first place of a
// line that holds both at or after pos (both).
func (s *seeker) next(data []byte, pos int, clock *clockedReader) int {
	f := clock.foundFor(data, pos, s.places())
	n := len(s.lits)
	switch {
	case s.run == nil:
		return s.firstOf(f, data, pos, 0, n)
	case n == 0:
		return s.firstOf(f, data, pos, n, n+1)
	default:
		return s.both(f, data, pos)
	}
}

// firstOf is where the first of places from to to occurs at or after pos,
// -1 for none, looking again for a place the record has before pos.
func (s *seeker) firstOf(f *found, data []byte, pos, from, to int) int {
	first := -1
	for i := from; i < to; i++ {
		if f.at[i] != -1 && f.at[i] < pos {
			f.at[i] = s.index(i, data, pos)
		}
		if at := f.at[i]; at >= 0 && (first < 0 || at < first) {
			first = at
		}
	}
	return first
}

// both is the earlier of a literal and a run at or after pos in the first
// line that holds both there, -1 for none. While the first literal and
// the first run are on two lines, no line before the later one's holds
// both, so the look goes on from the start of that line, which the record
// then knows as its last look. That start is past pos; the look moves on
// by a byte at least should a scan report a place before it.
func (s *seeker) both(f *found, data []byte, pos int) int {
	n := len(s.lits)
	for {
		lit := s.firstOf(f, data, pos, 0, n)
		if lit < 0 {
			return lit
		}
		run := s.firstOf(f, data, pos, n, n+1)
		if run < 0 {
			return run
		}
		a, b := min(lit, run), max(lit, run)
		if bytes.IndexByte(data[a:b], '\n') < 0 {
			return a
		}
		pos = max(pos+1, lineStart(data, b))
		f.from = pos
	}
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
//
// No match starts at the end of the text, since a match holds a literal or
// a run, and the window is of a place at or after pos whatever place next
// reports, so each turn moves pos past the window's line. A fault in a
// seeker's scan then costs matches, which the property test sees, rather
// than a search that never ends.
func (e Search) findNear(data []byte, start int, clock *clockedReader) []int {
	for pos := start; ; {
		c := e.seek.next(data, pos, clock)
		if c < 0 || pos == len(data) {
			return nil
		}
		w := e.windowOf(data, max(c, pos), clock)
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
