package rule

import (
	"errors"
	"fmt"
	"reflect"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/overloads"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/interpreter"

	"github.com/f-klement/errata/internal/semver"
)

// The semver helpers read versions with internal/semver, the library agree's
// compare: semver uses, so an assert and an agree rule never read one value
// two ways:
//
//	semver(s)                 the version s, which <, <=, >, >=, == and != compare
//	string(semver(s))         the version as internal/semver writes it
//	semver_satisfies(v, r)    whether the version v satisfies the range r
//
// semver(s) compares at full precision, a missing number as 0, so
// semver('20') == semver('20.0.0'); agree's precision rule is for agree.

// semverType is the CEL type of semver(s), as the checker sees it.
var semverType = cel.OpaqueType("semver")

// semverRuntimeType is the same type as a value reports it, with the
// Comparer trait that CEL's order operators look for.
var semverRuntimeType = types.NewObjectType("semver", traits.ComparerType)

// semverValue is a version in CEL.
type semverValue struct{ v semver.Version }

func (s semverValue) ConvertToNative(reflect.Type) (any, error) {
	return nil, errors.New("a semver has no native form")
}

func (s semverValue) ConvertToType(t ref.Type) ref.Val {
	if t == types.TypeType {
		return semverRuntimeType
	}
	return types.NewErr("a semver converts to no %s; string(v) writes it", t.TypeName())
}

func (s semverValue) Equal(other ref.Val) ref.Val {
	o, ok := other.(semverValue)
	if !ok {
		return types.MaybeNoSuchOverloadErr(other)
	}
	return types.Bool(semver.Compare(s.v, o.v) == 0)
}

// Compare orders s and other by semver.Compare.
func (s semverValue) Compare(other ref.Val) ref.Val {
	o, ok := other.(semverValue)
	if !ok {
		return types.MaybeNoSuchOverloadErr(other)
	}
	return types.Int(semver.Compare(s.v, o.v))
}

func (s semverValue) Type() ref.Type { return semverRuntimeType }
func (s semverValue) Value() any     { return s.v }

// semverHelpers declares semver, its order and semver_satisfies.
func semverHelpers() []cel.EnvOption {
	opts := []cel.EnvOption{
		cel.Function("semver",
			cel.Overload("errata_semver_string", []*cel.Type{cel.StringType}, semverType,
				cel.UnaryBinding(stringFunc("semver", func(s string) (ref.Val, error) {
					v, err := semver.ParseVersion(s)
					if err != nil {
						return nil, fmt.Errorf("%q is not a version: %w", s, err)
					}
					return semverValue{v}, nil
				})))),
		cel.Function(overloads.TypeConvertString,
			cel.Overload("errata_semver_to_string", []*cel.Type{semverType}, cel.StringType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					s, ok := v.(semverValue)
					if !ok {
						return types.MaybeNoSuchOverloadErr(v)
					}
					return types.String(s.v.String())
				}))),
		cel.Function("semver_satisfies",
			cel.Overload("errata_semver_satisfies_string_string", []*cel.Type{cel.StringType, cel.StringType}, cel.BoolType,
				cel.BinaryBinding(satisfies))),
	}
	// CEL binds each order operator once for every type with the
	// Comparer trait, so these overloads declare the types and the
	// operator calls semverValue.Compare.
	for _, o := range []struct{ op, name string }{
		{operators.Less, "less"}, {operators.LessEquals, "less_equals"},
		{operators.Greater, "greater"}, {operators.GreaterEquals, "greater_equals"},
	} {
		opts = append(opts, cel.Function(o.op,
			cel.Overload("errata_"+o.name+"_semver", []*cel.Type{semverType, semverType}, cel.BoolType)))
	}
	return opts
}

// semverCosts charges semver() and semver_satisfies() 1 and the bytes they
// read, so the cost limit counts their parsing as it counts a
// comprehension's steps (security review, finding 4).
func semverCosts() []interpreter.CostTrackerOption {
	charge := func(args []ref.Val, _ ref.Val) *uint64 {
		cost := uint64(1)
		for _, a := range args {
			if s, ok := a.(types.String); ok {
				cost += uint64(len(s))
			}
		}
		return &cost
	}
	return []interpreter.CostTrackerOption{
		interpreter.OverloadCostTracker("errata_semver_string", charge),
		interpreter.OverloadCostTracker("errata_semver_satisfies_string_string", charge),
	}
}

// satisfies is semver_satisfies: the version v against the range r, as
// agree's compare: semver tests them. A value that is not a version or not
// a range is an error naming it, as is one over the helpers' length limit.
func satisfies(v, r ref.Val) ref.Val {
	vs, ok := v.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(v)
	}
	rs, ok := r.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(r)
	}
	for _, s := range []types.String{vs, rs} {
		if len(s) > MaxArgument {
			return types.NewErr("semver_satisfies: an argument is %d bytes; a helper reads at most %d", len(s), MaxArgument)
		}
	}
	version, err := semver.ParseVersion(string(vs))
	if err != nil {
		return types.NewErr("semver_satisfies: %q is not a version: %v", string(vs), err)
	}
	rng, err := semver.ParseRange(string(rs))
	if err != nil {
		return types.NewErr("semver_satisfies: %q is not a range: %v", string(rs), err)
	}
	return types.Bool(semver.Satisfies(version, rng))
}
