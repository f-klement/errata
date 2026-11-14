package semver

import (
	"errors"
	"fmt"
	"strings"
)

func isAlnum(c byte) bool { return isDigit(c) || isLetter(c) }

// rubyValid reports whether t matches Gem::Version's
// ANCHORED_VERSION_PATTERN, stripped: a number, then parts of letters and
// digits after dots, then a part after - of letters, digits and hyphens,
// with more such parts after dots.
func rubyValid(t string) bool {
	i := runOf(t, 0, isDigit)
	if i == 0 {
		return false
	}
	for i+1 < len(t) && t[i] == '.' && isAlnum(t[i+1]) {
		i = runOf(t, i+1, isAlnum)
	}
	if i == len(t) {
		return true
	}
	return t[i] == '-' && rubyPreValid(t[i+1:])
}

// rubyPreValid reports whether s, what follows the - of a version, is
// parts of letters, digits and hyphens separated by dots.
func rubyPreValid(s string) bool {
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return false
		}
		for k := range len(part) {
			if c := part[k]; !isAlnum(c) && c != '-' {
				return false
			}
		}
	}
	return true
}

// parseRuby reads t as Gem::Version does. It splits the version into
// segments, runs of digits or of letters, each - read as .pre. as RubyGems
// reads it. The segments before the first run of letters are the numbers;
// from it on they are the pre-release, which orders by cmpRubyIdents. So
// 1.0.0.pre.1, 1.0.0.pre1 and 1.0.0-1 (1.0.0.pre.1) are one version, and
// 1.0 equals 1.0.0.
func parseRuby(t string) (Version, error) {
	if !rubyValid(t) {
		return Version{}, errors.New("it is not a version as RubyGems writes one, a number then parts of letters and digits after dots")
	}
	v := Version{epoch: "0", syn: codeRubyGems}
	start := 0
	for i := 0; i <= len(t); i++ {
		if !inRubySegment(t, start, i) {
			start = v.rubyCut(t, start, i)
		}
	}
	return v, nil
}

// inRubySegment reports whether the byte i of t carries on the segment
// from start: a letter after a letter or a digit after a digit.
func inRubySegment(t string, start, i int) bool {
	return i < len(t) && i > start && isDigit(t[i]) == isDigit(t[i-1]) && isAlnum(t[i])
}

// rubyCut ends the segment from start at i, the end of t or a byte that
// does not carry it on, reading a - as .pre., and is where the next one
// starts: past a separator, or at i.
func (v *Version) rubyCut(t string, start, i int) int {
	if i > start && isAlnum(t[i-1]) {
		v.rubySegment(t[start:i])
	}
	if i < len(t) && t[i] == '-' {
		v.rubySegment("pre")
	}
	if i < len(t) && !isAlnum(t[i]) {
		return i + 1
	}
	return i
}

// rubySegment appends a segment: a number to the release until the first
// run of letters, and from it on to the pre-release.
func (v *Version) rubySegment(seg string) {
	n, isNum := parseNum(seg)
	switch {
	case isNum && v.pre == nil:
		v.release = append(v.release, n)
	case isNum:
		v.pre = append(v.pre, ident{numeric: true, text: string(n)})
	default:
		v.pre = append(v.pre, ident{text: seg})
	}
}

// cmpRubyIdents orders two pre-releases as Gem::Version#<=> orders the
// segments after the numbers: in turn, a missing segment as 0, a run of
// letters below every number, numbers by value and letters by their bytes.
// So 1.0.a is below 1.0.a.1 and above 1.0.a.b.
func cmpRubyIdents(a, b []ident) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		if c := cmpRubyIdent(rubyIdentAt(a, i), rubyIdentAt(b, i)); c != 0 {
			return c
		}
	}
	return 0
}

// rubyIdentAt is the segment i of ids, 0 when ids is shorter.
func rubyIdentAt(ids []ident, i int) ident {
	if i < len(ids) {
		return ids[i]
	}
	return ident{numeric: true, text: "0"}
}

// cmpRubyIdent orders two segments of a pre-release: a run of letters
// below every number, numbers by value and letters by their bytes.
func cmpRubyIdent(x, y ident) int {
	switch {
	case x.numeric && y.numeric:
		return cmpNum(num(x.text), num(y.text))
	case x.numeric:
		return 1
	case y.numeric:
		return -1
	default:
		return strings.Compare(x.text, y.text)
	}
}

// rubyString is v as RubyGems' segments, joined by dots: 1.0.0.pre.1. A
// floor is written -0 after its numbers.
func (v Version) rubyString() string {
	var b strings.Builder
	for i, n := range v.release {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(string(n))
	}
	if v.floor {
		b.WriteString("-0")
	}
	for _, id := range v.pre {
		b.WriteString("." + id.text)
	}
	return b.String()
}

var rubyOperators = []string{"~>", "!=", ">=", "<=", "=", ">", "<"}

// rubyClause reads one requirement as Gem::Requirement does: no operator is
// =, and ~> V is >= V and below the next release of V's numbers but the
// last, so ~> 3.2 is >= 3.2, < 4 and ~> 3.2.6 is >= 3.2.6, < 3.3. A
// requirement names a pre-release, for the rule that keeps pre-releases out
// unless one is named, whatever its operator, as Gem::Requirement#prerelease?
// decides.
func rubyClause(t string) (clause, error) {
	op, rest, err := cutOperator(t, rubyOperators)
	if err != nil {
		return clause{}, err
	}
	v, err := parseRuby(rest)
	if err != nil {
		return clause{}, fmt.Errorf("%s is not a version: %w", quoteToken(rest), err)
	}
	c := clause{pre: v.prerelease()}
	switch op {
	case "~>":
		c.iv = interval{lo: bound{v: v, inc: true}, hi: bound{v: atFloor(bump(v, max(0, len(v.release)-2)))}}
	case "!=":
		c.iv, c.hole, c.holed = everything, interval{lo: bound{v: v, inc: true}, hi: bound{v: v, inc: true}}, true
	default:
		c.iv = orderedBy(op, v)
	}
	return c, nil
}
