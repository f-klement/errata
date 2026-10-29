package core

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// Each is one search for each of several values over one file, as the regex
// extractor makes for a pattern that references another extractor's values:
// the pattern for a value is EachPattern of it. It returns group 1 of every
// match of every pattern, in the order the matches start in the file, and
// two that start at one place in the order of the values. Every search
// shares one time limit, the one a search of the file has, so the cost of
// the values times the file stays under it however many values there are.
//
// A pattern is compiled just before its search and dropped after it, so an
// Each holds the values and one compiled pattern at a time: 100000
// patterns compiled at once held 351 MB.
type Each struct {
	Source
	kind, before, after string
	// values are the values, each already in RE2 syntax.
	values []string
	// deadline is when the time of every search runs out: the limit from
	// when NewEach was called.
	deadline time.Time
	// TimeLimit is the limit the deadline was set from, for a message.
	TimeLimit time.Duration
	// around is the context a match must have to be kept, nil for none.
	around *Around
}

// Around is e keeping only the matches with the context a gives, nil for
// every match. The lines that have it are found once for each file, not
// once for each value.
func (e Each) Around(a *Around) Each {
	e.around = a
	return e
}

// eachLimit is what a refusal on time calls the limit of an Each.
const eachLimit = "the limit of the extractor's searches for every value, together"

// EachPattern is the pattern of one value: value, already in RE2 syntax,
// between before and after, in a group of its own, so a repeat after it
// repeats the whole value and an alternation in it stays inside.
func EachPattern(before, value, after string) string {
	return before + "(?:" + value + ")" + after
}

// PatternError is the refusal of the pattern of one value: Index is the
// value's place in the list, so the caller can name where it was found.
type PatternError struct {
	Index int
	Err   error
}

func (e *PatternError) Error() string { return e.Err.Error() }
func (e *PatternError) Unwrap() error { return e.Err }

// NewEach is the search of an extractor of kind, which reads at most
// maxBytes, between before and after, with no value yet, which Add puts
// in, and no file, which At gives it. Its time limit, the regex time of
// maxBytes, runs from now, so preparing the values counts against it as
// their searches do. A pattern that does not compile is refused when its
// search is due, with a *PatternError.
func NewEach(kind, before, after string, maxBytes int64) Each {
	return newEach(kind, before, after, regexTime(MaxBytes(maxBytes)))
}

// newEach is NewEach with the time limit given, which a test shortens.
func newEach(kind, before, after string, limit time.Duration) Each {
	return Each{kind: kind, before: before, after: after,
		deadline: time.Now().Add(limit), TimeLimit: limit}
}

// Add puts in the value that value gives, in RE2 syntax, once it has
// checked that the time limit has not passed: a value can cost a compile
// to check, and a file can give 100000 of them. A refusal of value is
// returned as it is.
func (e *Each) Add(value func() (string, error)) error {
	if time.Now().After(e.deadline) {
		return fmt.Errorf("%s: preparing a pattern for each value took more than %s, %s, and stopped after %d values",
			e.kind, e.TimeLimit, eachLimit, len(e.values))
	}
	v, err := value()
	if err != nil {
		return err
	}
	e.values = append(e.values, v)
	return nil
}

// At is e reading file, at most maxBytes of it, with the deadline e has: a
// file set gives each of its files the same Each, so they share the limit.
func (e Each) At(kind, file string, maxBytes int64) (Each, error) {
	src, err := CheckSource(kind, file, maxBytes)
	if err != nil {
		return Each{}, err
	}
	e.Source = src
	return e, nil
}

// Extract returns the values of every pattern in the file under root.
func (e Each) Extract(root string) ([]check.Value, error) {
	data, err := e.Read(root)
	if err != nil {
		return nil, err
	}
	return e.FromData(data)
}

// FromShared is FromData: the searches share their time through the
// deadline, and spend no other budget of a set.
func (e Each) FromShared(data []byte, _ *Spent) ([]check.Value, error) {
	return e.FromData(data)
}

// FromData searches data with the pattern of each value, each as
// Search.FromData does, with CRLF read as LF. The matches of all of them
// are held to MaxValues, and the bytes of their values to the file's
// limit, since two values can match the same text; both count the matches
// before the context drops any.
func (e Each) FromData(data []byte) ([]check.Value, error) {
	data = lineFeeds(data)
	var all []Match
	var held int64
	for i, v := range e.values {
		s, err := compileOnce(e.kind, EachPattern(e.before, v, e.after))
		if err != nil {
			return nil, &PatternError{Index: i, Err: err}
		}
		s.Source, s.TimeLimit, s.shared, s.whose = e.Source, e.TimeLimit, e.deadline, eachLimit
		matches, err := s.Matches(data)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if m[2] < 0 {
				continue
			}
			if all = append(all, m); len(all) > MaxValues {
				return nil, TooManyValues(e.File)
			}
			if held += int64(m[3] - m[2]); held > e.Limit {
				return nil, fmt.Errorf("%s: the values of every pattern hold more than %d bytes in all, the extractor's max_bytes; narrow the pattern or set max_bytes: higher",
					e.File, e.Limit)
			}
		}
	}
	slices.SortStableFunc(all, func(a, b Match) int { return cmp.Compare(a[0], b[0]) })
	if e.around != nil {
		var err error
		if all, err = e.around.keep(data, all, e.deadline, e.File, e.TimeLimit, eachLimit); err != nil {
			return nil, err
		}
	}
	l := NewLineCursor(data)
	out := make([]check.Value, 0, len(all))
	for _, m := range all {
		out = append(out, check.Value{
			Text: string(data[m[2]:m[3]]),
			At:   check.Location{File: e.File, Line: l.At(m[0])},
		})
	}
	return out, nil
}
