package rule

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
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
	return cel.NewCustomEnv(append(opts, noting()...)...)
})

// identifier is what CEL reads as one variable name. An extractor named
// otherwise could not be written in the expression.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reserved are the words CEL's grammar keeps for itself.
var reserved = []string{
	"as", "break", "const", "continue", "else", "false", "for", "function", "if", "import", "in",
	"let", "loop", "package", "namespace", "null", "return", "true", "var", "void", "while",
}

// Compile type checks expr over the named extractors, each a list(string),
// and returns it ready to evaluate. Each of these is a load error: a name that
// is not an extractor, a type error, a result that is not a bool and a parse
// error, each with the expression and a caret under the column; an extractor
// the expression never uses; an extractor name CEL cannot read.
func Compile(expr string, extractors []string) (check.Assertion, error) {
	a, err := compile(expr, extractors, limits{cost: CostLimit, timeout: EvalTimeout, grace: stopGrace})
	if err != nil {
		// A nil *assertion in the interface would not compare equal to nil.
		return nil, err
	}
	return a, nil
}

// compile is Compile with the limits given, and with extra declarations,
// which only tests pass.
func compile(expr string, extractors []string, lim limits, extra ...cel.EnvOption) (*assertion, error) {
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
		vars = append(vars, cel.Variable(name, cel.ListType(cel.StringType)))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
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
	used := free(ast.NativeRep())
	for _, name := range extractors {
		if !used[name] {
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
	return &assertion{program: program, limits: lim}, nil
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
	child := e
	for parent, ok := e.Parent(); ok; parent, ok = parent.Parent() {
		if parent.Kind() == celast.ComprehensionKind {
			c := parent.AsComprehension()
			binds := name == c.IterVar() || name == c.IterVar2() || name == c.AccuVar()
			if binds && child.ID() != c.IterRange().ID() && child.ID() != c.AccuInit().ID() {
				return true
			}
		}
		child = parent
	}
	return false
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
}

type outcome struct {
	value ref.Val
	err   error
}

// Eval evaluates the compiled expression under the cost limit and the
// timeout. CEL looks at the context every interruptEvery comprehension steps,
// and Eval waits a grace after the timeout for that look. Every helper reads
// an argument of at most MaxArgument bytes, so a step is short and the look
// comes soon. An evaluation that does not look within the grace, such as one
// in a call that does not return, keeps its goroutine until it ends, and Eval
// returns without it; the run goes on. An error that && or || absorbed is
// still the result.
func (a *assertion) Eval(parent context.Context, values map[string][]string) (bool, error) {
	log := &errorLog{}
	vars := make(map[string]any, len(values)+1)
	for name, v := range values {
		vars[name] = v
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
