package rule

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
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
	}
	for _, tc := range cases {
		got, err := eval(t, tc.expr, tc.values...)
		if err != nil || !got {
			t.Errorf("%s over %.40q = %v, %v; want true", tc.expr, tc.values, got, err)
		}
	}
}

// Every function of the strings extension but split, join and replace is
// left out, so an expression that names one does not load.
func TestStringsLeavesTheRestOut(t *testing.T) {
	scratch, err := cel.NewCustomEnv(ext.Strings(ext.StringsVersion(stringsVersion)))
	if err != nil {
		t.Fatal(err)
	}
	var hidden int
	for name, fn := range scratch.Functions() {
		if slices.Contains(stringFunctions, name) {
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
		if _, err := Compile(expr, []string{"v"}); err == nil || !strings.Contains(err.Error(), "undeclared reference to '"+name[strings.LastIndex(name, ".")+1:]+"'") {
			t.Errorf("%s: load error = %v, want an undeclared reference", expr, err)
		}
	}
	if hidden < 10 {
		t.Fatalf("%d functions left out, want the extension's ten others at least", hidden)
	}
}

// split, join and replace give what Go's strings package gives.
func TestPropertyStringFunctionsAgreeWithGo(t *testing.T) {
	a, err := Compile("v[0].split(v[1], int(v[2])) == w && w.join(v[1]) == v[3] && v[0].replace(v[1], v[4], int(v[2])) == v[5]", []string{"v", "w"})
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
		if ok, err := a.Eval(context.Background(), values); err != nil || !ok {
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
// they read and build, by cel-go's own cost model.
func TestStringFunctionsCountTowardTheCostLimit(t *testing.T) {
	for _, expr := range []string{"size(v[0].split(',')) > 0", "v[0].split(',').join(';') != ''", "v[0].replace(',', ';') != ''"} {
		a, err := compile(expr, []string{"v"}, limits{cost: 100, timeout: time.Second, grace: stopGrace})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Eval(context.Background(), map[string][]string{"v": {strings.Repeat("a,", 100)}}); err == nil || err.Error() != "the evaluation exceeded the cost limit of 100" {
			t.Errorf("%s over 200 bytes under a limit of 100: error = %v", expr, err)
		}
		if ok, err := a.Eval(context.Background(), map[string][]string{"v": {"a,b"}}); err != nil || !ok {
			t.Errorf("%s over 3 bytes under a limit of 100: %v, %v", expr, ok, err)
		}
	}
}

// The guards' bounds are what the cost limit charges: a string of more than
// 4 bytes a code point, and a list of more than one member a unit.
func TestStringBoundsFollowTheCostLimit(t *testing.T) {
	if MaxBuilt != 4*int64(CostLimit) || MaxParts != int64(CostLimit) {
		t.Fatalf("MaxBuilt %d and MaxParts %d, with a cost limit of %d", MaxBuilt, MaxParts, CostLimit)
	}
}
