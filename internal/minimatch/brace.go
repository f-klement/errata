package minimatch

import (
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
)

// A port of brace-expansion 5.0.12 and balanced-match 4.0.4, which
// minimatch 10.2.6 expands a glob's braces with: a{b,c}d is abd and acd,
// a{1..3} is a1, a2 and a3, and a set that is no set stays as written.
//
// brace-expansion caps a glob at 100000 results of 4000000 characters in
// all. The port refuses a glob far below that, at MaxExpansions results or
// MaxExpandedLength characters, since minimatch then parses every result:
// below the bounds it gives what brace-expansion gives, and at them it
// returns errExpansion rather than a result cut short.
const (
	// MaxExpansions is the most globs one glob may expand to.
	MaxExpansions = 64
	// MaxExpandedLength is the most code units the expanded globs may
	// hold in all.
	MaxExpandedLength = 1 << 14
)

// errExpansion is the error of a glob whose braces expand past the bounds.
var errExpansion = errors.New("its braces expand to more than " + strconv.Itoa(MaxExpansions) +
	" globs or " + strconv.Itoa(MaxExpandedLength) + " characters")

// The bounds brace-expansion itself sets on nesting and on the {a},b}
// rewrite.
const (
	expansionMaxDepth    = 1000
	expansionMaxRewrites = 1000
)

// The sentinels brace-expansion puts in place of an escaped \\, \{, \},
// \, and \. while it expands. brace-expansion adds Math.random() to each;
// the port uses private use characters no glob in a config holds.
var (
	escSlash  = units{0, 0xE000, 0}
	escOpen   = units{0, 0xE001, 0}
	escClose  = units{0, 0xE002, 0}
	escComma  = units{0, 0xE003, 0}
	escPeriod = units{0, 0xE004, 0}
)

// expansion is one run of the expander, with whether a bound stopped it.
type expansion struct{ cut bool }

// braceExpand is minimatch's braceExpand: the glob alone unless it holds a
// { with a } after it and no { or line terminator between, and otherwise
// brace-expansion's expand.
func braceExpand(glob units) ([]units, error) {
	if !hasBraceSet(glob) {
		return []units{glob}, nil
	}
	e := &expansion{}
	out := e.expand(glob)
	if e.cut {
		return nil, errExpansion
	}
	return out, nil
}

// hasBraceSet is /\{(?:(?!\{).)*\}/.test(glob).
func hasBraceSet(glob units) bool {
	for i, c := range glob {
		if c != '{' {
			continue
		}
		for _, d := range glob[i+1:] {
			if d == '}' {
				return true
			}
			if d == '{' || lineTerminator(d) {
				break
			}
		}
	}
	return false
}

// replaceAll is s with every old replaced by new, left to right.
func replaceAll(s, old, new units) units {
	var out units
	for i := 0; i < len(s); {
		if hasPrefixAt(s, old, i) {
			out = append(out, new...)
			i += len(old)
			continue
		}
		out = append(out, s[i])
		i++
	}
	return out
}

func hasPrefixAt(s, p units, i int) bool {
	return i+len(p) <= len(s) && slices.Equal(s[i:i+len(p)], p)
}

func escapeBraces(s units) units {
	s = replaceAll(s, units{'\\', '\\'}, escSlash)
	s = replaceAll(s, units{'\\', '{'}, escOpen)
	s = replaceAll(s, units{'\\', '}'}, escClose)
	s = replaceAll(s, units{'\\', ','}, escComma)
	return replaceAll(s, units{'\\', '.'}, escPeriod)
}

func unescapeBraces(s units) units {
	s = replaceAll(s, escSlash, units{'\\'})
	s = replaceAll(s, escOpen, units{'{'})
	s = replaceAll(s, escClose, units{'}'})
	s = replaceAll(s, escComma, units{','})
	return replaceAll(s, escPeriod, units{'.'})
}

// balanced is balanced-match's balanced('{', '}', s): the first balanced
// pair, as pre, body and post.
type balancedMatch struct{ pre, body, post units }

func balanced(s units) (balancedMatch, bool) {
	r, ok := balancedRange(s)
	if !ok {
		return balancedMatch{}, false
	}
	return balancedMatch{pre: s[:r[0]], body: s[r[0]+1 : r[1]], post: s[r[1]+1:]}, true
}

func indexFrom(s units, c rune, from int) int {
	if from < 0 {
		from = 0
	}
	if from > len(s) {
		return -1
	}
	if i := slices.Index(s[from:], c); i >= 0 {
		return i + from
	}
	return -1
}

// balancedRange is balanced-match's range, step for step.
func balancedRange(s units) ([2]int, bool) {
	ai := indexFrom(s, '{', 0)
	bi := indexFrom(s, '}', ai+1)
	i := ai
	if ai < 0 || bi <= 0 {
		return [2]int{}, false
	}
	var begs []int
	left, right, haveRight := len(s), 0, false
	var result [2]int
	haveResult := false
	for i >= 0 && !haveResult {
		switch {
		case i == ai:
			begs = append(begs, i)
			ai = indexFrom(s, '{', i+1)
		case len(begs) == 1:
			r := begs[len(begs)-1]
			begs = begs[:len(begs)-1]
			result, haveResult = [2]int{r, bi}, true
		default:
			if len(begs) > 0 {
				beg := begs[len(begs)-1]
				begs = begs[:len(begs)-1]
				if beg < left {
					left, right, haveRight = beg, bi, true
				}
			}
			bi = indexFrom(s, '}', i+1)
		}
		if ai < bi && ai >= 0 {
			i = ai
		} else {
			i = bi
		}
	}
	if len(begs) > 0 && haveRight {
		result, haveResult = [2]int{left, right}, true
	}
	return result, haveResult
}

// splitUnits is s.split(sep) for one code unit.
func splitUnits(s units, sep rune) []units {
	var out []units
	start := 0
	for i, c := range s {
		if c == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func concat(parts ...units) units {
	var out units
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// parseCommaParts is s split at its top level commas.
func parseCommaParts(s units) []units {
	var parts []units
	var carry units
	for {
		m, ok := balanced(s)
		if !ok {
			tail := splitUnits(s, ',')
			tail[0] = concat(carry, tail[0])
			return append(parts, tail...)
		}
		p := splitUnits(m.pre, ',')
		p[0] = concat(carry, p[0])
		p[len(p)-1] = concat(p[len(p)-1], units{'{'}, m.body, units{'}'})
		if len(m.post) == 0 {
			return append(parts, p...)
		}
		carry = p[len(p)-1]
		parts = append(parts, p[:len(p)-1]...)
		s = m.post
	}
}

// expand is brace-expansion's expand.
func (e *expansion) expand(s units) []units {
	if len(s) == 0 {
		return nil
	}
	if len(s) >= 2 && s[0] == '{' && s[1] == '}' {
		s = concat(units{'\\', '{', '\\', '}'}, s[2:])
	}
	out := e.expandOne(escapeBraces(s), 0, expansionMaxRewrites, true)
	for i := range out {
		out[i] = unescapeBraces(out[i])
	}
	return out
}

// combine is every acc[a] + pre + values[v], held to the bounds, which mark
// the expansion cut when they stop it.
func (e *expansion) combine(acc []units, pre units, values []units, dropEmpties bool) []units {
	var out []units
	length := 0
	for _, a := range acc {
		for _, v := range values {
			if len(out) >= MaxExpansions {
				e.cut = true
				return out
			}
			x := concat(a, pre, v)
			if dropEmpties && len(x) == 0 {
				continue
			}
			if length+len(x) > MaxExpandedLength {
				e.cut = true
				return out
			}
			out = append(out, x)
			length += len(x)
		}
	}
	return out
}

// numeric is brace-expansion's numeric for a sequence's ends and step,
// which its patterns hold to -?\d+ or one letter: the number parseInt reads,
// or the letter's code.
func numeric(s units) float64 {
	if v, err := strconv.ParseFloat(string(s), 64); err == nil {
		return v
	}
	return float64(s[0])
}

// jsNumber is String(x) of an integral number.
func jsNumber(x float64) string {
	if x == 0 {
		return "0"
	}
	if math.Abs(x) < 1e21 {
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return strconv.FormatFloat(x, 'e', -1, 64)
}

// isPadded is /^-?0\d/.
func isPadded(s units) bool {
	if len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	return len(s) >= 2 && s[0] == '0' && s[1] >= '0' && s[1] <= '9'
}

// splitDots is s.split(/\.\./).
func splitDots(s units) []units {
	var out []units
	start := 0
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == '.' {
			out = append(out, s[start:i])
			start = i + 2
			i++
		}
	}
	return append(out, s[start:])
}

// expandSequence is the values of a numeric or alphabetic sequence body.
func (e *expansion) expandSequence(body units, alpha bool) []units {
	n := splitDots(body)
	x, y := numeric(n[0]), numeric(n[1])
	width := max(len(n[0]), len(n[1]))
	incr := 1.0
	if len(n) == 3 {
		incr = max(math.Abs(numeric(n[2])), 1)
	}
	reverse := y < x
	if reverse {
		incr = -incr
	}
	pad := slices.ContainsFunc(n, isPadded)
	var out []units
	length := 0
	for i := x; (reverse && i >= y) || (!reverse && i <= y); i += incr {
		if len(out) >= MaxExpansions {
			e.cut = true
			break
		}
		var c units
		if alpha {
			c = units{rune(int64(i) & 0xFFFF)}
			if c[0] == '\\' {
				c = units{}
			}
		} else {
			str := jsNumber(i)
			if pad {
				if need := width - len(str); need > 0 {
					z := strings.Repeat("0", need)
					if i < 0 {
						str = "-" + z + str[1:]
					} else {
						str = z + str
					}
				}
			}
			c = []rune(str)
		}
		if length+len(c) > MaxExpandedLength {
			e.cut = true
			break
		}
		out = append(out, c)
		length += len(c)
	}
	return out
}

// isNumericSequence is /^-?\d+\.\.-?\d+(?:\.\.-?\d+)?$/.
func isNumericSequence(b units) bool {
	n := splitDots(b)
	if len(n) != 2 && len(n) != 3 {
		return false
	}
	for _, x := range n {
		if len(x) > 0 && x[0] == '-' {
			x = x[1:]
		}
		if len(x) == 0 || slices.ContainsFunc(x, func(c rune) bool { return c < '0' || c > '9' }) {
			return false
		}
	}
	return true
}

// isAlphaSequence is /^[a-zA-Z]\.\.[a-zA-Z](?:\.\.-?\d+)?$/.
func isAlphaSequence(b units) bool {
	n := splitDots(b)
	if len(n) != 2 && len(n) != 3 {
		return false
	}
	letter := func(x units) bool {
		return len(x) == 1 && ((x[0] >= 'a' && x[0] <= 'z') || (x[0] >= 'A' && x[0] <= 'Z'))
	}
	if !letter(n[0]) || !letter(n[1]) {
		return false
	}
	if len(n) == 3 {
		return isNumericSequence(concat(units{'0', '.', '.'}, n[2]))
	}
	return true
}

// commaThenBrace is /,(?!,).*\}/.test(s): a comma not before a comma, then
// a } with no line terminator between.
func commaThenBrace(s units) bool {
	for i, c := range s {
		if c != ',' || (i+1 < len(s) && s[i+1] == ',') {
			continue
		}
		for _, d := range s[i+1:] {
			if d == '}' {
				return true
			}
			if lineTerminator(d) {
				break
			}
		}
	}
	return false
}

// expandOne is brace-expansion's expand_.
func (e *expansion) expandOne(s units, depth, maxRewrites int, isTop bool) []units {
	if depth > expansionMaxDepth {
		return []units{s}
	}
	acc := []units{{}}
	rewrites := 0
	dropEmpties, firstGroup := false, true
	for {
		m, ok := balanced(s)
		if !ok {
			return e.combine(acc, s, []units{{}}, dropEmpties)
		}
		pre := m.pre
		if len(pre) > 0 && pre[len(pre)-1] == '$' {
			acc = e.combine(acc, concat(pre, units{'{'}, m.body, units{'}'}), []units{{}}, dropEmpties && len(m.post) == 0)
			firstGroup = false
			if len(m.post) == 0 {
				break
			}
			s = m.post
			continue
		}
		numSeq, alphaSeq := isNumericSequence(m.body), isAlphaSequence(m.body)
		isSeq := numSeq || alphaSeq
		isOptions := slices.Contains(m.body, ',')
		if !isSeq && !isOptions {
			if rewrites < maxRewrites && commaThenBrace(m.post) {
				rewrites++
				s = concat(m.pre, units{'{'}, m.body, escClose, m.post)
				isTop = true
				continue
			}
			return e.combine(acc, concat(pre, units{'{'}, m.body, units{'}'}, m.post), []units{{}}, dropEmpties)
		}
		if firstGroup {
			dropEmpties = isTop && !isSeq
			firstGroup = false
		}
		var values []units
		if isSeq {
			values = e.expandSequence(m.body, alphaSeq)
		} else {
			n := parseCommaParts(m.body)
			if len(n) == 1 {
				inner := e.expandOne(n[0], depth+1, maxRewrites, false)
				n = make([]units, len(inner))
				for i, x := range inner {
					n[i] = concat(units{'{'}, x, units{'}'})
				}
				if len(n) == 1 {
					acc = e.combine(acc, concat(pre, n[0]), []units{{}}, dropEmpties && len(m.post) == 0)
					if len(m.post) == 0 {
						break
					}
					s = m.post
					continue
				}
			}
			dropsEmpties := dropEmpties && len(m.post) == 0 && len(pre) == 0
			for _, a := range acc {
				if len(a) > 0 {
					dropsEmpties = false
				}
			}
			valuesLength := 0
		outer:
			for _, part := range n {
				for _, v := range e.expandOne(part, depth+1, maxRewrites, false) {
					if dropsEmpties && len(v) == 0 {
						continue
					}
					if len(values) >= MaxExpansions || valuesLength+len(v) > MaxExpandedLength {
						e.cut = true
						break outer
					}
					values = append(values, v)
					valuesLength += len(v)
				}
			}
		}
		acc = e.combine(acc, pre, values, dropEmpties && len(m.post) == 0)
		if len(m.post) == 0 {
			break
		}
		s = m.post
	}
	return acc
}
