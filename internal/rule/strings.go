package rule

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/functions"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/ext"
)

// assert takes split, join and replace from cel-go's strings extension:
//
//	s.split(sep), s.split(sep, n)          a list of the parts of s between each sep
//	list.join(), list.join(sep)            the members of a list(string), with sep between them
//	s.replace(old, new), s.replace(old, new, n)  s with old replaced by new, at most n times
//
// The extension's other functions, such as indexOf, substring and format, are
// left out: each would need a bound of its own (indexOf compares every place
// in a string with every rune of the needle). They are declared disabled, so
// an expression that names one does not load.
//
// CEL charges each call after it returns, by the size of its result, so the
// cost limit stops an evaluation only once the call has built that result.
// One call can build far more than the limit pays for: v[0].replace('', v[0])
// over 1 MB is a string of 1 TB. Each overload is bound to the extension's own
// function behind a guard that works out the result's size first, without
// building it, and refuses a string of more than MaxBuilt bytes or a split
// into more than MaxParts parts. Each would exceed CostLimit by itself. A
// smaller result can exceed CostLimit too, and is built first: CEL charges a
// string by its code points, and the guard counts bytes, so it never refuses
// what the cost limit would let through. The cost model, the declarations and
// the functions themselves are cel-go's.

// stringsVersion is the version of cel-go's strings extension assert loads:
// the first with runtime costs for split, join and replace.
const stringsVersion = 5

// stringFunctions are the functions of the extension assert offers.
var stringFunctions = []string{"split", "join", "replace"}

// MaxBuilt is the most bytes one call of split, join or replace may build. A
// string of more bytes holds more than CostLimit code points, since a code
// point is at most 4 bytes, and CEL charges a string one for each code point,
// so the guard refuses only what the cost limit would stop anyway. It is 4
// times CostLimit, written out so that no arithmetic sits on a line no test
// can cover; TestStringBoundsFollowTheCostLimit holds the two together.
const MaxBuilt int64 = 4_000_000

// MaxParts is the most strings one split may return. CEL charges a list one
// for each member, so a list of more exceeds CostLimit by itself. It is
// CostLimit, written out as MaxBuilt is.
const MaxParts int64 = 1_000_000

// hiddenType is the argument type of the placeholder overload a disabled
// function is declared with. No expression can make a value of it.
var hiddenType = types.NewOpaqueType("errata.hidden")

// stringsExtension declares the strings extension with every function but
// stringFunctions disabled, and binds each overload of those to the
// extension's function behind its guard. An overload with no guard is an
// error, so a cel-go upgrade that adds one fails every test until it has one.
func stringsExtension() ([]cel.EnvOption, error) {
	lib := ext.Strings(ext.StringsVersion(stringsVersion))
	scratch, err := cel.NewCustomEnv(lib)
	if err != nil {
		return nil, err
	}
	opts := []cel.EnvOption{lib}
	for name, fn := range scratch.Functions() {
		if !slices.Contains(stringFunctions, name) {
			opts = append(opts, cel.Function(name, cel.DisableDeclaration(true),
				cel.Overload("errata_hidden_"+name, []*cel.Type{hiddenType}, hiddenType)))
			continue
		}
		bindings, err := fn.Bindings()
		if err != nil {
			return nil, err
		}
		var overloads []cel.FunctionOpt
		for _, o := range fn.OverloadDecls() {
			guard, ok := stringGuards[o.ID()]
			if !ok {
				return nil, fmt.Errorf("the strings extension's overload %s of %s has no size guard", o.ID(), name)
			}
			i := slices.IndexFunc(bindings, func(b *functions.Overload) bool { return b.Operator == o.ID() })
			if i < 0 {
				return nil, fmt.Errorf("the strings extension's overload %s of %s has no binding", o.ID(), name)
			}
			overloads = append(overloads, cel.MemberOverload(o.ID(), o.ArgTypes(), o.ResultType(),
				cel.FunctionBinding(guarded(name, guard, bindings[i]))))
		}
		opts = append(opts, cel.Function(name, overloads...))
	}
	return opts, nil
}

// guarded calls the extension's function f once guard has passed its
// arguments, and is guard's error, named after the function, when it has not.
func guarded(name string, guard func([]ref.Val) error, f *functions.Overload) functions.FunctionOp {
	return func(args ...ref.Val) ref.Val {
		if err := guard(args); err != nil {
			return types.NewErr("%s: %v", name, err)
		}
		// Ifs, not a switch: coverage counts an if's condition, and not a
		// case's, so mutation testing could not see these tested.
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

// stringGuards are the guards by overload ID. Each reads its arguments by the
// overload's signature, and passes what it cannot read to the extension,
// which then gives its own error.
var stringGuards = map[string]func([]ref.Val) error{
	"string_split_string":              func(a []ref.Val) error { return splitGuard(a[0], a[1], types.Int(-1)) },
	"string_split_string_int":          func(a []ref.Val) error { return splitGuard(a[0], a[1], a[2]) },
	"list_join":                        func(a []ref.Val) error { return joinGuard(a[0], types.String("")) },
	"list_join_string":                 func(a []ref.Val) error { return joinGuard(a[0], a[1]) },
	"string_replace_string_string":     func(a []ref.Val) error { return replaceGuard(a[0], a[1], a[2], types.Int(-1)) },
	"string_replace_string_string_int": func(a []ref.Val) error { return replaceGuard(a[0], a[1], a[2], a[3]) },
}

// splitGuard refuses a split into more than MaxParts strings. The parts share
// the memory of s, so their number is what the call builds.
func splitGuard(s, sep, limit ref.Val) error {
	str, ok1 := s.(types.String)
	by, ok2 := sep.(types.String)
	n, ok3 := limit.(types.Int)
	if !ok1 || !ok2 || !ok3 {
		return nil
	}
	if parts := splitParts(string(str), string(by), int64(n)); parts > MaxParts {
		return fmt.Errorf("the result would hold %d strings, more than the %d one call may build, since a longer list exceeds the cost limit of %d by itself", parts, MaxParts, CostLimit)
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

// joinGuard refuses a join that would build more than MaxBuilt bytes.
func joinGuard(list, sep ref.Val) error {
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
		if size += int64(len(s)); size > MaxBuilt {
			return built(size)
		}
	}
	return nil
}

// replaceGuard refuses a replace that would build more than MaxBuilt bytes.
func replaceGuard(s, old, repl, limit ref.Val) error {
	str, ok1 := s.(types.String)
	o, ok2 := old.(types.String)
	nw, ok3 := repl.(types.String)
	n, ok4 := limit.(types.Int)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil
	}
	if size := replacedSize(string(str), string(o), string(nw), int64(n)); size > MaxBuilt {
		return built(size)
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

func built(size int64) error {
	return fmt.Errorf("the result would be %d bytes, more than the %d one call may build, since a longer string exceeds the cost limit of %d by itself", size, MaxBuilt, CostLimit)
}
