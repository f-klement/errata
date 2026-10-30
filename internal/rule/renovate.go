package rule

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/interpreter"

	"github.com/f-klement/errata/internal/minimatch"
)

// renovate_match(s, p) is whether Renovate's matchRegexOrGlob(s, p) holds:
// how Renovate matches a string against one entry of managerFilePatterns,
// matchPackageNames, matchDepNames and the other keys its documentation
// calls string pattern matching. Renovate's lib/util/string-match.ts reads
// the entry in three ways:
//
//   - "*" matches every string.
//   - An entry that starts with / or !/ and ends with / or /i is a regex,
//     the text between the slashes, with the i flag when the entry ends in
//     i. A leading ! inverts the match. Renovate compiles it with the re2
//     module where that loads, as it does in its images, so errata reads it
//     with Go's RE2, searched anywhere in s, with ^ and $ at the ends of s.
//     A regex RE2 does not compile, such as a lookahead, makes the rule
//     unknown: Renovate would read the whole entry as a glob instead, and
//     a JavaScript pattern Go's RE2 lacks would then pass or fail by
//     accident.
//   - Any other entry is a glob, which Renovate hands to minimatch with dot
//     and nocase set, and errata to internal/minimatch, a port of the
//     minimatch Renovate pins. A glob on which minimatch throws makes the
//     rule unknown, as do the port's bounds (minimatch.Match).
//
// renovate_test.go holds cases, each checked against Renovate's own
// function (testdata/renovate-oracle.mjs).
const renovateMatch = "renovate_match"

// renovateOverload is the overload's id, which the cost tracker names.
const renovateOverload = "errata_renovate_match_string_string"

// renovateHelpers declares renovate_match.
func renovateHelpers() []cel.EnvOption {
	return []cel.EnvOption{cel.Function(renovateMatch,
		cel.Overload(renovateOverload, []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
			cel.BinaryBinding(renovateBinding)))}
}

// renovateCost charges renovate_match 1, the bytes it reads, and their
// product over renovateCostDivisor, so the cost limit counts its compiling,
// which grows with the pattern, and its matching, which grows with the
// string times the pattern.
func renovateCost() interpreter.CostTrackerOption {
	return interpreter.OverloadCostTracker(renovateOverload, func(args []ref.Val, _ ref.Val) *uint64 {
		cost := uint64(1)
		var lens []uint64
		for _, a := range args {
			if s, ok := a.(types.String); ok {
				cost += uint64(len(s))
				lens = append(lens, uint64(len(s)))
			}
		}
		if len(lens) == 2 {
			cost += lens[0] * lens[1] / renovateCostDivisor
		}
		return &cost
	})
}

// renovateCostDivisor scales the product of a call's two lengths into its
// cost. The slowest match measured on a 4 core workstation, 1000 bytes
// against a glob of 1003 with three classes of 165 code points, ran 34 ms
// before minimatch.MaxSteps stopped it; at 64, CostLimit pays for 63 such
// calls, about 2.1 s, inside EvalTimeout. A path of 32 bytes against a
// pattern of 38 costs 90 and took 29 to 38 us. The product does not charge
// compiling: the slowest compile, a glob of 1022 bytes whose braces expand
// to 16 globs of 249 nested extglobs, took 91 ms against a string of 2
// bytes and costs about 1050, so EvalTimeout stops a rule that repeats such
// a call. CEL looks at the timeout only between comprehension steps, and
// the calls inside one step, about 150 in the longest expression, run on
// after the rule is unknown (ADR 0014, The cost).
const renovateCostDivisor = 64

// renovateBinding is renovate_match's binding: an argument over
// MaxArgument, and an entry or string renovateMatches refuses, are errors
// that name it.
func renovateBinding(s, p ref.Val) ref.Val {
	in, ok := s.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(s)
	}
	pattern, ok := p.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(p)
	}
	for _, a := range []types.String{in, pattern} {
		if len(a) > MaxArgument {
			return types.NewErr("%s: an argument is %d bytes; a helper reads at most %d", renovateMatch, len(a), MaxArgument)
		}
	}
	match, err := renovateMatches(string(in), string(pattern))
	if err != nil {
		return types.NewErr("%s: %v", renovateMatch, err)
	}
	return types.Bool(match)
}

// The two ends of an entry that Renovate reads as a regex, its configValStart
// and configValEnd.
var (
	regexStart = regexp.MustCompile(`^!?/`)
	regexEnd   = regexp.MustCompile(`/i?$`)
)

// renovateMatches is matchRegexOrGlob(s, pattern), or an error where errata
// cannot compute it as Renovate does.
func renovateMatches(s, pattern string) (bool, error) {
	if pattern == "*" {
		return true, nil
	}
	if regexStart.MatchString(pattern) && regexEnd.MatchString(pattern) {
		// Renovate strips the start, then the end of what is left, so
		// "/" is the empty regex and "/i" the regex i with the i flag.
		body := regexEnd.ReplaceAllString(regexStart.ReplaceAllString(pattern, ""), "")
		if strings.HasSuffix(pattern, "i") {
			body = "(?i)" + body
		}
		re, err := regexp.Compile(body)
		if err != nil {
			return false, fmt.Errorf("%q is written as a regex between slashes, and RE2 does not compile it: %w", pattern, err)
		}
		return re.MatchString(s) != strings.HasPrefix(pattern, "!"), nil
	}
	match, err := minimatch.Match(s, pattern)
	var thrown *minimatch.SyntaxError
	switch {
	case errors.As(err, &thrown):
		return false, fmt.Errorf("minimatch throws on the glob %q, as JavaScript refuses the regex it builds: %w", pattern, err)
	case err != nil:
		return false, fmt.Errorf("the glob %q is not matched against %q: %w", pattern, s, err)
	}
	return match, nil
}
