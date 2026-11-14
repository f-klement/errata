package semver

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// bound is one end of an interval: a version, and whether the interval
// holds it. An unbounded end has inf set.
type bound struct {
	v   Version
	inc bool
	inf bool
}

// interval is the versions between lo and hi.
type interval struct {
	lo, hi bound
}

var everything = interval{lo: bound{inf: true}, hi: bound{inf: true}}

func (iv interval) empty() bool {
	if iv.lo.inf || iv.hi.inf {
		return false
	}
	c := Compare(iv.lo.v, iv.hi.v)
	return c > 0 || c == 0 && (!iv.lo.inc || !iv.hi.inc)
}

// cmpLo orders two lower bounds by where they start.
func cmpLo(a, b bound) int {
	switch {
	case a.inf && b.inf:
		return 0
	case a.inf:
		return -1
	case b.inf:
		return 1
	}
	if c := Compare(a.v, b.v); c != 0 {
		return c
	}
	switch {
	case a.inc == b.inc:
		return 0
	case a.inc:
		return -1
	}
	return 1
}

// cmpHi orders two upper bounds by where they end.
func cmpHi(a, b bound) int {
	switch {
	case a.inf && b.inf:
		return 0
	case a.inf:
		return 1
	case b.inf:
		return -1
	}
	if c := Compare(a.v, b.v); c != 0 {
		return c
	}
	switch {
	case a.inc == b.inc:
		return 0
	case a.inc:
		return 1
	}
	return -1
}

func (iv interval) intersect(other interval) interval {
	out := iv
	if cmpLo(other.lo, out.lo) > 0 {
		out.lo = other.lo
	}
	if cmpHi(other.hi, out.hi) < 0 {
		out.hi = other.hi
	}
	return out
}

func (iv interval) contains(v Version) bool {
	if !iv.lo.inf {
		if c := Compare(iv.lo.v, v); c > 0 || c == 0 && !iv.lo.inc {
			return false
		}
	}
	if !iv.hi.inf {
		if c := Compare(v, iv.hi.v); c > 0 || c == 0 && !iv.hi.inc {
			return false
		}
	}
	return true
}

// holdsRelease reports whether iv holds a release: a version with no
// pre-release and no dev release. A partial version stands for releases
// only, and two ranges overlap when they share one, so a range that admits
// only pre-releases of a version, such as >=2.0.0-alpha <2.0.0, shares
// nothing with <2.0.0.
func (iv interval) holdsRelease() bool {
	if iv.empty() {
		return false
	}
	lo := bound{v: Version{epoch: "0", release: []num{"0"}, floor: true}, inc: true}
	if !iv.lo.inf {
		lo = iv.lo
	}
	if lo.v.ceil && lo.v.pre == nil {
		// Above every post release of R lie the releases with more
		// numbers, R.0.1, R.0.0.1 and so on, with no lowest one; any
		// upper bound above R's posts admits some of them.
		return iv.hi.inf || Compare(lo.v, iv.hi.v) < 0
	}
	below := func(c Version) bool {
		if iv.hi.inf {
			return true
		}
		d := Compare(c, iv.hi.v)
		return d < 0 || d == 0 && iv.hi.inc
	}
	return below(firstRelease(lo))
}

// firstRelease is the lowest release, a version with neither a pre-release
// nor a dev release, that a lower bound admits. A post release counts as a
// release.
func firstRelease(lo bound) Version {
	v := lo.v
	switch {
	case v.floor || v.pre != nil:
		// A pre-release, or a post or dev release of one, lies below the
		// release of its numbers, the first release above it.
		return releaseOf(v)
	case v.hasDev && v.hasPost:
		// 1.0.post1.dev1: the post release it leads to.
		v.hasDev, v.dev = false, ""
		return v
	case v.hasDev:
		return releaseOf(v)
	case lo.inc:
		return v
	}
	// Above a release R the next release is R's next post release, R.post0
	// when R has none: nothing lies between, but dev releases of it.
	if v.hasPost {
		v.post = v.post.inc()
	} else {
		v.hasPost, v.post = true, "0"
	}
	return v
}

// intersectAll is the intersection of two lists of intervals, each sorted
// and disjoint, as a sorted disjoint list without empty intervals.
func intersectAll(a, b []interval) []interval {
	var out []interval
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if x := a[i].intersect(b[j]); !x.empty() {
			out = append(out, x)
		}
		// Advance the interval that ends first; the other may meet the next.
		if cmpHi(a[i].hi, b[j].hi) < 0 {
			i++
		} else {
			j++
		}
	}
	return out
}

// preRule is when a pre-release satisfies an alternative whose intervals
// hold it.
type preRule uint8

const (
	// preSame: when a comparator names a pre-release of the same numbers,
	// as npm and Cargo decide.
	preSame preRule = iota
	// preNamed: when any comparator names a pre-release, as PEP 440 and
	// RubyGems decide.
	preNamed
	// preFree: always, as Go orders its versions with no rule for
	// pre-releases.
	preFree
)

// alternative is one side of a range's ||: comparators that must all hold.
type alternative struct {
	// ivs are the versions the comparators admit together, sorted and
	// disjoint.
	ivs []interval
	// named are the comparators' versions that are pre-releases, under
	// preSame. A pre-release satisfies the alternative only when one of
	// them has its numbers.
	named []Version
	rule  preRule
	// namesPre is true, under preNamed, when a comparator names a
	// pre-release.
	namesPre bool
}

// Range is a version range: alternatives joined by ||, each a list of
// comparators that must all hold.
type Range struct {
	alts []alternative
	syn  Syntax
}

// String is r as the intervals it admits: each alternative as >=, >, <= and
// < comparators, = for a single version and * for every version, the
// alternatives joined by ||. Under PEP 440, =V admits V's local versions,
// and a bound written +* after a version lies above every local version
// of it. A bound npm writes with -0, such as <21.0.0-0,
// lies below every pre-release of 21.0.0.
func (r Range) String() string {
	var parts []string
	for _, a := range r.alts {
		var ivs []string
		if len(a.ivs) == 0 {
			ivs = append(ivs, "(no version)")
		}
		for _, iv := range a.ivs {
			ivs = append(ivs, iv.String())
		}
		parts = append(parts, strings.Join(ivs, " || "))
	}
	return strings.Join(parts, " || ")
}

func (iv interval) String() string {
	if s, ok := iv.single(); ok {
		return s
	}
	var parts []string
	if !iv.lo.inf {
		parts = append(parts, orderOp(">", iv.lo.inc)+full(iv.lo.v))
	}
	if !iv.hi.inf {
		parts = append(parts, orderOp("<", iv.hi.inc)+full(iv.hi.v))
	}
	return strings.Join(parts, " ")
}

// single is iv written as one comparator, and whether it is one: * for
// every version, and = for one version, or under PEP 440 for a version
// and its local versions.
func (iv interval) single() (string, bool) {
	switch {
	case iv.lo.inf && iv.hi.inf:
		return "*", true
	case !iv.lo.inf && !iv.hi.inf && Compare(iv.lo.v, iv.hi.v) == 0:
		return "=" + full(iv.lo.v), true
	case iv.withLocals():
		// ==V under PEP 440: V and its local versions.
		return "=" + full(iv.lo.v), true
	}
	return "", false
}

// withLocals reports whether iv is a public version V and its local
// versions, up to the bound above them.
func (iv interval) withLocals() bool {
	return !iv.lo.inf && !iv.hi.inf && iv.lo.inc && iv.hi.inc && iv.hi.v.localTop && iv.lo.v.build == "" && Compare(iv.lo.v, publicOf(iv.hi.v)) == 0
}

// orderOp is the operator op, > or <, with = after it when the bound is
// inclusive.
func orderOp(op string, inc bool) string {
	if inc {
		return op + "="
	}
	return op
}

// full is v written with at least three numbers, as the bounds of npm's,
// Cargo's and the unset syntax's ranges are. The other syntaxes write a
// bound with the numbers it has.
func full(v Version) string {
	if v.syn == codePEP440 || v.syn == codeGo || v.syn == codeRubyGems {
		return v.String()
	}
	for len(v.release) < 3 {
		v.release = append(slices.Clip(v.release), "0")
	}
	return v.String()
}

// operators are the comparison operators a range reads, longest first so a
// prefix match takes the whole operator.
var operators = []string{"===", "~>", "~=", "==", "!=", ">=", "<=", "^", "~", ">", "<", "="}

// ParseRange reads s as a range, in the syntax of any of the five
// ecosystems:
//
//   - npm: comparators separated by spaces, alternatives by ||, ^ and ~,
//     x, X and * in place of a number, and hyphen ranges such as
//     1.2.3 - 2.3.4;
//   - Cargo: comparators separated by commas, ^, ~, = and *;
//   - PEP 440: ~=, ==, != and a trailing .* after == or !=;
//   - RubyGems: ~>, such as ~> 3.2;
//   - Go modules: a version such as v1.2.3, which Go writes without ranges.
//
// A bare version of three numbers or more is that version; one of one or two
// numbers, such as 20 or 20.11, is every release that starts with them, as
// in npm. Cargo reads a bare requirement as ^, so write ^1.2 to mean it.
// Between two versions that npm and PEP 440 read differently, this reads
// npm: >1.2 and <=1.2 compare at the precision written, so >1.2 starts at
// 1.3.0, where PEP 440 and RubyGems would admit 1.2.1.
func ParseRange(s string) (Range, error) {
	return Any.ParseRange(s)
}

// parseAnyRange is ParseRange of a value already trimmed, not empty and
// within MaxLength.
func parseAnyRange(s string) (Range, error) {
	var r Range
	for _, text := range strings.Split(s, "||") {
		a, err := parseAlternative(strings.TrimSpace(text))
		if err != nil {
			return Range{}, err
		}
		r.alts = append(r.alts, a)
	}
	return r, nil
}

func parseAlternative(s string) (alternative, error) {
	if s == "" {
		return alternative{}, errors.New("an alternative beside || is empty")
	}
	if f := strings.Fields(s); len(f) == 3 && f[1] == "-" {
		return fold([]string{">=" + f[0], "<=" + f[2]}, anyClause, preSame)
	}
	return fold(joinOperators(strings.Fields(strings.ReplaceAll(s, ",", " ")), operators), anyClause, preSame)
}

// joinOperators joins an operator written apart from its version, as in
// ~> 3.2 or >= 20, to the field after it.
func joinOperators(fields, ops []string) []string {
	var tokens []string
	for _, f := range fields {
		if n := len(tokens); n > 0 && slices.Contains(ops, tokens[n-1]) {
			tokens[n-1] += f
			continue
		}
		tokens = append(tokens, f)
	}
	return tokens
}

// clause is what one comparator admits: an interval, and the hole it takes
// out of the alternative when holed is set (!=, and PEP 440's <V of a post
// release). The hole is kept by value, so a chain of != allocates nothing
// for each.
type clause struct {
	iv    interval
	hole  interval
	holed bool
	// named is the comparator's version when it is a pre-release, for
	// preSame; pre is whether it names one, for preNamed.
	named *Version
	pre   bool
}

// anyClause is comparator as a clause.
func anyClause(t string) (clause, error) {
	iv, hole, named, err := comparator(t)
	c := clause{iv: iv, named: named}
	if hole != nil {
		c.iv, c.hole, c.holed = everything, *hole, true
	}
	return c, err
}

// fold reads the comparators of one alternative and returns what they admit
// together. Every comparator but != narrows one interval, so fold keeps the
// greatest lower bound and the least upper bound; each != is a hole, and
// the holes are sorted once and cut out of that interval in one pass. Its
// time is k log k in the comparators, never their square.
func fold(tokens []string, read func(string) (clause, error), rule preRule) (alternative, error) {
	var (
		a     = alternative{rule: rule}
		keep  = everything
		holes []interval
		seen  map[string]bool
	)
	for _, t := range tokens {
		c, err := read(t)
		if err != nil {
			return alternative{}, err
		}
		keep = keep.intersect(c.iv)
		if c.holed {
			holes = append(holes, c.hole)
		}
		a.namesPre = a.namesPre || c.pre
		named := c.named
		if rule != preSame {
			named = nil
		}
		// Satisfies compares a named version's release only, so one per
		// release gives the same answers, and 94 pre-releases of one
		// release in 1024 bytes keep one version.
		if named != nil && !seen[releaseKey(*named)] {
			if seen == nil {
				seen = map[string]bool{}
			}
			seen[releaseKey(*named)] = true
			a.named = append(a.named, *named)
		}
	}
	a.ivs = cut(keep, holes)
	return a, nil
}

// releaseKey is v's epoch and numbers, without the zeros that end them, so
// two versions have one key when cmpRelease finds them equal.
func releaseKey(v Version) string {
	n := len(v.release)
	for n > 0 && v.release[n-1] == "0" {
		n--
	}
	var b strings.Builder
	b.WriteString(string(orZero(v.epoch)))
	for _, d := range v.release[:n] {
		b.WriteByte('.')
		b.WriteString(string(d))
	}
	return b.String()
}

// cut is keep with every hole taken out, as sorted disjoint intervals
// without empty ones.
func cut(keep interval, holes []interval) []interval {
	if keep.empty() {
		return nil
	}
	slices.SortFunc(holes, func(x, y interval) int { return cmpLo(x.lo, y.lo) })
	var out []interval
	cur := keep
	for _, h := range holes {
		if !h.lo.inf {
			piece := cur
			if end := (bound{v: h.lo.v, inc: !h.lo.inc}); cmpHi(end, piece.hi) < 0 {
				piece.hi = end
			}
			if !piece.empty() {
				out = append(out, piece)
			}
		}
		if h.hi.inf {
			return out
		}
		if after := (bound{v: h.hi.v, inc: !h.hi.inc}); cmpLo(after, cur.lo) > 0 {
			cur.lo = after
		}
	}
	if !cur.empty() {
		out = append(out, cur)
	}
	return out
}

// operand is the version a comparator names, with the precision written.
type operand struct {
	v Version
	// n is how many numbers are written: wildcards and missing numbers do
	// not count. A partial operand (one or two numbers, or a wildcard)
	// stands for every release that starts with its numbers.
	n        int
	partial  bool
	wildcard bool
}

// parseOperand reads a comparator's version, where x, X or * may stand for
// the numbers from some position on, as in 1.x, 1.2.* or *.
func parseOperand(s string) (operand, error) {
	t, prefixed := strings.CutPrefix(s, "v")
	epoch, t := cutOperandEpoch(t)
	parts := strings.Split(t, ".")
	n := leadingNumbers(parts)
	if n < len(parts) && isWild(parts[n]) {
		return wildOperand(s, parts, n, epoch, prefixed)
	}
	v, err := ParseVersion(s)
	if err != nil {
		return operand{}, fmt.Errorf("%s is not a version: %w", quoteToken(s), err)
	}
	return operand{v: v, n: len(v.release), partial: v.Partial()}, nil
}

// cutOperandEpoch cuts an epoch, N!, off the start of t, and is the
// epoch, 0 when there is none, and the rest.
func cutOperandEpoch(t string) (num, string) {
	if e, rest, ok := strings.Cut(t, "!"); ok {
		if n, isNum := parseNum(e); isNum {
			return n, rest
		}
	}
	return "0", t
}

// leadingNumbers is how many of parts, from the first, are numbers.
func leadingNumbers(parts []string) int {
	n := 0
	for n < len(parts) && parts[n] != "" && strings.Trim(parts[n], "0123456789") == "" {
		n++
	}
	return n
}

// wildOperand is the operand s whose parts are n numbers then
// wildcards, with the epoch epoch, prefixed when a v stood before it. It
// refuses a v before a wildcard and a number after one.
func wildOperand(s string, parts []string, n int, epoch num, prefixed bool) (operand, error) {
	if prefixed && n == 0 {
		return operand{}, fmt.Errorf("%s: a v stands before a version, not a wildcard", quoteToken(s))
	}
	for _, p := range parts[n:] {
		if !isWild(p) {
			return operand{}, fmt.Errorf("%s: a number follows a wildcard", quoteToken(s))
		}
	}
	o := operand{n: n, partial: true, wildcard: true, v: Version{epoch: epoch}}
	for _, p := range parts[:n] {
		d, _ := parseNum(p)
		o.v.release = append(o.v.release, d)
	}
	return o, nil
}

func isWild(p string) bool { return p == "x" || p == "X" || p == "*" }

// comparator reads one comparator, such as >=1.2.3, ^1.2 or ~> 3.2. It
// returns the interval it admits, or for != the hole it takes out, and its
// version when that is a pre-release.
func comparator(t string) (interval, *interval, *Version, error) {
	op, rest, err := comparatorOp(t)
	if err != nil {
		return interval{}, nil, nil, err
	}
	o, err := parseOperand(rest)
	if err != nil {
		return interval{}, nil, nil, err
	}
	if o.n == 0 {
		iv, err := wildAdmits(op, t)
		return iv, nil, nil, err
	}
	if o.wildcard && (op == "~>" || op == "~=") {
		return interval{}, nil, nil, fmt.Errorf("%s: %s takes a version, not a wildcard", quoteToken(t), op)
	}
	named := o.named()
	if op == "!=" {
		return interval{}, o.hole(), named, nil
	}
	iv, err := admits(op, o, t)
	return iv, nil, named, err
}

// comparatorOp cuts the operator off the comparator t, "" for none, and
// refuses === and an operator with no version after it.
func comparatorOp(t string) (string, string, error) {
	op := ""
	for _, o := range operators {
		if strings.HasPrefix(t, o) {
			op = o
			break
		}
	}
	rest := t[len(op):]
	switch {
	case op == "===":
		return "", "", fmt.Errorf("%s: === compares text, which errata does not read as a version", quoteToken(t))
	case rest == "":
		return "", "", fmt.Errorf("%s: the operator has no version after it", quoteToken(t))
	}
	return op, rest, nil
}

// wildAdmits is what op admits before *, x or X: every version, after
// any operator that admits it, and refuses the rest.
func wildAdmits(op, t string) (interval, error) {
	switch op {
	case "", "=", "==", ">=", "<=", "^", "~":
		return everything, nil
	}
	return interval{}, fmt.Errorf("%s: %s before a wildcard admits no version", quoteToken(t), op)
}

// named is o's version when it is a pre-release, which the comparator
// names, else nil.
func (o *operand) named() *Version {
	if o.v.prerelease() {
		return &o.v
	}
	return nil
}

// hole is what != takes out with the operand o: its version, or every
// release it stands for when it is a wildcard.
func (o *operand) hole() *interval {
	hole := interval{lo: bound{v: o.v, inc: true}, hi: bound{v: o.v, inc: true}}
	if o.wildcard {
		hole.hi = bound{v: atFloor(bump(o.v, o.n-1))}
	}
	return &hole
}

// admits is what op with the operand o admits, for every operator but !=.
func admits(op string, o operand, t string) (interval, error) {
	switch op {
	case "", "=":
		return o.exact(o.partial), nil
	case "==":
		return o.exact(o.wildcard), nil
	case ">=", ">", "<", "<=":
		return o.ordered(op), nil
	case "^":
		return o.upTo(o.caretAt()), nil
	case "~":
		return o.upTo(min(1, o.n-1)), nil
	case "~=":
		if o.n < 2 {
			return interval{}, fmt.Errorf("%s: ~= needs a version of at least two numbers", quoteToken(t))
		}
		return o.upTo(o.n - 2), nil
	}
	// The one operator left is ~>.
	return o.upTo(max(0, o.n-2)), nil
}

// at is the bound at o's version, inclusive.
func (o operand) at() bound { return bound{v: o.v, inc: true} }

// past is the bound below every version after the releases o stands for,
// when o is partial. It is made only where an operator needs it, since
// every bump allocates.
func (o operand) past() bound { return bound{v: atFloor(bump(o.v, o.n-1))} }

// upTo is from o's version up to the next change of its number i.
func (o operand) upTo(i int) interval {
	return interval{lo: o.at(), hi: bound{v: atFloor(bump(o.v, i))}}
}

// exact is = with o: its version, or every release it stands for when
// wide.
func (o operand) exact(wide bool) interval {
	if wide {
		return interval{lo: o.at(), hi: o.past()}
	}
	return interval{lo: o.at(), hi: o.at()}
}

// ordered is what the ordering operator op, >=, >, < or <=, admits with
// o: a partial version stands for every release it starts.
func (o operand) ordered(op string) interval {
	switch op {
	case ">=":
		return interval{lo: o.at(), hi: bound{inf: true}}
	case ">":
		return interval{lo: o.above(), hi: bound{inf: true}}
	case "<":
		return interval{lo: bound{inf: true}, hi: o.under()}
	default: // <=
		return interval{lo: bound{inf: true}, hi: o.atMost()}
	}
}

// above is the bound >V starts at: past the releases a partial V stands
// for, or just past V.
func (o operand) above() bound {
	if o.partial {
		return bound{v: bump(o.v, o.n-1), inc: true}
	}
	return bound{v: o.v}
}

// under is the bound <V ends at: below every release a partial V stands
// for, or just below V.
func (o operand) under() bound {
	if o.partial {
		return bound{v: atFloor(o.v)}
	}
	return bound{v: o.v}
}

// atMost is the bound <=V ends at: past the releases a partial V stands
// for, or at V.
func (o operand) atMost() bound {
	if o.partial {
		return o.past()
	}
	return o.at()
}

// caretAt is the number ^ keeps: the first that is not 0, among those
// written; when all are 0, the last one written.
func (o operand) caretAt() int {
	for k := range o.n {
		if o.v.release[k] != "0" {
			return k
		}
	}
	return o.n - 1
}

// Satisfies reports whether v satisfies r. A partial version satisfies r
// when some release it stands for does: 20 satisfies ^20.1. A pre-release
// satisfies an alternative only when one of its comparators names a
// pre-release of the same numbers, as npm and Cargo decide, so
// 2.0.0-beta.1 does not satisfy >=1.0.0; under PEP 440 and RubyGems, when
// any comparator names a pre-release; under Go, whenever its intervals
// hold it.
func Satisfies(v Version, r Range) bool {
	for _, a := range r.alts {
		if a.satisfiedBy(v) {
			return true
		}
	}
	return false
}

// satisfiedBy reports whether v satisfies the alternative a, by the rule
// Satisfies gives.
func (a alternative) satisfiedBy(v Version) bool {
	if v.Partial() {
		return partialIn(v, a.ivs)
	}
	if !slices.ContainsFunc(a.ivs, func(iv interval) bool { return iv.contains(v) }) {
		return false
	}
	switch {
	case !v.prerelease(), a.rule == preFree, a.rule == preNamed && a.namesPre:
		return true
	case a.rule == preSame && slices.ContainsFunc(a.named, func(n Version) bool { return cmpRelease(n, v) == 0 }):
		return true
	}
	return false
}

// partialIn reports whether some release the partial version v stands
// for is in ivs.
func partialIn(v Version, ivs []interval) bool {
	lo := bound{v: v, inc: true}
	own := []interval{{lo: lo, hi: bound{v: atFloor(bump(v, len(v.release)-1))}}}
	for _, iv := range intersectAll(ivs, own) {
		if iv.holdsRelease() {
			return true
		}
	}
	return false
}

// point is a place on the line of versions, between which the intervals of
// Overlap are half open: just before a version, or just after it, or past
// either end of the line.
type point struct {
	inf   int      // -1 below every version, 1 above every version
	v     *Version // nil when inf is not 0
	after bool
}

func cmpPoint(a, b point) int {
	if a.inf != 0 || b.inf != 0 {
		return cmpInt(a.inf, b.inf)
	}
	if c := Compare(*a.v, *b.v); c != 0 {
		return c
	}
	switch {
	case a.after == b.after:
		return 0
	case a.after:
		return 1
	}
	return -1
}

// startOf is where an interval with lower bound b starts: at b's version
// when it holds it, else just after it.
func startOf(b *bound) point {
	if b.inf {
		return point{inf: -1}
	}
	return point{v: &b.v, after: !b.inc}
}

// endOf is where an interval with upper bound b ends: just after b's
// version when it holds it, else just before it.
func endOf(b *bound) point {
	if b.inf {
		return point{inf: 1}
	}
	return point{v: &b.v, after: b.inc}
}

// boundAt is the bound of an interval that starts (lower) or ends at p.
func boundAt(p point, lower bool) bound {
	if p.inf != 0 {
		return bound{inf: true}
	}
	return bound{v: *p.v, inc: p.after != lower}
}

// Overlap reports whether one release satisfies every range of rs, which
// takes at least one.
func Overlap(rs []Range) bool {
	return len(rs) > 0 && Overlapping(rs) == len(rs)
}

// Overlapping is the length of the longest prefix of rs whose ranges one
// release satisfies: len(rs) when they overlap, else the index of the first
// range at which the ranges before it stop overlapping, 0 when that is the
// first, which no release satisfies alone. It sorts the ends of every
// interval once and sweeps them, keeping for each stretch between two ends
// the number of leading ranges that cover it; the longest prefix is the
// largest such number over the stretches that hold a release. A Fenwick
// tree over the ranges gives that number in time logarithmic in their
// count, so the sweep takes time in proportion to the intervals times
// their logarithm, as one sort of them does, and searches no prefix again.
// An event points at its bound rather than copying it, so the sweep holds
// 24 bytes for each end.
func Overlapping(rs []Range) int {
	events := rangeEvents(rs)
	slices.SortFunc(events, cmpEvent)
	// Go has no rule for pre-releases, so under it any version is one a
	// range may share.
	free := !slices.ContainsFunc(rs, func(r Range) bool { return r.syn != Go })
	sw := newSweep(len(rs))
	longest := 0
	for i := 0; i < len(events); {
		at := events[i].at
		if i = sw.through(events, i); i == len(events) {
			break
		}
		// Only a count above longest can lengthen the prefix. The test is
		// written without < or >, whose boundary mutant would only read a
		// stretch again and could not fail a test.
		if lead := sw.covered.leading(); max(lead, longest) != longest && stretchHolds(free, at, events[i].at) {
			if longest = lead; longest == len(rs) {
				return longest
			}
		}
	}
	return longest
}

// event is an end of an interval of the range rng, the place it starts
// or ends at, for Overlapping's sweep.
type event struct {
	at    point
	rng   int32
	start bool
}

// rangeEvents is the ends of every interval of rs, two for each.
func rangeEvents(rs []Range) []event {
	n := 0
	for _, r := range rs {
		for _, a := range r.alts {
			n += 2 * len(a.ivs)
		}
	}
	events := make([]event, 0, n)
	for i := range rs {
		for _, a := range rs[i].alts {
			for k := range a.ivs {
				iv := &a.ivs[k]
				events = append(events, event{startOf(&iv.lo), int32(i), true}, event{endOf(&iv.hi), int32(i), false})
			}
		}
	}
	return events
}

// cmpEvent orders events by place, and an end before a start at one
// place: the intervals are half open.
func cmpEvent(x, y event) int {
	if c := cmpPoint(x.at, y.at); c != 0 {
		return c
	}
	switch {
	case x.start == y.start:
		return 0
	case x.start:
		return 1
	}
	return -1
}

// sweep is Overlapping's state. depth counts, for each range, its
// intervals open at the sweep; the alternatives of one range may overlap,
// so a range covers a stretch when its depth is above 0. covered marks
// those ranges.
type sweep struct {
	depth   []int32
	covered *prefixTree
}

func newSweep(n int) sweep {
	return sweep{depth: make([]int32, n), covered: newPrefixTree(n)}
}

// through applies the events from i on that are at the place of event i,
// and is the index of the first event after them.
func (sw sweep) through(events []event, i int) int {
	at := events[i].at
	for i < len(events) && cmpPoint(events[i].at, at) == 0 {
		sw.apply(events[i])
		i++
	}
	return i
}

// apply opens or closes an interval of the range of e.
func (sw sweep) apply(e event) {
	if e.start {
		if sw.depth[e.rng] == 0 {
			sw.covered.add(int(e.rng), 1)
		}
		sw.depth[e.rng]++
		return
	}
	sw.depth[e.rng]--
	if sw.depth[e.rng] == 0 {
		sw.covered.add(int(e.rng), -1)
	}
}

// stretchHolds reports whether the stretch from at to next, the place of
// the next event, holds a version a range may share: a release, or, when
// free, any version.
func stretchHolds(free bool, at, next point) bool {
	iv := interval{lo: boundAt(at, true), hi: boundAt(next, false)}
	return free && !iv.empty() || iv.holdsRelease()
}

// prefixTree is a Fenwick tree over n flags, each 0 or 1, that says how
// many flags from the first on are all 1.
type prefixTree struct {
	sums []int // sums[k] is the sum of the flags k-(k&-k) to k-1
	top  int   // the largest power of two no greater than n
}

func newPrefixTree(n int) *prefixTree {
	top := 1
	for top*2 <= n {
		top *= 2
	}
	return &prefixTree{sums: make([]int, n+1), top: top}
}

// add adds d to flag i.
func (t *prefixTree) add(i int, d int) {
	for k := i + 1; k < len(t.sums); k += k & -k {
		t.sums[k] += d
	}
}

// leading is the number of flags from the first on that are all 1. It
// descends the tree once: the block of step flags after the first pos is
// all 1 when its sum is step.
func (t *prefixTree) leading() int {
	pos := 0
	for step := t.top; step > 0; step /= 2 {
		if next := pos + step; next < len(t.sums) && t.sums[next] == step {
			pos = next
		}
	}
	return pos
}
