package rule

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/interpreter"

	"github.com/f-klement/errata/internal/check"
)

// Each case plants one break in an expression or its extractors and names the
// load error, caret included, that Compile must give.
func TestCompileRefuses(t *testing.T) {
	cases := []struct {
		name       string
		expr       string
		extractors []string
		want       string
	}{
		{
			"a name that is not an extractor", "size(v) == size(w)", []string{"v"},
			"assert: undeclared reference to 'w', at line 1 column 17 of the expression:\n    size(v) == size(w)\n                    ^",
		},
		{
			"a list compared with a string", "v == 'x'", []string{"v"},
			"assert: found no matching overload for '_==_' applied to '(list(string), string)', at line 1 column 3 of the expression:\n    v == 'x'\n      ^",
		},
		{
			"a string added to an int", "v[0] + 1 > 2", []string{"v"},
			"found no matching overload for '_+_' applied to '(string, int)', at line 1 column 6",
		},
		{
			"a duration scaled by a double", "duration(v[0]) * 1.5 > duration('1h')", []string{"v"},
			"found no matching overload for '_*_' applied to '(duration, double)'",
		},
		{
			"a set of lists", "same_set([v], [v])", []string{"v"},
			"found no matching overload for 'same_set' applied to '(list(list(string)), list(list(string)))'",
		},
		{
			"a result that is an int", "size(v)", []string{"v"},
			"assert: the expression has type int; assert needs a bool, at line 1 column 5 of the expression:\n    size(v)\n        ^",
		},
		{
			"a result that is dyn", "dyn(size(v) > 0)", []string{"v"},
			"the expression has type dyn; assert needs a bool",
		},
		{
			"a parse error", "size(v) >", []string{"v"},
			"assert: Syntax error: mismatched input '<EOF>' expecting",
		},
		{
			"an error on the second line", "size(v) > 0 &&\n  v[0] > 3", []string{"v"},
			"at line 2 column 8 of the expression:\n      v[0] > 3\n           ^",
		},
		{
			"an extractor the expression does not use", "size(v) > 0", []string{"v", "w"},
			`extractor "w" is declared but assert does not use it`,
		},
		{
			"an extractor named only by a macro's variable", "size(v) > 0 && [1].all(w, w > 0)", []string{"v", "w"},
			`extractor "w" is declared but assert does not use it`,
		},
		{
			"an extractor named only by a nested macro's variable", "v.exists(a, [a].all(w, w != ''))", []string{"v", "w"},
			`extractor "w" is declared but assert does not use it`,
		},
		{
			// CEL counts a tab as one column, so the caret line has none.
			"a tab before the error", "\tv + 1 > 0", []string{"v"},
			"applied to '(list(string), int)', at line 1 column 4 of the expression:\n     v + 1 > 0\n       ^",
		},
		{
			// A parser limit has no place in the source, so no caret.
			"nesting deeper than the limit", "size(v) > 0 && size(" + nested(MaxDepth) + ") > 0", []string{"v"},
			"assert: the expression nests deeper than 32 levels (each term of a + chain, each ?: and each .call is a level)",
		},
		{
			// The parser's other depth error, which named no limit.
			"a + chain of 32 terms", "size(v[0]" + strings.Repeat(" + v[0]", 31) + ") > 0", []string{"v"},
			"assert: the expression nests deeper than 32 levels",
		},
		{
			"an expression longer than the limit", "size(v) > 0" + strings.Repeat(" && v.all(x, x == '')", 200), []string{"v"},
			"assert: expression code point size exceeds limit: size: 4211, limit 4096",
		},
		{
			"an extractor name CEL cannot read", "true", []string{"s3-endpoint"},
			`extractor "s3-endpoint": assert refers to an extractor by its name, so the name must be letters, digits and _`,
		},
		{
			"an extractor name that starts with a digit", "true", []string{"3d"},
			`extractor "3d": assert refers to an extractor by its name`,
		},
		{
			"an extractor named by a reserved word", "true", []string{"in"},
			`extractor "in": assert refers to an extractor by its name`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := Compile(tc.expr, tc.extractors, Lists{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error:\n%v\nwant it to contain:\n%s", err, tc.want)
			}
			if strings.Contains(err.Error(), "line -1") {
				t.Fatalf("error %q gives a place it does not have", err)
			}
			if a != nil {
				t.Fatalf("Compile returned %#v beside its error", a)
			}
		})
	}
}

// An extractor that is a macro's range, or is read beside a macro that
// reuses its name, is used.
func TestCompileAcceptsAnExtractorAMacroAlsoNames(t *testing.T) {
	for expr, extractors := range map[string][]string{
		"w.all(w, w != '')":                {"w"},
		"[1].all(w, w > 0) && size(w) > 0": {"w"},
		"v.exists(a, a in w)":              {"v", "w"},
		"w.map(w, w + 'x').size() > 0":     {"w"},
	} {
		if _, err := Compile(expr, extractors, Lists{}); err != nil {
			t.Errorf("%s: %v", expr, err)
		}
	}
}

// nested is a map literal nested depth deep, the shape whose type check took
// seconds before the parser limits.
func nested(depth int) string {
	return strings.Repeat("{1: ", depth) + "1" + strings.Repeat("}", depth)
}

// Load time is bounded: inputs that took 3 to 21 seconds to type check are
// refused at once, and the slowest shape measured that fits the limits still
// compiles.
func TestCompileTimeIsBounded(t *testing.T) {
	refused := []string{
		"size(v) > 0 && size(" + nested(200) + ") > 0",
		"size(v) > 0 && size(" + nested(240) + ") > 0",
		strings.Repeat("size("+nested(200)+") > 0 && ", 8) + "size(v) > 0",
		"size(v) > 0" + strings.Repeat(" && v.all(x, x == '')", 4000),
	}
	for _, expr := range refused {
		start := time.Now()
		if _, err := Compile(expr, []string{"v"}, Lists{}); err == nil || !strings.Contains(err.Error(), "deeper than 32 levels") && !strings.Contains(err.Error(), "limit 4096") {
			t.Errorf("%.40s...: error %v, want a limit named", expr, err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("%.40s...: refused after %s", expr, took)
		}
	}
	worst := "size(v) > 0"
	for len(worst)+60 <= MaxLength {
		worst += " && v.map(x, v.filter(y, v.exists(z, x+y == z))).size() > 0"
	}
	start := time.Now()
	if _, err := Compile(worst, []string{"v"}, Lists{}); err != nil {
		t.Fatalf("an expression of %d code points within the limits: %v", len(worst), err)
	}
	// 0.3 s on the build machine; the bound catches a limit that stopped
	// working, not a slow machine.
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("the slowest shape within the limits compiled in %s", took)
	}
}

// Names CEL also uses for a type or a function are still extractor names.
func TestCompileAcceptsNamesThatAreAlsoCELNames(t *testing.T) {
	for _, name := range []string{"int", "string", "duration", "size", "_x", "Port2"} {
		if _, err := Compile("size("+name+") > 0", []string{name}, Lists{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// assertRule is a rule with an assert compiled over the extractors' names.
func assertRule(t *testing.T, expr string, extractors ...check.NamedExtractor) check.Check {
	t.Helper()
	names := make([]string, len(extractors))
	var maps []string
	for i, e := range extractors {
		names[i] = e.Name
		if e.Maps {
			maps = append(maps, e.Name)
		}
	}
	a, err := Compile(expr, names, Lists{Maps: maps})
	if err != nil {
		t.Fatal(err)
	}
	return check.Check{ID: "r", Reason: "r", Scope: check.ScopeRepo, Kind: &check.Rule{Extractors: extractors, Assert: expr, Compiled: a}}
}

// The worked case from the ticket: a staleness threshold must lie between one
// backup interval and two.
func TestAssertStalenessThreshold(t *testing.T) {
	const expr = "duration(threshold[0]) >= cron_interval(schedule[0]) &&\n  duration(threshold[0]) <= cron_interval(schedule[0]) * 2"
	run := func(schedule, threshold string) check.Result {
		return Run(context.Background(), "", assertRule(t, expr,
			named("schedule", fake{values: []check.Value{at(schedule, "deploy/backup.yaml", 12)}}),
			named("threshold", fake{values: []check.Value{at(threshold, "monitoring/rules.yaml", 40)}}),
		))
	}
	for _, tc := range []struct {
		schedule, threshold string
		want                check.Verdict
	}{
		{"17 */6 * * *", "9h", check.VerdictTrue},
		{"17 */6 * * *", "6h", check.VerdictTrue},
		{"17 */6 * * *", "12h", check.VerdictTrue},
		{"H H/6 * * *", "540m", check.VerdictTrue},
		{"30 5 * * 0", "8d", check.VerdictTrue},
		{"17 */6 * * *", "5h59m", check.VerdictFalse},
		{"17 */6 * * *", "12h1m", check.VerdictFalse},
		{"17 */6 * * *", "1d", check.VerdictFalse},
	} {
		if res := run(tc.schedule, tc.threshold); res.Verdict != tc.want {
			t.Errorf("schedule %q threshold %q: verdict %s, want %s: %s", tc.schedule, tc.threshold, res.Verdict, tc.want, res.Message)
		}
	}

	res := run("17 */6 * * *", "13h")
	want := `assert: the expression is false
  expression: duration(threshold[0]) >= cron_interval(schedule[0]) &&
                duration(threshold[0]) <= cron_interval(schedule[0]) * 2
  schedule[0] = "17 */6 * * *" at deploy/backup.yaml:12
  threshold[0] = "13h" at monitoring/rules.yaml:40`
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant false and:\n%s", res.Verdict, res.Message, want)
	}

	res = run("30 5 * * 1-5", "26h")
	if res.Verdict != check.VerdictUnknown {
		t.Fatalf("a schedule with no fixed interval: verdict %s, want unknown: %s", res.Verdict, res.Message)
	}
	for _, w := range []string{
		`assert: cron_interval: "30 5 * * 1-5" has no fixed interval: its runs are 24h0m0s or 72h0m0s apart`,
		`schedule[0] = "30 5 * * 1-5" at deploy/backup.yaml:12`,
		`threshold[0] = "26h" at monitoring/rules.yaml:40`,
	} {
		if !strings.Contains(res.Message, w) {
			t.Errorf("message %q does not contain %q", res.Message, w)
		}
	}
}

// counting is an Assertion that records whether it ran.
type counting struct{ calls *int }

func (c counting) Eval(context.Context, map[string][]string, map[string][]string, map[string][]*check.Data) (bool, error) {
	*c.calls++
	return true, nil
}

// Fail closed, as agree does: an extractor that fails or finds nothing is
// unknown before CEL runs, and every such extractor is named.
func TestAssertUnknownBeforeCELRuns(t *testing.T) {
	one := []check.Value{at("x", "f", 1)}
	cases := []struct {
		name       string
		allowEmpty []string
		extractors []check.NamedExtractor
		want       []string
	}{
		{"an extractor error", nil,
			[]check.NamedExtractor{named("a", fake{values: one}), named("b", fake{err: errors.New("open b.yaml: no such file")})},
			[]string{`extractor "b": open b.yaml: no such file`}},
		{"an error from an extractor allow_empty lists", []string{"b"},
			[]check.NamedExtractor{named("a", fake{err: errors.New("boom a")}), named("b", fake{err: errors.New("boom b")})},
			[]string{`extractor "a": boom a`, `extractor "b": boom b`}},
		{"no value", []string{"a"},
			[]check.NamedExtractor{named("a", fake{}), named("b", fake{})},
			[]string{`extractor "b" found no value`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			c := check.Check{ID: "r", Kind: &check.Rule{Extractors: tc.extractors, AllowEmpty: tc.allowEmpty, Assert: "x", Compiled: counting{&calls}}}
			res := Run(context.Background(), "", c)
			if res.Verdict != check.VerdictUnknown {
				t.Fatalf("verdict %s, want unknown", res.Verdict)
			}
			for _, w := range tc.want {
				if !strings.Contains(res.Message, w) {
					t.Errorf("message %q does not contain %q", res.Message, w)
				}
			}
			if calls != 0 {
				t.Errorf("the expression ran %d times", calls)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	c := check.Check{ID: "r", Kind: &check.Rule{Extractors: []check.NamedExtractor{named("a", fake{values: one})}, Assert: "x", Compiled: counting{&calls}}}
	res := Run(ctx, "", c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, `cancelled before extractor "a" ran`) || calls != 0 {
		t.Fatalf("cancelled: verdict %s message %q calls %d", res.Verdict, res.Message, calls)
	}
}

// A rule that uses assert without a compiled expression is unknown, never
// true, and runs no extractor.
func TestAssertNotCompiled(t *testing.T) {
	var ran string
	c := check.Check{ID: "r", Kind: &check.Rule{
		Extractors: []check.NamedExtractor{named("a", fake{values: []check.Value{at("x", "f", 1)}, root: &ran})},
		Assert:     "size(a) > 0",
	}}
	res := Run(context.Background(), "/repo", c)
	if res.Verdict != check.VerdictUnknown || res.Message != "assert: the expression was not compiled at load, so it cannot run" {
		t.Fatalf("verdict %s message %q", res.Verdict, res.Message)
	}
	if ran != "" {
		t.Fatalf("an uncompiled assert ran its extractor at %q", ran)
	}
}

// An extractor that allow_empty lists reaches CEL as an empty list, and
// reading past its end is unknown with the error and the values.
func TestAssertAllowEmpty(t *testing.T) {
	extractors := func() []check.NamedExtractor {
		return []check.NamedExtractor{named("a", fake{values: []check.Value{at("x", "a.yaml", 1)}}), named("b", fake{})}
	}
	c := assertRule(t, "size(b) == 0 && a[0] == 'x'", extractors()...)
	c.Rule().AllowEmpty = []string{"b"}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s, want true: %s", res.Verdict, res.Message)
	}

	c = assertRule(t, "b[0] == a[0]", extractors()...)
	c.Rule().AllowEmpty = []string{"b"}
	res := Run(context.Background(), "", c)
	want := `assert: index out of bounds: 0
  expression: b[0] == a[0]
  a[0] = "x" at a.yaml:1
  b = [] (allow_empty)`
	if res.Verdict != check.VerdictUnknown || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant unknown and:\n%s", res.Verdict, res.Message, want)
	}
}

// A CEL runtime error is unknown with the error and the values, never false.
func TestAssertRuntimeErrorIsUnknown(t *testing.T) {
	for _, tc := range []struct{ expr, value, want string }{
		{"int(v[0]) > 2", "three", `assert: int: "three": type conversion error from 'string' to 'int'`},
		{"uint(v[0]) > 2u", "-1", `assert: uint: "-1": type conversion error from 'string' to 'uint'`},
		{"double(v[0]) > 2.0", "2,5", `assert: double: "2,5": type conversion error from 'string' to 'double'`},
		// The set cost tracker used to panic on an argument that is an error,
		// and the message said "internal error" instead of the cause.
		{"subset(['1'], v.map(x, string(int(x))))", "a", `assert: int: "a": type conversion error from 'string' to 'int'`},
		{"same_set(v, [v[5]])", "a", `assert: index out of bounds: 5`},
		{"duration(v[0]) > duration('1h')", "soon", `assert: duration: "soon" is not a Go duration such as 90s or 1h30m, nor a Prometheus duration such as 2d or 1w`},
		{"port_split(v[0]).host == '8080'", "8080:http", `assert: port_split: "8080:http": the container port "http" is not a number`},
		{"v[1] == 'x'", "x", `assert: index out of bounds: 1`},
		{"duration(v[0]) * 9223372036854775807 > duration('1s')", "2s", `assert: duration overflow: 2s * 9223372036854775807`},
	} {
		res := Run(context.Background(), "", assertRule(t, tc.expr, named("v", fake{values: []check.Value{at(tc.value, "f.yaml", 7)}})))
		if res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, tc.want) {
			t.Errorf("%s over %q: verdict %s message:\n%s\nwant unknown starting %q", tc.expr, tc.value, res.Verdict, res.Message, tc.want)
		}
		if w := fmt.Sprintf("v[0] = %q at f.yaml:7", tc.value); !strings.Contains(res.Message, w) {
			t.Errorf("%s: message %q does not show %s", tc.expr, res.Message, w)
		}
	}
}

// long is an extractor that finds n values of 10,000 characters each.
func long(n int) fake {
	values := make([]check.Value, n)
	for i := range values {
		values[i] = at(strings.Repeat(strconv.Itoa(i%10), 10_000), "big.yaml", i+1)
	}
	return fake{values: values}
}

// An expression that would take too long stops at the cost limit, with the
// limit named. The planted break joins every pair of 30 long values: CEL
// charges each join by its length, so the limit falls after about 500 joins,
// long before the timeout on any machine.
func TestAssertCostLimit(t *testing.T) {
	const expr = "v.all(a, v.all(b, size(a + b) > 0))"
	res := Run(context.Background(), "", assertRule(t, expr, named("v", long(30))))
	if res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, "assert: the evaluation exceeded the cost limit of 1000000\n") {
		t.Fatalf("verdict %s message %.200q, want unknown and the cost limit", res.Verdict, res.Message)
	}
	// The same expression over five values is within the limit.
	if res := Run(context.Background(), "", assertRule(t, expr, named("v", long(5)))); res.Verdict != check.VerdictTrue {
		t.Fatalf("five values: verdict %s, want true: %.200s", res.Verdict, res.Message)
	}
}

// Work the cost limit allows still stops at the timeout, with the timeout
// named. CEL sees the context end between comprehension steps and stops, so
// the error is CEL's interruption.
func TestAssertTimeout(t *testing.T) {
	a, err := compile("v.all(a, v.all(b, a + b != 'x'))", []string{"v"}, limits{cost: 1 << 62, timeout: time.Millisecond, grace: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, 3000)
	for i := range values {
		values[i] = strconv.Itoa(i)
	}
	start := time.Now()
	_, err = a.Eval(context.Background(), map[string][]string{"v": values}, nil, nil)
	if err == nil || err.Error() != "the evaluation did not finish within the timeout of 1ms" {
		t.Fatalf("error = %v, want the timeout named", err)
	}
	if !errors.Is(err, interpreter.InterruptError{}) {
		t.Fatalf("error %v did not come from CEL stopping", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Eval returned after %s", took)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Eval(ctx, map[string][]string{"v": values}, nil, nil); err == nil || !strings.HasPrefix(err.Error(), "cancelled: context canceled") {
		t.Fatalf("cancelled: error = %v", err)
	}
}

// One helper call that runs past the timeout is not a comprehension, so CEL
// never looks at the context during it; Eval returns after the grace without
// it. The helper here blocks until the test releases it.
func TestAssertTimeoutInsideAHelper(t *testing.T) {
	release, finished := make(chan struct{}), make(chan struct{})
	block := cel.Function("block", cel.Overload("block_list", []*cel.Type{cel.ListType(cel.StringType)}, cel.BoolType,
		cel.UnaryBinding(func(ref.Val) ref.Val {
			<-release
			close(finished)
			return types.True
		})))
	a, err := compile("block(v)", []string{"v"}, limits{cost: 1 << 62, timeout: time.Millisecond, grace: time.Millisecond}, block)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Eval(context.Background(), map[string][]string{"v": {"x"}}, nil, nil)
	if err == nil || err.Error() != "the evaluation did not finish within the timeout of 1ms" {
		t.Fatalf("error = %v, want the timeout named", err)
	}
	if errors.Is(err, interpreter.InterruptError{}) {
		t.Fatalf("error %v says CEL stopped, but it was inside one call", err)
	}
	select {
	case <-finished:
		t.Fatal("Eval waited for the helper")
	default:
	}
	close(release)
	<-finished

	// A helper that ends after the deadline but within the grace still
	// ran past the timeout, so its true is not the verdict.
	late := cel.Function("late", cel.Overload("late_list", []*cel.Type{cel.ListType(cel.StringType)}, cel.BoolType,
		cel.UnaryBinding(func(ref.Val) ref.Val {
			time.Sleep(50 * time.Millisecond)
			return types.True
		})))
	a, err = compile("late(v)", []string{"v"}, limits{cost: 1 << 62, timeout: time.Millisecond, grace: 10 * time.Second}, late)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Eval(context.Background(), map[string][]string{"v": {"x"}}, nil, nil); err == nil || err.Error() != "the evaluation did not finish within the timeout of 1ms" {
		t.Fatalf("a helper that ended in the grace: error = %v, want the timeout named", err)
	}
}

// CEL looks at the timeout after every comprehension step, so an evaluation
// whose steps are slow stops within one step of the deadline. Each step here
// sleeps 5 ms; a look every 100 steps would come 500 ms late, past the grace.
func TestAssertTimeoutLooksEveryStep(t *testing.T) {
	nap := cel.Function("nap", cel.Overload("nap_string", []*cel.Type{cel.StringType}, cel.BoolType,
		cel.UnaryBinding(func(ref.Val) ref.Val {
			time.Sleep(5 * time.Millisecond)
			return types.True
		})))
	a, err := compile("v.all(x, nap(x))", []string{"v"}, limits{cost: CostLimit, timeout: time.Millisecond, grace: 200 * time.Millisecond}, nap)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]string, 300)
	for i := range values {
		values[i] = strconv.Itoa(i)
	}
	_, err = a.Eval(context.Background(), map[string][]string{"v": values}, nil, nil)
	if err == nil || err.Error() != "the evaluation did not finish within the timeout of 1ms" || !errors.Is(err, interpreter.InterruptError{}) {
		t.Fatalf("error = %v, want the timeout, with CEL stopping in the grace", err)
	}
}

// An error that && or || absorbed, alone or inside exists and all, makes the
// rule unknown, with the value that failed named. An operand that does not
// run because the left one decided is not an error.
func TestAssertUnknownOnAnyError(t *testing.T) {
	run := func(expr string, extractors ...check.NamedExtractor) check.Result {
		c := assertRule(t, expr, extractors...)
		c.Rule().AllowEmpty = []string{"empty"}
		return Run(context.Background(), "", c)
	}
	ports := named("v", fake{values: []check.Value{at("${HTTPS_PORT}", "compose.yaml", 4), at("443", "compose.yaml", 5)}})
	for _, tc := range []struct {
		expr string
		want string
	}{
		{"v.exists(p, int(p) == 443)", `assert: int: "${HTTPS_PORT}": type conversion error`},
		{"int(v[0]) > 0 || size(v) == 2", `assert: int: "${HTTPS_PORT}"`},
		{"int(v[0]) > 0 && size(v) == 3", `assert: int: "${HTTPS_PORT}"`},
		{"v.all(p, p == '443' || int(p) > 0)", `assert: int: "${HTTPS_PORT}"`},
		{"!(int(v[0]) > 0) || true", `assert: int: "${HTTPS_PORT}"`},
	} {
		res := run(tc.expr, ports)
		if res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, tc.want) {
			t.Errorf("%s: verdict %s message:\n%s\nwant unknown starting %q", tc.expr, res.Verdict, res.Message, tc.want)
		}
		if !strings.Contains(res.Message, `v[0] = "${HTTPS_PORT}" at compose.yaml:4`) {
			t.Errorf("%s: message %q does not show the values", tc.expr, res.Message)
		}
	}

	// An error in the first run of an inner comprehension, which later runs
	// do not repeat, is still seen.
	res := run("v.all(a, w.exists(b, int(b) == size(a)))",
		named("v", fake{values: []check.Value{at("x", "a.yaml", 1), at("yy", "a.yaml", 2)}}),
		named("w", fake{values: []check.Value{at("z", "b.yaml", 1), at("1", "b.yaml", 2), at("2", "b.yaml", 3)}}))
	if res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, `assert: int: "z"`) {
		t.Errorf("nested: verdict %s message %q, want unknown naming \"z\"", res.Verdict, res.Message)
	}

	for _, tc := range []struct {
		expr string
		want check.Verdict
	}{
		{"v.filter(p, p.matches('^[0-9]+$')).exists(p, int(p) == 443)", check.VerdictTrue},
		{"v.exists(p, p.matches('^[0-9]+$') && int(p) == 443)", check.VerdictTrue},
		{"size(v) > 5 && int(v[5]) > 0", check.VerdictFalse},
		{"size(v) == 2 || int(v[0]) > 0", check.VerdictTrue},
		{"size(empty) > 0 ? int(empty[0]) > 0 : size(v) == 2", check.VerdictTrue},
		{"size(empty) == 0 || int(empty[0]) > size(v)", check.VerdictTrue},
	} {
		extractors := []check.NamedExtractor{ports}
		if strings.Contains(tc.expr, "empty") {
			extractors = append(extractors, named("empty", fake{}))
		}
		if res := run(tc.expr, extractors...); res.Verdict != tc.want {
			t.Errorf("%s: verdict %s, want %s: %s", tc.expr, res.Verdict, tc.want, res.Message)
		}
	}
}

// Values reach CEL normalised, as agree compares them, and the message shows
// that form; the result keeps them raw.
func TestAssertSeesNormalisedValues(t *testing.T) {
	raw := []check.Value{at(" 90S\n", "a.yaml", 3)}
	c := assertRule(t, "duration(v[0]) == duration('90s')", named("v", fake{values: raw}))
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictUnknown {
		t.Fatalf("without normalize: verdict %s, want unknown: %s", res.Verdict, res.Message)
	}
	c.Rule().Normalize = []check.Normalization{check.NormalizeTrim, check.NormalizeLower}
	res := Run(context.Background(), "", c)
	if res.Verdict != check.VerdictTrue {
		t.Fatalf("with normalize: verdict %s, want true: %s", res.Verdict, res.Message)
	}
	if want := []check.Extracted{{Extractor: "v", Values: raw}}; !reflect.DeepEqual(res.Extracted, want) {
		t.Fatalf("extracted = %+v, want %+v", res.Extracted, want)
	}
	c.Rule().Assert = "v[0] == 'x'"
	c.Rule().Compiled, _ = Compile(c.Rule().Assert, []string{"v"}, Lists{})
	if res := Run(context.Background(), "", c); !strings.Contains(res.Message, `v[0] = "90s" at a.yaml:3`) {
		t.Fatalf("message %q does not show the normalised value", res.Message)
	}
}

// The result carries every value in extractor name order, raw, and the
// message shows each by the index the expression reads it at.
func TestAssertMessageAndExtracted(t *testing.T) {
	ports := []check.Value{at("8080:80", "compose.yaml", 5), at("9090:90/udp", "compose.yaml", 6)}
	docs := []check.Value{at("8080", "README.md", 12)}
	c := assertRule(t, "subset(docs, ports.map(p, port_split(p).host)) && size(docs) == 2",
		named("docs", fake{values: docs}), named("ports", fake{values: ports}))
	res := Run(context.Background(), "", c)
	want := `assert: the expression is false
  expression: subset(docs, ports.map(p, port_split(p).host)) && size(docs) == 2
  docs[0] = "8080" at README.md:12
  ports[0] = "8080:80" at compose.yaml:5
  ports[1] = "9090:90/udp" at compose.yaml:6`
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant false and:\n%s", res.Verdict, res.Message, want)
	}
	if want := []check.Extracted{{Extractor: "docs", Values: docs}, {Extractor: "ports", Values: ports}}; !reflect.DeepEqual(res.Extracted, want) {
		t.Fatalf("extracted = %+v, want %+v", res.Extracted, want)
	}
}

// The evaluation log is an opaque CEL value: equal only to itself, and
// convertible to nothing, so an expression cannot read it as data.
func TestErrorLogIsOpaque(t *testing.T) {
	l, other := &errorLog{}, &errorLog{}
	same := ref.Val(l)
	if l.Equal(same) != types.True || l.Equal(other) != types.False {
		t.Fatalf("Equal: self %v, other %v", l.Equal(same), l.Equal(other))
	}
	if l.Value() != any(l) || l.Type() != logType {
		t.Fatalf("Value %v, Type %v", l.Value(), l.Type())
	}
	if _, err := l.ConvertToNative(reflect.TypeFor[string]()); err == nil {
		t.Fatal("ConvertToNative gave no error")
	}
	if v := l.ConvertToType(types.StringType); !types.IsError(v) {
		t.Fatalf("ConvertToType = %v, want an error", v)
	}
	l.note(types.True)
	l.note(types.NewErr("first"))
	l.note(types.NewErr("second"))
	if l.first == nil || l.first.Error() != "first" {
		t.Fatalf("first = %v, want the first error", l.first)
	}
}

// costs returns what plain CEL and the compiled assert charge for expr over
// values in the same environment.
func costs(t *testing.T, expr string, values []string) (plain, noted uint64) {
	t.Helper()
	base, err := baseEnv()
	if err != nil {
		t.Fatal(err)
	}
	env, err := base.Extend(cel.Variable("v", cel.ListType(cel.StringType)))
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	program, err := env.Program(ast, cel.CostLimit(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	_, det, err := program.Eval(map[string]any{"v": values})
	if err != nil {
		t.Fatal(err)
	}
	a, err := compile(expr, []string{"v"}, limits{cost: 1 << 62, timeout: time.Minute, grace: stopGrace})
	if err != nil {
		t.Fatal(err)
	}
	_, det2, err := a.program.ContextEval(context.Background(), map[string]any{"v": values, logVariable: &errorLog{}})
	if err != nil {
		t.Fatal(err)
	}
	return *det.ActualCost(), *det2.ActualCost()
}

// The notes cost nothing; each operand of && or || that runs costs one read
// of the log. exists and all cost what CEL charges for every member.
func TestAssertCost(t *testing.T) {
	values := make([]string, 100)
	for i := range values {
		values[i] = strconv.Itoa(i)
	}
	for expr, extra := range map[string]int{
		"v.all(x, x != '')":                                     0,
		"v.all(a, v.all(b, a + b != 'x'))":                      0,
		"v.filter(x, x != '').size() > 0":                       0,
		"size(v) > 0 && v[0] != ''":                             2,
		"v.all(x, x != '' && x != 'a' && x != 'b' && x != 'c')": 6 * len(values),
	} {
		plain, noted := costs(t, expr, values)
		if noted != plain+uint64(extra) {
			t.Errorf("%s: cost %d, want CEL's %d + %d", expr, noted, plain, extra)
		}
	}
}

// A non-bool that reaches && or || through dyn is logged like an error, and
// no message names the hidden note.
func TestAssertNonBoolOperand(t *testing.T) {
	v := named("v", fake{values: []check.Value{at("x", "a.yaml", 1)}})
	for _, expr := range []string{"dyn(v[0]) || true", "dyn(v[0]) && false", "false || dyn(v[0]) || true", "v.exists(p, dyn(p)) || true", "[true, 'x'].exists(b, b) || size(v) > 0"} {
		res := Run(context.Background(), "", assertRule(t, expr, v))
		if res.Verdict != check.VerdictUnknown || !strings.HasPrefix(res.Message, "assert: && and || need a bool and got a string") {
			t.Errorf("%s: verdict %s message %q", expr, res.Verdict, res.Message)
		}
		if strings.Contains(res.Message, "@errata") {
			t.Errorf("%s: message %q names the hidden note", expr, res.Message)
		}
	}
}

// exists and all run every member, so an error in any member is unknown
// whatever the order of the values; a guard inside the predicate still
// protects it.
func TestAssertEveryMemberRuns(t *testing.T) {
	orders := [][]string{{"443", "${HTTPS_PORT}"}, {"${HTTPS_PORT}", "443"}}
	for _, order := range orders {
		v := named("v", fake{values: []check.Value{at(order[0], "compose.yaml", 4), at(order[1], "compose.yaml", 5)}})
		for expr, want := range map[string]check.Verdict{
			"v.exists(p, int(p) == 443)":                                   check.VerdictUnknown,
			"v.all(p, int(p) != 80)":                                       check.VerdictUnknown,
			"v.all(p, int(p) == 443)":                                      check.VerdictUnknown,
			"v.exists_one(p, int(p) == 443)":                               check.VerdictUnknown,
			"v.filter(p, p.matches('^[0-9]+$')).exists(p, int(p) == 443)":  check.VerdictTrue,
			"v.exists(p, p.matches('^[0-9]+$') && int(p) == 443)":          check.VerdictTrue,
			"v.all(p, !p.matches('^[0-9]+$') || int(p) > 0)":               check.VerdictTrue,
			"v.filter(p, p.matches('^[0-9]+$')).all(p, int(p) == 80)":      check.VerdictFalse,
			"v.exists(p, p == '443') && v.exists(p, p.startsWith('${'))":   check.VerdictTrue,
			"v.all(p, p != 'x') && !v.exists(p, p == 'x') && size(v) == 2": check.VerdictTrue,
		} {
			res := Run(context.Background(), "", assertRule(t, expr, v))
			if res.Verdict != want {
				t.Errorf("%s over %q: verdict %s, want %s: %s", expr, order, res.Verdict, want, res.Message)
			}
			if want == check.VerdictUnknown && !strings.HasPrefix(res.Message, `assert: int: "${HTTPS_PORT}"`) {
				t.Errorf("%s over %q: message %q, want the failing value named", expr, order, res.Message)
			}
		}
	}
}

// A panic in a helper is reported as CEL's internal error even after an
// error that || absorbed, so the fuzz target can see it.
func TestAssertPanicAfterAbsorbedError(t *testing.T) {
	boom := cel.Function("boom", cel.Overload("boom_list", []*cel.Type{cel.ListType(cel.StringType)}, cel.BoolType,
		cel.UnaryBinding(func(ref.Val) ref.Val { panic("planted") })))
	for _, expr := range []string{"boom(v)", "int(v[0]) > 0 || boom(v)", "v.exists(p, int(p) > 0) || boom(v)"} {
		a, err := compile(expr, []string{"v"}, limits{cost: CostLimit, timeout: EvalTimeout, grace: stopGrace}, boom)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Eval(context.Background(), map[string][]string{"v": {"a"}}, nil, nil); err == nil || !strings.Contains(err.Error(), "internal error") {
			t.Errorf("%s: error %v, want the panic's internal error", expr, err)
		}
	}
}

// The timestamp accessors read 'UTC', fixed offsets and named zones, the
// last from the database embedded in errata.
func TestTimeZones(t *testing.T) {
	for zone, want := range map[string]string{
		"UTC": "12", "+01:00": "13", "-05:30": "6",
		"Europe/Berlin": "13", "America/New_York": "8", "Asia/Kolkata": "17", "Etc/GMT+5": "7",
	} {
		ok, err := eval(t, "string(timestamp('2020-03-08T12:00:00Z').getHours(v[0])) == '"+want+"'", zone)
		if err != nil || !ok {
			t.Errorf("getHours(%q): %v, %v; want %s", zone, ok, err, want)
		}
	}
	for zone, want := range map[string]string{"+01:00": "2021", "Europe/Berlin": "2021", "America/New_York": "2020"} {
		ok, err := eval(t, "string(timestamp('2020-12-31T23:30:00Z').getFullYear(v[0])) == '"+want+"'", zone)
		if err != nil || !ok {
			t.Errorf("getFullYear(%q): %v, %v; want %s", zone, ok, err, want)
		}
	}
	for _, zone := range []string{"Local", "utc", "", "Mars/Olympus", "../../etc/passwd", "Europe"} {
		_, err := eval(t, "timestamp('2020-03-08T12:00:00Z').getHours(v[0]) == 12", zone)
		want := fmt.Sprintf("getHours: the time zone %q is not in errata's time zone database (IANA %s)", zone, zoneDataVersion)
		if err == nil || err.Error() != want {
			t.Errorf("getHours(%q): error %v, want %q", zone, err, want)
		}
	}
}

// Every helper refuses an argument longer than MaxArgument, naming the
// limit, and reads one of exactly that length.
func TestHelpersBoundTheirArgument(t *testing.T) {
	for name, expr := range map[string]string{
		"duration":      "duration(v[0]) > duration('0s')",
		"cron_interval": "cron_interval(v[0]) > duration('0s')",
		"port_split":    "port_split(v[0]).host == ''",
		"int":           "int(v[0]) > 0",
		"uint":          "uint(v[0]) > 0u",
		"double":        "double(v[0]) > 0.0",
	} {
		long := strings.Repeat("1", MaxArgument+1)
		_, err := eval(t, expr, long)
		want := fmt.Sprintf("%s: the argument is %d bytes; a helper reads at most %d", name, MaxArgument+1, MaxArgument)
		if err == nil || err.Error() != want {
			t.Errorf("%s over %d bytes: error %v, want %q", name, len(long), err, want)
		}
		if _, err := eval(t, expr, long[1:]); err != nil && strings.Contains(err.Error(), "a helper reads at most") {
			t.Errorf("%s over %d bytes: %v", name, MaxArgument, err)
		}
	}
}
