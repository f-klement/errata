package rule

import (
	"context"
	"fmt"
	"regexp"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/decls"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/interpreter"
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
//     accident. So does a regex past the bounds that keep each step of
//     its parse, compile and match short (renovate_regex.go).
//   - Any other entry is a glob, which Renovate hands to minimatch with dot
//     and nocase set, and errata to internal/minimatch, a port of the
//     minimatch Renovate pins. A glob on which minimatch throws makes the
//     rule unknown, as do the port's bounds (internal/minimatch).
//
// renovate_test.go holds cases, each checked against Renovate's own
// function (testdata/renovate-oracle.mjs).
const renovateMatch = "renovate_match"

// renovateOverload is the overload's id, which the cost tracker names.
const renovateOverload = "errata_renovate_match_string_string"

// The hidden function renovateThroughLog puts in place of each
// renovate_match call, and its overload. A name that starts with @ cannot
// be written in an expression.
const (
	renovateHiddenFunction = "@errata_renovate_match"
	renovateHiddenOverload = "errata_renovate_match_hidden"
)

// renovateHelpers declares renovate_match, and the hidden function that
// runs it under the evaluation's context and through its pattern cache.
func renovateHelpers() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function(renovateMatch,
			cel.Overload(renovateOverload, []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
				cel.BinaryBinding(renovateBinding))),
		cel.Function(renovateHiddenFunction,
			cel.Overload(renovateHiddenOverload, []*cel.Type{cel.DynType, cel.DynType, logType}, cel.BoolType,
				cel.FunctionBinding(renovateHiddenBinding))),
	}
}

// renovateCost charges the hidden call by hiddenCallCost. CEL adds a
// call's cost as the call returns and checks the cost limit then, also
// between two calls of one comprehension step, where it does not look at
// the timeout. renovate_match itself has no cost of its own: every call of
// it is rewritten as the hidden call before a program is planned
// (renovateThroughLog), and only the hidden call carries the log its
// binding stores the price on.
func renovateCost() []interpreter.CostTrackerOption {
	return []interpreter.CostTrackerOption{
		interpreter.OverloadCostTracker(renovateHiddenOverload, func(args []ref.Val, _ ref.Val) *uint64 {
			cost := hiddenCallCost(args)
			return &cost
		}),
	}
}

// hiddenCallCost is the price the call's binding stored on the log, its
// third argument, which it clears. CEL charges only a call it ran, and
// renovateThroughLog passes every call the log variable, which Eval binds
// to the evaluation's log, so the price is that of the call just run.
// Since it is cleared, a tracker run for a call whose binding did not run,
// as cel-go would run one for a non-strict call with an error argument,
// reads 0 and never the price of the call before it
// (TestRenovateMatchChargesNoPriceForAnErrorArgument).
func hiddenCallCost(args []ref.Val) uint64 {
	log, ok := args[2].(*errorLog)
	if !ok {
		return 0
	}
	cost := log.cost
	log.cost = 0
	return cost
}

// renovateThroughLog rewrites every renovate_match(s, p) as
// @errata_renovate_match(s, p, log). The evaluation's log carries its
// context, so a call stops at the timeout (ADR 0016), and its pattern cache,
// so an entry compiles once in an evaluation (ADR 0014). CEL looks at the
// timeout only between comprehension steps, and one step can hold about
// 150 calls. The rewrite changes no operand and no order, so the notes and
// short circuit evaluation are as they were.
func renovateThroughLog(env *cel.Env, checked *cel.Ast) (*cel.Ast, error) {
	optimizer, err := cel.NewStaticOptimizer(renovateRewriter{})
	if err != nil {
		return nil, err
	}
	rewritten, issues := optimizer.Optimize(env, checked)
	return rewritten, issues.Err()
}

// renovateAllThroughLog refuses a program in which a renovate_match call
// is left that renovateThroughLog did not rewrite. Such a call would run
// renovateBinding, under no context, through no pattern cache, and charged
// only CEL's default of 1, since renovate_match has no cost of its own.
func renovateAllThroughLog(rewritten *cel.Ast) error {
	left := celast.MatchDescendants(celast.NavigateAST(rewritten.NativeRep()), celast.FunctionMatcher(renovateMatch))
	if len(left) > 0 {
		return fmt.Errorf("the rewrite through the evaluation log left %s uncharged, calls left: %d; this is errata's error", renovateMatch, len(left))
	}
	return nil
}

type renovateRewriter struct{}

func (renovateRewriter) Optimize(ctx *cel.OptimizerContext, a *celast.AST) *celast.AST {
	for _, e := range celast.MatchDescendants(celast.NavigateAST(a), celast.FunctionMatcher(renovateMatch)) {
		args := e.AsCall().Args()
		if len(args) != 2 || e.AsCall().IsMemberFunction() {
			continue
		}
		e.SetKindCase(ctx.NewCall(renovateHiddenFunction, args[0], args[1], ctx.NewIdent(logVariable)))
	}
	return a
}

// renovateHiddenBinding is renovateBinding under the context of the log,
// its third argument, with each entry compiled through the log's cache. It
// stores the call's price on the log for hiddenCallCost: what the call
// costs, less 1 for the read of the log variable that renovateThroughLog
// adds, so a rule costs what it did before the rewrite. Its first two
// parameters are dyn, so a value typed dyn that is not a string, such as a
// list from an extractor's map, reaches it rather than CEL's guard, which
// would name the hidden function; renovateCall refuses that value with the
// error CEL gives renovate_match.
func renovateHiddenBinding(args ...ref.Val) ref.Val {
	log, ok := args[2].(*errorLog)
	if !ok {
		return types.MaybeNoSuchOverloadErr(args[2])
	}
	v, cost := renovateCall(log.context(), args[0], args[1], log.patternCache())
	log.cost = cost - 1
	return v
}

// renovateWork is what one call did, which renovateWork.cost prices: the
// bytes of the arguments that are strings, whether both are, what its
// pattern did, and whether the evaluation was stopped by the time the call
// returned.
type renovateWork struct {
	s, pattern int
	pair       bool
	match      matchWork
	stopped    bool
}

// cost is ADR 0014's price of the work: 1, the bytes the call reads, and
// for two strings, their product over renovateCostDivisor or a glob's
// match steps over renovateStepDivisor where that is more,
// the compile cost of an entry the call compiled (renovateCompileWeight
// for each byte of a glob, regexCompileCost for a regex), and the string's
// bytes times the instructions of a regex that ran over
// renovateRegexDivisor. The product counts a glob's match, which grows
// with the string times the pattern, and the steps count it where a class
// or an extglob makes it longer. The compile cost counts the compile, and
// the last term RE2's match, which grows with the string times the
// program. A call that returned after the evaluation was
// stopped costs only 1 and its bytes, so the calls that stop at once do not
// reach the cost limit, and the rule is unknown on the timeout, which is
// what stopped it. That holds for a call that still ran to its verdict,
// since the rule is unknown on the timeout all the same.
func (w renovateWork) cost() uint64 {
	cost := 1 + uint64(w.s) + uint64(w.pattern) //nolint:gosec // lengths, never below 0
	if w.stopped || !w.pair {
		return cost
	}
	match := uint64(w.s) * uint64(w.pattern) / renovateCostDivisor //nolint:gosec // lengths, never below 0
	match = max(match, uint64(w.match.steps)/renovateStepDivisor)  //nolint:gosec // steps are minimatch.MaxSteps at most, and never below 0
	cost += match
	if w.match.compiled {
		cost += w.match.compileCost
	}
	return cost + uint64(w.s)*uint64(w.match.insts)/renovateRegexDivisor //nolint:gosec // a length and a count, never below 0
}

// renovateCostDivisor scales the product of a call's two lengths into its
// cost. The slowest match measured on a 4 core workstation, 1000 bytes
// against a glob of 1003 with three classes of 165 code points, ran 34 ms
// before minimatch.MaxSteps stopped it; at 64, CostLimit pays for 63 such
// calls, about 2.1 s, inside EvalTimeout. A path of 32 bytes against a
// pattern of 38 costs 90 and took 29 to 38 us.
const renovateCostDivisor = 64

// renovateStepDivisor scales the steps a glob's match took into its cost,
// where that is more than the product of the lengths: x, then 16 of @(|),
// then a class, spends every one of MaxSteps against xy, and only its
// first call in an evaluation pays its compile (patternCache). A match that
// spends every step costs 16384 at 8, so CostLimit pays for about 61 of
// them. minimatch charges a class test by its work, so a step takes about
// as long whatever the class: through the CI runner on the build node,
// 190 such calls in one step ended on the cost limit after 0.47 to 1.15 s
// over the classes measured, the slowest [:print:] and 60 code points apart against
// xⒶ. A compile charged at every call stopped 450 code points of the
// class after 2.22 s on the same node, before the class merged them.
//
// The charge raises the cost of an ordinary glob whose match runs a regex
// with an extglob, a class or a *, where it takes more steps over 8 than
// the product over 64: of 26 ordinary globs against 13 paths, 57 of the
// 338 pairs cost more, by 1 to 8 against the paths of 205 bytes or less,
// and by up to 42 percent against strings of 1000 bytes (**/*([[:word:]]).yml costs 1896
// where the product says 1333). The other pairs, and ADR 0014's example,
// cost the product (TestRenovateMatchChargesAnOrdinaryGlobByItsSteps).
const renovateStepDivisor = 8

// renovateCompileWeight is the cost of each byte of a glob, for compiling
// it. Through the CI runner on a 4 core node, the slowest compile, a glob
// of 1022 bytes whose braces expand to 16 globs of 249 nested extglobs,
// took 81 ms, and a short one, 168 bytes of six brace sets, two negations
// and 32 nested extglobs, took 15.9 ms, the most for each byte. At 18 the
// first costs about 19450 and the second about 3200, so CostLimit pays for
// about 52 or 312 such calls: 4.2 s or 5 s of compiling. CEL looks at the
// timeout only between comprehension steps; the cost limit stops the calls
// of one step before the timeout, and each call stops at the timeout
// itself after it (renovateThroughLog). A glob of 38 bytes costs 684 more
// for it, once in an evaluation (patternCache).
const renovateCompileWeight = 18

// isRegex is whether Renovate reads pattern as a regex between slashes.
func isRegex(pattern string) bool {
	return regexStart.MatchString(pattern) && regexEnd.MatchString(pattern)
}

// renovateBinding is renovate_match's binding, which a plain program of
// the declaration runs; every call in an assert is the hidden call. An
// argument over MaxArgument, and an entry or string
// renovateMatchesCompiling refuses, are errors that name it.
func renovateBinding(s, p ref.Val) ref.Val {
	v, _ := renovateCall(context.Background(), s, p, nil)
	return v
}

// renovateCall is renovate_match(s, p), stopped once ctx is done, with each
// entry compiled through cache, and what the call costs, priced from the
// work it did where it did it.
func renovateCall(ctx context.Context, s, p ref.Val, cache *patternCache) (ref.Val, uint64) {
	in, inString := s.(types.String)
	pattern, patternString := p.(types.String)
	w := renovateWork{s: len(in), pattern: len(pattern), pair: inString && patternString}
	v := decls.MaybeNoSuchOverload(renovateMatch, s, p)
	if w.pair {
		v = renovateVerdict(ctx, in, pattern, cache, &w)
	}
	w.stopped = ctx.Err() != nil
	return v, w.cost()
}

// renovateVerdict is renovateCall's verdict for two strings, with the work
// of its pattern recorded in w.
func renovateVerdict(ctx context.Context, in, pattern types.String, cache *patternCache, w *renovateWork) ref.Val {
	for _, a := range []types.String{in, pattern} {
		if len(a) > MaxArgument {
			return types.NewErr("%s: an argument is %d bytes; a helper reads at most %d", renovateMatch, len(a), MaxArgument)
		}
	}
	match, work, err := renovateMatchesCompiling(ctx, string(in), string(pattern), cache)
	w.match = work
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

// renovateMatchesCompiling is matchRegexOrGlob(s, entry), or an error
// where errata cannot compute it as Renovate does, stopped once ctx is
// done, with the entry compiled through cache by the adapter of its form
// (compilePattern), and the work the call did. It looks at ctx before it
// starts; each adapter looks at it again as it compiles and matches.
func renovateMatchesCompiling(ctx context.Context, s, entry string, cache *patternCache) (bool, matchWork, error) {
	if err := ctx.Err(); err != nil {
		return false, matchWork{}, fmt.Errorf("the evaluation was stopped before the call: %w", err)
	}
	return cache.match(ctx, s, entry)
}
