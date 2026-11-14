package minimatch

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// minimatch turns each glob segment into a JavaScript regex, with
// lookaheads RE2 lacks, so the port runs those regexes with a backtracking
// matcher of its own: the part of ECMAScript's RegExp the generated sources
// use (alternation, groups, lookaheads, greedy and lazy quantifiers, classes
// with ranges, escapes and \p{..}, and the i and u flags), read and matched
// by the specification's rules. A source that is no valid ECMAScript regex
// for its flags is an error, as new RegExp throws. Every match counts its
// steps against the meter of the Match call, so no glob runs on without
// bound.

// regex is a compiled JavaScript regex.
type regex struct {
	root   matcher
	icase  bool
	uflag  bool
	source units
}

// matcher matches at pos and calls k with each end it can reach, in the
// order the specification tries them, until k returns true.
type matcher func(m *machine, pos int, k func(int) bool) bool

// machine is one search: the input, the flags and the meter it spends
// steps from.
type machine struct {
	in    units
	icase bool
	uflag bool
	steps *meter
}

// SyntaxError is a source new RegExp refuses.
type SyntaxError struct {
	Source string
	Flags  string
	Reason string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("invalid regular expression /%s/%s: %s", e.Source, e.Flags, e.Reason)
}

// compileJS compiles src with the flags i and u as given.
func compileJS(src units, icase, uflag bool) (*regex, error) {
	p := &parser{src: src, icase: icase, uflag: uflag}
	root, err := p.disjunction()
	if err == nil && p.pos < len(p.src) {
		err = p.fail("unmatched ')'")
	}
	if err != nil {
		var se *SyntaxError
		if errors.As(err, &se) {
			flags := ""
			if icase {
				flags += "i"
			}
			if uflag {
				flags += "u"
			}
			se.Source, se.Flags = fromUnits(src), flags
		}
		return nil, err
	}
	return &regex{root: root, icase: icase, uflag: uflag, source: src}, nil
}

// test is RegExp.prototype.test from lastIndex 0: a match starting at any
// place in s.
func (r *regex) test(s units, steps *meter) (bool, error) {
	m := &machine{in: s, icase: r.icase, uflag: r.uflag, steps: steps}
	accept := func(int) bool { return true }
	for start := 0; start <= len(s); {
		if r.root(m, start, accept) {
			return true, nil
		}
		if steps.err != nil {
			return false, steps.err
		}
		if start == len(s) {
			break
		}
		_, w := m.char(start)
		start += w
	}
	return false, steps.err
}

// char is the character at pos and its width in code units: a surrogate
// pair is one character under the u flag.
func (m *machine) char(pos int) (rune, int) {
	r := m.in[pos]
	if m.uflag && r >= 0xD800 && r < 0xDC00 && pos+1 < len(m.in) && m.in[pos+1] >= 0xDC00 && m.in[pos+1] <= 0xDFFF {
		return 0x10000 + (r-0xD800)<<10 + (m.in[pos+1] - 0xDC00), 2
	}
	return r, 1
}

// parser reads a source by ECMAScript's pattern grammar, with Annex B's
// extensions when the u flag is off.
type parser struct {
	src   units
	pos   int
	icase bool
	uflag bool
}

func (p *parser) fail(reason string) error { return &SyntaxError{Reason: reason} }

func (p *parser) peek() rune {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return -1
}

func (p *parser) at(s string) bool {
	for i, c := range s {
		if p.pos+i >= len(p.src) || p.src[p.pos+i] != c {
			return false
		}
	}
	return true
}

// next reads one pattern character: a surrogate pair is one under u.
func (p *parser) next() rune {
	r := p.src[p.pos]
	p.pos++
	if p.uflag && r >= 0xD800 && r < 0xDC00 && p.pos < len(p.src) && p.src[p.pos] >= 0xDC00 && p.src[p.pos] <= 0xDFFF {
		r = 0x10000 + (r-0xD800)<<10 + (p.src[p.pos] - 0xDC00)
		p.pos++
	}
	return r
}

func (p *parser) disjunction() (matcher, error) {
	var alts []matcher
	for {
		a, err := p.alternative()
		if err != nil {
			return nil, err
		}
		alts = append(alts, a)
		if p.peek() != '|' {
			break
		}
		p.pos++
	}
	if len(alts) == 1 {
		return alts[0], nil
	}
	// Each alternative tried costs a step: an empty one spends none of its
	// own, and x@(|)@(|)... would otherwise try 2^n paths for free.
	return func(m *machine, pos int, k func(int) bool) bool {
		for _, a := range alts {
			if !m.steps.step() {
				return false
			}
			if a(m, pos, k) {
				return true
			}
			if m.steps.err != nil {
				return false
			}
		}
		return false
	}, nil
}

func (p *parser) alternative() (matcher, error) {
	var terms []matcher
	for p.pos < len(p.src) && p.peek() != '|' && p.peek() != ')' {
		t, err := p.term()
		if err != nil {
			return nil, err
		}
		terms = append(terms, t)
	}
	return sequence(terms), nil
}

// sequence matches terms one after another.
func sequence(terms []matcher) matcher {
	switch len(terms) {
	case 0:
		return func(_ *machine, pos int, k func(int) bool) bool { return k(pos) }
	case 1:
		return terms[0]
	}
	first, rest := terms[0], sequence(terms[1:])
	return func(m *machine, pos int, k func(int) bool) bool {
		return first(m, pos, func(p int) bool { return rest(m, p, k) })
	}
}

// The reasons of the refusals the parser gives more than once.
const (
	failNothingToRepeat = "nothing to repeat"
	failPropertyName    = "invalid property name"
	failUnicodeEscape   = "invalid unicode escape"
)

func (p *parser) term() (matcher, error) {
	if m, ok, err := p.assertion(); ok || err != nil {
		return m, err
	}
	atom, err := p.atom()
	if err != nil {
		return nil, err
	}
	return p.quantified(atom)
}

// assertion reads ^, $, \b, \B or a lookahead, or reports false for none.
func (p *parser) assertion() (matcher, bool, error) {
	switch {
	case p.peek() == '^':
		p.pos++
		return atStart, true, nil
	case p.peek() == '$':
		p.pos++
		return atEnd, true, nil
	case p.at(`\b`) || p.at(`\B`):
		return p.boundary(), true, nil
	case p.at("(?=") || p.at("(?!"):
		m, err := p.lookaheadTerm()
		return m, true, err
	}
	return nil, false, nil
}

// atStart is ^, which holds before the first character.
func atStart(_ *machine, pos int, k func(int) bool) bool { return pos == 0 && k(pos) }

// atEnd is $, which holds after the last character.
func atEnd(m *machine, pos int, k func(int) bool) bool { return pos == len(m.in) && k(pos) }

// boundary reads \b or \B.
func (p *parser) boundary() matcher {
	p.pos += 2
	want := p.src[p.pos-1] == 'b'
	return func(m *machine, pos int, k func(int) bool) bool {
		return m.wordBoundary(pos) == want && k(pos)
	}
}

// lookaheadTerm reads (?=..) or (?!..). Annex B lets a lookahead take a
// quantifier without u; with u one is refused.
func (p *parser) lookaheadTerm() (matcher, error) {
	negate := p.src[p.pos+2] == '!'
	p.pos += 3
	body, err := p.groupBody()
	if err != nil {
		return nil, err
	}
	look := lookahead(body, negate)
	if !p.uflag {
		return p.quantified(look)
	}
	if p.quantifierNext() {
		return nil, p.fail(failNothingToRepeat)
	}
	return look, nil
}

// groupBody reads a group's alternatives up to the ) that closes it.
func (p *parser) groupBody() (matcher, error) {
	body, err := p.disjunction()
	if err != nil {
		return nil, err
	}
	if p.peek() != ')' {
		return nil, p.fail("unterminated group")
	}
	p.pos++
	return body, nil
}

// quantifierNext reports whether a quantifier starts at the parser's
// place.
func (p *parser) quantifierNext() bool {
	q := p.peek()
	return q == '*' || q == '+' || q == '?' || (q == '{' && p.braceQuantifier())
}

// lookahead is (?=body) or, with negate, (?!body). Neither is tried again
// once it has decided, as the specification has it.
func lookahead(body matcher, negate bool) matcher {
	return func(m *machine, pos int, k func(int) bool) bool {
		if !m.steps.step() {
			return false
		}
		hit := body(m, pos, func(int) bool { return true })
		if m.steps.err != nil {
			return false
		}
		if hit == negate {
			return false
		}
		return k(pos)
	}
}

// braceQuantifier is whether a {n}, {n,} or {n,m} starts at the parser's
// place.
func (p *parser) braceQuantifier() bool {
	save := p.pos
	defer func() { p.pos = save }()
	_, _, ok := p.readBraces()
	return ok
}

// readBraces reads {n}, {n,} or {n,m}; max is -1 for no bound.
func (p *parser) readBraces() (lo, hi int, ok bool) {
	if p.peek() != '{' {
		return 0, 0, false
	}
	p.pos++
	lo, ok = p.digits()
	if !ok {
		return 0, 0, false
	}
	hi = lo
	if p.peek() == ',' {
		p.pos++
		hi = -1
		if d, ok := p.digits(); ok {
			hi = d
		}
	}
	if p.peek() != '}' {
		return 0, 0, false
	}
	p.pos++
	return lo, hi, true
}

func (p *parser) digits() (int, bool) {
	start := p.pos
	for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
		p.pos++
	}
	if p.pos == start {
		return 0, false
	}
	n, err := strconv.Atoi(string(p.src[start:p.pos]))
	if err != nil {
		n = 1 << 30
	}
	return n, true
}

// quantified reads a quantifier after atom, if one follows.
func (p *parser) quantified(atom matcher) (matcher, error) {
	lo, hi, ok, err := p.bounds()
	if err != nil {
		return nil, err
	}
	if !ok {
		return atom, nil
	}
	greedy := true
	if p.peek() == '?' {
		p.pos++
		greedy = false
	}
	return repeat(atom, lo, hi, greedy), nil
}

// bounds reads a quantifier's bounds, hi -1 for none, or reports false
// when no quantifier follows.
func (p *parser) bounds() (lo, hi int, ok bool, err error) {
	switch p.peek() {
	case '*':
		p.pos++
		return 0, -1, true, nil
	case '+':
		p.pos++
		return 1, -1, true, nil
	case '?':
		p.pos++
		return 0, 1, true, nil
	case '{':
		return p.braceBounds()
	}
	return 0, 0, false, nil
}

// braceBounds reads {n}, {n,} or {n,m} as a quantifier, or leaves the
// parser where it was when none starts there.
func (p *parser) braceBounds() (lo, hi int, ok bool, err error) {
	save := p.pos
	if lo, hi, ok = p.readBraces(); !ok {
		p.pos = save
		return 0, 0, false, nil
	}
	if hi >= 0 && hi < lo {
		return 0, 0, false, p.fail("numbers out of order in {} quantifier")
	}
	return lo, hi, true, nil
}

// repeat is atom between lo and hi times (hi -1 for no bound), as
// RepeatMatcher has it: an iteration past the minimum that matches nothing
// fails.
func repeat(atom matcher, lo, hi int, greedy bool) matcher {
	r := &repeater{atom: atom, lo: lo, hi: hi, greedy: greedy}
	return func(m *machine, pos int, k func(int) bool) bool { return r.try(m, pos, 0, k) }
}

// repeater is a quantified atom: between lo and hi times, greedy or lazy.
type repeater struct {
	atom   matcher
	lo, hi int
	greedy bool
}

// try matches the atom's iterations from count on, at pos, then k.
func (r *repeater) try(m *machine, pos, count int, k func(int) bool) bool {
	if !m.steps.step() {
		return false
	}
	if r.lazyHit(pos, count, k) {
		return true
	}
	if m.steps.err != nil {
		return false
	}
	if matched, stop := r.again(m, pos, count, k); stop {
		return matched
	}
	return count >= r.lo && r.greedy && k(pos)
}

// lazyHit reports whether a lazy repeat past its minimum ends here.
func (r *repeater) lazyHit(pos, count int, k func(int) bool) bool {
	return count >= r.lo && !r.greedy && k(pos)
}

// again tries one more iteration while hi allows one. An iteration past
// the minimum that matches nothing fails. stop is set when the answer is
// decided: a match, or the steps ran out.
func (r *repeater) again(m *machine, pos, count int, k func(int) bool) (matched, stop bool) {
	if r.hi >= 0 && count >= r.hi {
		return false, false
	}
	more := r.atom(m, pos, func(p int) bool {
		if p == pos && count >= r.lo {
			return false
		}
		return r.try(m, p, count+1, k)
	})
	return more, more || m.steps.err != nil
}

func (p *parser) atom() (matcher, error) {
	c := p.peek()
	switch c {
	case '.':
		p.pos++
		return anyChar, nil
	case '(':
		return p.parenAtom()
	case '[':
		p.pos++
		return p.class()
	case '\\':
		p.pos++
		return p.atomEscape()
	}
	if err := p.lone(c); err != nil {
		return nil, err
	}
	return literal(p.next()), nil
}

// anyChar is ., any character but a line terminator.
func anyChar(m *machine, pos int, k func(int) bool) bool {
	if pos >= len(m.in) || !m.steps.step() {
		return false
	}
	r, w := m.char(pos)
	return !lineTerminator(r) && k(pos+w)
}

// parenAtom reads a group, ( or (?:, and refuses any other (?.
func (p *parser) parenAtom() (matcher, error) {
	p.pos++
	if p.at("?:") {
		p.pos += 2
	} else if p.peek() == '?' {
		return nil, p.fail("invalid group")
	}
	return p.groupBody()
}

// lone refuses c, a quantifier or a bracket, where it stands as an atom.
// Without u, Annex B reads a { that starts no quantifier, a ] and a } as
// themselves.
func (p *parser) lone(c rune) error {
	switch c {
	case '*', '+', '?':
		return p.fail(failNothingToRepeat)
	case '{':
		if p.uflag || p.braceQuantifier() {
			return p.fail(failNothingToRepeat)
		}
	case ']', '}':
		if p.uflag {
			return p.fail("lone quantifier brackets")
		}
	}
	return nil
}

// literal matches the character c, in either case under the i flag.
func literal(c rune) matcher {
	return func(m *machine, pos int, k func(int) bool) bool {
		if pos >= len(m.in) || !m.steps.step() {
			return false
		}
		r, w := m.char(pos)
		return m.same(r, c) && k(pos+w)
	}
}

// same is whether the input character r matches the pattern character c.
func (m *machine) same(r, c rune) bool {
	if r == c {
		return true
	}
	if !m.icase {
		return false
	}
	if m.uflag {
		return slices.Contains(equivalentPoints(c), r)
	}
	return canonical(r) == canonical(c)
}

// wordBoundary is whether \b holds at pos.
func (m *machine) wordBoundary(pos int) bool {
	word := func(i int) bool {
		if i < 0 || i >= len(m.in) {
			return false
		}
		r := m.in[i]
		return slices.ContainsFunc(wordRanges(m.icase, m.uflag), func(span [2]rune) bool { return r >= span[0] && r <= span[1] })
	}
	return word(pos-1) != word(pos)
}

// wordRanges are the word characters of \w and \b: with the i and u flags
// also ſ and the Kelvin sign, which fold into s and k, as the
// specification's WordCharacters has it. wordBoundary asks twice for each
// \b it tests, so the two lists are built once.
func wordRanges(icase, uflag bool) [][2]rune {
	if icase && uflag {
		return foldedWordRanges
	}
	return basicWordRanges
}

// The two lists wordRanges gives. A caller that appends to one gets a new
// slice, since each is exactly as long as its capacity.
var (
	basicWordRanges  = [][2]rune{{'0', '9'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}}
	foldedWordRanges = [][2]rune{{'0', '9'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}, {0x017F, 0x017F}, {0x212A, 0x212A}}
)

// lineTerminator is whether . leaves r out.
func lineTerminator(r rune) bool { return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 }

// set is a class: ranges of characters, the class escapes it names, and
// negation, or a Unicode property, \p{..}. A class is a union, so class
// merges its ranges and names each escape once: x then 119 of [:word:]
// asked 476 properties for each character it tested, and asks 4.
type set struct {
	ranges   [][2]rune
	named    []*set
	property string
	negate   bool
	// key names a class escape, such as \d or \P{L}, so that a class
	// holds it once.
	key string
}

// A class test costs a step, and one more for each classWork units of
// work it took: a unit for each range it compared, and namedWork for each
// class escape it asked, a property's table search or a short list of
// ranges. So a step of a match takes about as long whatever the class
// (TestClassTestCostsItsWork). Through the CI runner on the build node, x
// and 16 of @(|) then a class, which spend every step against a string of
// two characters, took 16.4 us a step for 119 of [:word:] against xⒶ when
// each test cost one step and asked every property it named. Now the
// slowest such class measured, [:print:] and 60 code points apart against
// xⒶ, takes 134 ns a step, 17.6 ms a match, and [ab] 57 ns. An ordinary
// class such as [a-z0-9_-] takes under 256 units a test, one step. A
// class of POSIX classes costs a step more for a character it misses
// through 256 units of properties: [[:word:]] asks its 4 against -, and
// [[:alnum:]] its 3 for Ⓐ and again for its case partner ⓐ, 384 units.
const (
	classWork = 256
	namedWork = 64
)

// has is whether r is in s, before its negation, and the work that took.
func (s *set) has(r rune) (bool, int) {
	if s.property != "" {
		return hasProperty(s.property, r), namedWork
	}
	for i, span := range s.ranges {
		if r >= span[0] && r <= span[1] {
			return true, i + 1
		}
	}
	work := len(s.ranges)
	for _, n := range s.named {
		in, _ := n.has(r)
		work += namedWork
		if in != n.negate {
			return true, work
		}
	}
	return false, work
}

// matches is whether r is in s, comparing by case under the i flag, and
// the work that took.
func (m *machine) matches(s *set, r rune) (bool, int) {
	in, work := s.has(r)
	if !in && m.icase {
		var others []rune
		if m.uflag {
			others = equivalentPoints(r)
		} else {
			others = equivalentUnits(r)
		}
		for _, o := range others {
			if o == r {
				continue
			}
			hit, w := s.has(o)
			work += w
			if hit {
				in = true
				break
			}
		}
	}
	return in != s.negate, work
}

func (s *set) matcher() matcher {
	return func(m *machine, pos int, k func(int) bool) bool {
		if pos >= len(m.in) || !m.steps.step() {
			return false
		}
		r, w := m.char(pos)
		in, work := m.matches(s, r)
		if !m.steps.spend(work / classWork) {
			return false
		}
		return in && k(pos+w)
	}
}

// merge sorts s's ranges and joins those that overlap or touch, which
// changes no answer.
func (s *set) merge() {
	slices.SortFunc(s.ranges, func(a, b [2]rune) int { return int(a[0] - b[0]) })
	var out [][2]rune
	for _, span := range s.ranges {
		if n := len(out); n > 0 && span[0] <= out[n-1][1]+1 {
			out[n-1][1] = max(out[n-1][1], span[1])
			continue
		}
		out = append(out, span)
	}
	s.ranges = out
}

// class reads a class after its [.
func (p *parser) class() (matcher, error) {
	s := &set{}
	if p.peek() == '^' {
		p.pos++
		s.negate = true
	}
	for {
		if p.pos >= len(p.src) {
			return nil, p.fail("unterminated character class")
		}
		if p.peek() == ']' {
			p.pos++
			s.merge()
			return s.matcher(), nil
		}
		if err := p.classMember(s); err != nil {
			return nil, err
		}
	}
}

// classMember reads a member of a class into s: a character, a class
// escape, or a range.
func (p *parser) classMember(s *set) error {
	lo, loSet, err := p.classAtom()
	if err != nil {
		return err
	}
	if p.peek() == '-' && p.pos+1 < len(p.src) && p.src[p.pos+1] != ']' {
		p.pos++
		return p.classRange(s, lo, loSet)
	}
	s.add(lo, loSet)
	return nil
}

// classRange reads the end of a range that starts at lo, or loSet, after
// its -.
func (p *parser) classRange(s *set, lo rune, loSet *set) error {
	hi, hiSet, err := p.classAtom()
	if err != nil {
		return err
	}
	if loSet != nil || hiSet != nil {
		if p.uflag {
			return p.fail("invalid character class")
		}
		// Annex B: a class escape at either end makes the - literal.
		s.add(lo, loSet)
		s.ranges = append(s.ranges, [2]rune{'-', '-'})
		s.add(hi, hiSet)
		return nil
	}
	if hi < lo {
		return p.fail("range out of order in character class")
	}
	s.ranges = append(s.ranges, [2]rune{lo, hi})
	return nil
}

// add adds the character c, or the class escape named, to s: an escape
// of ranges alone, \d, \s or \w, adds its ranges, and any other one is
// added once.
func (s *set) add(c rune, named *set) {
	switch {
	case named == nil:
		s.ranges = append(s.ranges, [2]rune{c, c})
	case !named.negate && named.property == "":
		s.ranges = append(s.ranges, named.ranges...)
	case !slices.ContainsFunc(s.named, func(n *set) bool { return n.key == named.key }):
		s.named = append(s.named, named)
	}
}

// classAtom reads one character of a class, or a class escape such as \d
// or \p{L}, which it returns as a set.
func (p *parser) classAtom() (rune, *set, error) {
	if p.peek() != '\\' {
		return p.next(), nil, nil
	}
	p.pos++
	if p.pos >= len(p.src) {
		return 0, nil, p.fail(`\ at end of pattern`)
	}
	switch c := p.peek(); c {
	case 'b':
		p.pos++
		return '\b', nil, nil
	case '-':
		if p.uflag {
			p.pos++
			return '-', nil, nil
		}
	}
	if s, err := p.classEscape(); s != nil || err != nil {
		return 0, s, err
	}
	c, err := p.characterEscape(true)
	return c, nil, err
}

// atomEscape reads an escape outside a class, after its \.
func (p *parser) atomEscape() (matcher, error) {
	if p.pos >= len(p.src) {
		return nil, p.fail(`\ at end of pattern`)
	}
	if c := p.peek(); c >= '1' && c <= '9' {
		// Backreferences, and Annex B's octal escapes: no glob makes one.
		return nil, p.fail("backreferences are not read")
	}
	if s, err := p.classEscape(); s != nil || err != nil {
		return escapeMatcher(s, err)
	}
	c, err := p.characterEscape(false)
	if err != nil {
		return nil, err
	}
	return literal(c), nil
}

// escapeMatcher is the matcher of the class escape s, outside a class.
func escapeMatcher(s *set, err error) (matcher, error) {
	if err != nil {
		return nil, err
	}
	if s.property != "" && s.negate {
		// \P{..} is the complement first, and the i flag then folds
		// into it: under i and u, a is in \P{Ll} because A is. A
		// class holds the escape as a member, which does that.
		return (&set{named: []*set{s}}).matcher(), nil
	}
	return s.matcher(), nil
}

// classEscape reads \d, \D, \s, \S, \w, \W, and under u \p{..} and \P{..},
// or returns nil for any other escape.
func (p *parser) classEscape() (*set, error) {
	c := p.peek()
	var s *set
	switch c {
	case 'd', 'D':
		s = &set{ranges: [][2]rune{{'0', '9'}}}
	case 'w', 'W':
		s = &set{ranges: wordRanges(p.icase, p.uflag)}
	case 's', 'S':
		s = &set{ranges: [][2]rune{{'\t', '\r'}, {' ', ' '}, {0xA0, 0xA0}, {0x1680, 0x1680}, {0x2000, 0x200A},
			{0x2028, 0x2029}, {0x202F, 0x202F}, {0x205F, 0x205F}, {0x3000, 0x3000}, {0xFEFF, 0xFEFF}}}
	case 'p', 'P':
		return p.propertyEscape(c)
	default:
		return nil, nil
	}
	p.pos++
	s.negate = c == 'D' || c == 'W' || c == 'S'
	s.key = string(c)
	return s, nil
}

// propertyEscape reads \p{..} or \P{..}, c its letter, under u; without
// u it is no class escape.
func (p *parser) propertyEscape(c rune) (*set, error) {
	if !p.uflag {
		return nil, nil
	}
	p.pos++
	if p.peek() != '{' {
		return nil, p.fail(failPropertyName)
	}
	end := slices.Index(p.src[p.pos:], '}')
	if end < 0 {
		return nil, p.fail(failPropertyName)
	}
	name := string(p.src[p.pos+1 : p.pos+end])
	p.pos += end + 1
	if !knownProperty(name) {
		return nil, p.fail(failPropertyName)
	}
	return &set{property: name, negate: c == 'P', key: string(c) + name}, nil
}

// knownProperty is whether name is one of the general categories the port
// reads, the ones minimatch's POSIX classes name.
func knownProperty(name string) bool {
	switch name {
	case "L", "Nl", "Nd", "Zs", "Cc", "Z", "C", "Ll", "P", "Lu", "Pc":
		return true
	}
	return false
}

// characterEscape reads a character escape after its \: a control escape,
// \cX, \0, \xHH, \uHHHH, \u{..} under u, or an identity escape, which u
// allows only for the syntax characters and /.
func (p *parser) characterEscape(inClass bool) (rune, error) {
	c := p.next()
	if r, ok := controlEscapes[c]; ok {
		return r, nil
	}
	switch c {
	case 'c':
		return p.controlLetter(inClass)
	case '0':
		return p.nulEscape()
	case 'x':
		return p.hexEscape()
	case 'u':
		return p.unicodeEscape()
	}
	return p.identityEscape(c)
}

// controlEscapes are the control escapes, \f, \n, \r, \t and \v.
var controlEscapes = map[rune]rune{'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v'}

// controlLetter reads \cX after its c. Annex B reads a digit or _ in a
// class too, and \c without a letter as a backslash, with c read next.
func (p *parser) controlLetter(inClass bool) (rune, error) {
	if l := p.peek(); asciiLetter(l) {
		p.pos++
		return l % 32, nil
	}
	if l := p.peek(); !p.uflag && inClass && digitOrUnderscore(l) {
		p.pos++
		return l % 32, nil
	}
	if p.uflag {
		return 0, p.fail(failUnicodeEscape)
	}
	p.pos--
	return '\\', nil
}

// asciiLetter reports whether c is an ASCII letter.
func asciiLetter(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// digitOrUnderscore reports whether c is an ASCII digit or _.
func digitOrUnderscore(c rune) bool {
	return (c >= '0' && c <= '9') || c == '_'
}

// nulEscape reads \0, which a digit may not follow.
func (p *parser) nulEscape() (rune, error) {
	if d := p.peek(); d >= '0' && d <= '9' {
		return 0, p.fail("octal escapes are not read")
	}
	return 0, nil
}

// hexEscape reads \xHH after its x; without u, an x with no two digits is
// itself.
func (p *parser) hexEscape() (rune, error) {
	if v, ok := p.hex(2); ok {
		return v, nil
	}
	if p.uflag {
		return 0, p.fail("invalid escape")
	}
	return 'x', nil
}

// unicodeEscape reads \uHHHH, a surrogate pair of them under u, or \u{..}
// under u, after its u; without u, a u with no four digits is itself.
func (p *parser) unicodeEscape() (rune, error) {
	if p.uflag && p.peek() == '{' {
		return p.codePointEscape()
	}
	if v, ok := p.hex(4); ok {
		return p.surrogates(v), nil
	}
	if p.uflag {
		return 0, p.fail(failUnicodeEscape)
	}
	return 'u', nil
}

// codePointEscape reads {..}, a code point in hexadecimal, after \u.
func (p *parser) codePointEscape() (rune, error) {
	end := slices.Index(p.src[p.pos:], '}')
	if end < 2 {
		return 0, p.fail(failUnicodeEscape)
	}
	v, err := strconv.ParseUint(string(p.src[p.pos+1:p.pos+end]), 16, 32)
	if err != nil || v > 0x10FFFF {
		return 0, p.fail(failUnicodeEscape)
	}
	p.pos += end + 1
	return rune(v), nil
}

// surrogates is v, or under u the code point of the pair v makes with a
// \uHHHH low surrogate after it, which it then reads.
func (p *parser) surrogates(v rune) rune {
	if !p.uflag || v < 0xD800 || v >= 0xDC00 || !p.at(`\u`) {
		return v
	}
	save := p.pos
	p.pos += 2
	if lo, ok := p.hex(4); ok && lo >= 0xDC00 && lo <= 0xDFFF {
		return 0x10000 + (v-0xD800)<<10 + (lo - 0xDC00)
	}
	p.pos = save
	return v
}

// identityEscape reads an identity escape, which u allows only for the
// syntax characters and /.
func (p *parser) identityEscape(c rune) (rune, error) {
	if !p.uflag {
		return c, nil
	}
	switch c {
	case '^', '$', '\\', '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|', '/':
		return c, nil
	}
	return 0, p.fail("invalid escape")
}

// hex reads n hexadecimal digits, or leaves the parser where it was.
func (p *parser) hex(n int) (rune, bool) {
	if p.pos+n > len(p.src) {
		return 0, false
	}
	v, err := strconv.ParseUint(string(p.src[p.pos:p.pos+n]), 16, 32)
	if err != nil {
		return 0, false
	}
	p.pos += n
	return rune(v), true //nolint:gosec // at most 4 hexadecimal digits, which fit a rune
}
