package rule

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
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
// less than renovate_match, for the read of the log it adds. Each
// expression runs as an assert and in plain CEL with renovate_match's
// cost tracker. None has && or || or exists, whose notes and every
// member rewrite change what CEL charges (TestAssertCost).
func TestRenovateMatchCostsWhatItDid(t *testing.T) {
	values := []string{"infra/a.yaml", "/a\\.yaml$/", "infra/**/*.yaml"}
	for _, expr := range []string{
		"renovate_match(v[0], v[1])",
		"v.map(p, renovate_match('zzz', p)).size() == 3",
		"v.filter(p, renovate_match(v[0], p)).size() == 2",
		"[v[2], '*'].all(p, renovate_match(v[0], p))",
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
		program, err := env.Program(ast, cel.CostLimit(1<<62), cel.CostTrackerOptions(renovateCost()...))
		if err != nil {
			t.Fatal(err)
		}
		_, plain, err := program.Eval(map[string]any{"v": values})
		if err != nil {
			t.Fatal(err)
		}
		a, err := compile(expr, []string{"v"}, limits{cost: 1 << 62, timeout: time.Minute, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		_, stopping, err := a.program.ContextEval(context.Background(), map[string]any{"v": values, logVariable: &errorLog{}})
		if err != nil {
			t.Fatal(err)
		}
		if *stopping.ActualCost() != *plain.ActualCost() {
			t.Errorf("%s: cost %d, want %d", expr, *stopping.ActualCost(), *plain.ActualCost())
		}
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
