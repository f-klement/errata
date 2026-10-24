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
	if v, err := ParseVersion(s); err == nil {
		return Value{version: &v}, nil
	}
	r, err := ParseRange(s)
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
// one; real ranges hold one to a few. The README states
// the time the bound allows, measured with hostile ranges.
const MaxIntervals = 1_000_000

// maxIntervals is the bound a Reader applies; tests lower it.
var maxIntervals = MaxIntervals

// Reader parses the values of one comparison and counts the intervals of
// their ranges. It refuses the value that takes the count past
// MaxIntervals, before any value after it is parsed, so the parse, the
// sweep in Overlap and the tests in Satisfies all stay within the bound.
type Reader struct {
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
	v, err := Parse(s)
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
	// when the values hold, and when ranges alone do not overlap, since no
	// pair is to blame.
	First, Second int
}

// Agree decides whether values agree, as compare: semver reads them:
//
//   - every two versions agree at the precision each states (AgreeAt);
//   - every version satisfies every range (Satisfies);
//   - with no version among the values, one release satisfies every range
//     (Overlap).
//
// Versions that agree pairwise are equal where they state the same numbers,
// so Agree compares each with the first version stated in full and with the
// longest partial one, and each range with those two; its time is linear in
// the values' length, but for the sort in Overlap. A Reader bounds the
// intervals it is given.
func Agree(values []Value) Outcome {
	// The first version stated in full and the longest partial one, by
	// index, with the version itself.
	full, partial := -1, -1
	var fullV, partialV *Version
	fail := func(a, b int) Outcome { return Outcome{First: min(a, b), Second: max(a, b)} }
	for i, v := range values {
		if v.version == nil {
			continue
		}
		if fullV != nil && !AgreeAt(*fullV, *v.version) {
			return fail(full, i)
		}
		if partialV != nil && !AgreeAt(*partialV, *v.version) {
			return fail(partial, i)
		}
		switch {
		case !v.version.Partial():
			if fullV == nil {
				full, fullV = i, v.version
			}
		case partialV == nil || len(v.version.release) > len(partialV.release):
			partial, partialV = i, v.version
		}
	}
	if full < 0 && partial < 0 {
		var rs []Range
		for _, v := range values {
			rs = append(rs, *v.rng)
		}
		return Outcome{Holds: Overlap(rs), OnlyRanges: true, First: -1, Second: -1}
	}
	for j, v := range values {
		if v.rng == nil {
			continue
		}
		if fullV != nil && !Satisfies(*fullV, *v.rng) {
			return fail(full, j)
		}
		if partialV != nil && !Satisfies(*partialV, *v.rng) {
			return fail(partial, j)
		}
	}
	return Outcome{Holds: true, First: -1, Second: -1}
}
