package core

import (
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The regex inputs that took quadratic time, each a pattern searched in a
// file named f. Each stops on the clock, so Gremlins runs them too: the
// clock's counter is a mutant only they kill.
var replays = []extracttest.Replay[string]{
	// A regex whose optional tail scans the line searches it again after
	// every match. The time limit, 100 ms here, stops it. Linear 0.10 s,
	// 0.11 s under -race; quadratic 4.59 s; bound 1.5 s.
	{Name: "regex whose tail scans the line", Spec: `(\d)(?:.*x)?`,
		Body: func() string { return strings.Repeat("1 ", 10000) }, TimeLimit: 100 * time.Millisecond,
		Refused: "f: matching took more than 100ms, the limit, and stopped after", Bound: 1500 * time.Millisecond},
	// Linear 0.10 s, 0.12 s under -race;
	// quadratic 5.87 s; bound 1.5 s.
	{Name: "the same with (?s) over many lines", Spec: `(?s)(\d)(?:.*x)?`,
		Body: func() string { return strings.Repeat("1\n", 10000) }, TimeLimit: 100 * time.Millisecond,
		Refused: "f: matching took more than 100ms, the limit, and stopped after", Bound: 1500 * time.Millisecond},
	// One search that finds its one match at the very end: a counted
	// repeat keeps a thousand threads alive at each byte. The c the pattern
	// needs ends the line, so the search runs over the whole line rather
	// than being skipped (findNear). The limit stops it inside the search.
	// Stopped 0.12 s, 0.17 s under -race; the search unclocked, with the
	// limit checked only between searches, 3.3 s, 58.6 s under -race;
	// bound 1 s.
	{Name: "one search of a counted repeat", Spec: `([ab]{0,1000}c)`,
		Body: func() string { return strings.Repeat("b", 64<<10) + "c" }, TimeLimit: 100 * time.Millisecond,
		Refused: "f: matching took more than 100ms, the limit, and stopped after 0 matches", Bound: time.Second},
	// A pattern that starts with a literal: the prefix scan finds the
	// places it starts, and the try at the first stops after the most b
	// the counted repeat takes, so the rest are never searched. A search
	// of every place reads the 32 MiB of b through the clock in about
	// 0.8 s on the build node, eight times the limit, and is refused.
	// Read 0.05 s, 0.46 s under -race, most of it writing and reading the
	// file; bound 1 s.
	{Name: "a literal before a run it does not match", Spec: `x([ab]{0,1000}c)`,
		Body: func() string { return "x" + strings.Repeat("b", 32<<20-8) + "xac" }, TimeLimit: 100 * time.Millisecond,
		Values: 1, Last: "ac", Bound: time.Second},
	// The literal starts at every byte. The try at the first a reads to
	// the c after it, and find searches on unanchored from the next a,
	// reading the rest once; a try at every a would read each run once
	// for each of its a. The limit is 2 s here, since -race and coverage
	// under make test slowed the linear read to 0.6 s at four times the
	// size. Linear 0.01 s, 0.08 s under -race; a try at every a about 9 s
	// (37 s at four times the size); bound 2 s.
	{Name: "a literal at every byte", Spec: `a(a*b)`,
		Body: func() string { return strings.Repeat(strings.Repeat("a", 2000)+"c", 32) + "ab" }, TimeLimit: 2 * time.Second,
		Values: 1, Last: "b", Bound: 2 * time.Second},
	// Tries far apart, each of which keeps a thousand threads alive, since
	// [ab]* leaves the counted repeat to start at any b: the clock counts
	// the runes of every try, and the deadline stops the run of them.
	// Stopped 0.11 s, 0.19 s under -race; a try that ignores the stopped
	// clock, 6.3 s; bound 1 s.
	{Name: "tries of a counted repeat", Spec: `x([ab]*[ab]{0,1000}c)`,
		Body:      func() string { return strings.Repeat("x"+strings.Repeat("b", 1000)+strings.Repeat(" ", 100), 2000) },
		TimeLimit: 100 * time.Millisecond,
		Refused:   "f: matching took more than 100ms, the limit, and stopped after 0 matches", Bound: time.Second},
	// Many matches on one long line, the shape of a minified bundle: the
	// search with a seeker scanned the line for its bounds once for each
	// match, off the clock, so 20164 dashes on one line of 256 KiB took
	// 2.37 s, and 80659 on one of 1 MiB 37 s, past its own 10 s limit. It
	// now scans each line once. On the build node: linear 0.024 s, 0.32 s
	// under -race; quadratic 0.79 s at the fastest of three; bound 0.25 s.
	// Under make cover's -race and coverage on a loaded node it took more
	// than 1 s, so it runs in make slow, and a tenth of it in make test.
	{Name: "many matches on one long line", Spec: `([\x{2013}\x{2014}])`,
		Body:   func() string { return strings.Repeat("abcdefghij\u2014", 20164) },
		Values: 20164, Last: "\u2014", Bound: 250 * time.Millisecond, Full: true},
	{Name: "many matches on one long line, a tenth", Spec: `([\x{2013}\x{2014}])`,
		Body:   func() string { return strings.Repeat("abcdefghij\u2014", 2016) },
		Values: 2016, Last: "\u2014"},
}

// searchIn is the regex extractor of pattern over the file f.
func searchIn(pattern string) (check.Extractor, error) {
	return CompileSearchAt("regex", sourceOf("f"), pattern)
}

func TestReplayMeasuredInputs(t *testing.T) {
	extracttest.RunReplays(t, searchIn, replays, false)
}

// The full cases, at the sizes their times were measured at.
func TestReplayMeasuredInputsAtFullSize(t *testing.T) {
	slowOnly(t)
	extracttest.RunReplays(t, searchIn, replays, true)
}
