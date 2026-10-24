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

// alternative is one side of a range's ||: comparators that must all hold.
type alternative struct {
	// ivs are the versions the comparators admit together, sorted and
	// disjoint.
	ivs []interval
	// named are the comparators' versions that are pre-releases. A
	// pre-release satisfies the alternative only when one of them has its
	// numbers, as npm and Cargo decide.
	named []Version
}

// Range is a version range: alternatives joined by ||, each a list of
// comparators that must all hold.
type Range struct {
	alts []alternative
}

// String is r as the intervals it admits: each alternative as >=, >, <= and
// < comparators, = for a single version and * for every version, the
// alternatives joined by ||. A bound npm writes with -0, such as <21.0.0-0,
// lies below every pre-release of 21.0.0.
func (r Range) String() string {
	var parts []string
	for _, a := range r.alts {
		if len(a.ivs) == 0 {
			parts = append(parts, "(no version)")
		}
		for _, iv := range a.ivs {
			parts = append(parts, iv.String())
		}
	}
	return strings.Join(parts, " || ")
}

func (iv interval) String() string {
	switch {
	case iv.lo.inf && iv.hi.inf:
		return "*"
	case !iv.lo.inf && !iv.hi.inf && Compare(iv.lo.v, iv.hi.v) == 0:
		return "=" + full(iv.lo.v)
	}
	var parts []string
	if !iv.lo.inf {
		op := ">"
		if iv.lo.inc {
			op = ">="
		}
		parts = append(parts, op+full(iv.lo.v))
	}
	if !iv.hi.inf {
		op := "<"
		if iv.hi.inc {
			op = "<="
		}
		parts = append(parts, op+full(iv.hi.v))
	}
	return strings.Join(parts, " ")
}

// full is v written with at least three numbers, as a range's bounds are.
func full(v Version) string {
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
	if len(s) > MaxLength {
		return Range{}, errTooLong
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return Range{}, errors.New("it is empty")
	}
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
		return fold([]string{">=" + f[0], "<=" + f[2]})
	}
	var tokens []string
	for _, f := range strings.Fields(strings.ReplaceAll(s, ",", " ")) {
		// An operator written apart from its version, as in ~> 3.2 or
		// >= 20, joins the version after it.
		if n := len(tokens); n > 0 && slices.Contains(operators, tokens[n-1]) {
			tokens[n-1] += f
			continue
		}
		tokens = append(tokens, f)
	}
	return fold(tokens)
}

// fold reads the comparators of one alternative and returns what they admit
// together. Every comparator but != narrows one interval, so fold keeps the
// greatest lower bound and the least upper bound; each != is a hole, and
// the holes are sorted once and cut out of that interval in one pass. Its
// time is k log k in the comparators, never their square.
func fold(tokens []string) (alternative, error) {
	var (
		a     alternative
		keep  = everything
		holes []interval
		seen  map[string]bool
	)
	for _, t := range tokens {
		iv, hole, named, err := comparator(t)
		if err != nil {
			return alternative{}, err
		}
		if hole != nil {
			holes = append(holes, *hole)
		} else {
			keep = keep.intersect(iv)
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
	epoch := num("0")
	if e, rest, ok := strings.Cut(t, "!"); ok {
		if n, isNum := parseNum(e); isNum {
			epoch, t = n, rest
		}
	}
	parts := strings.Split(t, ".")
	n := 0
	for n < len(parts) && parts[n] != "" && strings.Trim(parts[n], "0123456789") == "" {
		n++
	}
	if n < len(parts) && isWild(parts[n]) {
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
	v, err := ParseVersion(s)
	if err != nil {
		return operand{}, fmt.Errorf("%s is not a version: %w", quoteToken(s), err)
	}
	return operand{v: v, n: len(v.release), partial: v.Partial()}, nil
}

func isWild(p string) bool { return p == "x" || p == "X" || p == "*" }

// comparator reads one comparator, such as >=1.2.3, ^1.2 or ~> 3.2. It
// returns the interval it admits, or for != the hole it takes out, and its
// version when that is a pre-release.
func comparator(t string) (interval, *interval, *Version, error) {
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
		return interval{}, nil, nil, fmt.Errorf("%s: === compares text, which errata does not read as a version", quoteToken(t))
	case rest == "":
		return interval{}, nil, nil, fmt.Errorf("%s: the operator has no version after it", quoteToken(t))
	}
	o, err := parseOperand(rest)
	if err != nil {
		return interval{}, nil, nil, err
	}
	var named *Version
	if o.v.prerelease() {
		named = &o.v
	}
	if o.n == 0 {
		// *, x or X: every version, after any operator that admits it.
		switch op {
		case "", "=", "==", ">=", "<=", "^", "~":
			return everything, nil, nil, nil
		}
		return interval{}, nil, nil, fmt.Errorf("%s: %s before a wildcard admits no version", quoteToken(t), op)
	}
	if o.wildcard && (op == "~>" || op == "~=") {
		return interval{}, nil, nil, fmt.Errorf("%s: %s takes a version, not a wildcard", quoteToken(t), op)
	}
	if op == "!=" {
		hole := interval{lo: bound{v: o.v, inc: true}, hi: bound{v: o.v, inc: true}}
		if o.wildcard {
			hole.hi = bound{v: atFloor(bump(o.v, o.n-1))}
		}
		return interval{}, &hole, named, nil
	}
	iv, err := admits(op, o, t)
	return iv, nil, named, err
}

// admits is what op with the operand o admits, for every operator but !=.
func admits(op string, o operand, t string) (interval, error) {
	v := o.v
	at := bound{v: v, inc: true}
	// past is the bound below every version after the releases o stands
	// for, when o is partial. It is made only where an operator needs it,
	// since every bump allocates.
	past := func() bound { return bound{v: atFloor(bump(v, o.n-1))} }
	upTo := func(i int) interval { return interval{lo: at, hi: bound{v: atFloor(bump(v, i))}} }
	from := func(lo bound) interval { return interval{lo: lo, hi: bound{inf: true}} }
	below := func(hi bound) interval { return interval{lo: bound{inf: true}, hi: hi} }
	switch op {
	case "", "=":
		if o.partial {
			return interval{lo: at, hi: past()}, nil
		}
		return interval{lo: at, hi: at}, nil
	case "==":
		if o.wildcard {
			return interval{lo: at, hi: past()}, nil
		}
		return interval{lo: at, hi: at}, nil
	case ">=":
		return from(at), nil
	case ">":
		if o.partial {
			return from(bound{v: bump(v, o.n-1), inc: true}), nil
		}
		return from(bound{v: v}), nil
	case "<":
		if o.partial {
			return below(bound{v: atFloor(v)}), nil
		}
		return below(bound{v: v}), nil
	case "<=":
		if o.partial {
			return below(past()), nil
		}
		return below(at), nil
	case "^":
		// Up to the next change of the first number that is not 0, among
		// those written; when all are 0, of the last one written.
		i := o.n - 1
		for k := range o.n {
			if v.release[k] != "0" {
				i = k
				break
			}
		}
		return upTo(i), nil
	case "~":
		return upTo(min(1, o.n-1)), nil
	case "~=":
		if o.n < 2 {
			return interval{}, fmt.Errorf("%s: ~= needs a version of at least two numbers", quoteToken(t))
		}
		return upTo(o.n - 2), nil
	}
	// The one operator left is ~>.
	return upTo(max(0, o.n-2)), nil
}

// Satisfies reports whether v satisfies r. A partial version satisfies r
// when some release it stands for does: 20 satisfies ^20.1. A pre-release
// satisfies an alternative only when one of its comparators names a
// pre-release of the same numbers, as npm and Cargo decide, so
// 2.0.0-beta.1 does not satisfy >=1.0.0.
func Satisfies(v Version, r Range) bool {
	for _, a := range r.alts {
		if v.Partial() {
			lo := bound{v: v, inc: true}
			own := []interval{{lo: lo, hi: bound{v: atFloor(bump(v, len(v.release)-1))}}}
			for _, iv := range intersectAll(a.ivs, own) {
				if iv.holdsRelease() {
					return true
				}
			}
			continue
		}
		if !slices.ContainsFunc(a.ivs, func(iv interval) bool { return iv.contains(v) }) {
			continue
		}
		if !v.prerelease() || slices.ContainsFunc(a.named, func(n Version) bool { return cmpRelease(n, v) == 0 }) {
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

// Overlap reports whether one release satisfies every range of rs. It sorts
// the ends of every interval once and sweeps them, counting the ranges that
// cover each stretch between two ends, so it takes time in proportion to the
// intervals times their logarithm. An event points at its bound rather than
// copying it, so the sweep holds 24 bytes for each end.
func Overlap(rs []Range) bool {
	type event struct {
		at    point
		rng   int32
		start bool
	}
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
	slices.SortFunc(events, func(x, y event) int {
		if c := cmpPoint(x.at, y.at); c != 0 {
			return c
		}
		switch {
		case x.start == y.start:
			return 0
		case x.start:
			return 1 // an end before a start at one place: the intervals are half open
		}
		return -1
	})
	// depth counts, for each range, its intervals open at the sweep; the
	// alternatives of one range may overlap, so a range covers a stretch
	// when its depth is above 0.
	depth := make([]int32, len(rs))
	covered := 0
	for i := 0; i < len(events); {
		at := events[i].at
		for i < len(events) && cmpPoint(events[i].at, at) == 0 {
			e := events[i]
			if e.start {
				if depth[e.rng] == 0 {
					covered++
				}
				depth[e.rng]++
			} else {
				depth[e.rng]--
				if depth[e.rng] == 0 {
					covered--
				}
			}
			i++
		}
		if covered == len(rs) && i < len(events) {
			iv := interval{lo: boundAt(at, true), hi: boundAt(events[i].at, false)}
			if iv.holdsRelease() {
				return true
			}
		}
	}
	return false
}
