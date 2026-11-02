package rule

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

// The slowest glob to compile known (ADR 0014, The cost): four brace sets,
// a negation and 249 nested extglobs, 1022 bytes, about 80 ms a call on a
// 4 core node until the source bound refuses it.
var slowGlob = strings.Repeat("{a,b}", 4) + "x!(a)" + strings.Repeat("a@(", 249) + "a" + strings.Repeat(")", 249)

// A regex of 603 instructions, which RE2 runs against 1024 bytes in about
// 6 ms on a 4 core node, and in up to 120 ms under the race detector.
var slowRegex = "/(?:\\pL?){300}b/"

// renovate_match stops at the timeout inside one step, where CEL does not
// look at it. Each shape is one step of calls that would run on for a
// second or more past a timeout of 200 ms: 60 compiles of distinct globs
// of slowGlob's shape, 150 runs of slowRegex, and 190 matches of one
// cached glob that spends its steps (stepsGlob). The globs are distinct
// because the cache compiles a repeated one once: 60 calls of slowGlob
// itself would return its cached refusal at once. The grace outlasts any
// shape, so Eval returns when the evaluation ends. A glob stops within a
// node of its compile or 1024 steps of its match, under 5 ms measured; a
// regex that has started runs to its end, one call of slowRegex. The calls
// after the timeout fail at once and are charged their bytes, so the rule
// is unknown on the timeout: charged as calls that ran, 52 compiles of
// slowGlob would pass the cost limit. Its point is how soon each shape
// ends, which the race detector and make test's coverage stretch, so it is
// slow only.
func TestRenovateMatchStopsAtTheTimeout(t *testing.T) {
	slowOnly(t)
	const timeout = 200 * time.Millisecond
	globs := []string{"ab"}
	for i := range 60 {
		// slowGlob's shape, with the first two brace sets naming other
		// letters.
		g := []byte(slowGlob)
		g[3], g[8] = byte('c'+i%20), byte('c'+i/20)
		globs = append(globs, string(g))
	}
	calls := make([]string, 60)
	for i := range calls {
		calls[i] = "renovate_match(v[0],v[" + strconv.Itoa(i+1) + "])"
	}
	for _, c := range []struct {
		name, expr string
		v          []string
	}{
		{"60 compiles of distinct globs", "size(v)==61&&(" + strings.Join(calls, "||") + ")", globs},
		{"150 runs of a regex of 603 instructions",
			// Without spaces, so 150 calls fit in the expression's 4096 characters.
			"size(v)==2&&(" + strings.Repeat("renovate_match(v[0],v[1])||", 149) + "renovate_match(v[0],v[1]))",
			[]string{strings.Repeat("a", MaxArgument), slowRegex}},
		{"190 matches of one glob that spends its steps", oneStepOf(190), []string{"xy", stepsGlob(16, 450)}},
	} {
		a, err := compile(c.expr, []string{"v"}, limits{cost: CostLimit, timeout: timeout, grace: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err = a.Eval(context.Background(), asExtracted(map[string][]string{"v": c.v}, nil, nil))
		past := time.Since(start) - timeout
		t.Logf("%s: ended %s past the timeout", c.name, past)
		if err == nil || err.Error() != "the evaluation did not finish within the timeout of 200ms" {
			t.Errorf("%s: error %v, want the timeout", c.name, err)
		}
		if past > 500*time.Millisecond {
			t.Errorf("%s: ended %s past the timeout, want under 500ms", c.name, past)
		}
	}
}

// A call under a context that is done does not start, with an error that
// wraps the context's, whatever the pattern.
func TestRenovateMatchStopsBeforeACall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, p := range []string{"*", "a*", "/a/"} {
		_, err := renovateMatchesContext(ctx, "a", p)
		if !errors.Is(err, context.Canceled) || err.Error() != "the evaluation was stopped before the call: context canceled" {
			t.Errorf("%q: error %v, want the call stopped", p, err)
		}
	}
}

// A regex is looked at once more after its compile, before it runs.
func TestRenovateMatchStopsBeforeARegexRuns(t *testing.T) {
	ctx := &doneOnLook{Context: context.Background(), look: 2}
	_, err := renovateMatchesContext(ctx, "a", "/a/")
	if !errors.Is(err, context.Canceled) || err.Error() != `the evaluation was stopped before the regex "/a/" ran: context canceled` {
		t.Errorf("error %v, want the regex stopped", err)
	}
}

// doneOnLook is a context that is done from its look-th look on.
type doneOnLook struct {
	context.Context
	looks, look int
}

func (c *doneOnLook) Err() error {
	c.looks++
	if c.looks >= c.look {
		return context.Canceled
	}
	return nil
}

// A rule costs what it cost before the rewrite: the hidden call costs 1
// less than renovate_match, for the read of the log it adds. The costs
// are those each expression had in plain CEL with renovate_match's own
// cost tracker, measured before #251 moved the pricing into the call and
// left the plain overload without one, and since #255 195 more where the
// expression calls the regex /a\.yaml$/, whose compile is charged 18 for
// each of its 10 bytes and 15 for its 9 instructions and 6 runes, once in
// the evaluation (regexCompileCost). None has && or || or exists, whose
// notes and every member rewrite change what CEL charges (TestAssertCost).
func TestRenovateMatchCostsWhatItDid(t *testing.T) {
	values := []string{"infra/a.yaml", "/a\\.yaml$/", "infra/**/*.yaml"}
	for _, c := range []struct {
		expr string
		cost uint64
	}{
		{"renovate_match(v[0], v[1])", 28 + 195},
		{"v.map(p, renovate_match('zzz', p)).size() == 3", 588 + 195},
		{"v.filter(p, renovate_match(v[0], p)).size() == 2", 629 + 195},
		{"[v[2], '*'].all(p, renovate_match(v[0], p))", 339},
	} {
		a, err := compile(c.expr, []string{"v"}, limits{cost: 1 << 62, timeout: time.Minute, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		_, details, err := a.program.ContextEval(context.Background(), map[string]any{"v": values, logVariable: &errorLog{}})
		if err != nil {
			t.Fatal(err)
		}
		if *details.ActualCost() != c.cost {
			t.Errorf("%s: cost %d, want %d", c.expr, *details.ActualCost(), c.cost)
		}
	}
}

// A call whose argument is an error is charged nothing, and never the
// price the call before it stored on the log. cel-go stops evaluating a
// strict call's arguments at the first error, before it reads the log
// variable, and its cost observer then charges no call at all. Over a list
// holding a glob call of 299, v[9] is out of range: on cel-go v0.33.1 the
// pair costs 309, the first call's 299 and the reads around it, and never
// a second 299 (v0.32.0 charged the same call and 319 for the pair, since
// it priced the list's operations otherwise). Were cel-go to run the
// tracker for such a call, it would read the log's price, which
// hiddenCallCost clears once read, so it would read 0 and not 299 again.
func TestRenovateMatchChargesNoPriceForAnErrorArgument(t *testing.T) {
	values := []string{"infra/a.yaml", "x", "infra/**/*.yaml"}
	for _, c := range []struct {
		expr string
		cost uint64
	}{
		{"[renovate_match(v[0], v[2])].size() == 1", 316},
		{"[renovate_match(v[0], v[2]), renovate_match(v[0], v[9])].size() == 2", 309},
	} {
		a, err := compile(c.expr, []string{"v"}, limits{cost: 1 << 62, timeout: time.Minute, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		_, details, _ := a.program.ContextEval(context.Background(), map[string]any{"v": values, logVariable: &errorLog{}})
		if *details.ActualCost() != c.cost {
			t.Errorf("%s: cost %d, want %d", c.expr, *details.ActualCost(), c.cost)
		}
	}
}

// The tracker reads a price once: a second read of the log, with no call
// between, is 0.
func TestHiddenCallCostReadsAPriceOnce(t *testing.T) {
	log := &errorLog{}
	args := []ref.Val{types.String("ab"), types.String("*.yaml"), log}
	renovateHiddenBinding(args...)
	if first, again := hiddenCallCost(args), hiddenCallCost(args); first != 1+2+6+6*18-1 || again != 0 {
		t.Errorf("the price read %d, then %d; want %d, then 0", first, again, 1+2+6+6*18-1)
	}
}

// A program that still calls renovate_match after the rewrite is refused,
// since that call would run uncharged; the rewritten one is not.
func TestRenovateMatchLeftUnrewrittenIsRefused(t *testing.T) {
	base, err := baseEnv()
	if err != nil {
		t.Fatal(err)
	}
	env, err := base.Extend(cel.Variable("v", cel.ListType(cel.StringType)), cel.Variable(logVariable, logType))
	if err != nil {
		t.Fatal(err)
	}
	checked, iss := env.Compile("v.all(p, renovate_match(v[0], p)) && renovate_match('a', 'b')")
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	want := "the rewrite through the evaluation log left renovate_match uncharged, calls left: 2; this is errata's error"
	if err := renovateAllThroughLog(checked); err == nil || err.Error() != want {
		t.Errorf("before the rewrite: error %v, want %q", err, want)
	}
	rewritten, err := renovateThroughLog(env, checked)
	if err != nil {
		t.Fatal(err)
	}
	if err := renovateAllThroughLog(rewritten); err != nil {
		t.Errorf("after the rewrite: %v", err)
	}
}

// A value typed dyn that is not a string, such as a list field passed for a
// string, is refused with the error plain CEL gives renovate_match, which
// names renovate_match and not the hidden function that runs it.
func TestRenovateMatchNamesItselfOnADynArgument(t *testing.T) {
	for _, expr := range []string{
		"size(v) > 0 && renovate_match(dyn(1), 'a')",
		"size(v) > 0 && renovate_match('a', dyn([1]))",
		"v.exists(p, renovate_match(dyn(size(p)), p))",
	} {
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
		program, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		_, _, want := program.Eval(map[string]any{"v": []string{"a"}})
		_, err = eval(t, expr, "a")
		if want == nil || err == nil || err.Error() != want.Error() {
			t.Errorf("%s: error %v, want %v", expr, err, want)
		}
	}
}
