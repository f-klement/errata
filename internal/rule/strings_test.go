package rule

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/ext"
	"pgregory.net/rapid"
)

func TestStringFunctions(t *testing.T) {
	cases := []struct {
		expr   string
		values []string
	}{
		{"v[0].split(',') == ['loki', 'mimir', 'tempo']", []string{"loki,mimir,tempo"}},
		{"v[0].split(',', 2) == ['loki', 'mimir,tempo']", []string{"loki,mimir,tempo"}},
		{"v[0].split(',', 0) == []", []string{"a,b"}},
		{"v[0].split(',', -1) == ['a', 'b']", []string{"a,b"}},
		{"v[0].split(',') == ['']", []string{""}},
		{"v[0].split('') == ['ä', 'b']", []string{"äb"}},
		{"v[0].split(':')[0] == '10.43.38.159'", []string{"10.43.38.159:3900"}},
		{"v.join() == 'ab'", []string{"a", "b"}},
		{"v.join(' ') == 'a b'", []string{"a", "b"}},
		{"v.filter(x, false).join('/') == ''", []string{"x"}},
		{"v[0].replace('-', '_') == 'a_b_c'", []string{"a-b-c"}},
		{"v[0].replace('-', '_', 1) == 'a_b-c'", []string{"a-b-c"}},
		{"v[0].replace('-', '_', 0) == 'a-b-c'", []string{"a-b-c"}},
		{"v[0].replace('', '.') == '.a.b.'", []string{"ab"}},
		// The comparison the k3s garage rule makes: a comma list against a
		// space list.
		{"v[0].split(',').filter(b, b != '') == v[1].split(' ').filter(b, b != '')", []string{"loki,mimir,", "loki  mimir"}},
		// A key built from two captures.
		{"v.map(n, 'renovate_' + n).join(',') == 'renovate_a,renovate_b'", []string{"a", "b"}},
		// The helpers' bound of MaxArgument bytes does not apply.
		{"size(v[0].split(',')) == 1001", []string{strings.Repeat("ab,", 1000)}},
		// The rest of the extension counts in code points.
		{"v[0].charAt(1) == 'ä' && v[0].charAt(3) == ''", []string{"aäb"}},
		{"v[0].indexOf('b') == 2 && v[0].indexOf('a', 1) == 3 && v[0].indexOf('z') == -1", []string{"aäba"}},
		{"v[0].lastIndexOf('a') == 3 && v[0].lastIndexOf('a', 2) == 0 && v[0].lastIndexOf('') == 4", []string{"aäba"}},
		{"v[0].split(':')[0] == v[0].substring(0, v[0].indexOf(':'))", []string{"10.43.38.159:3900"}},
		{"v[0].substring(1) == 'äb' && v[0].substring(1, 2) == 'ä'", []string{"aäb"}},
		{"v[0].trim() == 'a b'", []string{"  a b \n"}},
		{"v[0].lowerAscii() == 'Äb' && v[0].upperAscii() == 'ÄB'", []string{"ÄB"}},
		{"v[0].reverse() == 'bäa'", []string{"aäb"}},
		{"'%s:%d %.2f %x'.format([v[0], 3, 0.5, 'hi']) == 'x:3 0.50 6869'", []string{"x"}},
		{"v[0].format([v[1]]) == 'image: nginx'", []string{"image: %s", "nginx"}},
		{`strings.quote(v[0]) == '"a\\"b"'`, []string{`a"b`}},
	}
	for _, tc := range cases {
		got, err := eval(t, tc.expr, tc.values...)
		if err != nil || !got {
			t.Errorf("%s over %.40q = %v, %v; want true", tc.expr, tc.values, got, err)
		}
	}
}

// assert offers every function of the strings extension it loads. One it
// does not offer, as it would not a function a later version adds, does not
// load.
func TestStringsLeavesOutOnlyWhatItDoesNotOffer(t *testing.T) {
	fns, err := stringDecls()
	if err != nil {
		t.Fatal(err)
	}
	for name := range fns {
		if !slices.Contains(stringFunctions, name) {
			t.Errorf("the extension's %s is not offered", name)
		}
	}
	opts, err := stringsExtension([]string{"split"})
	if err != nil {
		t.Fatal(err)
	}
	env, err := cel.NewEnv(append(opts, cel.Variable("v", cel.ListType(cel.StringType)))...)
	if err != nil {
		t.Fatal(err)
	}
	if _, iss := env.Compile("size(v[0].split(',')) == 1"); iss.Err() != nil {
		t.Errorf("split, offered: %v", iss.Err())
	}
	var hidden int
	for name, fn := range fns {
		if name == "split" {
			continue
		}
		hidden++
		o := fn.OverloadDecls()[0]
		args := []string{}
		for range o.ArgTypes()[1:] {
			args = append(args, "v[0]")
		}
		expr := fmt.Sprintf("dyn(v[0].%s(%s)) == 1", name, strings.Join(args, ", "))
		if !o.IsMemberFunction() {
			expr = fmt.Sprintf("dyn(%s(v[0])) == 1", name)
		}
		// cel-go 0.32 names the function without its namespace, 0.33 with it.
		short := name[strings.LastIndex(name, ".")+1:]
		if _, iss := env.Compile(expr); iss.Err() == nil ||
			!strings.Contains(iss.Err().Error(), "undeclared reference to '"+name+"'") && !strings.Contains(iss.Err().Error(), "undeclared reference to '"+short+"'") {
			t.Errorf("%s: load error = %v, want an undeclared reference", expr, iss.Err())
		}
	}
	if hidden < 12 {
		t.Fatalf("%d functions left out, want the extension's twelve others at least", hidden)
	}
}

// split, join and replace give what Go's strings package gives.
func TestPropertyStringFunctionsAgreeWithGo(t *testing.T) {
	a, err := compileNamed("v[0].split(v[1], int(v[2])) == w && w.join(v[1]) == v[3] && v[0].replace(v[1], v[4], int(v[2])) == v[5]", []string{"v", "w"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.StringMatching(`[ab,ä]{0,12}`).Draw(t, "s")
		sep := rapid.StringMatching(`[ab,ä]{0,2}`).Draw(t, "sep")
		repl := rapid.StringMatching(`[xy]{0,3}`).Draw(t, "new")
		n := rapid.IntRange(-2, 14).Draw(t, "n")
		parts := strings.SplitN(s, sep, n)
		values := map[string][]string{
			"v": {s, sep, strconv.Itoa(n), strings.Join(parts, sep), repl, strings.Replace(s, sep, repl, n)},
			"w": parts,
		}
		if ok, err := a.Eval(context.Background(), asExtracted(values, nil, nil)); err != nil || !ok {
			t.Fatalf("over %q: %v, %v", values, ok, err)
		}
		if got := splitParts(s, sep, int64(n)); got != int64(len(parts)) {
			t.Fatalf("splitParts(%q, %q, %d) = %d, want %d", s, sep, n, got, len(parts))
		}
		if got, want := replacedSize(s, sep, repl, int64(n)), len(strings.Replace(s, sep, repl, n)); got != int64(want) {
			t.Fatalf("replacedSize(%q, %q, %q, %d) = %d, want %d", s, sep, repl, n, got, want)
		}
	})
}

// A call whose result would be more than MaxBuilt bytes or MaxParts parts is
// refused before it is built, with the bound named; one at the bound is
// built, and the cost limit then stops the evaluation. Without the guard both
// would end at the cost limit, after the call had built its result. A refusal
// that || or && would absorb still makes the rule unknown.
func TestStringFunctionsRefuseBeforeBuilding(t *testing.T) {
	const costLimit = "the evaluation exceeded the cost limit of 1000000"
	tooLong := fmt.Sprintf("the result would be %d bytes, more than the %d one call may build, since a longer string exceeds the cost limit of 1000000 by itself", MaxBuilt+1, MaxBuilt)
	half := int(MaxBuilt / 2)
	cases := []struct {
		expr   string
		values []string
		want   string
	}{
		{"v[0].replace('a', 'bb') != ''", []string{strings.Repeat("a", half)}, costLimit},
		{"v[0].replace('a', 'bb') != ''", []string{strings.Repeat("a", half) + "!"}, "replace: " + tooLong},
		{"v[0].replace('a', v[1]) != ''", []string{strings.Repeat("a", half) + "!", "bb"}, "replace: " + tooLong},
		// The limit counts: one replacement makes the string one byte longer.
		{"size(v[0].replace('a', 'bb', 1)) > 0", []string{strings.Repeat("a", half) + "!"}, costLimit},
		// An empty old string inserts new around every code point.
		{"v[0].replace('', v[0]) == '' || true", []string{strings.Repeat("x", 3000)}, "replace: the result would be 9006000 bytes, more than the 4000000 one call may build, since a longer string exceeds the cost limit of 1000000 by itself"},
		{"v.exists(x, x.replace('', x) == '') || true", []string{strings.Repeat("x", 3000)}, "replace: the result would be 9006000 bytes, more than the 4000000 one call may build, since a longer string exceeds the cost limit of 1000000 by itself"},
		{"v[0].replace('', v[0]) != ''", []string{strings.Repeat("x", 3000)}, "replace: the result would be 9006000 bytes, more than the 4000000 one call may build, since a longer string exceeds the cost limit of 1000000 by itself"},
		{"v.join() != ''", []string{strings.Repeat("a", half), strings.Repeat("a", half)}, costLimit},
		{"v.join() != ''", []string{strings.Repeat("a", half), strings.Repeat("a", half+1)}, "join: " + tooLong},
		{"v.join(',') != ''", []string{strings.Repeat("a", half), strings.Repeat("a", half)}, "join: " + tooLong},
		// A separator between each two members, so two of them among three.
		{"v.join(',') != ''", []string{strings.Repeat("a", half-2), "a", strings.Repeat("a", half)}, "join: " + tooLong},
		{"v.join(',') != ''", []string{strings.Repeat("a", half-3), "a", strings.Repeat("a", half)}, costLimit},
		{"size(v[0].split(',')) > 0", []string{strings.Repeat(",", int(MaxParts)-1)}, costLimit},
		{"size(v[0].split(',')) > 0", []string{strings.Repeat(",", int(MaxParts))}, fmt.Sprintf("split: the result would hold %d strings, more than the %d one call may build, since a longer list exceeds the cost limit of 1000000 by itself", MaxParts+1, MaxParts)},
		{"size(v[0].split('')) > 0", []string{strings.Repeat("ä", int(MaxParts)+1)}, fmt.Sprintf("split: the result would hold %d strings, more than the %d one call may build, since a longer list exceeds the cost limit of 1000000 by itself", MaxParts+1, MaxParts)},
	}
	for _, tc := range cases {
		_, err := eval(t, tc.expr, tc.values...)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s over %d values: error = %v, want %q", tc.expr, len(tc.values), err, tc.want)
		}
	}
	// A split with a limit builds few strings whatever the string holds.
	if ok, err := eval(t, "size(v[0].split(',', 2)) == 2", strings.Repeat(",", int(MaxParts))); err != nil || !ok {
		t.Errorf("a split into 2 of a long string: %v, %v", ok, err)
	}
}

// split, join and replace count toward the cost limit by the size of what
// they read and build, by cel-go's own cost model. The long value splits into
// 100 parts and joins and replaces into 299 bytes, within the guards' bounds
// under a limit of 100, so the cost limit is what stops each.
func TestStringFunctionsCountTowardTheCostLimit(t *testing.T) {
	for _, expr := range []string{"size(v[0].split(',')) > 0", "v[0].split(',').join(';') != ''", "v[0].replace(',', ';') != ''"} {
		a, err := compile(expr, []string{"v"}, limits{cost: 100, timeout: time.Second, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Eval(context.Background(), asExtracted(map[string][]string{"v": {strings.Repeat("ab,", 99) + "ab"}}, nil, nil)); err == nil || err.Error() != "the evaluation exceeded the cost limit of 100" {
			t.Errorf("%s over 299 bytes under a limit of 100: error = %v", expr, err)
		}
		if ok, err := a.Eval(context.Background(), asExtracted(map[string][]string{"v": {"a,b"}}, nil, nil)); err != nil || !ok {
			t.Errorf("%s over 3 bytes under a limit of 100: %v, %v", expr, ok, err)
		}
	}
}

// The guards' bounds are what the cost limit charges: a string of more than
// 4 bytes a code point, a list of more than one member a unit, and a read of
// more than 10 code points a unit, which CEL charges a tenth each. MaxBuilt
// and MaxParts are the bounds under CostLimit, and a limit too large for an
// int64 gives the bounds of maxUnits.
func TestStringBoundsFollowTheCostLimit(t *testing.T) {
	if MaxBuilt != 4*int64(CostLimit) || MaxParts != int64(CostLimit) {
		t.Fatalf("MaxBuilt %d and MaxParts %d, with a cost limit of %d", MaxBuilt, MaxParts, CostLimit)
	}
	for cost, want := range map[uint64]bounds{
		CostLimit:      {cost: CostLimit, parts: MaxParts, built: MaxBuilt, steps: 10 * int64(CostLimit)},
		100:            {cost: 100, parts: 100, built: 400, steps: 1000},
		0:              {},
		math.MaxUint64: {cost: math.MaxUint64, parts: maxUnits, built: 4 * maxUnits, steps: 10 * maxUnits},
	} {
		if got := boundsUnder(cost); got != want {
			t.Errorf("boundsUnder(%d) = %+v, want %+v", cost, got, want)
		}
	}
}

// Under a cost limit of 100, which a test sets, a guard refuses a result of
// more than 400 bytes or 100 parts, and names that limit; one at the bound is
// built, and the cost limit stops it. Bound under CostLimit, as the
// environment every rule extends declares them, each of these would be built.
func TestStringGuardsFollowTheRulesLimit(t *testing.T) {
	const costLimit = "the evaluation exceeded the cost limit of 100"
	tooLong := "the result would be 401 bytes, more than the 400 one call may build, since a longer string exceeds the cost limit of 100 by itself"
	cases := []struct {
		expr   string
		values []string
		want   string
	}{
		{"v[0].replace('a', 'bb') != ''", []string{strings.Repeat("a", 200)}, costLimit},
		{"v[0].replace('a', 'bb') != ''", []string{strings.Repeat("a", 200) + "!"}, "replace: " + tooLong},
		{"v.join() != ''", []string{strings.Repeat("a", 200), strings.Repeat("a", 200)}, costLimit},
		{"v.join() != ''", []string{strings.Repeat("a", 200), strings.Repeat("a", 201)}, "join: " + tooLong},
		{"size(v[0].split(',')) > 0", []string{strings.Repeat(",", 99)}, costLimit},
		{"size(v[0].split(',')) > 0", []string{strings.Repeat(",", 100)}, "split: the result would hold 101 strings, more than the 100 one call may build, since a longer list exceeds the cost limit of 100 by itself"},
	}
	for _, tc := range cases {
		a, err := compile(tc.expr, []string{"v"}, limits{cost: 100, timeout: time.Second, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Eval(context.Background(), asExtracted(map[string][]string{"v": tc.values}, nil, nil)); err == nil || err.Error() != tc.want {
			t.Errorf("%s over %d values under a limit of 100: error = %v, want %q", tc.expr, len(tc.values), err, tc.want)
		}
	}
}

// evalUnder evaluates expr over the values v under the cost limit cost.
func evalUnder(t *testing.T, cost uint64, expr string, values ...string) (bool, error) {
	t.Helper()
	a, err := compile(expr, []string{"v"}, limits{cost: cost, timeout: time.Second, grace: stopGrace})
	if err != nil {
		t.Fatal(err)
	}
	return a.Eval(context.Background(), asExtracted(map[string][]string{"v": values}, nil, nil))
}

// Under a cost limit of 100, each function of the rest of the extension
// refuses, before it runs, a call the limit would stop by itself: a read of
// more than 1000 characters, a search of more than 1000 pairs, a transform
// into more than 100 characters, and a quote or a format of more than 400
// bytes. A call at the bound runs, and the cost limit stops it.
func TestStringFunctionsRefuseBeforeReading(t *testing.T) {
	const costLimit = "the evaluation exceeded the cost limit of 100"
	read := func(fn string, n int) string {
		return fmt.Sprintf("%s: the call would read %d characters, more than the 1000 one call may read, since CEL charges a tenth for each and a longer read exceeds the cost limit of 100 by itself", fn, n)
	}
	search := func(fn string, n, m int) string {
		return fmt.Sprintf("%s: the search would compare %d characters with each of %d, more than the 1000 comparisons one call may make, since CEL charges a tenth for each and more exceed the cost limit of 100 by itself", fn, n, m)
	}
	transform := func(fn string) string {
		return fn + ": the result would be 101 characters, more than the 100 one call may build, since a longer string exceeds the cost limit of 100 by itself"
	}
	// A refused call costs what cel-go charges its arguments, so the cost
	// limit stops a read or a search that is refused, and names the refusal.
	stopped := func(refusal string) string { return costLimit + "; before it, " + refusal }
	const substring = "substring: the result would be %d characters, more than the 100 one call may build, since a longer string exceeds the cost limit of 100 by itself"
	const quoted = "strings.quote: the result would be 401 bytes, more than the 400 one call may build, since a longer string exceeds the cost limit of 100 by itself"
	const formatted = "format: the result could be more than the 400 bytes one call may build, since a longer string exceeds the cost limit of 100 by itself"
	a := strings.Repeat("a", 1000)
	cases := []struct {
		expr   string
		values []string
		want   string
	}{
		{"v[0].charAt(0) != ''", []string{a}, costLimit},
		{"v[0].charAt(0) != ''", []string{a + "ä"}, stopped(read("charAt", 1001))},
		// substring builds as many characters as it keeps.
		{"v[0].substring(900) != ''", []string{a}, costLimit},
		{"v[0].substring(899) != ''", []string{a}, stopped(fmt.Sprintf(substring, 101))},
		{"v[0].substring(5, 105) != ''", []string{a}, costLimit},
		{"v[0].substring(5, 106) != ''", []string{a}, stopped(fmt.Sprintf(substring, 101))},
		// From the first index, and to the end of s, are within range.
		{"v[0].substring(0) != ''", []string{a}, stopped(fmt.Sprintf(substring, 1000))},
		{"v[0].substring(0, 1000) != ''", []string{a}, stopped(fmt.Sprintf(substring, 1000))},
		{"v[0].substring(1) != ''", []string{a + "a"}, stopped(read("substring", 1001))},
		{"v[0].substring(0, 1) != ''", []string{a + "a"}, stopped(read("substring", 1001))},
		// Indexes out of range are the extension's error.
		{"v[0].substring(5, 2000) != ''", []string{a[:100]}, "index out of range: 2000"},
		{"v[0].substring(-1, 20) != ''", []string{a[:100]}, "index out of range: -1"},
		{"v[0].trim() != ''", []string{a}, costLimit},
		{"v[0].trim() != ''", []string{a + " "}, stopped(read("trim", 1001))},
		{"v[0].indexOf(v[1]) != -2", []string{a[:500], "bb"}, costLimit},
		{"v[0].indexOf(v[1]) != -2", []string{a[:501], "bb"}, stopped(search("indexOf", 501, 2))},
		{"v[0].indexOf(v[1], 1) != -2", []string{a[:334], "äää"}, stopped(search("indexOf", 334, 3))},
		{"v[0].lastIndexOf(v[1]) != -2", []string{a[:501], "ää"}, stopped(search("lastIndexOf", 501, 2))},
		{"v[0].lastIndexOf(v[1], 1) != -2", []string{a[:501], "bb"}, stopped(search("lastIndexOf", 501, 2))},
		// An empty needle counts as one, and is charged as one.
		{"v[0].indexOf('') == 0", []string{a}, costLimit},
		{"v[0].indexOf('') == 0", []string{a[:900]}, ""},
		{"v[0].indexOf('') == 0", []string{a + "a"}, stopped(search("indexOf", 1001, 1))},
		{"v[0].lowerAscii() != ''", []string{a[:100]}, costLimit},
		{"v[0].lowerAscii() != ''", []string{strings.Repeat("ä", 101)}, transform("lowerAscii")},
		{"v[0].upperAscii() != ''", []string{a[:101]}, transform("upperAscii")},
		{"v[0].reverse() != ''", []string{a[:101]}, transform("reverse")},
		{"strings.quote(v[0]) != ''", []string{a[:398]}, costLimit},
		{"strings.quote(v[0]) != ''", []string{a[:399]}, quoted},
		{"strings.quote(v[0]) != ''", []string{strings.Repeat("\n", 199)}, costLimit},
		{"strings.quote(v[0]) != ''", []string{strings.Repeat("\n", 199) + "a"}, quoted},
		// An invalid byte becomes U+FFFD, 3 bytes.
		{"strings.quote(v[0]) != ''", []string{strings.Repeat("\xff", 133)}, quoted},
		{"'%s'.format([v[0]]) != ''", []string{a[:398]}, costLimit},
		{"'%s'.format([v[0]]) != ''", []string{a[:399]}, formatted},
		{"'%x'.format([v[0]]) != ''", []string{a[:199]}, costLimit},
		{"'%x'.format([v[0]]) != ''", []string{a[:200]}, formatted},
		// The same value twice is twice the bytes.
		{"'%s%s'.format([v[0], v[0]]) != ''", []string{a[:199]}, formatted},
		// A list is written [a, b, ...], 3 bytes for each member of 1.
		{"'%s'.format([v]) != ''", strings.Split(strings.Repeat("a", 132), ""), costLimit},
		{"'%s'.format([v]) != ''", strings.Split(strings.Repeat("a", 133), ""), formatted},
		// A map is written {k: v, ...}: 4 bytes for each entry and its keys.
		{"'%s'.format([{'k': v[0]}]) != ''", []string{a[:391]}, costLimit},
		{"'%s'.format([{'k': v[0]}]) != ''", []string{a[:392]}, formatted},
		// The first value at the bound, then one more byte.
		{"'%s%s'.format([v[0], 'b']) != ''", []string{a[:396]}, formatted},
		// A refused format is charged what its count walked.
		{"'%s'.format([{'k': v[0], 'l': v[0]}]) != ''", []string{a[:391]}, stopped(formatted)},
		// A precision comes before the verb, and %s writes a string whole.
		{"'%.90s'.format([v[0]]) != ''", []string{a[:395]}, costLimit},
		{"'%.90s'.format([v[0]]) != ''", []string{a[:396]}, formatted},
		// A clause with no argument left is cel-go's error, counted as nothing.
		{"v[1].format([v[0]]) != ''", []string{a[:390], "%s%d"}, "index 1 out of range"},
		// A number counts 512 bytes, more than any number writes.
		{"'%d'.format([size(v)]) != ''", nil, formatted},
		// An argument no clause takes counts nothing.
		{"v[1].format([v[0]]) == '%'", []string{a, "%%"}, ""},
	}
	for _, tc := range cases {
		_, err := evalUnder(t, 100, tc.expr, tc.values...)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || err.Error() != tc.want) {
			t.Errorf("%s over %d values: error = %v, want %q", tc.expr, len(tc.values), err, tc.want)
		}
	}
}

// format and strings.quote count toward the cost limit by what they build,
// which cel-go does not charge: a quote of 95 characters, 97 with its
// quotes, is within the guard's 400 bytes and costs more than 100.
func TestFormatAndQuoteCountWhatTheyBuild(t *testing.T) {
	for _, expr := range []string{"strings.quote(v[0]) != ''", "'%s'.format([v[0]]) != ''"} {
		if _, err := evalUnder(t, 100, expr, strings.Repeat("a", 95)); err == nil || err.Error() != "the evaluation exceeded the cost limit of 100" {
			t.Errorf("%s over 95 bytes under a limit of 100: error = %v", expr, err)
		}
		if ok, err := evalUnder(t, 100, expr, strings.Repeat("a", 10)); err != nil || !ok {
			t.Errorf("%s over 10 bytes under a limit of 100: %v, %v", expr, ok, err)
		}
	}
}

// A guard refuses only a call that plain cel-go, under the same cost limit,
// stops on that limit: every function's guard but format's, whose count is an
// upper bound, and a search's for the empty string, which errata charges as
// one and cel-go not at all.
func TestPropertyStringGuardsRefuseOnlyWhatTheLimitStops(t *testing.T) {
	exprs := []string{
		"v[0].charAt(int(v[2])) != 'z'", "v[0].substring(int(v[2])) != 'z'", "v[0].substring(0, int(v[2])) != 'z'", "v[0].trim() != 'z'",
		"v[0].indexOf(v[1]) != -2", "v[0].indexOf(v[1], int(v[2])) != -2", "v[0].lastIndexOf(v[1]) != -2", "v[0].lastIndexOf(v[1], int(v[2])) != -2",
		"v[0].lowerAscii() != 'z'", "v[0].upperAscii() != 'z'", "v[0].reverse() != 'z'",
		"size(v[0].split(v[1])) >= 0", "v[0].replace(v[1], v[0]) != 'z'", "v.join(v[0]) != 'z'",
	}
	env, err := cel.NewEnv(ext.Strings(ext.StringsVersion(stringsVersion)), cel.Variable("v", cel.ListType(cel.StringType)))
	if err != nil {
		t.Fatal(err)
	}
	// Programs by expression and limit, compiled once: every draw tries
	// every expression, so a break in one function's guard shows within the
	// default number of draws.
	type key struct {
		expr  string
		limit uint64
	}
	guardedPrograms, plainPrograms := map[key]*assertion{}, map[key]cel.Program{}
	programs := func(t *rapid.T, k key) (*assertion, cel.Program) {
		if a, ok := guardedPrograms[k]; ok {
			return a, plainPrograms[k]
		}
		a, err := compile(k.expr, []string{"v"}, limits{cost: k.limit, timeout: time.Second, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		ast, iss := env.Compile(k.expr)
		if iss.Err() != nil {
			t.Fatal(iss.Err())
		}
		plain, err := env.Program(ast, cel.CostLimit(k.limit))
		if err != nil {
			t.Fatal(err)
		}
		guardedPrograms[k], plainPrograms[k] = a, plain
		return a, plain
	}
	rapid.Check(t, func(t *rapid.T) {
		// rapid draws a code point, not a byte, for \xff: the bytes are invalid UTF-8.
		s := rapid.StringMatching(`[aä😀\n ]{0,40}`).Draw(t, "s") + string(rapid.SliceOfN(rapid.Byte(), 0, 2).Draw(t, "bytes"))
		sub := rapid.StringMatching(`[aä😀]{0,4}`).Draw(t, "sub")
		offset := rapid.IntRange(-1, utf8.RuneCountInString(s)+1).Draw(t, "offset")
		// A limit near the string's size, where a guard stricter than the
		// cost limit would refuse what plain cel-go lets through.
		limit := rapid.Uint64Range(1, uint64(2*utf8.RuneCountInString(s)+12)).Draw(t, "limit")
		values := []string{s, sub, strconv.Itoa(offset)}
		for _, expr := range exprs {
			if sub == "" && strings.Contains(expr, "ndexOf") {
				// errata charges it, and plain cel-go does not.
				continue
			}
			a, plain := programs(t, key{expr, limit})
			_, err := a.Eval(context.Background(), asExtracted(map[string][]string{"v": values}, nil, nil))
			if err == nil || !strings.Contains(err.Error(), " one call may ") {
				continue
			}
			if _, _, perr := plain.Eval(map[string]any{"v": values}); perr == nil || !strings.Contains(perr.Error(), "cost limit exceeded") {
				t.Fatalf("%s over %q under a limit of %d: the guard refused (%v), and plain cel-go gave %v", expr, values, limit, err, perr)
			}
		}
	})
}

// quotedSize is the length of what strings.quote builds, and formattedSize
// at least the length of what format builds.
func TestPropertyQuoteAndFormatSizes(t *testing.T) {
	env, err := cel.NewEnv(ext.Strings(ext.StringsVersion(stringsVersion)),
		cel.Variable("s", cel.StringType), cel.Variable("args", cel.ListType(cel.DynType)))
	if err != nil {
		t.Fatal(err)
	}
	program := func(expr string) cel.Program {
		ast, iss := env.Compile(expr)
		if iss.Err() != nil {
			t.Fatal(iss.Err())
		}
		p, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	quote, format := program("size(bytes(strings.quote(s)))"), program("size(bytes(s.format(args)))")
	scalar := rapid.OneOf(
		rapid.Map(rapid.StringMatching(`[aä"\\\n\x07]{0,6}`), func(s string) any { return s }),
		rapid.Map(rapid.SliceOfN(rapid.Byte(), 0, 4), func(b []byte) any { return b }),
		rapid.Map(rapid.Int64(), func(n int64) any { return n }),
		rapid.Map(rapid.Uint64(), func(n uint64) any { return n }),
		rapid.Map(rapid.SampledFrom([]float64{0, -0.5, 1e308, -1.7976931348623157e308, 5e-324, -5e-324, 1.0 / 3}), func(f float64) any { return f }),
		rapid.Map(rapid.Bool(), func(b bool) any { return b }),
		rapid.Map(rapid.Int64(), func(n int64) any { return time.Duration(n) }),
		rapid.Just[any](nil),
	)
	nested := func(member *rapid.Generator[any]) *rapid.Generator[any] {
		return rapid.OneOf(scalar,
			rapid.Map(rapid.SliceOfN(member, 0, 3), func(l []any) any { return l }),
			rapid.Map(rapid.MapOfN(rapid.StringMatching(`[ab]{0,2}`), member, 0, 3), func(m map[string]any) any { return m }),
		)
	}
	// Two levels of lists and maps.
	value := nested(nested(scalar))
	rapid.Check(t, func(t *rapid.T) {
		// rapid draws a code point, not a byte, for \xff: the bytes are invalid UTF-8.
		s := rapid.StringMatching(`([aä"\\\n\x07%]|%s|%x|%X|%d|%f|%\.100f|%e|%\.3e|%b|%o|%%){0,8}`).Draw(t, "s") +
			string(rapid.SliceOfN(rapid.Byte(), 0, 3).Draw(t, "bytes"))
		args := rapid.SliceOfN(value, 0, 6).Draw(t, "args")
		vars := map[string]any{"s": s, "args": args}
		if out, _, err := quote.Eval(vars); err != nil || int64(out.(types.Int)) != quotedSize(s) {
			t.Fatalf("quote of %q is %v bytes, %v; quotedSize says %d", s, out, err, quotedSize(s))
		}
		out, _, err := format.Eval(vars)
		if err != nil {
			return
		}
		list := types.DefaultTypeAdapter.NativeToValue(args).(traits.Lister)
		if got := formattedSize(s, list, math.MaxInt64); got < int64(out.(types.Int)) {
			t.Fatalf("%q.format(%v) is %v bytes; formattedSize says %d", s, args, out, got)
		}
	})
}

// A call that runs costs what plain cel-go charges it: the rewrite through
// the log adds nothing. Two prices are errata's own: format and
// strings.quote cost the code points of their result more, and a search for
// the empty string a tenth of each code point of the string.
// TestStringRefusalsSpendTheLimit prices a refused call.
func TestPropertyStringCostsAreCelGos(t *testing.T) {
	exprs := []string{
		"v[0].charAt(int(v[2])) != 'z'", "v[0].substring(int(v[2])) != 'z'", "v[0].substring(0, int(v[2])) != 'z'", "v[0].trim() != 'z'",
		"v[0].indexOf(v[1]) != -2", "v[0].indexOf(v[1], int(v[2])) != -2", "v[0].lastIndexOf(v[1]) != -2", "v[0].lastIndexOf(v[1], int(v[2])) != -2",
		"v[0].lowerAscii() != 'z'", "v[0].upperAscii() != 'z'", "v[0].reverse() != 'z'",
		"size(v[0].split(v[1])) >= 0", "size(v[0].split(v[1], int(v[2]))) >= 0",
		"v[0].replace(v[1], v[0]) != 'z'", "v[0].replace(v[1], v[0], int(v[2])) != 'z'", "v.join() != 'z'", "v.join(v[0]) != 'z'",
		// A list of as many members as the string has code points.
		"v[0].split('').join(v[1]) != 'z'",
		"strings.quote(v[0]) != 'z'", "v[0].format(v) != 'z'",
	}
	env, err := cel.NewEnv(ext.Strings(ext.StringsVersion(stringsVersion)), cel.Variable("v", cel.ListType(cel.StringType)))
	if err != nil {
		t.Fatal(err)
	}
	type compiled struct {
		guarded       *assertion
		plain, result cel.Program
	}
	programs := map[string]compiled{}
	for _, expr := range exprs {
		a, err := compile(expr, []string{"v"}, limits{cost: 1 << 20, timeout: time.Second, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		c := compiled{guarded: a}
		for i, text := range []string{expr, strings.TrimSuffix(strings.TrimSuffix(expr, " != 'z'"), " != -2")} {
			ast, iss := env.Compile(text)
			if iss.Err() != nil {
				t.Fatal(iss.Err())
			}
			p, err := env.Program(ast, cel.CostLimit(1<<40))
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				c.plain = p
			} else {
				c.result = p
			}
		}
		programs[expr] = c
	}
	rapid.Check(t, func(t *rapid.T) {
		// rapid draws a code point, not a byte, for \xff: the bytes are invalid UTF-8.
		s := rapid.StringMatching(`[aä😀\n %s]{0,40}`).Draw(t, "s") + string(rapid.SliceOfN(rapid.Byte(), 0, 3).Draw(t, "bytes"))
		sub := rapid.StringMatching(`[aä😀]{0,4}`).Draw(t, "sub")
		offset := rapid.IntRange(-1, utf8.RuneCountInString(s)+1).Draw(t, "offset")
		values := []string{s, sub, strconv.Itoa(offset)}
		list := types.NewStringList(types.DefaultTypeAdapter, values)
		for _, expr := range exprs {
			c := programs[expr]
			got, details, gotErr := c.guarded.program.Eval(map[string]any{"v": list, logVariable: &errorLog{}})
			want, plainDetails, wantErr := c.plain.Eval(map[string]any{"v": list})
			if gotErr != nil && !strings.Contains(gotErr.Error(), " one call may ") && fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
				t.Fatalf("%s over %q: error %v, and %v in plain cel-go", expr, values, gotErr, wantErr)
			}
			var more uint64
			switch {
			case strings.Contains(expr, "quote") || strings.Contains(expr, "format"):
				result, _, _ := c.result.Eval(map[string]any{"v": list})
				more = size(result)
			case strings.Contains(expr, "ndexOf") && sub == "":
				more = tenths(uint64(utf8.RuneCountInString(s)))
			}
			if gotErr == nil && wantErr == nil && want != got || *details.ActualCost() != *plainDetails.ActualCost()+more {
				t.Fatalf("%s over %q is %v (%v) and costs %d, and %v (%v) and %d in plain cel-go, %d more", expr, values, got, gotErr, *details.ActualCost(), want, wantErr, *plainDetails.ActualCost(), more)
			}
		}
	})
}

// formattedSize counts the format string whole and what each clause takes,
// and stops counting a value once its size is past what is left of the
// limit, so its walk over a long or nested list ends there.
func TestFormattedSize(t *testing.T) {
	tenA := []any{"a", "a", "a", "a", "a", "a", "a", "a", "a", "a"}
	cases := []struct {
		format string
		args   []any
		limit  int64
		want   int64
	}{
		// %% takes no argument; a % at the end takes one, as a number.
		{"%%%s", []any{"abc"}, math.MaxInt64, 4 + 3},
		{"a%", []any{"x"}, math.MaxInt64, 2 + numberSize},
		{"%x", []any{[]byte("ab")}, math.MaxInt64, 2 + 4},
		{"%s", []any{tenA}, math.MaxInt64, 2 + 2 + 10*3},
		// 2 for the format; [ ] and the first member, past the 3 left.
		{"%s", []any{tenA}, 5, 2 + 2 + 3},
		// The outer list has 4 left, the inner one 2.
		{"%s", []any{[]any{tenA}}, 6, 2 + 2 + (2 + 3) + 2},
		// The map's value starts past the limit, and counts its brackets.
		{"%s", []any{map[string]any{"k": tenA}}, 8, 2 + 2 + 1 + 4 + 2},
	}
	for _, tc := range cases {
		list := types.DefaultTypeAdapter.NativeToValue(tc.args).(traits.Lister)
		if got := formattedSize(tc.format, list, tc.limit); got != tc.want {
			t.Errorf("formattedSize(%q, %v, %d) = %d, want %d", tc.format, tc.args, tc.limit, got, tc.want)
		}
	}
}

// A refused call costs what cel-go charges its arguments, so refusals in a
// comprehension spend the cost limit by what their guards read, a tenth for
// each code point: of 100 refused reads of 10001 code points, the first,
// which costs 1002, ends the evaluation under a limit of 1000 and is named. Were a refusal to cost 1, 138 of them, 1.38
// million code points, ran before a limit of 1000 stopped them.
func TestStringRefusalsSpendTheLimit(t *testing.T) {
	const expr = "[0, 1, 2, 3, 4, 5, 6, 7, 8, 9].all(i, [0, 1, 2, 3, 4, 5, 6, 7, 8, 9].all(j, v[0].charAt(0) != 'z'))"
	long := strings.Repeat("a", 10001)
	a, err := compile(expr, []string{"v"}, limits{cost: 1000, timeout: time.Second, grace: stopGrace})
	if err != nil {
		t.Fatal(err)
	}
	const refusal = "charAt: the call would read 10001 characters, more than the 10000 one call may read, since CEL charges a tenth for each and a longer read exceeds the cost limit of 1000 by itself"
	if _, err := a.Eval(context.Background(), asExtracted(map[string][]string{"v": {long}}, nil, nil)); err == nil || err.Error() != "the evaluation exceeded the cost limit of 1000; before it, "+refusal {
		t.Errorf("under a limit of 1000: error = %v", err)
	}
	// The cost when the limit stopped it: one refusal's 1002, and the few
	// units before it. Two refusals would cost 2004.
	list := types.NewStringList(types.DefaultTypeAdapter, []string{long})
	_, details, err := a.program.Eval(map[string]any{"v": list, logVariable: &errorLog{}})
	if err == nil || !strings.Contains(err.Error(), "cost limit exceeded") || details == nil || *details.ActualCost() <= 1000 || *details.ActualCost() >= 2*1002 {
		t.Fatalf("the evaluation stopped with %v, after a cost of %v", err, details)
	}
}

// An overload of the extension with no guard, no price, or no binding to put
// behind its guard makes the environment an error that names it, so a cel-go
// upgrade that adds one cannot bind it unguarded or unpriced.
func TestStringsRefuseAnOverloadWithNoGuard(t *testing.T) {
	fns, err := stringDecls()
	if err != nil {
		t.Fatal(err)
	}
	overloads := maps.Clone(stringOverloads)
	delete(overloads, "string_trim")
	if _, err := stringsBound(CostLimit, stringFunctions, fns, overloads); err == nil || err.Error() != "the strings extension's overload string_trim of trim has no size guard" {
		t.Errorf("with no entry for string_trim: %v", err)
	}
	overloads["string_trim"] = stringOverload{price: read}
	if _, err := stringsBound(CostLimit, stringFunctions, fns, overloads); err == nil || err.Error() != "the strings extension's overload string_trim of trim has no size guard" {
		t.Errorf("with no guard for string_trim: %v", err)
	}
	// A guarded overload with no price would cost 1 a call, refused or not.
	overloads["string_trim"] = stringOverload{guard: stringOverloads["string_trim"].guard}
	if _, err := stringsBound(CostLimit, stringFunctions, fns, overloads); err == nil || err.Error() != "the strings extension's overload string_trim of trim has no price" {
		t.Errorf("with no price for string_trim: %v", err)
	}
	unbound, err := cel.NewEnv(cel.Function("trim", cel.MemberOverload("string_trim", []*cel.Type{cel.StringType}, cel.StringType)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stringsBound(CostLimit, []string{"trim"}, unbound.Functions(), stringOverloads); err == nil || err.Error() != "the strings extension's overload string_trim of trim has no binding" {
		t.Errorf("with no binding for string_trim: %v", err)
	}
}

// A call of the strings extension left unrewritten is errata's error at
// load, since it would note no refusal and cel-go alone would price it.
func TestStringsAllThroughLogRefusesACallLeft(t *testing.T) {
	base, err := baseEnv()
	if err != nil {
		t.Fatal(err)
	}
	env, err := base.Extend(cel.Variable("v", cel.ListType(cel.StringType)))
	if err != nil {
		t.Fatal(err)
	}
	checked, iss := env.Compile("v[0].trim() == strings.quote(v[0])")
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	const left = "the rewrite through the evaluation log left the strings extension's calls unpriced, calls left: 2; this is errata's error"
	if err := stringsAllThroughLog(checked); err == nil || err.Error() != left {
		t.Errorf("before the rewrite: %v", err)
	}
	rewritten, err := stringsThroughLog(env, checked)
	if err != nil {
		t.Fatal(err)
	}
	if err := stringsAllThroughLog(rewritten); err != nil {
		t.Errorf("after the rewrite: %v", err)
	}
	// A call is found by the overload the checker resolved, not by its
	// name: renamed, as a cel-go that names it otherwise would, it is still
	// a call left.
	renamed, iss := env.Compile("v[0].trim() == 'a'")
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	for _, e := range celast.MatchDescendants(celast.NavigateAST(renamed.NativeRep()), celast.FunctionMatcher("trim")) {
		e.SetKindCase(celast.NewExprFactory().NewMemberCall(e.ID(), "trimmed", e.AsCall().Target()))
	}
	if err := stringsAllThroughLog(renamed); err == nil || err.Error() != strings.Replace(left, "calls left: 2", "calls left: 1", 1) {
		t.Errorf("with trim renamed: %v", err)
	}
	// On a typed receiver, a function of another library that shares a name
	// is not the extension's. On a dyn receiver it would be (stringCall).
	other, err := env.Extend(cel.Function("reverse", cel.MemberOverload("errata_test_list_reverse", []*cel.Type{cel.ListType(cel.StringType)}, cel.ListType(cel.StringType),
		cel.UnaryBinding(func(v ref.Val) ref.Val { return v }))))
	if err != nil {
		t.Fatal(err)
	}
	list, iss := other.Compile("v.reverse() == v")
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	if err := stringsAllThroughLog(list); err != nil {
		t.Errorf("a list's reverse: %v", err)
	}
	if _, err := stringsThroughLog(other, list); err != nil {
		t.Errorf("a list's reverse, rewritten: %v", err)
	}
}

// A refused call costs what plain cel-go charges the call, less the result
// it did not build, for each family of price: a refused format adds a tenth
// of what its count walked, and format and quote, which cel-go does not
// charge their result, cost what cel-go charges them. The price of a call is
// what plain cel-go's whole cost is, less what its arguments cost.
func TestStringRefusedPrices(t *testing.T) {
	env, err := cel.NewEnv(ext.Strings(ext.StringsVersion(stringsVersion)), cel.Variable("v", cel.ListType(cel.StringType)))
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]any{"v": types.NewStringList(types.DefaultTypeAdapter, []string{"äaaa,aaaa,aaaa,aaaa", "ab"})}
	run := func(expr string) (ref.Val, uint64) {
		ast, iss := env.Compile(expr)
		if iss.Err() != nil {
			t.Fatal(iss.Err())
		}
		p, err := env.Program(ast, cel.CostLimit(1<<40))
		if err != nil {
			t.Fatal(err)
		}
		v, details, err := p.Eval(vars)
		if err != nil {
			t.Fatal(err)
		}
		return v, *details.ActualCost()
	}
	cases := []struct {
		id, call string
		args     []string
		// built tells whether cel-go charges the result, which a refused
		// call does not build.
		built bool
		read  int64
	}{
		{"string_char_at_int", "v[0].charAt(1)", []string{"v[0]", "1"}, false, 0},
		{"string_substring_int_int", "v[0].substring(1, 3)", []string{"v[0]", "1", "3"}, true, 0},
		{"string_trim", "v[0].trim()", []string{"v[0]"}, true, 0},
		{"string_index_of_string", "v[0].indexOf(v[1])", []string{"v[0]", "v[1]"}, false, 0},
		{"string_last_index_of_string_int", "v[0].lastIndexOf(v[1], 3)", []string{"v[0]", "v[1]", "3"}, false, 0},
		{"string_lower_ascii", "v[0].lowerAscii()", []string{"v[0]"}, true, 0},
		{"string_reverse", "v[0].reverse()", []string{"v[0]"}, true, 0},
		{"strings_quote", "strings.quote(v[0])", []string{"v[0]"}, false, 0},
		{"string_split_string", "v[0].split(',')", []string{"v[0]", "','"}, true, 0},
		{"list_join_string", "v.join(',')", []string{"v", "','"}, true, 0},
		{"string_replace_string_string", "v[0].replace('a', 'bb')", []string{"v[0]", "'a'", "'bb'"}, true, 0},
		{"string_format", "'%s%s'.format([v[0], v[1]])", []string{"'%s%s'", "[v[0], v[1]]"}, false, 50},
	}
	for _, tc := range cases {
		result, price := run(tc.call)
		args := make([]ref.Val, len(tc.args))
		for i, arg := range tc.args {
			var c uint64
			args[i], c = run(arg)
			price -= c
		}
		if tc.built {
			price -= size(result)
		}
		refusal := types.WrapErr(&guardRefusal{msg: "refused", read: tc.read})
		if got, want := stringPrice(tc.id, args, refusal), price+tenths(uint64(tc.read)); got != want {
			t.Errorf("%s refused costs %d, want %d", tc.call, got, want)
		}
	}
}

// A call that resolves to a guarded overload under a name errata does not
// offer has no hidden function to take its place, so the rewrite leaves it
// and the rule does not load.
func TestStringsCallLeftDoesNotLoad(t *testing.T) {
	renamed := cel.Function("trimmed", cel.MemberOverload("string_trim", []*cel.Type{cel.StringType}, cel.StringType))
	_, err := compile("v[0].trimmed() == ''", []string{"v"}, limits{cost: CostLimit, timeout: time.Second, grace: stopGrace}, renamed)
	if err == nil || err.Error() != "assert: the rewrite through the evaluation log left the strings extension's calls unpriced, calls left: 1; this is errata's error" {
		t.Errorf("a guarded overload under another name: %v", err)
	}
}
