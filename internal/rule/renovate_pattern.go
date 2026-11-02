package rule

import (
	"context"
	"fmt"
)

// An entry renovate_match reads is one of three forms, and each has an
// adapter that compiles it and matches a string against it: * matches
// every string, a regex between slashes runs in Go's RE2
// (renovate_regex.go), and a glob in the port of minimatch
// (renovate_glob.go). compilePattern is the one place that tells the forms
// apart; everything after it, the cache, the call and its price, reads a
// pattern.

// pattern is an entry compiled by its adapter, or why the adapter refuses
// it, which match then returns.
type pattern interface {
	// match is whether s matches the entry under ctx, the work the match
	// took, and a *refusal where errata cannot match s as Renovate does,
	// or an error that wraps ctx's once ctx is done.
	match(ctx context.Context, s string) (bool, matchWork, error)
	// size is the units the pattern holds in memory, which bound the
	// cache: what a pattern keeps grows with it.
	size() int
	// compileCost is what its compile costs, which the call that compiled
	// it pays (ADR 0014, The cost).
	compileCost() uint64
}

// matchWork is what one call's pattern did, which renovateWork prices:
// whether the call compiled its entry, what that compile costs, the steps
// a glob's match took, and the instructions of the regex program that
// ran.
type matchWork struct {
	compiled    bool
	compileCost uint64
	steps       int
	insts       int
}

// compilePattern is entry compiled under ctx by the adapter of its form.
func compilePattern(ctx context.Context, entry string) pattern {
	switch {
	case entry == "*":
		return everyString{}
	case isRegex(entry):
		return compileRegex(entry)
	}
	return compileGlob(ctx, entry)
}

// everyString is the entry *, which Renovate matches with every string
// before it reads anything.
type everyString struct{}

func (everyString) match(context.Context, string) (bool, matchWork, error) {
	return true, matchWork{}, nil
}

func (everyString) size() int { return 0 }

// compileCost is 0: * compiles nothing.
func (everyString) compileCost() uint64 { return 0 }

// refusal is an entry that errata does not match against s as Renovate
// would, from either adapter. Both give it the same form in the rule's
// unknown message: the form of the entry, the entry, the string, and the
// adapter's reason.
type refusal struct {
	form, entry, s string
	reason         error
}

func (r *refusal) Error() string {
	return fmt.Sprintf("the %s %q is not matched against %q: %v", r.form, r.entry, r.s, r.reason)
}

func (r *refusal) Unwrap() error { return r.reason }

// A rule calls renovate_match with the same few entries over and over:
// 200 files against 10 entries of managerFilePatterns is 2000 calls of 10
// entries. Each call compiled its glob and paid renovateCompileWeight for
// each byte of it, so that shape ended unknown on the cost limit. The
// cache compiles an entry once in an evaluation, and its compile is
// charged once.
//
// The cache is a field of the evaluation's errorLog, which
// renovateThroughLog hands every call as a hidden argument. A new
// evaluation has a new log, so no compiled entry outlives the evaluation
// that compiled it.

// The bounds of one evaluation's cache. A compiled pattern holds memory
// that grows with its size, and a refused one what its error keeps.
// Measured through the CI runner, the most for each unit was 61.7 bytes
// for a glob, 200 segments of /**/1, and 46 for a regex, an anchored one
// of 124 empty captures; 256 small globs held 0.44 MiB and 256 small
// regexes 0.58 MiB. So a full cache holds about 3.9 MiB at most, whatever
// its mix (TestGlobCacheIsBounded, TestPatternCacheBoundsRegexes). An
// entry the cache has no room for is compiled and charged at every call,
// as every entry was before the cache.
const (
	maxCachedPatterns = 256
	maxCachedUnits    = 1 << 16
)

// patternCache is one evaluation's compiled entries. units is the sum of
// their sizes. A nil cache holds nothing: every call compiles its entry.
type patternCache struct {
	entries map[string]pattern
	units   int
}

// match is s matched against entry under ctx, with the work it took.
// These are the only calls of a pattern's compile and match, so the work a
// call is charged for is the work it read here.
func (c *patternCache) match(ctx context.Context, s, entry string) (bool, matchWork, error) {
	p, compiled := c.compiled(ctx, entry)
	match, work, err := p.match(ctx, s)
	if compiled {
		work.compiled, work.compileCost = true, p.compileCost()
	}
	return match, work, err
}

// compiled is entry compiled under ctx, from the cache or now, and whether
// it was compiled now, which the call pays for. An entry compiled now is
// kept while the cache has room for it, unless ctx was done by its end: a
// stopped compile holds no glob, and every later call of the evaluation
// stops anyway. So an entry the cache holds is compiled, and paid for, by
// the first call of it, and one it has no room for by every call.
func (c *patternCache) compiled(ctx context.Context, entry string) (pattern, bool) {
	if c == nil {
		return compilePattern(ctx, entry), true
	}
	if p, ok := c.entries[entry]; ok {
		return p, false
	}
	p := compilePattern(ctx, entry)
	if ctx.Err() == nil && len(c.entries) < maxCachedPatterns && c.units+p.size() <= maxCachedUnits {
		if c.entries == nil {
			c.entries = map[string]pattern{}
		}
		c.entries[entry] = p
		c.units += p.size()
	}
	return p, true
}

// patternCache is the evaluation's cache of compiled entries.
func (l *errorLog) patternCache() *patternCache {
	return &l.patterns
}
