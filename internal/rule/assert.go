package rule

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/ext"
	"cel.dev/cel-go/interpreter"

	"github.com/f-klement/errata/internal/check"
)

// The limits on one evaluation of an assert, so that an expression in a
// repository's config cannot stall CI. Exceeding either makes the rule
// unknown, with the limit named.
const (
	// CostLimit is the CEL runtime cost one evaluation may spend: the budget
	// Kubernetes gives one CRD validation rule (PerCallLimit in apiserver).
	CostLimit uint64 = 1_000_000
	// EvalTimeout bounds one evaluation's wall time, for work that the cost
	// does not count, such as a helper reading a long string.
	EvalTimeout = 5 * time.Second
)

// interruptEvery is how many comprehension steps CEL takes between two looks
// at the timeout: every step. CEL's own timestamp(), size() and matches() are
// not under MaxArgument, and one step over a 16 MB value takes up to 86 ms,
// so looking every 100 steps let an evaluation run on for 18 s after its
// timeout. Looking every step costs about 8% on a cheap all over 8000 values.
const interruptEvery = 1

// limits are the bounds one compiled assert evaluates under. Tests lower them.
type limits struct {
	cost    uint64
	timeout time.Duration
	// grace is how long Eval waits after the timeout for the evaluation to
	// see it and stop.
	grace time.Duration
}

// stopGrace is the default grace. CEL looks at the context after every
// comprehension step, so the grace has to outlast one step.
const stopGrace = 100 * time.Millisecond

// The limits on an assert's source, so that type checking, which runs at load
// outside the cost limit and the timeout, cannot stall the load either. The
// checker's time grows faster than the depth and the length; at these limits
// the slowest expression measured compiles in about 0.3 s.
const (
	// MaxDepth is how deeply the expression may nest.
	MaxDepth = 32
	// MaxLength is the expression's length in code points.
	MaxLength = 4096
)

// baseEnv is the CEL environment every assert extends with its extractors.
var baseEnv = sync.OnceValues(func() (*cel.Env, error) {
	opts := []cel.EnvOption{stdlib(), cel.ParserRecursionLimit(MaxDepth), cel.ParserExpressionSizeLimit(MaxLength)}
	opts = append(opts, helpers()...)
	strs, err := stringsExtension()
	if err != nil {
		return nil, err
	}
	opts = append(opts, strs...)
	opts = append(opts, ext.Bindings(ext.BindingsVersion(bindingsVersion)))
	return cel.NewCustomEnv(append(opts, noting()...)...)
})

// bindingsVersion is the version of cel-go's bindings library assert
// declares: cel.bind(name, value, expr) names value in expr, so a rule can
// write an intermediate value once, such as the matching rules of a fold.
// Version 0 is the macro alone. A later one adds cel.@block, which only
// cel-go's optimizer writes, and is left out.
const bindingsVersion = 0

// identifier is what CEL reads as one variable name. An extractor named
// otherwise could not be written in the expression.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reserved are the words CEL's grammar keeps for itself.
var reserved = []string{
	"as", "break", "const", "continue", "else", "false", "for", "function", "if", "import", "in",
	"let", "loop", "package", "namespace", "null", "return", "true", "var", "void", "while",
}

// by_file(x) is the values of the extractor x grouped by file: a map from
// each file that gave x a value to its values, in the order x found them.
// A macro rewrites it as a hidden variable of x's, byFilePrefix and x's
// name, which a name that starts with @ keeps out of reach of an
// expression; Eval fills it from the files of x's values.
const (
	byFile       = "by_file"
	byFilePrefix = "@errata_by_file_"
)

// The types an extractor's variable and its by_file have: a list of
// strings, or for an extractor with as: map a list of maps, each from a key
// to a string, a list or a map again, which CEL types dyn.
var (
	mapType       = cel.MapType(cel.StringType, cel.DynType)
	byFileType    = cel.MapType(cel.StringType, cel.ListType(cel.StringType))
	byFileMapType = cel.MapType(cel.StringType, cel.ListType(mapType))
)

// byFileMacro rewrites by_file(x), for x one of extractors, as x's hidden
// variable. Any other argument is a load error, at the argument.
func byFileMacro(extractors []string) cel.EnvOption {
	return cel.Macros(cel.GlobalMacro(byFile, 1, func(eh cel.MacroExprFactory, _ celast.Expr, args []celast.Expr) (celast.Expr, *common.Error) {
		arg := args[0]
		if arg.Kind() == celast.IdentKind && slices.Contains(extractors, arg.AsIdent()) {
			return eh.NewIdent(byFilePrefix + arg.AsIdent()), nil
		}
		return nil, eh.NewError(arg.ID(), "by_file takes the name of one of the rule's extractors, such as by_file(apps)")
	}))
}

// Lists names the extractors of a rule that assert reads apart from the
// rest. They are two lists of names with different meanings, so each has its
// own field: a call cannot hand one where the other belongs.
type Lists struct {
	// Maps are read as lists of maps, list(map(string, dyn)), rather than as
	// lists of strings.
	Maps []string
	// Read are the extractors another extractor of the rule reads the
	// values of. The expression need not name them, since what reads them
	// does.
	Read []string
}

// Compile type checks expr over the named extractors, each a list(string)
// but those l.Maps names, each a list(map(string, dyn)), and returns it ready
// to evaluate. Each of these is a load error: a name that
// is not an extractor, a type error, a result that is not a bool and a parse
// error, each with the expression and a caret under the column; an extractor
// the expression never uses, unless l.Read lists it; an extractor name CEL
// cannot read.
func Compile(expr string, extractors []string, l Lists) (check.Assertion, error) {
	a, err := compileWith(expr, extractors, l, limits{cost: CostLimit, timeout: EvalTimeout, grace: stopGrace})
	if err != nil {
		// A nil *assertion in the interface would not compare equal to nil.
		return nil, err
	}
	return a, nil
}

// compile is Compile with the limits given, and with extra declarations,
// which only tests pass.
func compile(expr string, extractors []string, lim limits, extra ...cel.EnvOption) (*assertion, error) {
	return compileWith(expr, extractors, Lists{}, lim, extra...)
}

// compileWith is compile with the extractors l names read apart.
func compileWith(expr string, extractors []string, l Lists, lim limits, extra ...cel.EnvOption) (*assertion, error) {
	base, err := baseEnv()
	if err != nil {
		return nil, fmt.Errorf("assert: the CEL environment: %w", err)
	}
	var (
		errs []error
		vars = make([]cel.EnvOption, 0, len(extractors))
	)
	for _, name := range extractors {
		if !identifier.MatchString(name) || slices.Contains(reserved, name) {
			errs = append(errs, fmt.Errorf("extractor %q: assert refers to an extractor by its name, so the name must be letters, digits and _, must not start with a digit and must not be one of CEL's reserved words", name))
			continue
		}
		if slices.Contains(l.Maps, name) {
			vars = append(vars, cel.Variable(name, cel.ListType(mapType)), cel.Variable(byFilePrefix+name, byFileMapType))
			continue
		}
		vars = append(vars, cel.Variable(name, cel.ListType(cel.StringType)), cel.Variable(byFilePrefix+name, byFileType))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	vars = append(vars, byFileMacro(extractors))
	env, err := base.Extend(append(vars, extra...)...)
	if err != nil {
		return nil, fmt.Errorf("assert: the CEL environment: %w", err)
	}

	ast, issues := env.Compile(expr)
	if issues.Err() != nil {
		for _, e := range issues.Errors() {
			errs = append(errs, located(expr, e.Location, parserMessage(e.Message)))
		}
		return nil, errors.Join(errs...)
	}
	if t := ast.OutputType(); !t.IsExactType(cel.BoolType) {
		root := ast.NativeRep()
		return nil, located(expr, root.SourceInfo().GetStartLocation(root.Expr().ID()),
			fmt.Sprintf("the expression has type %s; assert needs a bool", cel.FormatCELType(t)))
	}
	if errs := mapLiterals(expr, ast.NativeRep()); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	used := free(ast.NativeRep())
	var grouped []string
	for _, name := range extractors {
		if used[byFilePrefix+name] {
			grouped = append(grouped, name)
		}
		if !used[name] && !used[byFilePrefix+name] && !slices.Contains(l.Read, name) {
			// It would still run, but the relation would hold or fail without
			// testing the place the author added it for.
			errs = append(errs, fmt.Errorf("extractor %q is declared but assert does not use it", name))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	noted, err := noteOperands(env, ast)
	if err != nil {
		return nil, fmt.Errorf("assert: %w", err)
	}
	program, err := env.Program(noted,
		cel.CostLimit(lim.cost),
		cel.CostTrackerOptions(append(append(setCosts(), semverCosts()...), noteCost)...),
		cel.InterruptCheckFrequency(interruptEvery),
	)
	if err != nil {
		return nil, fmt.Errorf("assert: %w", err)
	}
	var whole []string
	for _, name := range extractors {
		if slices.Contains(l.Maps, name) {
			whole = append(whole, name)
		}
	}
	return &assertion{program: program, limits: lim, grouped: grouped, maps: whole}, nil
}

// mapLiterals refuses each ==, != and in that compares a value CEL types
// dyn, which a value of a map is, or a list or map that holds such values,
// with an operand of a type such a value never equals: bool, int, uint,
// double, null, bytes, duration or timestamp, or a list or map whose
// members, keys or values are of such a type. A map's scalar is always a
// string, and CEL's equality makes such a comparison false, or != true, on
// every input; the checker cannot see it, since dyn takes any type, so
// r.automerge != true would hold on the rule it exists to refuse. The pass
// goes by the operand's checked type, so int('5'), size(m), !false, a
// ternary and v.map(x, int(x)) are refused as a literal is. A list or map
// literal is refused at each element, key or value of such a type, so an
// empty one loads, and so is the literal a cel.bind name binds or a
// comprehension variable runs over, since a literal that mixes types, such
// as {'name': 'x', 'automerge': true}, is typed map(string, dyn). Any other
// operand is checked by its type alone. A value the expression writes as
// dyn() itself is left alone, which is also the way out for a dyn that is
// not a map's, such as [1, 'a'][0]. Each refusal is at the operand, or the
// member, of that type.
func mapLiterals(expr string, checked *celast.AST) []error {
	errs, _ := refusals(expr, checked)
	return errs
}

// refusals is mapLiterals, and the number of nodes it visited. Each node is
// visited at most once as a compared operand and once as the right side of
// in, so the visits are linear in the expression however often a followed
// name is read: without that, a chain of binds that each name the last one
// k times took k to the power of its depth, 56 s for 400 bytes. The calls
// are not: each read of a comprehension variable follows it to every
// element of its range before the visited set answers, so they grow as the
// reads times the range's length, about 0.1 s at MaxLength.
func refusals(expr string, checked *celast.AST) ([]error, int) {
	var errs []error
	nodes := map[int64]celast.NavigableExpr{}
	for _, n := range celast.MatchDescendants(celast.NavigateAST(checked), func(celast.NavigableExpr) bool { return true }) {
		nodes[n.ID()] = n
	}
	dyn := func(e celast.Expr) bool {
		if e.Kind() == celast.CallKind && e.AsCall().FunctionName() == "dyn" {
			return false
		}
		return holdsDyn(checked.GetType(e.ID()))
	}
	refused := map[int64]bool{}
	at := func(e celast.Expr, msg string) {
		if !refused[e.ID()] {
			refused[e.ID()] = true
			errs = append(errs, located(expr, checked.SourceInfo().GetStartLocation(e.ID()), msg))
		}
	}
	// follow is what a name stands for: the value a cel.bind name binds, or
	// the elements of the list literal a comprehension variable runs over.
	// It is nil, and false, for any other expression.
	follow := func(e celast.Expr) ([]celast.Expr, bool) {
		if e.Kind() != celast.IdentKind {
			return nil, false
		}
		c, ok := binder(nodes[e.ID()], e.AsIdent())
		if !ok {
			return nil, false
		}
		if c.AccuVar() == e.AsIdent() {
			return []celast.Expr{c.AccuInit()}, true
		}
		if c.IterVar() == e.AsIdent() && c.IterRange().Kind() == celast.ListKind {
			return c.IterRange().AsList().Elements(), true
		}
		return nil, false
	}
	visits := 0
	compared, searched := map[int64]bool{}, map[int64]bool{}
	var refuse, member func(e celast.Expr)
	refuse = func(e celast.Expr) {
		if compared[e.ID()] {
			return
		}
		compared[e.ID()] = true
		visits++
		t := checked.GetType(e.ID())
		if neverEqual(t) {
			at(e, dynMessage(e, t))
			return
		}
		if e.Kind() == celast.ListKind {
			for _, el := range e.AsList().Elements() {
				refuse(el)
			}
			return
		}
		if e.Kind() == celast.MapKind {
			for _, entry := range e.AsMap().Entries() {
				refuse(entry.AsMapEntry().Key())
				refuse(entry.AsMapEntry().Value())
			}
			return
		}
		if to, ok := follow(e); ok {
			for _, f := range to {
				refuse(f)
			}
			return
		}
		if holdsNever(t) {
			at(e, dynMessage(e, t))
		}
	}
	// member refuses the members of e, the right side of in: those of a
	// literal one by one, and any other by e's member type.
	member = func(e celast.Expr) {
		if searched[e.ID()] {
			return
		}
		searched[e.ID()] = true
		visits++
		if to, ok := follow(e); ok {
			for _, f := range to {
				member(f)
			}
			return
		}
		t := checked.GetType(e.ID())
		switch {
		case e.Kind() == celast.ListKind:
			for _, el := range e.AsList().Elements() {
				refuse(el)
			}
		case e.Kind() == celast.MapKind:
			for _, entry := range e.AsMap().Entries() {
				refuse(entry.AsMapEntry().Key())
			}
		case t.Kind() == types.ListKind && holdsNever(t.Parameters()[0]),
			t.Kind() == types.MapKind && holdsNever(t.Parameters()[0]):
			at(e, fmt.Sprintf("an expression of type %s is searched for %sit never holds one; build it of strings, such as with string()%s", cel.FormatCELType(t), dynValue, escapeValue))
		}
	}
	for _, e := range celast.MatchDescendants(celast.NavigateAST(checked), celast.KindMatcher(celast.CallKind)) {
		call := e.AsCall()
		args := call.Args()
		fn := call.FunctionName()
		if (fn == operators.Equals || fn == operators.NotEquals) && len(args) == 2 {
			if dyn(args[0]) {
				refuse(args[1])
			}
			if dyn(args[1]) {
				refuse(args[0])
			}
		}
		if fn == operators.In && len(args) == 2 {
			if t := checked.GetType(args[1].ID()); dyn(args[1]) || (t.Kind() == types.ListKind && holdsDyn(t.Parameters()[0])) {
				refuse(args[0])
			}
			if dyn(args[0]) {
				member(args[1])
			}
		}
	}
	return errs, visits
}

// holdsNever reports whether t is a type a value of a map never equals, or
// a list or map with members, keys or values of such a type.
func holdsNever(t *types.Type) bool {
	switch t.Kind() {
	case types.ListKind:
		return holdsNever(t.Parameters()[0])
	case types.MapKind:
		return holdsNever(t.Parameters()[0]) || holdsNever(t.Parameters()[1])
	default:
		return neverEqual(t)
	}
}

// holdsDyn reports whether a value of type t may be or hold a value of a
// map: t is dyn, or a list or map whose members are of such a type, as a
// map(string, dyn) and a list(map(string, dyn)) are.
func holdsDyn(t *types.Type) bool {
	switch t.Kind() {
	case types.DynKind:
		return true
	case types.ListKind:
		return holdsDyn(t.Parameters()[0])
	case types.MapKind:
		return holdsDyn(t.Parameters()[1])
	default:
		return false
	}
}

// neverEqual reports whether a value of type t is never equal to a value of
// a map: a string, a list or a map.
func neverEqual(t *types.Type) bool {
	switch t.Kind() {
	case types.BoolKind, types.IntKind, types.UintKind, types.DoubleKind, types.NullTypeKind,
		types.BytesKind, types.DurationKind, types.TimestampKind:
		return true
	default:
		return false
	}
}

// dynValue names the side a refusal compares with, and says why the two
// never meet; escape and escapeValue are the way out, for == and for in.
const (
	dynValue    = "a value CEL types dyn, such as one from a map; a map's scalars are always strings, so "
	escape      = "; if the other side does not come from a map, wrap it in dyn()"
	escapeValue = "; if the value does not come from a map, wrap it in dyn()"
)

// dynMessage is the refusal of e, of type t, compared with a value CEL
// types dyn. A bool, number or null literal is named by its value, any
// other expression by its type; a list or map by what its members are.
func dynMessage(e celast.Expr, t *types.Type) string {
	subject := "an expression of type " + cel.FormatCELType(t)
	never := "the two are never equal"
	var hint string
	switch t.Kind() {
	case types.BoolKind:
		hint = "compare with the string 'true' or 'false'"
	case types.NullTypeKind:
		hint = "a mapping holds null as the string 'null', so compare with 'null'"
	case types.IntKind, types.UintKind, types.DoubleKind:
		hint = "compare with a string, or convert the value with int(), uint() or double()"
	case types.ListKind, types.MapKind:
		never = "its members never equal a map's, and the two are equal only when both are empty"
		hint = "build it of strings, such as with string()"
	default:
		hint = fmt.Sprintf("convert the value with %s()", cel.FormatCELType(t))
	}
	if e.Kind() == celast.LiteralKind {
		switch v := e.AsLiteral().(type) {
		case types.Bool:
			subject = fmt.Sprintf("the bool %t", bool(v))
			hint = fmt.Sprintf("compare with the string '%t'", bool(v))
		case types.Null:
			subject = "null"
		case types.Int, types.Uint, types.Double:
			var text string
			switch n := v.(type) {
			case types.Int:
				text = strconv.FormatInt(int64(n), 10)
			case types.Uint:
				text = strconv.FormatUint(uint64(n), 10)
			case types.Double:
				text = strconv.FormatFloat(float64(n), 'g', -1, 64)
			}
			subject = "the number " + text
			hint = fmt.Sprintf("compare with the string '%s', or convert the value with int(), uint() or double()", text)
		}
	}
	return subject + " is compared with " + dynValue + never + "; " + hint + escape
}

// The names of the hidden function and variable noteOperands adds. A name
// that starts with @ cannot be written in an expression.
const (
	noteFunction = "@errata_note"
	noteOverload = "errata_note"
	logVariable  = "@errata_log"
)

// noteCost charges a note nothing, so the cost limit counts the
// expression's own work. Reading the log variable still costs 1 per note.
var noteCost = interpreter.OverloadCostTracker(noteOverload, func([]ref.Val, ref.Val) *uint64 {
	var zero uint64
	return &zero
})

// logType is the CEL type of the per-evaluation errorLog.
var logType = types.NewOpaqueType("errata.log")

// noting declares the function and the variable noteOperands uses.
func noting() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Variable(logVariable, logType),
		cel.Function(noteFunction,
			// The operand is dyn so that a non-bool, which an operand typed dyn
			// can be at runtime, reaches the note instead of failing its
			// overload, and is logged.
			cel.Overload(noteOverload, []*cel.Type{cel.DynType, logType}, cel.BoolType,
				cel.OverloadIsNonStrict(),
				cel.BinaryBinding(func(value, log ref.Val) ref.Val {
					if l, ok := log.(*errorLog); ok {
						l.note(value)
					}
					return value
				}))),
	}
}

// noteOperands rewrites every operand x of && and || as @errata_note(x, log),
// which returns x unchanged and records it in the evaluation's log when it is
// an error. CEL's && and || absorb an error when the other operand decides
// the result, and exists and all are built from them; the log keeps that
// error, so the rule is unknown however the operators decided. Short circuit
// evaluation is unchanged: an operand that does not run is not an error, so
// size(v) > 0 && int(v[0]) > 0 is false, not unknown, when v is empty.
func noteOperands(env *cel.Env, checked *cel.Ast) (*cel.Ast, error) {
	optimizer, err := cel.NewStaticOptimizer(operandNoter{})
	if err != nil {
		return nil, err
	}
	noted, issues := optimizer.Optimize(env, checked)
	return noted, issues.Err()
}

type operandNoter struct{}

func (operandNoter) Optimize(ctx *cel.OptimizerContext, a *celast.AST) *celast.AST {
	// Post order, so an operand that is itself && or || is rewritten before
	// it moves under its note.
	celast.PostOrderVisit(a.Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() == celast.ComprehensionKind {
			everyMember(ctx, e.AsComprehension())
			return
		}
		if e.Kind() != celast.CallKind {
			return
		}
		if fn := e.AsCall().FunctionName(); fn != operators.LogicalAnd && fn != operators.LogicalOr {
			return
		}
		for _, operand := range e.AsCall().Args() {
			moved := ctx.NewIdent(logVariable) // a node with a new id, given the operand's content
			moved.SetKindCase(operand)
			operand.SetKindCase(ctx.NewCall(noteFunction, moved, ctx.NewIdent(logVariable)))
		}
	}))
	return a
}

// everyMember makes exists and all evaluate every member, so whether an
// error in one member makes the rule unknown does not depend on the order
// of the values. The macros stop at the first member that decides: their
// loop condition is @not_strictly_false(...) of the result so far, and their
// step is result || pred or result && pred, which skips pred once the result
// is decided. The condition becomes true and the step pred || result, so
// pred runs for every member and gives the same verdict. exists_one, map and
// filter already run every member.
func everyMember(ctx *cel.OptimizerContext, c celast.ComprehensionExpr) {
	cond, step := c.LoopCondition(), c.LoopStep()
	if cond.Kind() != celast.CallKind || cond.AsCall().FunctionName() != operators.NotStrictlyFalse || step.Kind() != celast.CallKind {
		return
	}
	args := step.AsCall().Args()
	if fn := step.AsCall().FunctionName(); (fn != operators.LogicalAnd && fn != operators.LogicalOr) || len(args) != 2 {
		return
	}
	cond.SetKindCase(ctx.NewLiteral(types.True))
	held := ctx.NewIdent(logVariable) // a node with a new id, to hold one operand while they swap
	held.SetKindCase(args[0])
	args[0].SetKindCase(args[1])
	args[1].SetKindCase(held)
}

// errorLog keeps the first error an evaluation's notes saw. It is the value
// of the hidden log variable, one per evaluation.
type errorLog struct {
	first *types.Err
}

// note logs v when it is an error, or not a bool, which && and || would
// treat as an error and absorb.
func (l *errorLog) note(v ref.Val) {
	if l.first != nil {
		return
	}
	switch v := v.(type) {
	case types.Bool:
	case *types.Err:
		l.first = v
	default:
		l.first = types.NewErr("&& and || need a bool and got a %s", v.Type().TypeName()).(*types.Err)
	}
}

func (l *errorLog) ConvertToNative(reflect.Type) (any, error) {
	return nil, errors.New("the evaluation log has no native form")
}
func (l *errorLog) ConvertToType(ref.Type) ref.Val {
	return types.NewErr("the evaluation log converts to no type")
}
func (l *errorLog) Equal(other ref.Val) ref.Val { return types.Bool(other == ref.Val(l)) }
func (l *errorLog) Type() ref.Type              { return logType }
func (l *errorLog) Value() any                  { return l }

// free returns the names the expression reads that no comprehension binds.
// An extractor named like a macro's variable, as in [1].all(w, w > 0), is
// not used where that variable is.
func free(checked *celast.AST) map[string]bool {
	names := map[string]bool{}
	for _, id := range celast.MatchDescendants(celast.NavigateAST(checked), celast.KindMatcher(celast.IdentKind)) {
		if !bound(id, id.AsIdent()) {
			names[id.AsIdent()] = true
		}
	}
	return names
}

// bound reports whether a comprehension around e binds name where e is: in
// its condition, step or result, and not in the range or the initial value.
func bound(e celast.NavigableExpr, name string) bool {
	_, ok := binder(e, name)
	return ok
}

// binder is the innermost comprehension around e that binds name where e
// is, as bound tests it.
func binder(e celast.NavigableExpr, name string) (celast.ComprehensionExpr, bool) {
	child := e
	for parent, ok := e.Parent(); ok; parent, ok = parent.Parent() {
		if parent.Kind() == celast.ComprehensionKind {
			c := parent.AsComprehension()
			binds := name == c.IterVar() || name == c.IterVar2() || name == c.AccuVar()
			if binds && child.ID() != c.IterRange().ID() && child.ID() != c.AccuInit().ID() {
				return c, true
			}
		}
		child = parent
	}
	return nil, false
}

// depthMessage replaces both of the parser's depth errors. One names the
// limit and the other does not, and neither says what counts as a level.
var depthMessage = fmt.Sprintf("the expression nests deeper than %d levels (each term of a + chain, each ?: and each .call is a level)", MaxDepth)

// parserMessage is CEL's message for a load error, with the depth errors
// replaced and the container CEL names, which assert never sets, left out.
func parserMessage(msg string) string {
	if msg == "max recursion depth exceeded" || strings.HasPrefix(msg, "expression recursion limit exceeded") {
		return depthMessage
	}
	return strings.TrimSuffix(msg, " (in container '')")
}

// located is a load error for expr at loc: the message, then the line of the
// expression with a caret under the column. An error with no place in the
// source, such as a parser limit, is the message alone.
func located(expr string, loc common.Location, msg string) error {
	line, col := loc.Line(), loc.Column()
	if line < 1 {
		return fmt.Errorf("assert: %s", msg)
	}
	lines := strings.Split(expr, "\n")
	text := ""
	if line >= 1 && line <= len(lines) {
		text = strings.ReplaceAll(strings.TrimRight(lines[line-1], "\r"), "\t", " ")
	}
	col = max(0, min(col, utf8.RuneCountInString(text)))
	return fmt.Errorf("assert: %s, at line %d column %d of the expression:\n    %s\n    %s^", msg, line, col+1, text, strings.Repeat(" ", col))
}

// assertion is a compiled assert. It implements check.Assertion.
type assertion struct {
	program cel.Program
	limits  limits
	// grouped are the extractors the expression reads by_file of.
	grouped []string
	// maps are the extractors it reads as lists of maps.
	maps []string
}

type outcome struct {
	value ref.Val
	err   error
}

// Eval evaluates the compiled expression under the cost limit and the
// timeout. CEL looks at the context every interruptEvery comprehension steps,
// and Eval waits a grace after the timeout for that look. Every helper reads
// an argument of at most MaxArgument bytes. split, join and replace are
// linear in what they read, and each builds at most MaxBuilt bytes. So a step
// is short and the look comes soon. An evaluation that does not look within
// the grace, such as one in a call that does not return, keeps its goroutine
// until it ends, and Eval returns without it; the run goes on. An error that
// && or || absorbed is still the result.
func (a *assertion) Eval(parent context.Context, values, files map[string][]string, maps map[string][]*check.Data) (bool, error) {
	log := &errorLog{}
	vars := map[string]any{}
	members := map[string][]ref.Val{}
	for name, v := range values {
		vars[name] = v
		members[name] = celStrings(v)
	}
	for _, name := range a.maps {
		if len(maps[name]) != len(files[name]) {
			return false, fmt.Errorf("extractor %q: %d maps came with %d files, so the values are not whole", name, len(maps[name]), len(files[name]))
		}
		list := make([]ref.Val, len(maps[name]))
		for i, d := range maps[name] {
			list[i] = toCEL(d)
		}
		vars[name] = types.NewRefValList(types.DefaultTypeAdapter, list)
		members[name] = list
	}
	for _, name := range a.grouped {
		groups, err := groupByFile(members[name], files[name])
		if err != nil {
			return false, fmt.Errorf("by_file(%s): %w", name, err)
		}
		vars[byFilePrefix+name] = groups
	}
	vars[logVariable] = log
	ctx, cancel := context.WithTimeout(parent, a.limits.timeout)
	defer cancel()
	done := make(chan outcome, 1)
	go func() {
		v, _, err := a.program.ContextEval(ctx, vars)
		done <- outcome{value: v, err: err}
	}()

	var o outcome
	select {
	case o = <-done:
	case <-ctx.Done():
		select {
		case o = <-done:
		case <-time.After(a.limits.grace):
			return false, a.stopped(parent, nil)
		}
	}
	var cancelled interpreter.EvalCancelledError
	switch {
	case errors.As(o.err, &cancelled) && cancelled.Cause == interpreter.CostLimitExceeded:
		return false, fmt.Errorf("the evaluation exceeded the cost limit of %d", a.limits.cost)
	case errors.Is(o.err, interpreter.InterruptError{}):
		return false, a.stopped(parent, o.err)
	case ctx.Err() != nil:
		// It finished in the grace without looking at the context.
		return false, a.stopped(parent, nil)
	case o.err != nil:
		// An error result names its own cause, a panic's internal error
		// included; the log matters when the result was decided.
		return false, o.err
	case log.first != nil:
		return false, log.first
	}
	result, ok := o.value.Value().(bool)
	if !ok {
		return false, fmt.Errorf("the expression gave %v, not a bool", o.value)
	}
	return result, nil
}

// groupByFile is values grouped by the file of each, files[i] the file of
// values[i], as a CEL map whose keys come in the order of each file's
// first value, so a comprehension over it, and the first error one meets,
// is the same on every run. A count of files other than the values' is
// refused, so a group never holds a value under a file that did not give
// it.
func groupByFile(values []ref.Val, files []string) (ref.Val, error) {
	if len(files) != len(values) {
		return nil, fmt.Errorf("%d values came with %d files, so they cannot be grouped", len(values), len(files))
	}
	groups := map[string][]ref.Val{}
	var order []string
	for i, v := range values {
		if _, ok := groups[files[i]]; !ok {
			order = append(order, files[i])
		}
		groups[files[i]] = append(groups[files[i]], v)
	}
	m := map[ref.Val]ref.Val{}
	for file, group := range groups {
		m[types.String(file)] = types.NewRefValList(types.DefaultTypeAdapter, group)
	}
	return orderedMap{Mapper: types.NewRefValMap(types.DefaultTypeAdapter, m), keys: order}, nil
}

// celStrings is values as CEL strings.
func celStrings(values []string) []ref.Val {
	out := make([]ref.Val, len(values))
	for i, v := range values {
		out[i] = types.String(v)
	}
	return out
}

// toCEL is d as CEL reads it: a scalar as a string, a sequence as a list,
// and a mapping as a map whose keys come in file order, so a comprehension
// over it, and the error it reports, is the same on every run.
func toCEL(d *check.Data) ref.Val {
	if d.Kind == check.DataScalar {
		return types.String(d.Text)
	}
	items := make([]ref.Val, len(d.Items))
	for i, it := range d.Items {
		items[i] = toCEL(it)
	}
	if d.Kind == check.DataList {
		return types.NewRefValList(types.DefaultTypeAdapter, items)
	}
	m := map[ref.Val]ref.Val{}
	for i, k := range d.Keys {
		m[types.String(k)] = items[i]
	}
	return orderedMap{Mapper: types.NewRefValMap(types.DefaultTypeAdapter, m), keys: d.Keys}
}

// orderedMap is a CEL map that iterates its keys in the order keys gives,
// where a Go map's order changes from run to run.
type orderedMap struct {
	traits.Mapper
	keys []string
}

func (m orderedMap) Iterator() traits.Iterator { return &keyIterator{keys: m.keys} }

// keyIterator yields an orderedMap's keys in order: keys holds those not
// yet yielded.
type keyIterator struct {
	keys []string
}

func (it *keyIterator) HasNext() ref.Val { return types.Bool(len(it.keys) != 0) }

func (it *keyIterator) Next() ref.Val {
	if len(it.keys) == 0 {
		return types.NewErr("the iterator has no key left")
	}
	key := it.keys[0]
	it.keys = it.keys[1:]
	return types.String(key)
}

func (it *keyIterator) ConvertToNative(reflect.Type) (any, error) {
	return nil, errors.New("an iterator has no native form")
}
func (it *keyIterator) ConvertToType(ref.Type) ref.Val {
	return types.NewErr("an iterator converts to no type")
}
func (it *keyIterator) Equal(ref.Val) ref.Val { return types.False }
func (it *keyIterator) Type() ref.Type        { return types.IteratorType }
func (it *keyIterator) Value() any            { return it }

// stopped says why an evaluation stopped before it finished. cause is CEL's
// interruption when CEL saw the context end, and nil when Eval stopped
// waiting.
func (a *assertion) stopped(parent context.Context, cause error) error {
	if err := parent.Err(); err != nil {
		return &stopError{msg: "cancelled: " + err.Error(), cause: cause}
	}
	return &stopError{msg: fmt.Sprintf("the evaluation did not finish within the timeout of %s", a.limits.timeout), cause: cause}
}

// stopError is an evaluation that stopped early. Its message is the reason;
// it unwraps to CEL's interruption when there was one.
type stopError struct {
	msg   string
	cause error
}

func (e *stopError) Error() string { return e.msg }
func (e *stopError) Unwrap() error { return e.cause }
