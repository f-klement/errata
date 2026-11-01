package rule

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/decls"
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
// runs it under the evaluation's context and through its glob cache.
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

// renovateCost charges renovate_match by renovateCallCost, and the hidden
// call by hiddenCallCost. CEL adds a call's cost as the call returns and
// checks the cost limit then, also between two calls of one comprehension
// step, where it does not look at the timeout.
func renovateCost() []interpreter.CostTrackerOption {
	return []interpreter.CostTrackerOption{
		interpreter.OverloadCostTracker(renovateOverload, func(args []ref.Val, result ref.Val) *uint64 {
			cost := renovateCallCost(args, result)
			return &cost
		}),
		interpreter.OverloadCostTracker(renovateHiddenOverload, func(args []ref.Val, result ref.Val) *uint64 {
			cost := hiddenCallCost(args, result)
			return &cost
		}),
	}
}

// hiddenCallCost is what renovate_match costs, less 1 for the read of the
// log variable that renovateThroughLog adds, and less the compile when an
// earlier call of the evaluation paid for it (globCache). So a rule that
// compiles each of its globs once costs what it did. A call that ended
// after the timeout costs only its bytes, and pays for no compile, so the
// calls that stop at once do not reach the cost limit, and the rule is
// unknown on the timeout, which is what stopped it. That holds for a call
// that still ran to its verdict, since the rule is unknown on the timeout
// all the same.
func hiddenCallCost(args []ref.Val, result ref.Val) uint64 {
	// CEL charges only a call it ran, and renovateThroughLog passes every
	// call the log variable, which Eval binds to the evaluation's log.
	log := args[2].(*errorLog)
	if log.context().Err() != nil {
		return renovateStoppedCost(args)
	}
	return renovateCallCostPaying(args[:2], result, log.globCache().pays, log.steps) - 1
}

// renovateThroughLog rewrites every renovate_match(s, p) as
// @errata_renovate_match(s, p, log). The evaluation's log carries its
// context, so a call stops at the timeout (ADR 0016), and its glob cache,
// so a glob compiles once in an evaluation (ADR 0014). CEL looks at the
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
// its third argument, with each glob compiled through the log's cache. Its
// first two parameters are dyn, so a value typed dyn that is not a string,
// such as a list from an extractor's map, reaches it rather than CEL's
// guard, which would name the hidden function; it refuses that value with
// the error CEL gives renovate_match.
func renovateHiddenBinding(args ...ref.Val) ref.Val {
	log, ok := args[2].(*errorLog)
	if !ok {
		return types.MaybeNoSuchOverloadErr(args[2])
	}
	for _, a := range args[:2] {
		if _, ok := a.(types.String); !ok {
			log.steps = 0
			return decls.MaybeNoSuchOverload(renovateMatch, args[0], args[1])
		}
	}
	v, steps := renovateCall(log.context(), args[0], args[1], log.globCache().compiled)
	log.steps = steps
	return v
}

// renovateCallCost is 1, the bytes a call reads, their product over
// renovateCostDivisor, renovateCompileWeight for each byte of a glob, and
// for a regex that ran, the string's bytes times its program's
// instructions over renovateRegexDivisor. The product counts the matching
// of a glob, which grows with the string times the pattern, the weight its
// compiling, which grows with the glob, and the last term RE2's matching,
// which grows with the string times the program.
func renovateCallCost(args []ref.Val, result ref.Val) uint64 {
	return renovateCallCostPaying(args, result, func(string) bool { return true }, 0)
}

// renovateCallCostPaying is renovateCallCost with the weight for compiling
// a glob charged only where pays says the call pays for it (globCache),
// and a glob's match charged by the steps it took where that is more than
// the product of the lengths.
func renovateCallCostPaying(args []ref.Val, result ref.Val, pays func(glob string) bool, steps int) uint64 {
	cost := uint64(1)
	var strs []string
	for _, a := range args {
		if s, ok := a.(types.String); ok {
			cost += uint64(len(s))
			strs = append(strs, string(s))
		}
	}
	if len(strs) != 2 {
		return cost
	}
	s, pattern := strs[0], strs[1]
	match := uint64(len(s)) * uint64(len(pattern)) / renovateCostDivisor
	match = max(match, uint64(steps)/renovateStepDivisor) //nolint:gosec // steps are minimatch.MaxSteps at most, and never below 0
	cost += match
	switch {
	case isRegex(pattern):
		// A refused regex did not run; its error says why.
		if _, ok := result.(types.Bool); ok {
			if c := compiledRegex(pattern); c.err == nil {
				cost += uint64(len(s)) * uint64(len(c.prog.Inst)) / renovateRegexDivisor
			}
		}
	case pattern != "*" && pays(pattern):
		cost += uint64(len(pattern)) * renovateCompileWeight
	}
	return cost
}

// renovateStoppedCost is the bytes a call reads, what a call that ended
// after the timeout is charged.
func renovateStoppedCost(args []ref.Val) uint64 {
	var cost uint64
	for _, a := range args {
		if s, ok := a.(types.String); ok {
			cost += uint64(len(s))
		}
	}
	return cost
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
// first call in an evaluation pays its compile (globCache). A match that
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
// for it, once in an evaluation (globCache).
const renovateCompileWeight = 18

// isRegex is whether Renovate reads pattern as a regex between slashes.
func isRegex(pattern string) bool {
	return regexStart.MatchString(pattern) && regexEnd.MatchString(pattern)
}

// renovateBinding is renovate_match's binding: an argument over
// MaxArgument, and an entry or string renovateMatchesCompiling refuses,
// are errors that name it.
func renovateBinding(s, p ref.Val) ref.Val {
	v, _ := renovateCall(context.Background(), s, p, minimatch.CompileContext)
	return v
}

// renovateCall is renovate_match(s, p), stopped once ctx is done, with
// each glob compiled by compile, and the steps a glob's match took.
func renovateCall(ctx context.Context, s, p ref.Val, compile func(context.Context, string) *minimatch.Glob) (ref.Val, int) {
	in, ok := s.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(s), 0
	}
	pattern, ok := p.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(p), 0
	}
	for _, a := range []types.String{in, pattern} {
		if len(a) > MaxArgument {
			return types.NewErr("%s: an argument is %d bytes; a helper reads at most %d", renovateMatch, len(a), MaxArgument), 0
		}
	}
	match, steps, err := renovateMatchesCompiling(ctx, string(in), string(pattern), compile)
	if err != nil {
		return types.NewErr("%s: %v", renovateMatch, err), steps
	}
	return types.Bool(match), steps
}

// The two ends of an entry that Renovate reads as a regex, its configValStart
// and configValEnd.
var (
	regexStart = regexp.MustCompile(`^!?/`)
	regexEnd   = regexp.MustCompile(`/i?$`)
)

// renovateMatchesCompiling is matchRegexOrGlob(s, pattern), or an error
// where errata cannot compute it as Renovate does, stopped once ctx is
// done, with a glob compiled by compile, and the steps a glob's match took
// (minimatch.MatchSteps). It looks at ctx before it starts, before a regex runs, and inside
// minimatch's compile and match. Go's regexp takes no context, so a regex
// that has started runs to its end, at most about 120 ms (maxRegexInsts).
func renovateMatchesCompiling(ctx context.Context, s, pattern string, compile func(context.Context, string) *minimatch.Glob) (bool, int, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, fmt.Errorf("the evaluation was stopped before the call: %w", err)
	}
	if pattern == "*" {
		return true, 0, nil
	}
	if isRegex(pattern) {
		c := compiledRegex(pattern)
		var refused errRegexBound
		switch {
		case errors.As(c.err, &refused):
			return false, 0, fmt.Errorf("%q is a regex, and %w", pattern, c.err)
		case c.err != nil:
			return false, 0, fmt.Errorf("%q is written as a regex between slashes, and RE2 does not compile it: %w", pattern, c.err)
		}
		if err := ctx.Err(); err != nil {
			return false, 0, fmt.Errorf("the evaluation was stopped before the regex %q ran: %w", pattern, err)
		}
		return c.re.MatchString(s) != strings.HasPrefix(pattern, "!"), 0, nil
	}
	match, steps, err := compile(ctx, pattern).MatchSteps(ctx, s)
	var thrown *minimatch.SyntaxError
	switch {
	case errors.As(err, &thrown):
		return false, steps, fmt.Errorf("minimatch throws on the glob %q, as JavaScript refuses the regex it builds: %w", pattern, err)
	case err != nil:
		return false, steps, fmt.Errorf("the glob %q is not matched against %q: %w", pattern, s, err)
	}
	return match, steps, nil
}
