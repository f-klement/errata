package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"regexp/syntax"
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
	// TimeLimit bounds the matching. CompileSearch sets it from the limit
	// (regexTime); a test shortens it, so a refusal on time comes fast.
	TimeLimit time.Duration
}

// CompileSearch checks the file and the pattern of an extractor of kind that
// searches with a regular expression, and returns the search. Every refusal
// starts with kind.
func CompileSearch(kind, file, pattern string, maxBytes int64) (Search, error) {
	src, err := CheckSource(kind, file, maxBytes)
	if err != nil {
		return Search{}, err
	}
	if pattern == "" {
		return Search{}, fmt.Errorf("%s: pattern is required", kind)
	}
	// A pattern can be as long as the configuration, and regexp's error
	// quotes it too, so a message quotes the first maxQuote bytes of it.
	if _, err := regexp.Compile(pattern); err != nil {
		return Search{}, fmt.Errorf("%s: pattern %s: %w", kind, Quote(pattern), ClipError(err))
	}
	re := regexp.MustCompile("(?m)" + pattern)
	switch n := re.NumSubexp(); {
	case n == 0:
		return Search{}, fmt.Errorf("%s: pattern %s has no capture group; the extractor returns group 1", kind, Quote(pattern))
	case n > MaxGroups:
		return Search{}, fmt.Errorf("%s: pattern %s: it has %d capture groups, over the limit of %d; write (?:...) for a group that need not capture",
			kind, Quote(pattern), n, MaxGroups)
	}
	e := Search{Source: src, re: re, TimeLimit: regexTime(src.Limit)}
	if looksBack(pattern) {
		if e.after, err = compileAfter(pattern); err != nil {
			return Search{}, fmt.Errorf("%s: pattern %s: %w", kind, Quote(pattern), ClipError(err))
		}
	}
	return e, nil
}

// looksBack reports whether the pattern holds ^, \A, \b or \B, which look
// at the text before a position. A pattern without them matches the same
// from a position whether or not the text before it is there.
func looksBack(pattern string) bool {
	parsed, err := syntax.Parse("(?m)"+pattern, syntax.Perl)
	if err != nil {
		return true
	}
	var walk func(*syntax.Regexp) bool
	walk = func(r *syntax.Regexp) bool {
		switch r.Op {
		case syntax.OpBeginLine, syntax.OpBeginText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
			return true
		default: // every other op looks at the text from where it starts
		}
		for _, sub := range r.Sub {
			if walk(sub) {
				return true
			}
		}
		return false
	}
	return walk(parsed)
}

// compileAfter compiles the pattern to search from a position past the
// start: \A and one rune, the rune before the position, which ^ and \b look
// at, then anything, as little as it can, and then the pattern in group 1. A
// pattern that ends inside \Q, which runs to the end of the pattern, takes \E
// before the group closes. A pattern nested as deeply as RE2 allows is
// refused here, since the wrapping nests it two levels deeper.
func compileAfter(pattern string) (*regexp.Regexp, error) {
	const head = `\A(?s:.)(?s:.)*?((?m)`
	re, err := regexp.Compile(head + pattern + `)`)
	if err == nil {
		return re, nil
	}
	if re, errQ := regexp.Compile(head + pattern + `\E)`); errQ == nil {
		return re, nil
	}
	var perr *syntax.Error
	if errors.As(err, &perr) && perr.Code == syntax.ErrNestingDepth {
		return nil, errors.New("it nests within two levels of the deepest nesting RE2 allows; errata needs those two to search after a match")
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

// FromData matches data with every CRLF line ending read as LF, so $ matches
// before a CRLF too and no value ends in its CR. Only CRs go, so the lines
// are those of the file.
func (e Search) FromData(data []byte) ([]check.Value, error) {
	if crlf := []byte("\r\n"); bytes.Contains(data, crlf) {
		data = bytes.ReplaceAll(data, crlf, []byte("\n"))
	}
	matches, err := e.Matches(data)
	if err != nil {
		return nil, err
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
	clock := &clockedReader{deadline: time.Now().Add(e.TimeLimit)}
	var out []Match
	for pos, prevEnd := 0, -1; pos <= len(data); {
		m := e.find(data, pos, clock)
		if clock.stopped || clock.late() {
			return nil, fmt.Errorf("%s: matching took more than %s, the limit, and stopped after %d matches; "+
				"start the pattern with ^, drop an optional part that scans to the end of the line, such as (?:.*x)?, "+
				"or shorten a counted repeat, such as {0,1000}",
				e.File, e.TimeLimit, len(out))
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
			if out = append(out, Match(m[:4])); len(out) > MaxValues {
				return nil, TooManyValues(e.File)
			}
		}
	}
	return out, nil
}

// find returns the first match at or after pos. Past the start, a pattern
// that looks back searches with after from the rune before pos, so ^, \A and
// \b see what comes before pos, as in a search of the whole file; any other
// pattern searches the text from pos. The search reads through clock.
func (e Search) find(data []byte, pos int, clock *clockedReader) []int {
	from, re, skip := pos, e.re, 0
	if pos > 0 && e.after != nil {
		_, w := utf8.DecodeLastRune(data[:pos])
		from, re, skip = pos-w, e.after, 2
	}
	clock.data, clock.pos = data[from:], 0
	m := re.FindReaderSubmatchIndex(clock)
	if m == nil {
		return nil
	}
	m = m[skip:]
	for i := range m {
		if m[i] >= 0 {
			m[i] += from
		}
	}
	return m
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
		return rune(c), 1, nil
	}
	c, w := utf8.DecodeRune(r.data[r.pos:])
	r.pos += w
	return c, w, nil
}

// late reports whether the deadline has passed.
func (r *clockedReader) late() bool {
	return time.Now().After(r.deadline)
}
