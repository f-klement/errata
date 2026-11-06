package semver

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Syntax is the ecosystem whose rules read a value. Any, the zero value,
// reads the syntax of all five, npm's rules winning where they differ
// (ParseVersion, ParseRange); each other value reads only its own
// ecosystem's versions and ranges, as that ecosystem documents them.
type Syntax string

// The syntaxes. Any is the reading of a rule that sets no syntax:.
const (
	Any      Syntax = ""
	NPM      Syntax = "npm"
	Cargo    Syntax = "cargo"
	PEP440   Syntax = "pep440"
	Go       Syntax = "go"
	RubyGems Syntax = "rubygems"
)

// Syntaxes lists every syntax but Any, in the order a load error names them.
var Syntaxes = []Syntax{NPM, Cargo, PEP440, Go, RubyGems}

// ParseSyntax reads a syntax by its name, which must be one of Syntaxes.
func ParseSyntax(name string) (Syntax, error) {
	if s := Syntax(name); slices.Contains(Syntaxes, s) {
		return s, nil
	}
	names := make([]string, len(Syntaxes))
	for i, s := range Syntaxes {
		names[i] = string(s)
	}
	return Any, fmt.Errorf("the syntax %s is none of %s", quoteToken(name), strings.Join(names, ", "))
}

// checkValue trims s and refuses it when it is too long or empty.
func checkValue(s string) (string, error) {
	if len(s) > MaxLength {
		return "", errTooLong
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return "", errors.New("it is empty")
	}
	return t, nil
}

// ParseVersion reads s as one version of the syntax. Surrounding white space
// is ignored, and a value of more than MaxLength bytes is no version.
func (syn Syntax) ParseVersion(s string) (Version, error) {
	t, err := checkValue(s)
	if err != nil {
		return Version{}, err
	}
	switch syn {
	case Any:
		return parseAny(t)
	case NPM:
		return parseSemVer(t, npmForm)
	case Cargo:
		return parseSemVer(t, cargoForm)
	case PEP440:
		return parsePEP440(t)
	case Go:
		return parseGo(t)
	case RubyGems:
		return parseRuby(t)
	}
	_, err = ParseSyntax(string(syn))
	return Version{}, err
}

// ParseRange reads s as a range of the syntax. Under npm an empty range is
// every version, as node-semver reads "" (a package.json "dep": "").
func (syn Syntax) ParseRange(s string) (Range, error) {
	if syn == NPM && len(s) <= MaxLength && strings.TrimSpace(s) == "" {
		s = "*"
	}
	t, err := checkValue(s)
	if err != nil {
		return Range{}, err
	}
	var r Range
	switch syn {
	case Any:
		r, err = parseAnyRange(t)
	case NPM:
		r, err = parseNPMRange(t)
	case Cargo:
		r, err = parseCargoRange(t)
	case PEP440:
		r, err = parseListRange(t, "PEP 440", pep440Clause, preNamed)
	case Go:
		r, err = parseGoRange(t)
	case RubyGems:
		r, err = parseListRange(t, "RubyGems", rubyClause, preNamed)
	default:
		_, err = ParseSyntax(string(syn))
	}
	if err != nil {
		return Range{}, err
	}
	r.syn = syn
	return r, nil
}

// semverForm is how one ecosystem writes a SemVer version.
type semverForm struct {
	syn synCode
	// who names the ecosystem in an error.
	who string
	// fewest is the fewest numbers a version may have.
	fewest int
	// v is whether a v may lead, needV whether one must, as in a Go
	// module's version.
	v, needV bool
	// exact makes a version of fewer numbers stand for itself, as Go's
	// v1.2 is v1.2.0.
	exact bool
	// afterWild lets numbers follow a wildcard, which node-semver reads as
	// wildcards, so 1.*.3 is 1.x.
	afterWild bool
}

var (
	npmForm      = semverForm{syn: codeNPM, who: "npm", fewest: 1, v: true, afterWild: true}
	cargoForm    = semverForm{syn: codeCargo, who: "Cargo", fewest: 1}
	goModuleForm = semverForm{syn: codeGo, who: "a Go module", fewest: 1, v: true, needV: true, exact: true}
)

// strictNum reads a number as SemVer writes one: digits, no leading zero
// but in 0.
func strictNum(s string) (num, bool) {
	if s == "" || len(s) > 1 && s[0] == '0' {
		return "", false
	}
	return parseNum(s)
}

// parseSemVer reads t as SemVer 2.0.0 writes a version, in the form f: up
// to three numbers, then a pre-release after - and build metadata after +,
// both only after three numbers.
func parseSemVer(t string, f semverForm) (Version, error) {
	rest := t
	if f.v {
		var cut bool
		if rest, cut = strings.CutPrefix(rest, "v"); f.needV && !cut {
			return Version{}, fmt.Errorf("%s writes a v before its version", f.who)
		}
	}
	v := Version{epoch: "0", syn: f.syn, exact: f.exact, module: f.needV}
	var pre, build string
	var hasPre, hasBuild bool
	rest, build, hasBuild = strings.Cut(rest, "+")
	rest, pre, hasPre = strings.Cut(rest, "-")
	parts := strings.Split(rest, ".")
	if len(parts) > 3 {
		return Version{}, fmt.Errorf("%s writes at most three numbers", f.who)
	}
	for _, p := range parts {
		n, ok := strictNum(p)
		if !ok {
			return Version{}, fmt.Errorf("%s is not a number as %s writes one, digits with no leading zero", quoteToken(p), f.who)
		}
		v.release = append(v.release, n)
	}
	if len(parts) < f.fewest {
		return Version{}, fmt.Errorf("%s writes a version with three numbers", f.who)
	}
	if (hasPre || hasBuild) && len(parts) < 3 {
		return Version{}, fmt.Errorf("%s writes a pre-release or build only after three numbers", f.who)
	}
	if hasPre {
		ids, err := semverIdents(pre, "pre-release after -", true)
		if err != nil {
			return Version{}, err
		}
		v.pre = ids
	}
	if hasBuild {
		if _, err := semverIdents(build, "build after +", false); err != nil {
			return Version{}, err
		}
		v.build = build
	}
	return v, nil
}

// semverIdents reads SemVer identifiers separated by dots: letters, digits
// and hyphens, not empty, and in a pre-release no number with a leading
// zero.
func semverIdents(s, what string, numbers bool) ([]ident, error) {
	var ids []ident
	for _, part := range strings.Split(s, ".") {
		if part == "" {
			return nil, fmt.Errorf("the %s has an empty part", what)
		}
		for i := range len(part) {
			if c := part[i]; !isDigit(c) && !isLetter(c) && c != '-' {
				return nil, fmt.Errorf("the %s holds %q, which a version does not", what, c)
			}
		}
		id := newIdent(part)
		if numbers && id.numeric && id.text != part {
			return nil, fmt.Errorf("the %s holds %s, a number with a leading zero", what, quoteToken(part))
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// strictOperand reads a comparator's version as npm or Cargo writes it, x,
// X or * standing for the numbers from some position on.
func strictOperand(s string, f semverForm) (operand, error) {
	t := s
	if f.v {
		t = strings.TrimPrefix(t, "v")
	}
	core := t
	if i := strings.IndexAny(t, "-+"); i >= 0 {
		core = t[:i]
	}
	parts := strings.Split(core, ".")
	wild := slices.IndexFunc(parts, isWild)
	if wild < 0 {
		v, err := parseSemVer(t, semverForm{syn: f.syn, who: f.who, fewest: 1})
		if err != nil {
			return operand{}, fmt.Errorf("%s is not a version: %w", quoteToken(s), err)
		}
		return operand{v: v, n: len(v.release), partial: v.Partial()}, nil
	}
	if core != t || len(parts) > 3 {
		return operand{}, fmt.Errorf("%s: %s writes a wildcard in place of at most three numbers, with nothing after", quoteToken(s), f.who)
	}
	o := operand{n: wild, partial: true, wildcard: true, v: Version{epoch: "0", syn: f.syn}}
	for i, p := range parts {
		if i >= wild {
			if _, ok := strictNum(p); !isWild(p) && (!f.afterWild || !ok) {
				return operand{}, fmt.Errorf("%s: a number follows a wildcard", quoteToken(s))
			}
			continue
		}
		n, ok := strictNum(p)
		if !ok {
			return operand{}, fmt.Errorf("%s: %s is not a number as %s writes one", quoteToken(s), quoteToken(p), f.who)
		}
		o.v.release = append(o.v.release, n)
	}
	return o, nil
}

// cutOperator splits t into the longest of ops it starts with and the rest,
// trimmed, which must be one word.
func cutOperator(t string, ops []string) (op, rest string, err error) {
	for _, o := range ops {
		if strings.HasPrefix(t, o) {
			op = o
			break
		}
	}
	rest = strings.TrimSpace(t[len(op):])
	switch {
	case rest == "" && op != "":
		return "", "", fmt.Errorf("%s: the operator has no version after it", quoteToken(t))
	case strings.ContainsAny(rest, " \t\r\n\v\f"):
		return "", "", fmt.Errorf("%s: a comparator is one operator and one version", quoteToken(t))
	}
	return op, rest, nil
}

// semverClause reads one comparator of npm or Cargo once its operator is
// known: the interval admits gives for its version, and the version when
// it is a pre-release.
func semverClause(op string, o operand, t string) (clause, error) {
	var named *Version
	if o.v.prerelease() {
		named = &o.v
	}
	if o.n == 0 {
		switch op {
		case "", "=", ">=", "<=", "^", "~":
			return clause{iv: everything}, nil
		}
		return clause{}, fmt.Errorf("%s: %s before a wildcard admits no version", quoteToken(t), op)
	}
	iv, err := admits(op, o, t)
	return clause{iv: iv, named: named}, err
}

// npmOperators are node-semver's: its primitives, ^, ~, and ~>, which it
// reads as ~.
var npmOperators = []string{"<=", ">=", "~>", "<", ">", "=", "^", "~"}

// parseNPMRange reads a range as node-semver does: comparator sets joined
// by ||, each a hyphen range or comparators separated by white space. An
// empty set is every version.
func parseNPMRange(s string) (Range, error) {
	var r Range
	for _, text := range strings.Split(s, "||") {
		f := strings.Fields(text)
		tokens := joinOperators(f, npmOperators)
		switch {
		case len(f) == 0:
			tokens = []string{"*"}
		case len(f) == 3 && f[1] == "-":
			tokens = []string{">=" + f[0], "<=" + f[2]}
		}
		a, err := fold(tokens, npmClause, preSame)
		if err != nil {
			return Range{}, err
		}
		r.alts = append(r.alts, a)
	}
	return r, nil
}

func npmClause(t string) (clause, error) {
	op, rest, err := cutOperator(t, npmOperators)
	if err != nil {
		return clause{}, err
	}
	o, err := strictOperand(rest, npmForm)
	if err != nil {
		return clause{}, err
	}
	if op == "~>" {
		op = "~"
	}
	return semverClause(op, o, t)
}

// parseListRange reads a range that its ecosystem writes as comparators
// separated by commas, with no ||: Cargo's, PEP 440's and RubyGems'.
func parseListRange(s, who string, read func(string) (clause, error), rule preRule) (Range, error) {
	if strings.Contains(s, "||") {
		return Range{}, fmt.Errorf("%s joins comparators with commas and has no ||", who)
	}
	parts := strings.Split(s, ",")
	for i, p := range parts {
		if parts[i] = strings.TrimSpace(p); parts[i] == "" {
			return Range{}, errors.New("a comparator beside a comma is empty")
		}
	}
	a, err := fold(parts, read, rule)
	if err != nil {
		return Range{}, err
	}
	return Range{alts: []alternative{a}}, nil
}

// parseCargoRange reads a requirement as Cargo does: comparators separated
// by commas, where a lone wildcard, such as *, stands alone.
func parseCargoRange(s string) (Range, error) {
	r, err := parseListRange(s, "Cargo", cargoClause, preSame)
	if err != nil || !strings.Contains(s, ",") {
		return r, err
	}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "=")); isWild(p) {
			return Range{}, fmt.Errorf("%s: Cargo writes a wildcard of every version as the only comparator", quoteToken(p))
		}
	}
	return r, nil
}

var cargoOperators = []string{"<=", ">=", "<", ">", "=", "^", "~"}

// cargoClause reads one of Cargo's comparators: a version with no operator
// is a caret requirement, a wildcard takes no operator but =, and * stands
// alone. An upper bound lies below the pre-releases of the version it
// names, as npm's does: the Cargo book writes ^1.2.3 as >=1.2.3, <2.0.0,
// and the semver crate's matches_caret, matches_tilde and matches_less of
// a partial version (src/eval.rs) first hold the major, and the minor
// where written, to the comparator's, so 2.0.0-alpha.1 satisfies neither
// ^1.2.3 nor <2 beside >=2.0.0-alpha.0.
func cargoClause(t string) (clause, error) {
	op, rest, err := cutOperator(t, cargoOperators)
	if err != nil {
		return clause{}, err
	}
	o, err := strictOperand(rest, cargoForm)
	if err != nil {
		return clause{}, err
	}
	switch {
	case o.wildcard && op != "" && op != "=":
		return clause{}, fmt.Errorf("%s: Cargo writes a wildcard with no operator", quoteToken(t))
	case o.v.build != "":
		return clause{}, fmt.Errorf("%s: a requirement holds no build metadata", quoteToken(t))
	case !o.wildcard && op == "":
		op = "^"
	}
	return semverClause(op, o, t)
}

var goOperators = []string{"<=", ">=", "<", ">", "="}

// parseGoRange reads a range of Go versions. Go writes none; errata reads
// <, <=, >, >= and = before a version, separated by spaces or commas, sets
// of them joined by ||, every version standing for itself.
func parseGoRange(s string) (Range, error) {
	var r Range
	for _, text := range strings.Split(s, "||") {
		tokens := joinOperators(strings.Fields(strings.ReplaceAll(text, ",", " ")), goOperators)
		if len(tokens) == 0 {
			return Range{}, errors.New("an alternative beside || is empty")
		}
		a, err := fold(tokens, goClause, preFree)
		if err != nil {
			return Range{}, err
		}
		r.alts = append(r.alts, a)
	}
	return r, nil
}

func goClause(t string) (clause, error) {
	op, rest, err := cutOperator(t, goOperators)
	if err != nil {
		return clause{}, err
	}
	v, err := parseGo(rest)
	if err != nil {
		return clause{}, fmt.Errorf("%s is not a Go version: %w", quoteToken(rest), err)
	}
	return clause{iv: orderedBy(op, v)}, nil
}

// orderedBy is what one of the operators =, <, <=, > and >= admits beside
// the version v, which stands for itself; no operator is =.
func orderedBy(op string, v Version) interval {
	at := bound{v: v, inc: true}
	switch op {
	case ">=":
		return interval{lo: at, hi: bound{inf: true}}
	case ">":
		return interval{lo: bound{v: v}, hi: bound{inf: true}}
	case "<=":
		return interval{lo: bound{inf: true}, hi: at}
	case "<":
		return interval{lo: bound{inf: true}, hi: bound{v: v}}
	}
	return interval{lo: at, hi: at}
}

// parseGo reads a Go version: a module's, which starts with v and is
// SemVer, v1 and v1.2 standing for v1.0.0 and v1.2.0; or a toolchain's,
// with or without go before it, ordered as go.dev/doc/toolchain orders
// them: 1.21 < 1.21rc1 < 1.21.0, where 1.21 is the language version, and
// before Go 1.21, 1.20 is the release 1.20.0.
func parseGo(t string) (Version, error) {
	if strings.HasPrefix(t, "v") {
		return parseSemVer(t, goModuleForm)
	}
	rest := strings.TrimPrefix(t, "go")
	v := Version{epoch: "0", syn: codeGo, exact: true}
	major, rest, ok := cutStrictNum(rest)
	if !ok {
		return Version{}, errors.New("a Go version starts with a number, or with v for a module's")
	}
	if rest == "" {
		v.release = []num{major, "0", "0"}
		return v, nil
	}
	if rest[0] != '.' {
		return Version{}, errors.New("a dot follows the first number of a Go version")
	}
	minor, rest, ok := cutStrictNum(rest[1:])
	if !ok {
		return Version{}, errors.New("a number follows the dot of a Go version")
	}
	v.release = []num{major, minor}
	switch {
	case rest == "" && cmpNum(minor, "21") < 0:
		v.release = append(v.release, "0")
		return v, nil
	case rest == "":
		v.floor, v.lang = true, true
		return v, nil
	case rest[0] == '.':
		patch, after, ok := cutStrictNum(rest[1:])
		if !ok || after != "" {
			return Version{}, errors.New("a Go release ends with its third number")
		}
		v.release = append(v.release, patch)
		return v, nil
	}
	i := 0
	for i < len(rest) && rest[i] >= 'a' && rest[i] <= 'z' {
		i++
	}
	if i == 0 {
		return Version{}, fmt.Errorf("%s follows the numbers of a Go version, where rc1 or beta2 may", quoteToken(rest))
	}
	v.pre, v.glued = []ident{{text: rest[:i]}}, true
	if rest = rest[i:]; rest != "" {
		n, after, ok := cutStrictNum(rest)
		if !ok || after != "" {
			return Version{}, fmt.Errorf("%s follows the numbers of a Go version, where rc1 or beta2 may", quoteToken(rest))
		}
		v.pre = append(v.pre, ident{numeric: true, text: string(n)})
	}
	return v, nil
}

// cutStrictNum reads the number at the start of s, as strictNum reads one,
// and returns what follows it.
func cutStrictNum(s string) (num, string, bool) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	n, ok := strictNum(s[:i])
	return n, s[i:], ok
}
