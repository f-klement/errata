// Package minimatch is a port of minimatch 10.2.6, the glob matcher
// Renovate reads a glob in managerFilePatterns and its other string pattern
// keys with, for the options Renovate passes, dot and nocase. It follows
// the JavaScript step for step, its quirks included, with brace-expansion
// 5.0.12 and balanced-match 4.0.4 for the braces, and a matcher of its own
// for the JavaScript regexes minimatch builds (regexp.go). Its case mapping
// and Unicode properties are Node's (unicode_node.go). The cases in
// testdata, each with the answer minimatch itself gave, hold it to that.
//
// Four things make Match return an error where minimatch would answer, so
// that one glob cannot run on without bound: braces that expand past
// MaxExpansions globs, a segment of negations whose tree passes maxNodes,
// segments whose regex sources pass maxSource in all, and a match that
// takes more than MaxSteps steps. A glob whose regex JavaScript
// refuses, which makes minimatch throw, is an error too. CompileContext
// and MatchSteps also stop, with an error, once their context is done.
package minimatch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"
)

// MaxSteps is the most steps one Match may take: each try of a regex
// node, and each segment the globstar matcher compares. A class test
// counts more steps for a class that takes it more work (classWork).
const MaxSteps = 1 << 17

// maxSource is the most regex source the segments of one glob may build
// in all, its brace expansions' included.
const maxSource = 1 << 15

var errSource = errors.New("its segments build more than " + strconv.Itoa(maxSource) + " characters of regex source")

// globstar stands for a segment that is exactly **.
type segment struct {
	globstar bool
	literal  units
	re       *regex
	fast     func(units) bool
}

// The patterns minimatch tests a segment with first, and the tests it puts
// in place of the segment's regex when one matches, for dot and nocase.
func fastTest(seg units) func(units) bool {
	allOf := func(s units, c rune) bool { return !slices.ContainsFunc(s, func(x rune) bool { return x != c }) }
	extChar := func(c rune) bool { return !slices.Contains([]rune(`+@!?*[(`), c) }
	notDots := func(f units) bool {
		return !slices.Equal(f, units{'.'}) && !slices.Equal(f, units{'.', '.'})
	}
	stars := 0
	for stars < len(seg) && seg[stars] == '*' {
		stars++
	}
	// starRE: ^\*+$
	if stars > 0 && stars == len(seg) {
		return func(f units) bool { return len(f) != 0 && notDots(f) }
	}
	// starDotExtRE: ^\*+([^+@!?*[(]*)$
	if stars > 0 && !slices.ContainsFunc(seg[stars:], func(c rune) bool { return !extChar(c) }) {
		ext := jsLower(seg[stars:])
		return func(f units) bool { return hasSuffix(jsLower(f), ext) }
	}
	// qmarksRE: ^\?+([^+@!?*[(]*)?$
	q := 0
	for q < len(seg) && seg[q] == '?' {
		q++
	}
	if q > 0 && !slices.ContainsFunc(seg[q:], func(c rune) bool { return !extChar(c) }) {
		n := len(seg)
		ext := jsLower(seg[q:])
		return func(f units) bool {
			return len(f) == n && notDots(f) && (len(ext) == 0 || hasSuffix(jsLower(f), ext))
		}
	}
	// starDotStarRE: ^\*+\.\*+$
	if stars > 0 && stars+1 < len(seg) && seg[stars] == '.' && allOf(seg[stars+1:], '*') {
		return func(f units) bool { return notDots(f) && slices.Contains(f, '.') }
	}
	// dotStarRE: ^\.\*+$
	if len(seg) > 1 && seg[0] == '.' && allOf(seg[1:], '*') {
		return func(f units) bool { return notDots(f) && len(f) > 0 && f[0] == '.' }
	}
	return nil
}

func hasSuffix(s, suffix units) bool {
	return len(s) >= len(suffix) && slices.Equal(s[len(s)-len(suffix):], suffix)
}

// mm is a parsed glob: minimatch's Minimatch.
type mm struct {
	set     [][]segment
	negate  bool
	comment bool
	empty   bool
	steps   *budget
	// source is what the glob's segments have spent of maxSource.
	source sourceBudget
}

// sourceBudget is the regex source one glob's segments may build: left is
// what maxSource leaves for their regexes, and written counts every unit
// their builds appended, refused ones included, which a test holds to the
// bound.
type sourceBudget struct {
	left    int
	written int
	// ctx, when set, stops the build once it is done.
	ctx context.Context
}

// Match is whether minimatch matches file with glob under dot and nocase,
// as new Minimatch(glob, {dot: true, nocase: true}).match(file) answers,
// or an error where the port does not compute that answer:
// CompileContext then MatchSteps, under a context that is never done.
func Match(file, glob string) (bool, error) {
	ctx := context.Background()
	match, _, err := CompileContext(ctx, glob).MatchSteps(ctx, file)
	return match, err
}

// Glob is a glob compiled once, to match many strings: new Minimatch(glob,
// {dot: true, nocase: true}), or why the port does not compile it.
type Glob struct {
	m   *mm
	err error
}

var errUTF8 = errors.New("a string that is not UTF-8")

// CompileContext compiles glob as Match does, stopped once ctx is done: it
// looks at ctx at each brace set it expands, before it parses each
// segment, and at each node of a segment's tree as it builds the regex
// source. A glob Match refuses, a stopped compile included, is a Glob
// whose MatchSteps returns that error, after it has refused a file that is
// not UTF-8, as Match would.
func CompileContext(ctx context.Context, glob string) *Glob {
	if !utf8.ValidString(glob) {
		return &Glob{err: errUTF8}
	}
	m, err := compileContext(ctx, toUnits(glob))
	if err != nil {
		return &Glob{err: err}
	}
	return &Glob{m: m}
}

// MatchSteps is Match(file, glob) for the glob g was compiled from, and
// the steps the match took, at most MaxSteps: what a caller charges a
// match by, since a glob can spend every step against a string of two
// bytes. Each call has its own MaxSteps, and stops once ctx is done,
// whatever context g was compiled under: the match looks at ctx every
// checkEvery steps. A stopped call's error wraps ctx.Err().
func (g *Glob) MatchSteps(ctx context.Context, file string) (bool, int, error) {
	if !utf8.ValidString(file) {
		return false, 0, errUTF8
	}
	if g.err != nil {
		return false, 0, g.err
	}
	m := *g.m
	m.steps = &budget{left: MaxSteps, ctx: ctx}
	match, err := m.match(toUnits(file))
	return match, MaxSteps - m.steps.left, err
}

// Size is the code units g holds, its segments' literals and the sources
// of their regexes, and one for each segment, which holds memory of its
// own. A glob the port refuses holds its error, and a regex JavaScript
// refuses keeps the whole source the segment built, up to maxSource; Size
// counts its bytes. What a compiled glob keeps in memory grows with it.
func (g *Glob) Size() int {
	if g.err != nil {
		var refused *SyntaxError
		if errors.As(g.err, &refused) {
			return len(refused.Source)
		}
		return 0
	}
	n := 0
	for _, segs := range g.m.set {
		for _, s := range segs {
			n += 1 + len(s.literal)
			if s.re != nil {
				n += len(s.re.source)
			}
		}
	}
	return n
}

// stopped is the error of a call whose context is done, or nil.
func stopped(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the match was stopped: %w", err)
	}
	return nil
}

func compile(pattern units) (*mm, error) {
	return compileContext(context.Background(), pattern)
}

// compileContext is compile, stopped once ctx is done; the mm it returns
// stops its match then too.
func compileContext(ctx context.Context, pattern units) (*mm, error) {
	m := &mm{steps: &budget{left: MaxSteps, ctx: ctx}}
	if len(pattern) > 0 && pattern[0] == '#' {
		m.comment = true
		return m, nil
	}
	if len(pattern) == 0 {
		m.empty = true
		return m, nil
	}
	// parseNegate: each leading ! flips the match.
	i := 0
	for i < len(pattern) && pattern[i] == '!' {
		m.negate = !m.negate
		i++
	}
	pattern = pattern[i:]
	expanded, err := braceExpandContext(ctx, pattern)
	if err != nil {
		return nil, err
	}
	var globSet []units
	for _, g := range expanded {
		if !slices.ContainsFunc(globSet, func(x units) bool { return slices.Equal(x, g) }) {
			globSet = append(globSet, g)
		}
	}
	m.source.left = maxSource
	m.source.ctx = ctx
	for _, g := range globSet {
		var segs []segment
		for _, s := range levelOneOptimize(slashSplit(g)) {
			if err := stopped(ctx); err != nil {
				return m, err
			}
			seg, err := parseSegment(s, &m.source)
			if err != nil {
				// m is returned for what it spent, which a test reads.
				return m, err
			}
			segs = append(segs, seg)
		}
		m.set = append(m.set, segs)
	}
	return m, nil
}

// slashSplit is p.split(/\/+/).
func slashSplit(p units) []units {
	var out []units
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] != '/' {
			continue
		}
		out = append(out, p[start:i])
		for i+1 < len(p) && p[i+1] == '/' {
			i++
		}
		start = i + 1
	}
	return append(out, p[start:])
}

// levelOneOptimize drops a ** after a ** and resolves a .. after a segment
// that is no .., . or **.
func levelOneOptimize(parts []units) []units {
	var out []units
	isAny := func(p units, s string) bool { return slices.Equal(p, []rune(s)) }
	for _, p := range parts {
		if len(out) > 0 {
			prev := out[len(out)-1]
			if isAny(p, "**") && isAny(prev, "**") {
				continue
			}
			if isAny(p, "..") && len(prev) > 0 && !isAny(prev, "..") && !isAny(prev, ".") && !isAny(prev, "**") {
				out = out[:len(out)-1]
				continue
			}
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return []units{{}}
	}
	return out
}

// parseSegment is Minimatch.parse.
func parseSegment(s units, sb *sourceBudget) (segment, error) {
	if slices.Equal(s, units{'*', '*'}) {
		return segment{globstar: true}, nil
	}
	if len(s) == 0 {
		return segment{literal: units{}}, nil
	}
	fast := fastTest(s)
	p, err := fromGlob(s).toMMPattern(sb)
	if err != nil {
		return segment{}, err
	}
	if p.re == nil {
		return segment{literal: p.literal}, nil
	}
	return segment{re: p.re, fast: fast}, nil
}

// test is whether the segment matches the file's segment f.
func (m *mm) test(p segment, f units) (bool, error) {
	if !m.steps.step() {
		return false, m.steps.err
	}
	switch {
	case p.re == nil:
		return slices.Equal(f, p.literal), nil
	case p.fast != nil:
		return p.fast(f), nil
	}
	return p.re.test(f, m.steps)
}

// match is Minimatch.match without partial, matchBase or flipNegate.
func (m *mm) match(f units) (bool, error) {
	if m.comment {
		return false, nil
	}
	if m.empty {
		return len(f) == 0, nil
	}
	ff := slashSplit(f)
	for _, pattern := range m.set {
		hit, err := m.matchOne(ff, pattern)
		if err != nil {
			return false, err
		}
		if hit {
			return !m.negate, nil
		}
	}
	return m.negate, nil
}

func (m *mm) matchOne(file []units, pattern []segment) (bool, error) {
	if slices.ContainsFunc(pattern, func(s segment) bool { return s.globstar }) {
		return m.matchGlobstar(file, pattern)
	}
	return m.matchParts(file, pattern, 0)
}

// maxGlobstarRecursion is minimatch's default: past it a body section is
// not tried, which minimatch accepts as a false negative.
const maxGlobstarRecursion = 200

func isDots(f units) bool {
	return slices.Equal(f, units{'.'}) || slices.Equal(f, units{'.', '.'})
}

func (m *mm) matchGlobstar(file []units, pattern []segment) (bool, error) {
	firstgs := slices.IndexFunc(pattern, func(s segment) bool { return s.globstar })
	lastgs := firstgs
	for i := len(pattern) - 1; i >= 0; i-- {
		if pattern[i].globstar {
			lastgs = i
			break
		}
	}
	head, tail := pattern[:firstgs], pattern[lastgs+1:]
	var body []segment
	if lastgs > firstgs {
		body = pattern[firstgs+1 : lastgs]
	}
	fileIndex := 0
	if len(head) > 0 {
		end := min(len(head), len(file))
		ok, err := m.matchParts(file[:end], head, 0)
		if err != nil || !ok {
			return false, err
		}
		fileIndex += len(head)
	}
	fileTailMatch := 0
	if len(tail) > 0 {
		if len(tail)+fileIndex > len(file) {
			return false, nil
		}
		tailStart := len(file) - len(tail)
		ok, err := m.matchParts(file, tail, tailStart)
		if err != nil {
			return false, err
		}
		if ok {
			fileTailMatch = len(tail)
		} else {
			if len(file[len(file)-1]) != 0 || fileIndex+len(tail) == len(file) {
				return false, nil
			}
			tailStart--
			ok, err := m.matchParts(file, tail, tailStart)
			if err != nil || !ok {
				return false, err
			}
			fileTailMatch = len(tail) + 1
		}
	}
	if len(body) == 0 {
		sawSome := fileTailMatch > 0
		for i := fileIndex; i < len(file)-fileTailMatch; i++ {
			sawSome = true
			if isDots(file[i]) {
				return false, nil
			}
		}
		return sawSome, nil
	}
	type section struct {
		parts []segment
		after int
	}
	sections := []*section{{}}
	cur := sections[0]
	nonGs := 0
	sums := []int{0}
	for _, b := range body {
		if b.globstar {
			sums = append(sums, nonGs)
			cur = &section{}
			sections = append(sections, cur)
		} else {
			cur.parts = append(cur.parts, b)
			nonGs++
		}
	}
	i := len(sections) - 1
	fileLength := len(file) - fileTailMatch
	for _, s := range sections {
		s.after = fileLength - (sums[i] + len(s.parts))
		i--
	}
	var walk func(fileIndex, bodyIndex, depth int, sawTail bool) (int8, error)
	// walk returns 1 for a match, 0 for none, and -1 for none and no use
	// in trying further, minimatch's true, false and null.
	walk = func(fileIndex, bodyIndex, depth int, sawTail bool) (int8, error) {
		if bodyIndex == len(sections) {
			for i := fileIndex; i < len(file); i++ {
				sawTail = true
				if isDots(file[i]) {
					return 0, nil
				}
			}
			return boolInt(sawTail), nil
		}
		s := sections[bodyIndex]
		for fileIndex <= s.after {
			end := min(fileIndex+len(s.parts), len(file))
			hit, err := m.matchParts(file[:end], s.parts, fileIndex)
			if err != nil {
				return 0, err
			}
			if hit && depth < maxGlobstarRecursion {
				sub, err := walk(fileIndex+len(s.parts), bodyIndex+1, depth+1, sawTail)
				if err != nil {
					return 0, err
				}
				if sub != 0 {
					return sub, nil
				}
			}
			if fileIndex < len(file) && isDots(file[fileIndex]) {
				return 0, nil
			}
			fileIndex++
		}
		return -1, nil
	}
	r, err := walk(fileIndex, 0, 0, fileTailMatch > 0)
	return r == 1, err
}

// matchParts is minimatch's #matchOne without partial.
func (m *mm) matchParts(file []units, pattern []segment, fi int) (bool, error) {
	fl, pl := len(file), len(pattern)
	pi := 0
	for ; fi < fl && pi < pl; fi, pi = fi+1, pi+1 {
		p := pattern[pi]
		if p.globstar {
			return false, nil
		}
		hit, err := m.test(p, file[fi])
		if err != nil || !hit {
			return false, err
		}
	}
	switch {
	case fi == fl && pi == pl:
		return true, nil
	case fi == fl:
		return false, nil
	}
	return fi == fl-1 && len(file[fi]) == 0, nil
}
