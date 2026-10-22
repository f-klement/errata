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
	// One search that finds nothing: a counted repeat keeps a thousand
	// threads alive at each byte. The limit stops it inside the search.
	// Stopped 0.12 s, 0.17 s under -race; the search unclocked, with the
	// limit checked only between searches, 3.3 s, 58.6 s under -race;
	// bound 1 s.
	{Name: "one search of a counted repeat", Spec: `([ab]{0,1000}c)`,
		Body: func() string { return strings.Repeat("b", 64<<10) }, TimeLimit: 100 * time.Millisecond,
		Refused: "f: matching took more than 100ms, the limit, and stopped after 0 matches", Bound: time.Second},
}

// searchIn is the regex extractor of pattern over the file f.
func searchIn(pattern string) (check.Extractor, error) {
	return CompileSearch("regex", "f", pattern, 0)
}

func TestReplayMeasuredInputs(t *testing.T) {
	extracttest.RunReplays(t, searchIn, replays, false)
}
