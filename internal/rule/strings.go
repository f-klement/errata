package rule

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/cost"
	"cel.dev/cel-go/common/decls"
	"cel.dev/cel-go/common/functions"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/ext"
)

// assert takes every function of cel-go's strings extension:
//
//	s.split(sep), s.split(sep, n)          a list of the parts of s between each sep
//	list.join(), list.join(sep)            the members of a list(string), with sep between them
//	s.replace(old, new), s.replace(old, new, n)  s with old replaced by new, at most n times
//	s.charAt(i), s.substring(i), s.substring(i, j), s.trim()
//	s.indexOf(t), s.indexOf(t, i), s.lastIndexOf(t), s.lastIndexOf(t, i)
//	s.lowerAscii(), s.upperAscii(), s.reverse()
//	s.format(list), strings.quote(s)
//
// CEL charges each call after it returns, by what it read and the size of its
// result, so the cost limit stops an evaluation only once the call has done
// that work. One call can do far more than the limit pays for:
// v[0].replace('', v[0]) over 1 MB is a string of 1 TB, and v[0].indexOf(v[1])
// compares every character of v[0] with every one of v[1]. Each overload is
// bound to the extension's own function behind a guard that works out first,
// without doing it, what the call would build or read, and refuses a call the
// cost limit would stop by itself once it returned:
//
//   - a string of more than 4 bytes for each unit of the limit (join,
//     replace, strings.quote and format), since CEL charges a string one for
//     each code point, which is at most 4 bytes;
//   - a split into more parts than the limit has units;
//   - a transform (lowerAscii, upperAscii, reverse) or a substring of more
//     code points than the limit has units, since the result has as many;
//   - a read of more than 10 code points for each unit (charAt, substring,
//     trim), since CEL charges a tenth for each, and a search (indexOf,
//     lastIndexOf) whose string's code points times its needle's, at least
//     one, are more.
//
// A smaller call can still exceed the limit, and is done first. The guard
// never refuses what the cost limit would let through, but for format, whose
// size is an upper bound. The bounds follow the cost limit the rule is
// compiled under (stringsUnder), not CostLimit alone.
//
// Every call is rewritten as a hidden call that also takes the evaluation's
// log (stringsThroughLog, as ADR 0016 does for renovate_match), so a guard
// notes its refusal there. A refused call costs what cel-go charges its
// arguments, which pays for what its guard read: in a comprehension the cost
// limit stops refusals as it stops calls, and the rule names the refusal
// ("before it, ..."). The declarations and the functions themselves are
// cel-go's, and stringCosts prices each hidden call as cel-go prices the call
// it replaced, but for format and strings.quote, which are charged what they
// build as well, and a search for the empty string, charged as for a needle
// of one code point.

// stringsVersion is the version of cel-go's strings extension assert loads:
// the first with runtime costs for the functions but format and quote.
const stringsVersion = 5

// stringFunctions are the functions of the extension assert offers. A
// version that adds a function fails TestStringsLeavesOutOnlyWhatItDoesNotOffer
// until the function is listed here with a guard for each overload; until
// then it is declared disabled.
var stringFunctions = []string{
	"charAt", "format", "indexOf", "join", "lastIndexOf", "lowerAscii", "replace", "reverse",
	"split", "strings.quote", "substring", "trim", "upperAscii",
}

// MaxBuilt is the most bytes one call of the extension may build under
// CostLimit, the limit every rule runs under. A string of more bytes
// holds more than CostLimit code points, since a code point is at most 4
// bytes, and CEL charges a string one for each code point, so the guard
// refuses only what the cost limit would stop anyway. boundsUnder works it out
// for any limit; TestStringBoundsFollowTheCostLimit holds the two together.
const MaxBuilt int64 = 4_000_000

// MaxParts is the most strings one split may return under CostLimit. CEL
// charges a list one for each member, so a list of more exceeds CostLimit by
// itself.
const MaxParts int64 = 1_000_000

// bounds are what one call may build and read under a cost limit.
type bounds struct {
	// cost is the cost limit, which a refusal names.
	cost uint64
	// parts is the most strings a split may return, and the most code points
	// a transform may build: one for each unit of the limit, since CEL
	// charges a list one for each member and a string one for each code point.
	parts int64
	// built is the most bytes a call may build: 4 for each unit, since CEL
	// charges a string one for each code point, which is at most 4 bytes.
	built int64
	// steps is the most code points a call may read, and the most pairs of
	// code points a search may compare: 10 for each unit, since CEL charges a
	// tenth for each.
	steps int64
}

// maxUnits is the largest cost limit boundsUnder works a bound out from, so
// that every bound fits an int64. A limit above it gives the bounds of
// maxUnits, which no string in memory reaches.
const maxUnits = math.MaxInt64 / 16

// boundsUnder is what one call may build under the cost limit cost.
func boundsUnder(cost uint64) bounds {
	units := int64(min(cost, maxUnits))
	return bounds{cost: cost, parts: units, built: 4 * units, steps: 10 * units}
}

// hiddenType is the argument type of the placeholder overload a disabled
// function is declared with. No expression can make a value of it.
var hiddenType = types.NewOpaqueType("errata.hidden")

// stringsLibrary is the strings extension assert loads.
var stringsLibrary = ext.Strings(ext.StringsVersion(stringsVersion))

// stringDecls are the strings extension's functions, from a scratch
// environment of it alone.
var stringDecls = sync.OnceValues(func() (map[string]*decls.FunctionDecl, error) {
	scratch, err := cel.NewCustomEnv(stringsLibrary)
	if err != nil {
		return nil, err
	}
	return scratch.Functions(), nil
})

// stringsExtension declares the strings extension with every function but
// offered disabled, and binds each overload of those behind its guard under
// CostLimit. compileWith binds them again under the rule's own limit.
func stringsExtension(offered []string) ([]cel.EnvOption, error) {
	fns, err := stringDecls()
	if err != nil {
		return nil, err
	}
	opts := []cel.EnvOption{stringsLibrary}
	for name := range fns {
		if !slices.Contains(offered, name) {
			opts = append(opts, cel.Function(name, cel.DisableDeclaration(true),
				cel.Overload("errata_hidden_"+name, []*cel.Type{hiddenType}, hiddenType)))
		}
	}
	bound, err := stringsUnder(CostLimit, offered)
	if err != nil {
		return nil, err
	}
	return append(opts, bound...), nil
}

// stringsUnder declares, with the bounds of the cost limit cost, each
// overload of the functions offered twice: bound to the extension's function
// behind its guard, which takes the place of the binding the extension
// declares, and as a hidden function that also takes the evaluation's log,
// which stringsThroughLog puts in place of every call.
func stringsUnder(cost uint64, offered []string) ([]cel.EnvOption, error) {
	fns, err := stringDecls()
	if err != nil {
		return nil, err
	}
	return stringsBound(cost, offered, fns, stringOverloads)
}

// stringsBound is stringsUnder over the functions fns, with the guards and
// prices of overloads by overload id. An overload with no guard or no price
// is an error, so a cel-go upgrade that adds one fails every test until it
// has both.
func stringsBound(cost uint64, offered []string, fns map[string]*decls.FunctionDecl, overloads map[string]stringOverload) ([]cel.EnvOption, error) {
	b := boundsUnder(cost)
	var opts []cel.EnvOption
	for name, fn := range fns {
		if !slices.Contains(offered, name) {
			continue
		}
		bindings, err := fn.Bindings()
		if err != nil {
			return nil, err
		}
		var bound, hidden []cel.FunctionOpt
		for _, o := range fn.OverloadDecls() {
			entry, ok := overloads[o.ID()]
			if !ok || entry.guard == nil {
				return nil, fmt.Errorf("the strings extension's overload %s of %s has no size guard", o.ID(), name)
			}
			if entry.price == nil {
				return nil, fmt.Errorf("the strings extension's overload %s of %s has no price", o.ID(), name)
			}
			i := slices.IndexFunc(bindings, func(f *functions.Overload) bool { return f.Operator == o.ID() })
			if i < 0 {
				return nil, fmt.Errorf("the strings extension's overload %s of %s has no binding", o.ID(), name)
			}
			call := guarded(name, b, entry.guard, bindings[i])
			withLog := append(slices.Clone(o.ArgTypes()), logType)
			if o.IsMemberFunction() {
				bound = append(bound, cel.MemberOverload(o.ID(), o.ArgTypes(), o.ResultType(), cel.FunctionBinding(call)))
				hidden = append(hidden, cel.MemberOverload(hiddenOverload(o.ID()), withLog, o.ResultType(), cel.FunctionBinding(throughLog(call))))
			} else {
				bound = append(bound, cel.Overload(o.ID(), o.ArgTypes(), o.ResultType(), cel.FunctionBinding(call)))
				hidden = append(hidden, cel.Overload(hiddenOverload(o.ID()), withLog, o.ResultType(), cel.FunctionBinding(throughLog(call))))
			}
		}
		opts = append(opts, cel.Function(name, bound...), cel.Function(hiddenFunction(name), hidden...))
	}
	return opts, nil
}

// hiddenFunction is the name of the hidden function that takes the place of
// name, and hiddenOverload the id of an overload of it. A name that starts
// with @ cannot be written in an expression.
func hiddenFunction(name string) string { return "@errata_" + name }
func hiddenOverload(id string) string   { return "errata_" + id }

// throughLog is call with the evaluation's log as its last argument, which
// call does not take. A refusal is noted on the log before the call returns,
// so a rule that the cost limit then stops still names it ("before it, ...").
func throughLog(call functions.FunctionOp) functions.FunctionOp {
	return func(args ...ref.Val) ref.Val {
		log, ok := args[len(args)-1].(*errorLog)
		if !ok {
			return types.MaybeNoSuchOverloadErr(args[len(args)-1])
		}
		v := call(args[:len(args)-1]...)
		if refused(v) {
			log.note(v)
		}
		return v
	}
}

// guarded calls the extension's function f once guard has passed its
// arguments under b, and is guard's error, named after the function, when it
// has not.
func guarded(name string, b bounds, guard func(bounds, []ref.Val) error, f *functions.Overload) functions.FunctionOp {
	return func(args ...ref.Val) ref.Val {
		if err := guard(b, args); err != nil {
			r := &guardRefusal{msg: fmt.Sprintf("%s: %v", name, err)}
			var walked *formatTooLong
			if errors.As(err, &walked) {
				r.read = walked.counted
			}
			return types.WrapErr(r)
		}
		if len(args) == 1 && f.Unary != nil {
			return f.Unary(args[0])
		}
		if len(args) == 2 && f.Binary != nil {
			return f.Binary(args[0], args[1])
		}
		if f.Function != nil {
			return f.Function(args...)
		}
		return types.NewErr("%s: no binding takes %d arguments", name, len(args))
	}
}

// guardRefusal is a guard's refusal of a call, which the call returns as its
// error.
type guardRefusal struct {
	msg string
	// read is what the guard counted that the call's own price does not:
	// the bytes format's walk counted.
	read int64
}

func (r *guardRefusal) Error() string { return r.msg }

// refused tells whether v is a guard's refusal.
func refused(v ref.Val) bool {
	return refusalOf(v) != nil
}

// refusalOf is the guard's refusal v is, or nil.
func refusalOf(v ref.Val) *guardRefusal {
	e, ok := v.(*types.Err)
	var r *guardRefusal
	if ok && errors.As(e, &r) {
		return r
	}
	return nil
}

// stringsThroughLog rewrites every call of a function of the strings
// extension as the call of its hidden function, with the evaluation's log as
// its last argument, as renovateThroughLog does renovate_match's (ADR 0016).
// The rewrite changes no operand and no order.
func stringsThroughLog(env *cel.Env, checked *cel.Ast) (*cel.Ast, error) {
	optimizer, err := cel.NewStaticOptimizer(stringsRewriter{})
	if err != nil {
		return nil, err
	}
	rewritten, issues := optimizer.Optimize(env, checked)
	return rewritten, issues.Err()
}

type stringsRewriter struct{}

func (stringsRewriter) Optimize(ctx *cel.OptimizerContext, a *celast.AST) *celast.AST {
	for _, e := range celast.MatchDescendants(celast.NavigateAST(a), stringCall(a)) {
		call := e.AsCall()
		if !slices.Contains(stringFunctions, call.FunctionName()) {
			// No hidden function of its name: stringsAllThroughLog
			// refuses it.
			continue
		}
		args := append(slices.Clone(call.Args()), ctx.NewIdent(logVariable))
		if call.IsMemberFunction() {
			e.SetKindCase(ctx.NewMemberCall(hiddenFunction(call.FunctionName()), call.Target(), args...))
			continue
		}
		e.SetKindCase(ctx.NewCall(hiddenFunction(call.FunctionName()), args...))
	}
	return a
}

// stringCall matches a call that the checker resolved to an overload of the
// strings extension, by the overload ids of a's references, not by its name:
// a call whose checked name differs from the one errata lists is still
// found, and a call of another library's function of the same name on a
// typed receiver, such as a list's reverse, is not. A hidden call's
// overloads are errata's own, so after the rewrite it matches only a call
// that was left.
//
// On a dyn receiver the checker keeps every candidate, and the call matches
// if any is the extension's: dyn(v).reverse() would be rewritten to the
// strings overload alone and no longer load. No library assert loads has a
// function of these names today. The day one does, such as ext.Lists, a
// call whose candidates mix the two must be left for stringsAllThroughLog to
// refuse with the shared name, or the hidden function must take the other
// library's overloads too.
func stringCall(a *celast.AST) celast.ExprMatcher {
	return func(e celast.NavigableExpr) bool {
		if e.Kind() != celast.CallKind {
			return false
		}
		return slices.ContainsFunc(a.GetOverloadIDs(e.ID()), func(id string) bool {
			_, guarded := stringOverloads[id]
			return guarded
		})
	}
}

// stringsAllThroughLog refuses a program in which a call of the strings
// extension is left that stringsThroughLog did not rewrite. Such a call
// would note no refusal on the log, and be priced by cel-go alone.
func stringsAllThroughLog(rewritten *cel.Ast) error {
	a := rewritten.NativeRep()
	if left := celast.MatchDescendants(celast.NavigateAST(a), stringCall(a)); len(left) > 0 {
		return fmt.Errorf("the rewrite through the evaluation log left the strings extension's calls unpriced, calls left: %d; this is errata's error", len(left))
	}
	return nil
}

// stringOverloads are, by overload id, each overload's guard and price.
// stringsBound refuses an overload of the extension with no entry, no guard
// or no price, so neither can be left out.
//
// The guard reads its arguments by the overload's signature, and passes
// what it cannot read to the extension, which then gives its own error. The
// price is cel-go's, from the arguments and the result, in code points:
// charAt 2 and a tenth for each code point; a search 1 and a tenth for each
// code point times each of the needle's (search); a split 1, the list's 10,
// a tenth for each code point and one more, and one for each part; a join 1,
// a tenth for each member and one more, and the result; a replace 1, a tenth
// for each code point of s times each of old, each at least 1, and the
// result; format and quote (build) and the rest (read) as their functions
// say.
var stringOverloads = map[string]stringOverload{
	"string_split_string": {
		guard: func(b bounds, a []ref.Val) error { return splitGuard(b, a[0], a[1], types.Int(-1)) },
		price: split,
	},
	"string_split_string_int": {
		guard: func(b bounds, a []ref.Val) error { return splitGuard(b, a[0], a[1], a[2]) },
		price: split,
	},
	"list_join": {
		guard: func(b bounds, a []ref.Val) error { return joinGuard(b, a[0], types.String("")) },
		price: join,
	},
	"list_join_string": {
		guard: func(b bounds, a []ref.Val) error { return joinGuard(b, a[0], a[1]) },
		price: join,
	},
	"string_replace_string_string": {
		guard: func(b bounds, a []ref.Val) error { return replaceGuard(b, a[0], a[1], a[2], types.Int(-1)) },
		price: replace,
	},
	"string_replace_string_string_int": {
		guard: func(b bounds, a []ref.Val) error { return replaceGuard(b, a[0], a[1], a[2], a[3]) },
		price: replace,
	},
	"string_char_at_int": {
		guard: func(b bounds, a []ref.Val) error { return readGuard(b, a[0]) },
		price: func(args []ref.Val, _ ref.Val) uint64 { return 2 + tenths(codePoints(args[0])) },
	},
	"string_substring_int": {
		guard: func(b bounds, a []ref.Val) error { return substringGuard(b, a[0], a[1], nil) },
		price: read,
	},
	"string_substring_int_int": {
		guard: func(b bounds, a []ref.Val) error { return substringGuard(b, a[0], a[1], a[2]) },
		price: read,
	},
	"string_trim": {
		guard: func(b bounds, a []ref.Val) error { return readGuard(b, a[0]) },
		price: read,
	},
	"string_index_of_string": {
		guard: func(b bounds, a []ref.Val) error { return searchGuard(b, a[0], a[1]) },
		price: search,
	},
	"string_index_of_string_int": {
		guard: func(b bounds, a []ref.Val) error { return searchGuard(b, a[0], a[1]) },
		price: search,
	},
	"string_last_index_of_string": {
		guard: func(b bounds, a []ref.Val) error { return searchGuard(b, a[0], a[1]) },
		price: search,
	},
	"string_last_index_of_string_int": {
		guard: func(b bounds, a []ref.Val) error { return searchGuard(b, a[0], a[1]) },
		price: search,
	},
	"string_lower_ascii": {
		guard: func(b bounds, a []ref.Val) error { return transformGuard(b, a[0]) },
		price: read,
	},
	"string_upper_ascii": {
		guard: func(b bounds, a []ref.Val) error { return transformGuard(b, a[0]) },
		price: read,
	},
	"string_reverse": {
		guard: func(b bounds, a []ref.Val) error { return transformGuard(b, a[0]) },
		price: read,
	},
	"strings_quote": {
		guard: func(b bounds, a []ref.Val) error { return quoteGuard(b, a[0]) },
		price: build,
	},
	"string_format": {
		guard: func(b bounds, a []ref.Val) error { return formatGuard(b, a[0], a[1]) },
		price: build,
	},
}

// stringOverload is an overload's guard and price.
type stringOverload struct {
	guard func(bounds, []ref.Val) error
	price func(args []ref.Val, result ref.Val) uint64
}

// readGuard refuses a call that reads more than b.steps code points of s.
// charAt and substring read s whole into code points, 4 bytes each.
func readGuard(b bounds, s ref.Val) error {
	str, ok := s.(types.String)
	if !ok {
		return nil
	}
	if n := int64(utf8.RuneCountInString(string(str))); n > b.steps {
		return fmt.Errorf("the call would read %d characters, more than the %d one call may read, since CEL charges a tenth for each and a longer read exceeds the cost limit of %d by itself", n, b.steps, b.cost)
	}
	return nil
}

// substringGuard refuses what readGuard refuses, and a substring of s from
// start, to end or the end of s, of more than b.parts code points: the result
// is a new string of as many, as a transform's is. Indexes out of range pass,
// and the extension gives its own error.
func substringGuard(b bounds, s, start, end ref.Val) error {
	if err := readGuard(b, s); err != nil {
		return err
	}
	str, ok1 := s.(types.String)
	i, ok2 := start.(types.Int)
	if !ok1 || !ok2 {
		return nil
	}
	j := types.Int(utf8.RuneCountInString(string(str)))
	if e, ok := end.(types.Int); ok {
		if e > j {
			return nil
		}
		j = e
	}
	if n := int64(j - i); i >= 0 && n > b.parts {
		return fmt.Errorf("the result would be %d characters, more than the %d one call may build, since a longer string exceeds the cost limit of %d by itself", n, b.parts, b.cost)
	}
	return nil
}

// searchGuard refuses a search of s for sub whose code points, times those of
// sub, are more than b.steps. cel-go compares each place in s with each code
// point of sub. An empty sub counts as one, since cel-go still reads s whole
// into code points.
func searchGuard(b bounds, s, sub ref.Val) error {
	str, ok1 := s.(types.String)
	needle, ok2 := sub.(types.String)
	if !ok1 || !ok2 {
		return nil
	}
	n := int64(utf8.RuneCountInString(string(str)))
	m := max(int64(utf8.RuneCountInString(string(needle))), 1)
	// n*m > steps, without the product, which could overflow.
	if n > b.steps/m {
		return fmt.Errorf("the search would compare %d characters with each of %d, more than the %d comparisons one call may make, since CEL charges a tenth for each and more exceed the cost limit of %d by itself", n, m, b.steps, b.cost)
	}
	return nil
}

// transformGuard refuses a transform of s into more than b.parts code
// points: the result has as many as s.
func transformGuard(b bounds, s ref.Val) error {
	str, ok := s.(types.String)
	if !ok {
		return nil
	}
	if n := int64(utf8.RuneCountInString(string(str))); n > b.parts {
		return fmt.Errorf("the result would be %d characters, more than the %d one call may build, since a longer string exceeds the cost limit of %d by itself", n, b.parts, b.cost)
	}
	return nil
}

// quoteGuard refuses a strings.quote that would build more than b.built
// bytes.
func quoteGuard(b bounds, s ref.Val) error {
	str, ok := s.(types.String)
	if !ok {
		return nil
	}
	if size := quotedSize(string(str)); size > b.built {
		return b.tooLong(size)
	}
	return nil
}

// quotedSize is the length in bytes of what strings.quote makes of s: two
// quotes, two bytes for each character it escapes, and each other character
// in UTF-8, where an invalid byte becomes U+FFFD, 3 bytes.
func quotedSize(s string) int64 {
	size := int64(2)
	for _, r := range s {
		switch r {
		case '\a', '\b', '\f', '\n', '\r', '\t', '\v', '\\', '"':
			size += 2
		default:
			size += int64(utf8.RuneLen(r))
		}
	}
	return size
}

// splitGuard refuses a split into more than b.parts strings. The parts share
// the memory of s, so their number is what the call builds.
func splitGuard(b bounds, s, sep, limit ref.Val) error {
	str, ok1 := s.(types.String)
	by, ok2 := sep.(types.String)
	n, ok3 := limit.(types.Int)
	if !ok1 || !ok2 || !ok3 {
		return nil
	}
	if parts := splitParts(string(str), string(by), int64(n)); parts > b.parts {
		return fmt.Errorf("the result would hold %d strings, more than the %d one call may build, since a longer list exceeds the cost limit of %d by itself", parts, b.parts, b.cost)
	}
	return nil
}

// splitParts is the number of strings strings.SplitN(s, sep, n) returns.
// A limit of 0 gives min(parts, 0), no parts.
func splitParts(s, sep string, n int64) int64 {
	parts := int64(strings.Count(s, sep)) + 1
	if sep == "" {
		parts = int64(utf8.RuneCountInString(s))
	}
	if n < 0 {
		return parts
	}
	return min(parts, n)
}

// joinGuard refuses a join that would build more than b.built bytes.
func joinGuard(b bounds, list, sep ref.Val) error {
	l, ok1 := list.(traits.Lister)
	by, ok2 := sep.(types.String)
	if !ok1 || !ok2 {
		return nil
	}
	var size int64
	count := int64(0)
	for it := l.Iterator(); it.HasNext() == types.True; count++ {
		s, ok := it.Next().(types.String)
		if !ok {
			return nil
		}
		if count > 0 {
			size += int64(len(by))
		}
		if size += int64(len(s)); size > b.built {
			return b.tooLong(size)
		}
	}
	return nil
}

// replaceGuard refuses a replace that would build more than b.built bytes.
func replaceGuard(b bounds, s, old, repl, limit ref.Val) error {
	str, ok1 := s.(types.String)
	o, ok2 := old.(types.String)
	nw, ok3 := repl.(types.String)
	n, ok4 := limit.(types.Int)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil
	}
	if size := replacedSize(string(str), string(o), string(nw), int64(n)); size > b.built {
		return b.tooLong(size)
	}
	return nil
}

// replacedSize is the length in bytes of strings.Replace(s, old, repl, n).
// Every count is at most the length of a string, so no product overflows.
func replacedSize(s, old, repl string, n int64) int64 {
	var count int64
	if old == "" {
		// Go inserts repl before every code point and at the end.
		count = int64(utf8.RuneCountInString(s)) + 1
	} else {
		count = int64(strings.Count(s, old))
	}
	if n >= 0 {
		count = min(count, n)
	}
	return int64(len(s)) + count*(int64(len(repl))-int64(len(old)))
}

// tooLong is the refusal of a string of size bytes.
func (b bounds) tooLong(size int64) error {
	return fmt.Errorf("the result would be %d bytes, more than the %d one call may build, since a longer string exceeds the cost limit of %d by itself", size, b.built, b.cost)
}

// stringCosts prices each call of the strings extension's hidden functions,
// which stringsThroughLog puts in place of every call, as cel-go's model
// prices the call it replaced, in code points, with each tenth rounded up:
// TestPropertyStringCostsAreCelGos holds the two equal. A refused call keeps
// its whole price, which pays for what its guard read, so refusals in a
// comprehension spend the cost limit as the calls would. Three prices are
// errata's own:
//
//   - format and strings.quote, which cel-go charges by what they read
//     alone, so that a call that builds 4 MB would cost about as little as
//     one that builds 4 bytes, are charged the code points of their result
//     as well, and a refused format what its count walked;
//   - a search for the empty string is charged as one for a needle of one
//     code point, since cel-go reads the whole string into code points for
//     it and charges nothing.
//
// Each price is less 1, the cost of reading the log, so a rule costs what it
// would without the rewrite.
//
// A decorator around each call (cel.CustomDecoratorV2) would keep cel-go's
// own price by the call's own overload id, but it cannot note a refusal
// that price puts over the limit: cel-go's tracker charges a call inside the
// call's own Exec and panics there on the limit (common/cost/tracker.go,
// checkLimit), so a wrapper that delegates to the call never sees its result.
func stringCosts() []cost.TrackerOption {
	var opts []cost.TrackerOption
	for id := range stringOverloads {
		opts = append(opts, cost.OverloadTracker(hiddenOverload(id), func(args []ref.Val, result ref.Val) *uint64 {
			c := stringPrice(id, args, result) - 1
			return &c
		}))
	}
	return opts
}

// stringPrice is what a call of the overload id costs, as cel-go prices it,
// and for a refused format what its count walked as well.
func stringPrice(id string, args []ref.Val, result ref.Val) uint64 {
	c := stringOverloads[id].price(args, result)
	if r := refusalOf(result); r != nil {
		c += tenths(uint64(r.read)) //nolint:gosec // a count, never below 0
	}
	return c
}

// read is 1, a tenth for each code point the call reads, and the result's
// code points.
func read(args []ref.Val, result ref.Val) uint64 {
	return 1 + tenths(codePoints(args[0])) + size(result)
}

// build is cel-go's charge of format and quote, at least 1, with the
// result's code points.
func build(args []ref.Val, result ref.Val) uint64 {
	return max(tenths(codePoints(args[0])), 1) + size(result)
}

func split(args []ref.Val, result ref.Val) uint64 {
	return 1 + 10 + tenths(codePoints(args[0])+1) + size(result)
}

func join(args []ref.Val, result ref.Val) uint64 {
	return 1 + tenths(size(args[0])+1) + size(result)
}

func replace(args []ref.Val, result ref.Val) uint64 {
	return 1 + tenths(max(codePoints(args[0]), 1)*max(codePoints(args[1]), 1)) + size(result)
}

// search is cel-go's charge of a search: 1 and a tenth for each code point
// of the string times each of the needle, which counts as at least one. A
// search the guard let through compares at most steps pairs, and a refused
// one has a string and a needle in memory, so the product fits.
func search(args []ref.Val, _ ref.Val) uint64 {
	return 1 + tenths(codePoints(args[0])*max(codePoints(args[1]), 1))
}

// tenths is n/10, rounded up, as cel-go scales a count it charges a tenth
// for.
func tenths(n uint64) uint64 {
	return (n + 9) / 10
}

// size is what CEL charges v's size: the code points of a string, the
// members of a list, and 0 for any other value, such as an error.
func size(v ref.Val) uint64 {
	if l, ok := v.(traits.Lister); ok {
		if n, ok := l.Size().(types.Int); ok {
			return uint64(n) //nolint:gosec // a size, never below 0
		}
	}
	return codePoints(v)
}

// codePoints is the number of code points of v, a string, and 0 for any
// other value, such as an error.
func codePoints(v ref.Val) uint64 {
	s, ok := v.(types.String)
	if !ok {
		return 0
	}
	return uint64(utf8.RuneCountInString(string(s))) //nolint:gosec // a count, never below 0
}
