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
// steps against the budget of the Match call, so no glob runs on without
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

// machine is one search: the input, the flags and the step budget.
type machine struct {
	in    units
	icase bool
	uflag bool
	steps *budget
}

// budget is the steps one Match call may take, shared by every regex and
// segment it matches.
type budget struct {
	left int
	err  error
}

// errBudget is the error of a match that ran out of steps.
var errBudget = errors.New("matching takes more than the step budget")

// step spends one step, and reports false once none is left.
func (b *budget) step() bool {
	if b.left <= 0 {
		b.err = errBudget
		return false
	}
	b.left--
	return true
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
func (r *regex) test(s units, steps *budget) (bool, error) {
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

func (p *parser) term() (matcher, error) {
	c := p.peek()
	switch c {
	case '^':
		p.pos++
		return func(_ *machine, pos int, k func(int) bool) bool { return pos == 0 && k(pos) }, nil
	case '$':
		p.pos++
		return func(m *machine, pos int, k func(int) bool) bool { return pos == len(m.in) && k(pos) }, nil
	case '\\':
		if p.at(`\b`) || p.at(`\B`) {
			p.pos += 2
			want := p.src[p.pos-1] == 'b'
			return func(m *machine, pos int, k func(int) bool) bool {
				return m.wordBoundary(pos) == want && k(pos)
			}, nil
		}
	case '(':
		if p.at("(?=") || p.at("(?!") {
			negate := p.src[p.pos+2] == '!'
			p.pos += 3
			body, err := p.disjunction()
			if err != nil {
				return nil, err
			}
			if p.peek() != ')' {
				return nil, p.fail("unterminated group")
			}
			p.pos++
			look := lookahead(body, negate)
			if !p.uflag {
				// Annex B lets a lookahead take a quantifier without u.
				return p.quantified(look)
			}
			if q := p.peek(); q == '*' || q == '+' || q == '?' || (q == '{' && p.braceQuantifier()) {
				return nil, p.fail("nothing to repeat")
			}
			return look, nil
		}
	}
	atom, err := p.atom()
	if err != nil {
		return nil, err
	}
	return p.quantified(atom)
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
	lo, hi := 0, 0
	switch p.peek() {
	case '*':
		p.pos++
		lo, hi = 0, -1
	case '+':
		p.pos++
		lo, hi = 1, -1
	case '?':
		p.pos++
		lo, hi = 0, 1
	case '{':
		var ok bool
		save := p.pos
		if lo, hi, ok = p.readBraces(); !ok {
			p.pos = save
			return atom, nil
		}
		if hi >= 0 && hi < lo {
			return nil, p.fail("numbers out of order in {} quantifier")
		}
	default:
		return atom, nil
	}
	greedy := true
	if p.peek() == '?' {
		p.pos++
		greedy = false
	}
	return repeat(atom, lo, hi, greedy), nil
}

// repeat is atom between lo and hi times (hi -1 for no bound), as
// RepeatMatcher has it: an iteration past the minimum that matches nothing
// fails.
func repeat(atom matcher, lo, hi int, greedy bool) matcher {
	var try func(m *machine, pos, count int, k func(int) bool) bool
	try = func(m *machine, pos, count int, k func(int) bool) bool {
		if !m.steps.step() {
			return false
		}
		if count >= lo && !greedy && k(pos) {
			return true
		}
		if m.steps.err != nil {
			return false
		}
		if hi < 0 || count < hi {
			more := atom(m, pos, func(p int) bool {
				if p == pos && count >= lo {
					return false
				}
				return try(m, p, count+1, k)
			})
			if more {
				return true
			}
			if m.steps.err != nil {
				return false
			}
		}
		return count >= lo && greedy && k(pos)
	}
	return func(m *machine, pos int, k func(int) bool) bool { return try(m, pos, 0, k) }
}

func (p *parser) atom() (matcher, error) {
	c := p.peek()
	switch c {
	case '.':
		p.pos++
		return func(m *machine, pos int, k func(int) bool) bool {
			if pos >= len(m.in) || !m.steps.step() {
				return false
			}
			r, w := m.char(pos)
			return !lineTerminator(r) && k(pos+w)
		}, nil
	case '(':
		p.pos++
		if p.at("?:") {
			p.pos += 2
		} else if p.peek() == '?' {
			return nil, p.fail("invalid group")
		}
		body, err := p.disjunction()
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, p.fail("unterminated group")
		}
		p.pos++
		return body, nil
	case '[':
		p.pos++
		return p.class()
	case '\\':
		p.pos++
		return p.atomEscape()
	case '*', '+', '?':
		return nil, p.fail("nothing to repeat")
	case '{':
		if p.uflag || p.braceQuantifier() {
			return nil, p.fail("nothing to repeat")
		}
	case ']', '}':
		if p.uflag {
			return nil, p.fail("lone quantifier brackets")
		}
	}
	return literal(p.next()), nil
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
// specification's WordCharacters has it.
func wordRanges(icase, uflag bool) [][2]rune {
	ranges := [][2]rune{{'0', '9'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}}
	if icase && uflag {
		ranges = append(ranges, [2]rune{0x017F, 0x017F}, [2]rune{0x212A, 0x212A})
	}
	return ranges
}

// lineTerminator is whether . leaves r out.
func lineTerminator(r rune) bool { return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 }

// set is a class: ranges of characters, named classes, and negation.
type set struct {
	ranges [][2]rune
	preds  []func(rune) bool
	negate bool
}

func (s *set) has(r rune) bool {
	for _, span := range s.ranges {
		if r >= span[0] && r <= span[1] {
			return true
		}
	}
	for _, f := range s.preds {
		if f(r) {
			return true
		}
	}
	return false
}

// matches is whether r is in s, comparing by case under the i flag.
func (m *machine) matches(s *set, r rune) bool {
	in := s.has(r)
	if !in && m.icase {
		var others []rune
		if m.uflag {
			others = equivalentPoints(r)
		} else {
			others = equivalentUnits(r)
		}
		in = slices.ContainsFunc(others, s.has)
	}
	return in != s.negate
}

func (s *set) matcher() matcher {
	return func(m *machine, pos int, k func(int) bool) bool {
		if pos >= len(m.in) || !m.steps.step() {
			return false
		}
		r, w := m.char(pos)
		return m.matches(s, r) && k(pos+w)
	}
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
			return s.matcher(), nil
		}
		lo, loSet, err := p.classAtom()
		if err != nil {
			return nil, err
		}
		if p.peek() == '-' && p.pos+1 < len(p.src) && p.src[p.pos+1] != ']' {
			p.pos++
			hi, hiSet, err := p.classAtom()
			if err != nil {
				return nil, err
			}
			if loSet != nil || hiSet != nil {
				if p.uflag {
					return nil, p.fail("invalid character class")
				}
				// Annex B: a class escape at either end makes the - literal.
				s.add(lo, loSet)
				s.ranges = append(s.ranges, [2]rune{'-', '-'})
				s.add(hi, hiSet)
				continue
			}
			if hi < lo {
				return nil, p.fail("range out of order in character class")
			}
			s.ranges = append(s.ranges, [2]rune{lo, hi})
			continue
		}
		s.add(lo, loSet)
	}
}

func (s *set) add(c rune, named *set) {
	if named != nil {
		s.preds = append(s.preds, func(r rune) bool { return named.has(r) != named.negate })
		return
	}
	s.ranges = append(s.ranges, [2]rune{c, c})
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
		if err != nil {
			return nil, err
		}
		return s.matcher(), nil
	}
	c, err := p.characterEscape(false)
	if err != nil {
		return nil, err
	}
	return literal(c), nil
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
		if !p.uflag {
			return nil, nil
		}
		p.pos++
		if p.peek() != '{' {
			return nil, p.fail("invalid property name")
		}
		end := slices.Index(p.src[p.pos:], '}')
		if end < 0 {
			return nil, p.fail("invalid property name")
		}
		name := string(p.src[p.pos+1 : p.pos+end])
		p.pos += end + 1
		if !knownProperty(name) {
			return nil, p.fail("invalid property name")
		}
		return &set{preds: []func(rune) bool{func(r rune) bool { return hasProperty(name, r) }}, negate: c == 'P'}, nil
	default:
		return nil, nil
	}
	p.pos++
	s.negate = c == 'D' || c == 'W' || c == 'S'
	return s, nil
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
	switch c {
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'v':
		return '\v', nil
	case 'c':
		if l := p.peek(); (l >= 'a' && l <= 'z') || (l >= 'A' && l <= 'Z') {
			p.pos++
			return l % 32, nil
		}
		if l := p.peek(); !p.uflag && inClass && ((l >= '0' && l <= '9') || l == '_') {
			p.pos++
			return l % 32, nil
		}
		if p.uflag {
			return 0, p.fail("invalid unicode escape")
		}
		// Annex B: \c without a letter is a backslash, and c is read next.
		p.pos--
		return '\\', nil
	case '0':
		if d := p.peek(); d >= '0' && d <= '9' {
			return 0, p.fail("octal escapes are not read")
		}
		return 0, nil
	case 'x':
		if v, ok := p.hex(2); ok {
			return v, nil
		}
		if p.uflag {
			return 0, p.fail("invalid escape")
		}
		return 'x', nil
	case 'u':
		if p.uflag && p.peek() == '{' {
			end := slices.Index(p.src[p.pos:], '}')
			if end < 2 {
				return 0, p.fail("invalid unicode escape")
			}
			v, err := strconv.ParseUint(string(p.src[p.pos+1:p.pos+end]), 16, 32)
			if err != nil || v > 0x10FFFF {
				return 0, p.fail("invalid unicode escape")
			}
			p.pos += end + 1
			return rune(v), nil
		}
		if v, ok := p.hex(4); ok {
			if p.uflag && v >= 0xD800 && v < 0xDC00 && p.at(`\u`) {
				save := p.pos
				p.pos += 2
				if lo, ok := p.hex(4); ok && lo >= 0xDC00 && lo <= 0xDFFF {
					return 0x10000 + (v-0xD800)<<10 + (lo - 0xDC00), nil
				}
				p.pos = save
			}
			return v, nil
		}
		if p.uflag {
			return 0, p.fail("invalid unicode escape")
		}
		return 'u', nil
	}
	if p.uflag {
		switch c {
		case '^', '$', '\\', '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|', '/':
			return c, nil
		}
		return 0, p.fail("invalid escape")
	}
	return c, nil
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
