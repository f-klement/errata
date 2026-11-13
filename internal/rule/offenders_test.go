package rule

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// The address rule of the ticket, with one address at a real provider
// among four: the message shows that one value with its place, counts the
// rest, and under --all-values shows every value after it. Planted breaks:
// the predicate of the all noted as any operand, which gave no account
// and so the whole list; the offenders kept where the predicate was true,
// which showed the three reserved addresses.
func TestAFalseAllShowsTheValuesItFoundFalse(t *testing.T) {
	const expr = "addresses.all(a, a.endsWith('@example.org'))"
	addresses := named("addresses", fake{values: []check.Value{
		at("kim@example.org", "backend/a.py", 3), at("sam@example.org", "backend/a.py", 9),
		at("kim.jones@gmail.com", "backend/b.py", 12), at("lee@example.org", "backend/c.py", 1),
	}})
	c := assertRule(t, expr, addresses)
	res := Run(context.Background(), ".", c)
	want := `assert: the expression is false
  expression: addresses.all(a, a.endsWith('@example.org'))
  the all is false for 1 of the 4 values of addresses:
  addresses[2] = "kim.jones@gmail.com" at backend/b.py:12
  3 more values not shown: errata check --all-values prints every value`
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("verdict %s, message:\n%s\nwant false and:\n%s", res.Verdict, res.Message, want)
	}
	res = Run(check.WithAllValues(context.Background()), ".", c)
	want = `assert: the expression is false
  expression: addresses.all(a, a.endsWith('@example.org'))
  the all is false for 1 of the 4 values of addresses:
  addresses[2] = "kim.jones@gmail.com" at backend/b.py:12
  every value:
  addresses[0] = "kim@example.org" at backend/a.py:3
  addresses[1] = "sam@example.org" at backend/a.py:9
  addresses[2] = "kim.jones@gmail.com" at backend/b.py:12
  addresses[3] = "lee@example.org" at backend/c.py:1`
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("--all-values: verdict %s, message:\n%s\nwant false and:\n%s", res.Verdict, res.Message, want)
	}
}

// The fence rule of the ticket: an all over by_file(x) is false for a
// file, and the message shows every value of the rule in each such file,
// in the order of the extractors and their values, and counts the rest.
// Planted break: the files taken in sorted order rather than in the order
// by_file runs them, which blamed a.md.
func TestAFalseAllOverByFileShowsTheValuesOfItsFalseFiles(t *testing.T) {
	const expr = "by_file(fences).all(f, size(by_file(fences)[f]) % 2 == 0)"
	fences := named("fences", fake{values: []check.Value{
		at("```", "z.md", 1), at("```", "z.md", 4), at("```", "a.md", 2),
		at("```", "m.md", 5), at("```", "m.md", 9), at("```", "a.md", 7), at("```", "a.md", 8),
	}})
	res := Run(context.Background(), ".", assertRule(t, expr, fences))
	want := "assert: the expression is false\n" +
		"  expression: by_file(fences).all(f, size(by_file(fences)[f]) % 2 == 0)\n" +
		"  the all is false for 1 of the 3 files of by_file(fences); their values:\n" +
		"  fences[2] = \"```\" at a.md:2\n" +
		"  fences[5] = \"```\" at a.md:7\n" +
		"  fences[6] = \"```\" at a.md:8\n" +
		"  4 more values not shown: errata check --all-values prints every value"
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("verdict %s, message:\n%s\nwant false and:\n%s", res.Verdict, res.Message, want)
	}
}

// An all over by_file(x) that reads another extractor shows that
// extractor's values in the file it was false for too, and the line that
// says an extractor found none, which is no value left out; a cel.bind
// around the all leaves it the expression's value.
func TestAFalseAllShowsEveryExtractorInItsFalseFiles(t *testing.T) {
	const (
		top      = "cel.bind(want, 'RespectIgnoreDifferences=true', by_file(ignores).all(f, f in by_file(options) && want in by_file(options)[f] && size(none) == 0))"
		underAnd = "cel.bind(want, 'RespectIgnoreDifferences=true', by_file(ignores).all(f, f in by_file(options) && want in by_file(options)[f]) && size(none) == 0)"
	)
	c := assertRule(t, top,
		named("ignores", fake{values: []check.Value{at("Secret", "apps/a.yaml", 9), at("Secret", "apps/b.yaml", 12)}}),
		named("none", fake{}),
		named("options", fake{values: []check.Value{
			at("RespectIgnoreDifferences=true", "apps/a.yaml", 21), at("ServerSideApply=true", "apps/b.yaml", 30),
			at("RespectIgnoreDifferences=true", "apps/c.yaml", 4),
		}}),
	)
	c.Rule().AllowEmpty = []string{"none"}
	res := Run(context.Background(), ".", c)
	want := `  the all is false for 1 of the 2 files of by_file(ignores); their values:
  ignores[1] = "Secret" at apps/b.yaml:12
  none = [] (allow_empty)
  options[1] = "ServerSideApply=true" at apps/b.yaml:30
  3 more values not shown: errata check --all-values prints every value`
	if res.Verdict != check.VerdictFalse || !strings.HasSuffix(res.Message, want) {
		t.Fatalf("verdict %s, message:\n%s\nwant false and to end:\n%s", res.Verdict, res.Message, want)
	}
	// The && after the all makes the all no longer the value.
	if res := Run(context.Background(), ".", assertRule(t, underAnd, c.Rule().Extractors...)); strings.Contains(res.Message, "the all is false") {
		t.Errorf("an all under &&: %s", res.Message)
	}
}

// An assert whose value is not an all over an extractor shows every value,
// as before: an all under an operator, over a list built from an
// extractor or a literal, over a name a cel.bind binds again, and exists.
// The message is the one saw writes. Planted break: topAll blind to a
// cel.bind that names the extractor again, which counted the members of
// the bound list against the extractor's values.
func TestAnyOtherFalseAssertShowsEveryValue(t *testing.T) {
	v := named("v", fake{values: []check.Value{at("a", "f", 1), at("b", "f", 2), at("c", "g", 3)}})
	for _, expr := range []string{
		"size(v) > 0 && v.all(x, x == 'a')",
		"!v.exists(x, x == 'b')",
		"v.filter(x, x != 'c').all(x, x == 'a')",
		"v.exists(x, x == 'z')",
		"['z'].all(x, x in v)",
		"cel.bind(v, v.filter(x, x != 'a'), v.all(x, x == 'a'))",
		"cel.bind(v, v.map(x, 'a' + x), v.all(x, x == 'aa'))",
		"v.map(x, x + x).all(x, x == 'aa')",
		"v.all(x, x == 'a') == true",
		"by_file(v).exists(f, f == 'z')",
	} {
		c := assertRule(t, expr, v)
		res := Run(context.Background(), ".", c)
		if want := "assert: the expression is false\n" + saw(*c.Rule(), res.Extracted); res.Verdict != check.VerdictFalse || res.Message != want {
			t.Errorf("%s: verdict %s, message:\n%s\nwant false and:\n%s", expr, res.Verdict, res.Message, want)
		}
	}
}

// A member whose predicate errors makes the rule unknown, as before, and
// its message shows every value: an error is no offender. The same all
// without that member is false, with the member that is false shown.
func TestAnAllWhoseMemberErrorsIsUnknownWithEveryValue(t *testing.T) {
	const expr = "ports.all(p, int(p) > 0)"
	ports := named("ports", fake{values: []check.Value{at("80", "c.yaml", 4), at("${PORT}", "c.yaml", 5), at("0", "c.yaml", 6)}})
	c := assertRule(t, expr, ports)
	res := Run(context.Background(), ".", c)
	if res.Verdict != check.VerdictUnknown || !strings.HasSuffix(res.Message, "\n"+saw(*c.Rule(), res.Extracted)) {
		t.Errorf("a member that errors: verdict %s, message:\n%s", res.Verdict, res.Message)
	}
	ports = named("ports", fake{values: []check.Value{at("80", "c.yaml", 4), at("0", "c.yaml", 6)}})
	res = Run(context.Background(), ".", assertRule(t, expr, ports))
	if res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, "the all is false for 1 of the 2 values of ports:\n  ports[1] = \"0\" at c.yaml:6\n") {
		t.Errorf("a member that is false: verdict %s, message:\n%s", res.Verdict, res.Message)
	}
}

// Over random values and predicates, the verdict of a top level all is
// the verdict of the same all under == true, which is no top level all,
// and when it is false, the values the message shows are exactly those
// the predicate is false for, each alone. A predicate that errors on
// some values (int()) makes both unknown. Planted break: everyMember
// leaving an all's loop condition alone, which stopped at the first false
// member and showed only it.
func TestTheOffendersAreTheMembersThePredicateIsFalseFor(t *testing.T) {
	preds := []string{"x.startsWith('a')", "size(x) < 3", "int(x) % 2 == 0", "x.contains('b') || x.endsWith('c')"}
	r := rand.New(rand.NewPCG(1, 2))
	for round := range 200 {
		pred := preds[round%len(preds)]
		var values []check.Value
		for i := range 1 + r.IntN(8) {
			text := []string{"a", "ab", "abc", "bc", "c", "2", "13", "40"}[r.IntN(8)]
			values = append(values, at(text, fmt.Sprintf("f%d", r.IntN(3)), i+1))
		}
		v := named("v", fake{values: values})
		top := Run(context.Background(), ".", assertRule(t, "v.all(x, "+pred+")", v))
		under := Run(context.Background(), ".", assertRule(t, "v.all(x, "+pred+") == true", v))
		if top.Verdict != under.Verdict {
			t.Fatalf("%s over %v: verdict %s at the top, %s under ==", pred, values, top.Verdict, under.Verdict)
		}
		if top.Verdict != check.VerdictFalse {
			continue
		}
		for i, value := range values {
			one := Run(context.Background(), ".", assertRule(t, "v.all(x, "+pred+")", named("v", fake{values: []check.Value{value}})))
			line := fmt.Sprintf("\n  v[%d] = %q at %s:%d\n", i, value.Text, value.At.File, value.At.Line)
			if shown := strings.Contains(top.Message+"\n", line); shown != (one.Verdict == check.VerdictFalse) {
				t.Fatalf("%s over %v: v[%d] shown %v, alone %s:\n%s", pred, values, i, shown, one.Verdict, top.Message)
			}
		}
	}
}

// The predicate's note costs what the note it replaces cost, so a rule
// near the cost limit keeps its verdict. Each figure is the least limit
// under which the expression evaluates at 1a7e7967, before the
// predicate was marked. Planted break: memberCost left out of the cost
// trackers, which charged each member 1 more.
func TestAMarkedAllCostsWhatItCostBefore(t *testing.T) {
	for expr, want := range map[string]uint64{
		"v.all(x, x != 'value-7' && x != 'z')":        447,
		"by_file(v).all(f, size(by_file(v)[f]) == 2)": 177,
		"cel.bind(k, 'z', v.all(x, x != k))":          312,
	} {
		if got := leastCost(t, expr, 50); got != want {
			t.Errorf("%s: least cost limit %d, want %d", expr, got, want)
		}
	}
}

// leastCost is the least cost limit under which expr evaluates over v, n
// values two to a file.
func leastCost(t *testing.T, expr string, n int) uint64 {
	t.Helper()
	values, files := make([]string, n), make([]string, n)
	for i := range n {
		values[i], files[i] = fmt.Sprintf("value-%d", i), fmt.Sprintf("f%d", i/2)
	}
	extracted := asExtracted(map[string][]string{"v": values}, map[string][]string{"v": files}, nil)
	lo, hi := uint64(1), CostLimit
	for lo < hi {
		mid := (lo + hi) / 2
		a, err := compile(expr, []string{"v"}, limits{cost: mid, timeout: EvalTimeout, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Eval(context.Background(), extracted); err != nil {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// An account that does not match the values, which no evaluation gives,
// leaves the message whole rather than show the wrong values. Planted
// break: an offender past the last member let through.
func TestAnAccountThatDoesNotMatchShowsEveryValue(t *testing.T) {
	r := check.Rule{Assert: "v.all(x, x == 'a')"}
	extracted := []check.Extracted{{Extractor: "v", Values: []check.Value{at("a", "f", 1), at("b", "g", 2)}}}
	whole := saw(r, extracted)
	for name, off := range map[string]*offending{
		"none":                  nil,
		"another extractor":     {population: population{extractor: "w"}, members: 2, offenders: []int{1}},
		"no member false":       {population: population{extractor: "v"}, members: 2},
		"a member too many":     {population: population{extractor: "v"}, members: 3, offenders: []int{1}},
		"a file too many":       {population: population{extractor: "v", byFile: true}, members: 3, offenders: []int{1}},
		"a member out of range": {population: population{extractor: "v"}, members: 2, offenders: []int{2}},
	} {
		if got := sawFalse(r, extracted, off, false); got != whole {
			t.Errorf("%s: message\n%s\nwant\n%s", name, got, whole)
		}
	}
}

// An all that is false for every value leaves none out, so no line counts
// any, and an all over one value or one file calls it the only one.
// Mutant killed: the count printed when nothing was left out.
func TestAnAllFalseForEveryValueCountsNoneLeftOut(t *testing.T) {
	v := named("v", fake{values: []check.Value{at("b", "f", 1), at("c", "f", 2)}})
	res := Run(context.Background(), ".", assertRule(t, "v.all(x, x == 'a')", v))
	want := `assert: the expression is false
  expression: v.all(x, x == 'a')
  the all is false for 2 of the 2 values of v:
  v[0] = "b" at f:1
  v[1] = "c" at f:2`
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("verdict %s, message:\n%s\nwant false and:\n%s", res.Verdict, res.Message, want)
	}
	one := named("v", fake{values: []check.Value{at("b", "f", 1)}})
	for expr, header := range map[string]string{
		"v.all(x, x == 'a')":                          "the all is false for the only value of v:",
		"by_file(v).all(f, size(by_file(v)[f]) == 2)": "the all is false for the only file of by_file(v); their values:",
	} {
		res := Run(context.Background(), ".", assertRule(t, expr, one))
		if !strings.Contains(res.Message, "\n  "+header+"\n  v[0] = \"b\" at f:1") {
			t.Errorf("%s: message:\n%s\nwant the header %q", expr, res.Message, header)
		}
	}
}

// A false top level all blames the first value its message shows, where a
// SARIF result points; any other false assert blames the first value, and
// a true or unknown one blames none. Planted break: blameOf returning
// firstValue always, which pointed the address rule at kim@example.org.
func TestAFalseAllBlamesTheFirstValueItShows(t *testing.T) {
	addresses := named("addresses", fake{values: []check.Value{
		at("kim@example.org", "a.py", 3), at("kim@gmail.com", "b.py", 12), at("lee@yahoo.com", "c.py", 1),
	}})
	fences := named("fences", fake{values: []check.Value{at("```", "z.md", 1), at("```", "z.md", 4), at("```", "a.md", 2)}})
	ports := named("ports", fake{values: []check.Value{at("80", "c.yaml", 4), at("x", "c.yaml", 5)}})
	for _, tc := range []struct {
		expr string
		x    check.NamedExtractor
		want *check.Blame
	}{
		{"addresses.all(a, a.endsWith('@example.org'))", addresses, &check.Blame{Value: 1}},
		{"by_file(fences).all(f, size(by_file(fences)[f]) % 2 == 0)", fences, &check.Blame{Value: 2}},
		{"size(addresses) == 0 && addresses.all(a, a.endsWith('@example.org'))", addresses, &check.Blame{}},
		{"addresses.exists(a, a == 'z')", addresses, &check.Blame{}},
		{"addresses.all(a, a != 'z')", addresses, nil},
		{"ports.all(p, int(p) > 0)", ports, nil},
	} {
		res := Run(context.Background(), ".", assertRule(t, tc.expr, tc.x))
		if !reflect.DeepEqual(res.Blamed, tc.want) {
			t.Errorf("%s: %s, blamed %+v, want %+v", tc.expr, res.Verdict, res.Blamed, tc.want)
		}
	}
}
