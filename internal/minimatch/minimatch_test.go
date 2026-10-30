package minimatch

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode"
)

// The port answers what minimatch answers, for each case of
// testdata/cases.json: random globs of every form minimatch reads (braces,
// sequences, extglobs, negations, comments, classes, POSIX classes, escapes,
// empty, . and .. segments) against random paths, and a sample of code
// points against globs that fold case or name a POSIX class; then cases
// aimed at what mutation testing found unpinned: globstars around sections
// of every shape of a, b and ** to five segments, against paths with dots
// and trailing slashes, 199 to 201 sections against the recursion bound,
// nested extglobs, the fast tests' patterns, final sigma, and a long first
// section before a short one, which minimatch's section bound refuses. Each
// holds the answer testdata/oracle.mjs gave from minimatch 10.2.6 under
// Node. Where minimatch throws, the port returns a SyntaxError.
func TestMatchAgreesWithMinimatch(t *testing.T) {
	data, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][3]any
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 14000 {
		t.Fatalf("%d cases; the file holds over 14000", len(cases))
	}
	counts := map[any]int{}
	for _, c := range cases {
		file, glob := c[0].(string), c[1].(string)
		got, err := Match(file, glob)
		var thrown *SyntaxError
		var answer any = got
		switch {
		case errors.As(err, &thrown):
			answer = "throw"
		case err != nil:
			t.Errorf("Match(%q, %q): %v; minimatch says %v", file, glob, err, c[2])
			continue
		}
		counts[answer]++
		if answer != c[2] {
			t.Errorf("Match(%q, %q) = %v; minimatch says %v", file, glob, answer, c[2])
		}
	}
	// All three answers are common, so a port that always gives one fails.
	for _, a := range []any{true, false, "throw"} {
		if counts[a] < 400 {
			t.Errorf("%d cases answer %v", counts[a], a)
		}
	}
}

// Each bound refuses a glob past it, naming the bound, and lets one just
// inside it through.
func TestMatchBounds(t *testing.T) {
	word := strings.Repeat("[[:word:]]", 21)
	for _, c := range []struct {
		name, file, inside, outside, want string
	}{
		{"expansions", "a", strings.Repeat("{a,b}", 6), strings.Repeat("{a,b}", 7),
			"its braces expand to more than 64 globs or 16384 characters"},
		{"expanded length", "a", "{" + strings.Repeat("a", 7000) + "," + strings.Repeat("b", 7000) + "}",
			"{" + strings.Repeat("a", 8500) + "," + strings.Repeat("b", 8500) + "}",
			"its braces expand to more than 64 globs or 16384 characters"},
		{"sequence", "a", "{1..64}", "{1..65}", "its braces expand to more than 64 globs or 16384 characters"},
		// Two sets, each under the bound, whose product is 64 and 65.
		{"product", "a", "{a,b,c,d}{1..16}", "{a,b,c,d,e}{1..13}", "its braces expand to more than 64 globs or 16384 characters"},
		// Four globs of 4096 characters are 16384 in all.
		{"product's length", "a", "{" + strings.Repeat("a", 4095) + "," + strings.Repeat("b", 4095) + "}{c,d}",
			"{" + strings.Repeat("a", 4096) + "," + strings.Repeat("b", 4096) + "}{c,d}",
			"its braces expand to more than 64 globs or 16384 characters"},
		// The root, then two nodes for each @(a) and three for @(a|b).
		{"nodes", "a", strings.Repeat("@(a)", 2046) + "@(a|b)", strings.Repeat("@(a)", 2048),
			"a segment holds so many negations that its tree passes 4096 nodes"},
		// Each negation doubles the tree minimatch builds: past the bound
		// the port stops copying (TestNegationsStopCopyingAtTheBound).
		{"negations", "xa", "x" + strings.Repeat("!(a)", 10), "x" + strings.Repeat("!(a)", 16),
			"a segment holds so many negations that its tree passes 4096 nodes"},
		{"regex source", "a", strings.Repeat("{a,b}", 5) + word, strings.Repeat("{a,b}", 6) + word,
			"its segments build more than 32768 characters of regex source"},
		// ^, 32766 units and $ fill the bound.
		{"regex source of one segment", "a", strings.Repeat("a", maxSource-2), strings.Repeat("a", maxSource-1),
			"its segments build more than 32768 characters of regex source"},
		{"steps", strings.Repeat("a", 1000) + "b", "*(a|aa)b", strings.Repeat("*(a|aa)", 140),
			"matching takes more than the step budget"},
		// An empty alternative spends no step of its own: only the charge
		// for trying it stops 2^40 paths.
		{"empty alternatives", "xy", "x" + strings.Repeat("@(|)", 8), "x" + strings.Repeat("@(|)", 40),
			"matching takes more than the step budget"},
	} {
		if _, err := Match(c.file, c.inside); err != nil {
			t.Errorf("%s: inside the bound: %v", c.name, err)
		}
		if _, err := Match(c.file, c.outside); err == nil || err.Error() != c.want {
			t.Errorf("%s: past the bound: error %v, want %q", c.name, err, c.want)
		}
	}
}

// A segment without magic is compared as its text, and is not held to
// maxSource: 16385 of -, each escaped as \-, build 32770 units of source.
func TestALiteralSegmentPassesTheSourceBound(t *testing.T) {
	glob := strings.Repeat("-", maxSource/2+1)
	if ok, err := Match(glob, glob); !ok || err != nil {
		t.Errorf("Match of %d of -: %v, %v", len(glob), ok, err)
	}
}

// A match may take exactly the steps the budget holds, and no more.
func TestTheStepBudgetIsExact(t *testing.T) {
	r, err := compileJS(toUnits(`^(?:a|b)*c$`), true, false)
	if err != nil {
		t.Fatal(err)
	}
	in := toUnits("abababc")
	full := &budget{left: MaxSteps}
	if ok, err := r.test(in, full); !ok || err != nil {
		t.Fatalf("with the whole budget: %v, %v", ok, err)
	}
	spent := MaxSteps - full.left
	if ok, err := r.test(in, &budget{left: spent}); !ok || err != nil {
		t.Errorf("with the %d steps it takes: %v, %v", spent, ok, err)
	}
	if ok, err := r.test(in, &budget{left: spent - 1}); ok || !errors.Is(err, errBudget) {
		t.Errorf("with one step fewer: %v, %v; want %v", ok, err, errBudget)
	}
}

// Past maxNodes the port stops copying a negation's tail, so x followed by
// 16 of !(a), whose tree minimatch doubles 16 times, builds little more than
// maxNodes nodes before it is refused.
func TestNegationsStopCopyingAtTheBound(t *testing.T) {
	n := fromGlob(toUnits("x" + strings.Repeat("!(a)", 16)))
	if _, err := n.toMMPattern(&sourceBudget{left: maxSource}); !errors.Is(err, errNodes) {
		t.Fatalf("error %v, want %v", err, errNodes)
	}
	if *n.count > 2*maxNodes {
		t.Errorf("built %d nodes, more than twice the bound of %d", *n.count, maxNodes)
	}
}

// Every alternative a regex tries costs a step, so x followed by n of @(|),
// whose regex tries 2^n paths that each fail at $, spends at least 2^n
// steps. The bounds row above shows the same at 40, where a matcher that
// does not charge runs for about a day; this one shows it at 12.
func TestEveryAlternativeCostsAStep(t *testing.T) {
	m, err := compile(toUnits("x" + strings.Repeat("@(|)", 12)))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := m.match(toUnits("xy")); ok || err != nil {
		t.Fatalf("match: %v, %v", ok, err)
	}
	if spent := MaxSteps - m.steps.left; spent < 1<<12 {
		t.Errorf("2^12 paths spent %d steps", spent)
	}
}

// A string that is not UTF-8 has no UTF-16 form to match.
func TestMatchRefusesInvalidUTF8(t *testing.T) {
	for _, c := range [][2]string{{"\xff", "*"}, {"a", "\xff"}} {
		if _, err := Match(c[0], c[1]); err == nil || err.Error() != "a string that is not UTF-8" {
			t.Errorf("Match(%q, %q): %v", c[0], c[1], err)
		}
	}
}

// Compiling nested extglobs takes time polynomial in the depth: each node
// asks isEnd of its ancestors once, where ast.js asks twice and so doubles
// the work at each level. 20 levels make 442 calls, where the double call
// makes millions; 250 levels compile, and minimatch answers false for them
// against b.
func TestNestingCompilesInPolynomialTime(t *testing.T) {
	for _, depth := range []int{20, 250} {
		n := fromGlob(toUnits(strings.Repeat("a@(", depth) + "a" + strings.Repeat(")", depth)))
		if _, err := n.toMMPattern(&sourceBudget{left: maxSource}); err != nil {
			t.Fatal(err)
		}
		// Every level asks at least once, so fewer calls than levels is a
		// count that does not count.
		if limit := 2 * depth * depth; n.ends > limit || n.ends < depth {
			t.Fatalf("%d levels ran isEnd %d times, not between %d and %d", depth, n.ends, depth, limit)
		}
	}
	if ok, err := Match("b", strings.Repeat("a@(", 250)+"a"+strings.Repeat(")", 250)); ok || err != nil {
		t.Errorf("250 levels: %v, %v", ok, err)
	}
}

// Compiling stops as soon as a bound is passed: the node bound refuses
// before any source is written, and the source bound while it is written.
// Each glob is 1 KB or so. ast.js builds each node's source and copies it
// into its parent's, so a negation whose tail fillNegs copies into 4096
// nodes, or 248 extglobs nested under braces and negations, built
// megabytes before a bound refused them.
func TestCompilingStopsAtTheBound(t *testing.T) {
	tail := strings.Repeat("b*", 500)
	for _, c := range []struct {
		name, glob string
		want       error
		// most is the source a build may write before it refuses: none
		// for the node bound, and for the source bound maxSource and the
		// last write, which is the tail's source of about 3500 units or
		// a few units of nesting.
		most int
	}{
		{"12 negations and a tail", "x" + strings.Repeat("!(a)", 12) + tail[:1023-49], errNodes, 0},
		{"10 negations and a tail", "x" + strings.Repeat("!(a)", 10) + tail[:1023-41], errSource, maxSource + 4096},
		{"nesting under braces", strings.Repeat("{a,b}", 3) + "x" + strings.Repeat("!(a)", 3) +
			strings.Repeat("a@(", 248) + "a" + strings.Repeat(")", 248), errSource, maxSource + 64},
	} {
		m, err := compile(toUnits(c.glob))
		if !errors.Is(err, c.want) {
			t.Errorf("%s: error %v, want %v", c.name, err, c.want)
			continue
		}
		if m.source.written > c.most {
			t.Errorf("%s: wrote %d units of source before the refusal, more than %d", c.name, m.source.written, c.most)
		}
	}
}

// unicode_node.go lists where Node's tables differ from Go's, so it holds
// only for the Unicode version of the Go that gen.go ran under. A Go
// toolchain that moves to another version shifts answers until the tables
// are generated again.
func TestUnicodeTablesMatchGo(t *testing.T) {
	const generatedUnder = "17.0.0"
	if unicode.Version != generatedUnder {
		t.Errorf("Go's tables are Unicode %s, and unicode_node.go was generated against %s: run "+
			"testdata/unicode/gen.go and gen.mjs again (ADR 0014)", unicode.Version, generatedUnder)
	}
}
