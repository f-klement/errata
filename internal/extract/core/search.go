package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"regexp/syntax"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
)

// Search is the regex extractor, and the search the text extractor runs
// over prose. It returns capture group 1 of every match. The pattern is Go's
// RE2 syntax with the m flag set, so ^ and $ match at the start and end of
// each line; . still stops at a newline, so a match stays on one line unless
// the pattern names \n. (?-m) at the start of a pattern turns the flag off.
type Search struct {
	Source
	re    *regexp.Regexp // the pattern, with the m flag
	after *regexp.Regexp // the same, matched from the rune before a position; nil when the pattern looks at none
	// prefix is the literal every match starts with, nil when the pattern
	// has none. anchored compiles the pattern anchored where a try starts
	// on the first try, so a pattern whose prefix never occurs costs no
	// second program. atStart is whether the pattern holds \A, or ^
	// without the m flag, which parses to the same.
	prefix   []byte
	anchored func() *anchors
	atStart  bool
	// TimeLimit bounds the matching. CompileSearch sets it from the limit
	// (regexTime); a test shortens it, so a refusal on time comes fast.
	TimeLimit time.Duration
	// shared is the deadline the searches of a file set share, which a
	// FileSet sets in place of TimeLimit from now, or the searches of an
	// Each; zero for a search of one file.
	shared time.Time
	// whose is what a refusal on time calls the limit of shared, "" for
	// the limit of a file set's searches.
	whose string
	// fields names the capture groups a record holds, by group number, ""
	// for a group it leaves out; nil for a search that returns group 1
	// (Records).
	fields []string
	// around is the context a match must have to be kept, nil for none.
	around *Around
}

// Around is e keeping only the matches with the context a gives, nil for
// every match. The context's searches share e's time limit.
func (e Search) Around(a *Around) Search {
	e.around = a
	return e
}

// CompileSearch checks the file and the pattern of an extractor of kind that
// searches with a regular expression, and returns the search. Every refusal
// starts with kind.
func CompileSearch(kind, file, pattern string, maxBytes int64) (Search, error) {
	src, err := CheckSource(kind, file, maxBytes)
	if err != nil {
		return Search{}, err
	}
	e, err := compilePattern(kind, pattern)
	if err != nil {
		return Search{}, err
	}
	e.Source, e.TimeLimit = src, regexTime(src.Limit)
	return e, nil
}

// compilePattern is CompileSearch without the file: the search of pattern,
// with no Source and no TimeLimit.
func compilePattern(kind, pattern string) (Search, error) {
	if pattern == "" {
		return Search{}, fmt.Errorf("%s: pattern is required", kind)
	}
	// A pattern can be as long as the configuration, and regexp's error
	// quotes it too, so a message quotes the first maxQuote bytes of it.
	if _, err := regexp.Compile(pattern); err != nil {
		return Search{}, fmt.Errorf("%s: pattern %s: %w", kind, Quote(pattern), ClipError(err))
	}
	return compiled(kind, pattern, regexp.MustCompile("(?m)"+pattern))
}

// compileOnce is compilePattern with one compile, for a pattern errata
// built, such as the pattern of a value in an Each, rather than one a
// config wrote: its refusal quotes RE2's message about the pattern with
// the m flag, where compilePattern compiles again to quote the pattern as
// written.
func compileOnce(kind, pattern string) (Search, error) {
	re, err := regexp.Compile("(?m)" + pattern)
	if err != nil {
		return Search{}, fmt.Errorf("%s: pattern %s: %w", kind, Quote(pattern), ClipError(err))
	}
	return compiled(kind, pattern, re)
}

// compiled is the search of pattern, which re is with the m flag: its
// capture groups checked, and the forms find searches with set up.
func compiled(kind, pattern string, re *regexp.Regexp) (Search, error) {
	switch n := re.NumSubexp(); {
	case n == 0:
		return Search{}, fmt.Errorf("%s: pattern %s has no capture group; the extractor returns group 1", kind, Quote(pattern))
	case n > MaxGroups:
		return Search{}, fmt.Errorf("%s: pattern %s: it has %d capture groups, over the limit of %d; write (?:...) for a group that need not capture",
			kind, Quote(pattern), n, MaxGroups)
	}
	e, err := searchOf(pattern, re)
	if err != nil {
		return Search{}, fmt.Errorf("%s: pattern %s: %w", kind, Quote(pattern), ClipError(err))
	}
	return e, nil
}

// searchOf is the search of pattern, which re is with the m flag, with
// the forms find searches with set up, and its groups not checked.
func searchOf(pattern string, re *regexp.Regexp) (Search, error) {
	e := Search{re: re}
	back, atStart := looksBack(pattern)
	if back {
		var err error
		if e.after, err = compileAfter(pattern); err != nil {
			return Search{}, err
		}
	}
	if prefix, _ := re.LiteralPrefix(); prefix != "" {
		e.prefix, e.atStart = []byte(prefix), atStart
		e.anchored = sync.OnceValue(func() *anchors { return anchor(pattern, back) })
	}
	return e, nil
}

// anchors is the pattern anchored at the start of the text a try reads,
// and, when the pattern looks back, anchored after the rune before it.
type anchors struct{ at, after *regexp.Regexp }

// anchor compiles the anchors of the pattern, or returns nil when one does
// not compile; the pattern is then searched with its prefix but no tries.
// That happens when the pattern nests as deeply as RE2 allows and its
// outermost node is a group or a repeat, which the anchoring nests one
// level deeper; after, when the pattern looks back, nests it as deeply, so
// such a pattern is refused before.
func anchor(pattern string, back bool) *anchors {
	at, err := wrap(`\A(?:(?m)`, pattern)
	var after *regexp.Regexp
	if err == nil && back {
		after, err = wrap(`\A(?s:.)((?m)`, pattern)
	}
	if err != nil {
		return nil
	}
	return &anchors{at, after}
}

// looksBack reports whether the pattern holds ^, \A, \b or \B, which look
// at the text before a position, and whether it holds \A (or (?-m)^, the
// same op), which tells the
// start of the text from the start of a line. A pattern without them
// matches the same from a position whether or not the text before it is
// there.
func looksBack(pattern string) (back, atStart bool) {
	parsed, err := syntax.Parse("(?m)"+pattern, syntax.Perl)
	if err != nil {
		return true, true
	}
	var walk func(*syntax.Regexp)
	walk = func(r *syntax.Regexp) {
		switch r.Op {
		case syntax.OpBeginText:
			back, atStart = true, true
		case syntax.OpBeginLine, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
			back = true
		default: // every other op looks at the text from where it starts
		}
		for _, sub := range r.Sub {
			walk(sub)
		}
	}
	walk(parsed)
	return back, atStart
}

// compileAfter compiles the pattern to search from a position past the
// start: \A and one rune, the rune before the position, which ^ and \b look
// at, then anything, as little as it can, and then the pattern in group 1. A
// pattern that ends inside \Q, which runs to the end of the pattern, takes \E
// before the group closes. A pattern nested as deeply as RE2 allows is
// refused here, since the wrapping nests it two levels deeper.
func compileAfter(pattern string) (*regexp.Regexp, error) {
	re, err := wrap(`\A(?s:.)(?s:.)*?((?m)`, pattern)
	if err == nil {
		return re, nil
	}
	var perr *syntax.Error
	if errors.As(err, &perr) && perr.Code == syntax.ErrNestingDepth {
		return nil, errors.New("it nests within two levels of the deepest nesting RE2 allows; errata needs those two to search after a match")
	}
	return nil, err
}

// wrap compiles head, the pattern and a closing parenthesis, with \E
// before it when the pattern ends inside \Q.
func wrap(head, pattern string) (*regexp.Regexp, error) {
	re, err := regexp.Compile(head + pattern + `)`)
	if err == nil {
		return re, nil
	}
	if re, errQ := regexp.Compile(head + pattern + `\E)`); errQ == nil {
		return re, nil
	}
	return nil, err
}

// Extract returns group 1 of every match in the file under root, at the line
// where the match starts. A match in which group 1 took no part, such as
// (a)?b matching b, gives no value.
func (e Search) Extract(root string) ([]check.Value, error) {
	data, err := e.Read(root)
	if err != nil {
		return nil, err
	}
	return e.FromData(data)
}

// FromShared is FromData: a search's time is shared through its deadline,
// and it spends no other budget of a set.
func (e Search) FromShared(data []byte, _ *Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// FromData matches data with every CRLF line ending read as LF, so $ matches
// before a CRLF too and no value ends in its CR. Only CRs go, so the lines
// are those of the file. A search for records returns one for each match
// (Records). A match without the context e.around asks for gives no value,
// and the context's searches run under the deadline the matching did.
func (e Search) FromData(data []byte) ([]check.Value, error) {
	data = lineFeeds(data)
	if e.fields != nil {
		return e.records(data)
	}
	clock := e.clock()
	matches, err := e.matches(data, clock)
	if err != nil {
		return nil, err
	}
	if e.around != nil {
		if matches, err = e.around.keep(data, matches, clock.deadline, e.File, e.TimeLimit, e.whoseLimit()); err != nil {
			return nil, err
		}
	}
	l := NewLineCursor(data) // matches start in file order
	out := []check.Value{}
	for _, m := range matches {
		if m[2] < 0 {
			continue
		}
		out = append(out, check.Value{
			Text: string(data[m[2]:m[3]]),
			At:   check.Location{File: e.File, Line: l.At(m[0])},
		})
	}
	return out, nil
}

// lineFeeds is data with every CRLF read as LF.
func lineFeeds(data []byte) []byte {
	if crlf := []byte("\r\n"); bytes.Contains(data, crlf) {
		return bytes.ReplaceAll(data, crlf, []byte("\n"))
	}
	return data
}

// Match is where a match starts and ends, and where group 1 starts and
// ends, -1 for both when it took no part: the first four of what
// FindSubmatchIndex returns. A match keeps no other group.
type Match [4]int

// Matches returns the matches FindAllSubmatchIndex finds, and refuses more
// than maxValues matches. FindAll searches again after each match and cannot
// be stopped, and a pattern whose optional tail scans the rest of the line
// makes each search scan it, which is quadratic in the line. So matches runs
// the searches itself, with FindAll's rules for empty matches, and each
// search reads the file through a clock that ends it once the time limit has
// passed, so one search that finds nothing is stopped too.
func (e Search) Matches(data []byte) ([]Match, error) {
	return e.matches(data, e.clock())
}

// clock is the clock of one search of a file: TimeLimit from now, or the
// deadline the files of a set share.
func (e Search) clock() *clockedReader {
	clock := newClock(e.TimeLimit)
	if !e.shared.IsZero() {
		clock.deadline = e.shared
	}
	return clock
}

// whoseLimit is what a refusal on time calls TimeLimit: the limit of the
// one file, or of every file of a set together.
func (e Search) whoseLimit() string {
	if e.shared.IsZero() {
		return "the limit"
	}
	if e.whose != "" {
		return e.whose
	}
	return "the limit of the files the extractor reads, together"
}

// newClock is the clock of one Matches: its deadline limit from now, and
// maxTry bytes spare for find.
func newClock(limit time.Duration) *clockedReader {
	return &clockedReader{deadline: time.Now().Add(limit), spare: maxTry}
}

// matches is Matches through clock, which a test reads after.
func (e Search) matches(data []byte, clock *clockedReader) ([]Match, error) {
	var out []Match
	err := e.each(data, clock, func(m []int) error {
		if out = append(out, Match(m[:4])); len(out) > MaxValues {
			return TooManyValues(e.File)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// each runs the searches of Matches through clock and hands keep each match
// it accepts, every group's start and end, as FindSubmatchIndex returns
// them, in file order. It stops at the first error keep returns.
func (e Search) each(data []byte, clock *clockedReader, keep func(m []int) error) error {
	accepted := 0
	for pos, prevEnd := 0, -1; pos <= len(data); {
		m := e.find(data, pos, clock)
		if clock.stopped || clock.late() {
			return fmt.Errorf("%s: matching took more than %s, %s, and stopped after %d matches; "+
				"start the pattern with ^, drop an optional part that scans to the end of the line, such as (?:.*x)?, "+
				"or shorten a counted repeat, such as {0,1000}",
				e.File, e.TimeLimit, e.whoseLimit(), accepted)
		}
		if m == nil {
			break
		}
		accept := true
		if m[1] == pos {
			// An empty match at pos. One that touches the match before is
			// dropped, and the next search starts one rune on.
			accept = m[0] != prevEnd
			if _, w := utf8.DecodeRune(data[pos:]); w > 0 {
				pos += w
			} else {
				pos = len(data) + 1
			}
		} else {
			pos = m[1]
		}
		prevEnd = m[1]
		if accept {
			accepted++
			if err := keep(m); err != nil {
				return err
			}
		}
	}
	return nil
}

// find returns the first match at or after start, through clock. A
// pattern with a literal prefix can only match where the prefix starts, so
// find looks for it with bytes.Index, as regexp does over bytes, and tries
// the anchored pattern at each place it occurs. find searches on
// unanchored, as it does a pattern without a prefix, from a place less
// than minGap bytes past the text the last try read, where a try costs
// more than the reading it skips, and from a place whose try read to where
// it was cut off, since its match may not be the one the whole text gives.
//
// No shape reads more than a search of every place does, but for the
// clock.spare bytes, maxTry for the whole of Matches, that a search after
// the tries may read again: a try reads no more than the bytes still
// spare, and the search after it takes from them what it reads again.
// Once they are spent, find skips to the next place and searches on
// unanchored from there. A pattern that
// looks back is searched from the start, before its first match, with re,
// which costs about half what after costs; so a search after the tries
// starts at the start of the line, where ^ and \b see the newline before
// it as they see the start of the text, or at the start of the text for a
// pattern that holds \A, and reads the tries since again.
//
// The clock counts the runes of every try, so the deadline stops a run of
// them too. It does not count the bytes.Index scans: they start where the
// last one stopped, so in all they read the file once, about 0.1 s at
// 32 MiB at worst. The first try compiles the anchored form inside the
// time limit: the 23 KB pattern of a{1000} measured, the largest, takes
// 0.78 s on the build node, against the 10 s the limit is at least.
func (e Search) find(data []byte, start int, clock *clockedReader) []int {
	if e.prefix == nil {
		m, _ := e.search(data, start, e.re, e.after, clock)
		return m
	}
	first := start == 0 && e.after != nil
	pos, from, end, tried := start, start, -minGap, 0
	for {
		i := bytes.Index(data[pos:], e.prefix)
		if i < 0 {
			return nil
		}
		if first && !e.atStart {
			if nl := bytes.LastIndexByte(data[pos:pos+i], '\n'); nl >= 0 {
				from, tried = pos+nl+1, 0
			}
		}
		pos += i
		if !first {
			from = pos
		}
		a, spare := e.anchored(), clock.spare-tried
		if a == nil || spare <= 0 || pos < end+minGap {
			break
		}
		stop, read := min(pos+spare, len(data)), clock.read
		var m []int
		m, end = e.search(data[:stop], pos, a.at, a.after, clock)
		if first {
			tried += clock.read - read
		}
		if end == stop && stop < len(data) {
			break
		}
		if m != nil || clock.stopped {
			return m
		}
		pos++
	}
	if first {
		clock.spare -= tried
		m, _ := e.search(data, from, e.re, nil, clock)
		return m
	}
	clock.spare -= max(0, end-pos)
	m, _ := e.search(data, pos, e.re, e.after, clock)
	return m
}

// minGap is the fewest bytes the prefix scan must skip for find to try the
// anchored pattern at the next place rather than search on unanchored, and
// maxTry the most bytes a search after the tries may read again in one
// Matches. A try costs about as much as reading three bytes: on the build
// node, 262144 tries of x(\d), one every 128 bytes, took 0.02 s, and
// reading the same 32 MiB 0.81 s. The reader reads a rune past where a try
// fails, so an x every 66 bytes is still searched on unanchored.
const (
	minGap = 64
	maxTry = 4096
)

// search returns the first match of re at or after pos, and where it
// stopped reading. Past the start, a pattern that looks back searches with
// after from the rune before pos, so ^, \A and \b see what comes before pos,
// as in a search of the whole file; any other pattern searches the text
// from pos.
func (e Search) search(data []byte, pos int, re, after *regexp.Regexp, clock *clockedReader) ([]int, int) {
	from, skip := pos, 0
	if pos > 0 && after != nil {
		_, w := utf8.DecodeLastRune(data[:pos])
		from, re, skip = pos-w, after, 2
	}
	clock.data, clock.pos = data[from:], 0
	m := re.FindReaderSubmatchIndex(clock)
	end := from + clock.pos
	if m == nil {
		return nil, end
	}
	m = m[skip:]
	for i := range m {
		if m[i] >= 0 {
			m[i] += from
		}
	}
	return m, end
}

// ClockEvery is how many runes a search reads between two looks at the
// clock. A look costs about as much as reading a rune. ([ab]{0,1000}c)
// costs about 60 microseconds a rune, so it runs about 15 ms past the
// limit, and a wider pattern longer, in proportion to its width.
const ClockEvery = 256

// clockedReader reads data as runes, decoding them as a search of the bytes
// does, so the search finds the same matches at the same offsets. Once the
// deadline has passed it ends the text early and sets stopped; a match found
// then may be one the whole text would not give, so the caller refuses it.
type clockedReader struct {
	data     []byte
	pos      int
	deadline time.Time
	runes    int // runes read since the last look at the clock
	stopped  bool
	read     int // bytes read in all
	spare    int // bytes a search after the tries may still read again (find)
}

func (r *clockedReader) ReadRune() (rune, int, error) {
	if r.pos >= len(r.data) {
		return 0, 0, io.EOF
	}
	if r.runes++; r.runes == ClockEvery {
		r.runes = 0
		if r.late() {
			r.stopped = true
			return 0, 0, io.EOF
		}
	}
	if c := r.data[r.pos]; c < utf8.RuneSelf {
		r.pos++
		r.read++
		return rune(c), 1, nil
	}
	c, w := utf8.DecodeRune(r.data[r.pos:])
	r.pos += w
	r.read += w
	return c, w, nil
}

// late reports whether the deadline has passed.
func (r *clockedReader) late() bool {
	return time.Now().After(r.deadline)
}
