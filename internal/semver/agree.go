package semver

import "fmt"

// Value is a value agree reads: a version or a range.
type Value struct {
	version *Version
	rng     *Range
}

// Parse reads s as a version, and failing that as a range. A bare version
// such as 1.2.3 or 20 is a version; one with an operator, a wildcard, a
// comma or || is a range. The error says why s is neither.
func Parse(s string) (Value, error) {
	return Any.Parse(s)
}

// Parse reads s as a version of the syntax, and failing that as a range of
// it. So under Cargo a bare 1.35 is a partial version, as rust-version
// writes one, and a requirement reaches a rule with its operator, as the
// dependency extractor writes ^1.35.
func (syn Syntax) Parse(s string) (Value, error) {
	if v, err := syn.ParseVersion(s); err == nil {
		return Value{version: &v}, nil
	}
	r, err := syn.ParseRange(s)
	if err != nil {
		return Value{}, fmt.Errorf("it is neither a version nor a range: %w", err)
	}
	return Value{rng: &r}, nil
}

// IsRange reports whether the value is a range.
func (v Value) IsRange() bool { return v.rng != nil }

// String names what the value parsed as: "version 20.11" or
// "range >=20.1.0 <21.0.0-0".
func (v Value) String() string {
	switch {
	case v.version != nil:
		return "version " + v.version.String()
	case v.rng != nil:
		return "range " + v.rng.String()
	}
	return "nothing"
}

// MaxIntervals is the most intervals the ranges of one comparison may hold,
// counted as a Reader parses them. Each alternative of a range, each piece
// a != leaves, and each release a comparator names a pre-release of, is
// one; real ranges hold one to a few. docs/reference.md#compare states
// the time the bound allows, measured with hostile ranges.
const MaxIntervals = 1_000_000

// maxIntervals is the bound a Reader applies; tests lower it.
var maxIntervals = MaxIntervals

// Reader parses the values of one comparison and counts the intervals of
// their ranges. It refuses the value that takes the count past
// MaxIntervals, before any value after it is parsed, so the parse, the
// sweep in Overlap and the tests in Satisfies all stay within the bound.
type Reader struct {
	// Syntax is the syntax the Reader reads every value in.
	Syntax            Syntax
	values, intervals int
}

// BoundError is a Reader's refusal at MaxIntervals.
type BoundError struct {
	// Values is how many values the Reader had parsed, the refused one
	// included.
	Values int
}

func (e *BoundError) Error() string {
	return fmt.Sprintf("the ranges of the first %d values hold more than %d intervals, the most errata compares in one rule", e.Values, maxIntervals)
}

// Parse is Parse, counting the intervals of a range. Past the bound it
// returns a *BoundError.
func (r *Reader) Parse(s string) (Value, error) {
	r.values++
	v, err := r.Syntax.Parse(s)
	if err != nil || v.rng == nil {
		return v, err
	}
	for _, a := range v.rng.alts {
		// A named pre-release is kept as an interval is, so it counts as one.
		r.intervals += len(a.ivs) + len(a.named)
	}
	if r.intervals > maxIntervals {
		return Value{}, &BoundError{Values: r.values}
	}
	return v, nil
}

// Outcome is what Agree found.
type Outcome struct {
	// Holds is true when the values agree.
	Holds bool
	// OnlyRanges is true when no value was a version, so the ranges were
	// compared with each other.
	OnlyRanges bool
	// First and Second index the first two values found not to agree: two
	// versions, or a version and a range it does not satisfy. Both are -1
	// when the values hold. When ranges alone do not overlap, no pair is
	// to blame: First is -1, and Second is the first range at which the
	// ranges before it, in the values' order, stop overlapping.
	First, Second int
}

// Agree decides whether values agree, as compare: semver reads them:
//
//   - every two versions agree at the precision each states (AgreeAt);
//   - every version satisfies every range (Satisfies);
//   - with no version among the values, one release satisfies every range
//     (Overlapping).
//
// Versions that agree pairwise are equal where they state the same numbers,
// so Agree compares each with the first version stated in full and with the
// longest partial one, and each range with those two; its time is linear in
// the values' length, but for the sort in Overlapping. A Reader bounds the
// intervals it is given.
func Agree(values []Value) Outcome {
	a := anchors{full: -1, partial: -1}
	if out, failed := a.versions(values); failed {
		return out
	}
	if a.full < 0 && a.partial < 0 {
		return rangesAgree(values)
	}
	if out, failed := a.ranges(values); failed {
		return out
	}
	return Outcome{Holds: true, First: -1, Second: -1}
}

// anchors are the versions Agree compares every value with: the first
// version stated in full and the longest partial one, by index, -1 for
// none, with the version itself.
type anchors struct {
	full, partial   int
	fullV, partialV *Version
}

// failAt is the outcome of the values at a and b not agreeing.
func failAt(a, b int) Outcome { return Outcome{First: min(a, b), Second: max(a, b)} }

// versions compares each version of values with the anchors found before
// it, and takes it as an anchor when it is the first in full or the
// longest partial one. It reports the outcome of the first two that do
// not agree, and whether there are such.
func (a *anchors) versions(values []Value) (Outcome, bool) {
	for i, v := range values {
		if v.version == nil {
			continue
		}
		if out, failed := a.agreeWith(i, *v.version); failed {
			return out, true
		}
		a.take(i, v.version)
	}
	return Outcome{}, false
}

// agreeWith compares the version v, value i, with each anchor.
func (a *anchors) agreeWith(i int, v Version) (Outcome, bool) {
	if a.fullV != nil && !AgreeAt(*a.fullV, v) {
		return failAt(a.full, i), true
	}
	if a.partialV != nil && !AgreeAt(*a.partialV, v) {
		return failAt(a.partial, i), true
	}
	return Outcome{}, false
}

// take takes the version v, value i, as the full anchor when it is the
// first stated in full, or as the partial one when it is longer.
func (a *anchors) take(i int, v *Version) {
	switch {
	case !v.Partial():
		if a.fullV == nil {
			a.full, a.fullV = i, v
		}
	case a.partialV == nil || len(v.release) > len(a.partialV.release):
		a.partial, a.partialV = i, v
	}
}

// ranges holds each anchor to each range of values.
func (a *anchors) ranges(values []Value) (Outcome, bool) {
	for j, v := range values {
		if v.rng == nil {
			continue
		}
		if a.fullV != nil && !Satisfies(*a.fullV, *v.rng) {
			return failAt(a.full, j), true
		}
		if a.partialV != nil && !Satisfies(*a.partialV, *v.rng) {
			return failAt(a.partial, j), true
		}
	}
	return Outcome{}, false
}

// rangesAgree is the outcome of values that are ranges alone: they hold
// when one release satisfies every range.
func rangesAgree(values []Value) Outcome {
	var rs []Range
	for _, v := range values {
		rs = append(rs, *v.rng)
	}
	if k := Overlapping(rs); k < len(rs) {
		return Outcome{OnlyRanges: true, First: -1, Second: k}
	}
	return Outcome{Holds: true, OnlyRanges: true, First: -1, Second: -1}
}
