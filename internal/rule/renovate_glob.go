package rule

import (
	"context"

	"github.com/f-klement/errata/internal/minimatch"
)

// A rule calls renovate_match with the same few globs over and over: 200
// files against 10 entries of managerFilePatterns is 2000 calls of 10
// globs. Each call compiled its glob and paid renovateCompileWeight for
// each byte of it, so that shape ended unknown on the cost limit. The glob
// cache compiles a glob once in an evaluation and charges the compile once.
//
// The cache is a field of the evaluation's errorLog, which
// renovateThroughLog hands every call as a hidden argument. A new
// evaluation has a new log, so no compiled glob outlives the evaluation
// that compiled it.

// The bounds of one evaluation's glob cache. A compiled glob holds memory
// that grows with its Size, and a refused one the source its error keeps.
// Measured through the CI runner, the most for each unit was 61.7 bytes,
// for 200 segments of /**/1, and 256 small globs held 0.44 MiB, so a full
// cache holds about 3.9 MiB at most (TestGlobCacheIsBounded). A glob the
// cache has no room for is compiled and charged at every call, as every
// glob was before the cache.
const (
	maxCachedGlobs     = 256
	maxCachedGlobUnits = 1 << 16
)

// globCache is one evaluation's compiled globs, each with whether a call
// has paid for its compile yet. units is the sum of their Sizes.
type globCache struct {
	entries map[string]*cachedGlob
	units   int
}

type cachedGlob struct {
	glob    *minimatch.Glob
	charged bool
}

// compiled is pattern compiled under ctx, from the cache or now. A glob
// compiled now is kept while the cache has room for it, unless ctx was
// done by its end: a stopped compile holds no glob, and every later call
// of the evaluation stops anyway.
func (c *globCache) compiled(ctx context.Context, pattern string) *minimatch.Glob {
	if e, ok := c.entries[pattern]; ok {
		return e.glob
	}
	g := minimatch.CompileContext(ctx, pattern)
	if ctx.Err() == nil && len(c.entries) < maxCachedGlobs && c.units+g.Size() <= maxCachedGlobUnits {
		if c.entries == nil {
			c.entries = map[string]*cachedGlob{}
		}
		c.entries[pattern] = &cachedGlob{glob: g}
		c.units += g.Size()
	}
	return g
}

// pays is whether this call of pattern pays for compiling it: the first
// call of a glob the cache holds, and every call of one it does not.
func (c *globCache) pays(pattern string) bool {
	e, ok := c.entries[pattern]
	if !ok {
		return true
	}
	first := !e.charged
	e.charged = true
	return first
}

// globCache is the evaluation's glob cache.
func (l *errorLog) globCache() *globCache {
	return &l.globs
}
