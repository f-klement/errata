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
// refuses, which makes minimatch throw, is an error too.
package minimatch

import (
	"errors"
	"slices"
	"strconv"
	"unicode/utf8"
)

// MaxSteps is the most steps one Match may take: each try of a regex
// node, and each segment the globstar matcher compares.
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
}

// Match is whether minimatch matches file with glob under dot and nocase,
// as new Minimatch(glob, {dot: true, nocase: true}).match(file) answers,
// or an error where the port does not compute that answer.
func Match(file, glob string) (bool, error) {
	if !utf8.ValidString(file) || !utf8.ValidString(glob) {
		return false, errors.New("a string that is not UTF-8")
	}
	m, err := compile(toUnits(glob))
	if err != nil {
		return false, err
	}
	return m.match(toUnits(file))
}

func compile(pattern units) (*mm, error) {
	m := &mm{steps: &budget{left: MaxSteps}}
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
	expanded, err := braceExpand(pattern)
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
	for _, g := range globSet {
		var segs []segment
		for _, s := range levelOneOptimize(slashSplit(g)) {
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
