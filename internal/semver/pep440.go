package semver

import (
	"errors"
	"fmt"
	"strings"
)

// pep440Pre is each pre-release spelling in its normal form, longest
// first where one starts another, as the packaging library's pattern
// tries them.
var pep440Pre = []struct{ word, normal string }{
	{"alpha", "a"}, {"a", "a"}, {"beta", "b"}, {"b", "b"}, {"preview", "rc"}, {"pre", "rc"}, {"c", "rc"}, {"rc", "rc"},
}

var pep440Post = []string{"post", "rev", "r"}

// errPEP440 is the refusal of a value that is no PEP 440 version.
var errPEP440 = errors.New("it is not a version as PEP 440 writes one")

// pep440Scan reads a PEP 440 version from the left, as the pattern of the
// PyPA's version specifiers specification (Appendix: parsing version
// strings) matches one, case-insensitive:
//
//	v? (N!)? N(.N)* (S? pre S? N?)? (-N | S? post S? N?)? (S? dev S? N?)? (+local)?
//
// where S is one of - _ and ., each optional part taken when it is there.
// It makes one pass, and allocates only the version it returns.
type pep440Scan struct{ s string }

func (p *pep440Scan) digits() string {
	i := 0
	for i < len(p.s) && isDigit(p.s[i]) {
		i++
	}
	d := p.s[:i]
	p.s = p.s[i:]
	return d
}

func isSep(c byte) bool { return c == '-' || c == '_' || c == '.' }

// word takes the first of words that p starts with, after an optional
// separator, and reports which, -1 for none; it takes nothing then.
func (p *pep440Scan) word(words func(int) string, n int) int {
	t := p.s
	if t != "" && isSep(t[0]) {
		t = t[1:]
	}
	for i := range n {
		w := words(i)
		if len(t) >= len(w) && strings.EqualFold(t[:len(w)], w) {
			p.s = t[len(w):]
			return i
		}
	}
	return -1
}

// number takes an optional separator and number after a word, 0 when no
// number follows. A separator with no number after it is taken, as the
// pattern takes it.
func (p *pep440Scan) number() num {
	if p.s != "" && isSep(p.s[0]) {
		p.s = p.s[1:]
	}
	return numOrZero(p.digits())
}

// parsePEP440 reads t as PEP 440 reads a version, in its normal form: the
// pre-release words alpha, beta, c, pre and preview are a, b and rc, a
// missing number is 0, 1.0-1 is 1.0.post1, and the local version is in
// lower case with its parts joined by dots, each number without leading
// zeros.
func parsePEP440(t string) (Version, error) {
	p := pep440Scan{s: t}
	if p.s != "" && (p.s[0] == 'v' || p.s[0] == 'V') {
		p.s = p.s[1:]
	}
	v := Version{epoch: "0", syn: codePEP440}
	first := p.digits()
	if first == "" {
		return Version{}, errPEP440
	}
	if p.s != "" && p.s[0] == '!' {
		v.epoch, _ = parseNum(first)
		p.s = p.s[1:]
		if first = p.digits(); first == "" {
			return Version{}, errPEP440
		}
	}
	n, _ := parseNum(first)
	v.release = []num{n}
	for len(p.s) >= 2 && p.s[0] == '.' && isDigit(p.s[1]) {
		p.s = p.s[1:]
		n, _ := parseNum(p.digits())
		v.release = append(v.release, n)
	}
	if i := p.word(func(i int) string { return pep440Pre[i].word }, len(pep440Pre)); i >= 0 {
		v.pre = []ident{{text: pep440Pre[i].normal}, {numeric: true, text: string(p.number())}}
	}
	if len(p.s) >= 2 && p.s[0] == '-' && isDigit(p.s[1]) {
		p.s = p.s[1:]
		v.hasPost, v.post = true, numOrZero(p.digits())
	} else if p.word(func(i int) string { return pep440Post[i] }, len(pep440Post)) >= 0 {
		v.hasPost, v.post = true, p.number()
	}
	if p.word(func(int) string { return "dev" }, 1) >= 0 {
		v.hasDev, v.dev = true, p.number()
	}
	if p.s == "" {
		return v, nil
	}
	local, ok := strings.CutPrefix(p.s, "+")
	if !ok {
		return Version{}, errPEP440
	}
	parts := strings.FieldsFunc(local, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	if len(parts) == 0 || isSep(local[0]) || isSep(local[len(local)-1]) || strings.Count(local, ".")+strings.Count(local, "-")+strings.Count(local, "_") != len(parts)-1 {
		return Version{}, errPEP440
	}
	for i, part := range parts {
		for k := range len(part) {
			if c := part[k]; !isDigit(c) && !isLetter(c) {
				return Version{}, errPEP440
			}
		}
		if n, ok := parseNum(part); ok {
			parts[i] = string(n)
		} else {
			parts[i] = strings.ToLower(part)
		}
	}
	v.build = strings.Join(parts, ".")
	return v, nil
}

// publicOf is v without a local version or the bound above them.
func publicOf(v Version) Version {
	v.build, v.localTop = "", false
	return v
}

func numOrZero(s string) num {
	if n, ok := parseNum(s); ok {
		return n
	}
	return "0"
}

// pep440String is v in PEP 440's normal form, such as 1!2.0rc1.post2.dev3+abc.5.
// A floor is written -0 after its numbers, as for every syntax, and the
// bound above the post releases of a version is written .post* after it.
func (v Version) pep440String() string {
	var b strings.Builder
	if v.epoch != "0" && v.epoch != "" {
		b.WriteString(string(v.epoch) + "!")
	}
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
		b.WriteString(id.text)
	}
	if v.ceil {
		b.WriteString(".post*")
	}
	if v.hasPost {
		b.WriteString(".post" + string(v.post))
	}
	if v.hasDev {
		b.WriteString(".dev" + string(v.dev))
	}
	if v.build != "" {
		b.WriteString("+" + v.build)
	}
	if v.localTop {
		b.WriteString("+*")
	}
	return b.String()
}

var pep440Operators = []string{"===", "~=", "==", "!=", "<=", ">=", "<", ">"}

// pep440Clause reads one clause of a PEP 440 specifier, as the
// specification's sections on each operator define it:
//
//   - ==V and !=V compare with the release zero padded, and match a
//     candidate with any local version unless V has one; ==V.* and !=V.*
//     match by prefix, a pre, post or dev release of V included;
//   - <=V and >=V are ordered, <=V admitting V's local versions;
//   - <V admits no pre-release of V unless V is one, and >V no post
//     release of V unless V is one, nor a local version of V;
//   - ~=V.N is >=V.N and ==V.*.
//
// The local versions are ordered (cmpLocal), so a range of them is an
// interval like any other. A clause names a pre-release, for the rule that
// keeps pre-releases out unless one is named, when any operator but !=
// names one, as packaging 25.0's Specifier.prereleases decides and the
// specification's own <V.rc1 example asks.
func pep440Clause(t string) (clause, error) {
	op, rest, err := cutOperator(t, pep440Operators)
	switch {
	case err != nil:
		return clause{}, err
	case op == "":
		return clause{}, fmt.Errorf("%s: PEP 440 writes an operator before every version", quoteToken(t))
	case op == "===":
		return clause{}, fmt.Errorf("%s: === compares text, which errata does not read as a version", quoteToken(t))
	}
	wild := false
	if op == "==" || op == "!=" {
		rest, wild = strings.CutSuffix(rest, ".*")
	}
	v, err := parsePEP440(rest)
	switch {
	case err != nil:
		return clause{}, fmt.Errorf("%s is not a version: %w", quoteToken(rest), err)
	case wild && (v.suffixed() || v.build != ""):
		return clause{}, fmt.Errorf("%s: .* follows the release numbers only", quoteToken(t))
	case v.build != "" && op != "==" && op != "!=":
		return clause{}, fmt.Errorf("%s: only == and != take a local version", quoteToken(t))
	case op == "~=" && len(v.release) < 2:
		return clause{}, fmt.Errorf("%s: ~= needs a version of at least two numbers", quoteToken(t))
	}
	c := clause{pre: !wild && v.prerelease() && op != "!="}
	at := bound{v: v, inc: true}
	// A public version matches itself with any local version: up to top,
	// above them all. A version with a local version matches only itself.
	top := v
	top.localTop = true
	same := interval{lo: at, hi: bound{v: top, inc: true}}
	if v.build != "" {
		same.hi = at
	}
	prefix := interval{lo: bound{v: atFloor(v), inc: true}, hi: bound{v: atFloor(bump(v, len(v.release)-1))}}
	if wild {
		same = prefix
	}
	switch op {
	case "==":
		c.iv = same
	case "!=":
		c.iv, c.hole, c.holed = everything, same, true
	case ">=":
		c.iv = interval{lo: at, hi: bound{inf: true}}
	case "<=":
		c.iv = interval{lo: bound{inf: true}, hi: bound{v: top, inc: true}}
	case ">":
		lo := bound{v: ceilOf(v)}
		if v.hasPost || v.hasDev {
			lo = bound{v: top}
		}
		c.iv = interval{lo: lo, hi: bound{inf: true}}
	case "<":
		c.iv = interval{lo: bound{inf: true}, hi: bound{v: v}}
		switch {
		case v.prerelease():
		case v.hasPost:
			c.hole, c.holed = interval{lo: bound{v: atFloor(v), inc: true}, hi: bound{v: releaseOf(v)}}, true
		default:
			c.iv.hi = bound{v: atFloor(v)}
		}
	default: // ~=
		c.iv = interval{lo: at, hi: bound{v: atFloor(bump(v, len(v.release)-2))}}
	}
	return c, nil
}
