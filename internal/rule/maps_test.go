package rule

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"cel.dev/cel-go/cel"

	"github.com/f-klement/errata/internal/check"
)

// str, obj and seq build the values an extractor with as: map returns, in
// one file, each at its line.
func str(text string, line int) *check.Data {
	return &check.Data{Kind: check.DataScalar, Text: text, At: check.Location{File: "renovate.json", Line: line}}
}

func obj(line int, pairs ...any) *check.Data {
	d := &check.Data{Kind: check.DataMap, At: check.Location{File: "renovate.json", Line: line}}
	for i := 0; i < len(pairs); i += 2 {
		d.Keys = append(d.Keys, pairs[i].(string))
		d.Items = append(d.Items, pairs[i+1].(*check.Data))
	}
	return d
}

func seq(line int, items ...*check.Data) *check.Data {
	return &check.Data{Kind: check.DataList, Items: items, At: check.Location{File: "renovate.json", Line: line}}
}

// whole is the value of d as an extractor with as: map returns it.
func whole(d *check.Data) check.Value {
	return check.Value{Text: d.Show(check.ShownBytes), At: d.At, Data: d}
}

// mapsOf is an extractor named name that returns each of ds whole.
func mapsOf(name string, ds ...*check.Data) check.NamedExtractor {
	values := make([]check.Value, len(ds))
	for i, d := range ds {
		values[i] = whole(d)
	}
	return check.NamedExtractor{Name: name, Extractor: fake{values: values}, Maps: true}
}

// blastRadius is k3s's blast_radius_charts_never_automerge as a rule: the
// charts the never-automerge rule names end with automerge not true once
// every packageRule is applied in order, the last that sets automerge for
// a name winning.
const blastRadius = `rules.exists(b, has(b.automerge) && b.automerge == 'false' && !has(b.matchUpdateTypes) && has(b.prPriority) && b.prPriority == '5' && has(b.matchPackageNames)) &&
rules.filter(b, has(b.automerge) && b.automerge == 'false' && !has(b.matchUpdateTypes) && has(b.prPriority) && b.prPriority == '5' && has(b.matchPackageNames)).all(b,
  b.matchPackageNames.all(n,
    rules.map(r, has(r.automerge) && has(r.matchPackageNames) && n in r.matchPackageNames, r.automerge)[
      size(rules.filter(r, has(r.automerge) && has(r.matchPackageNames) && n in r.matchPackageNames)) - 1] != 'true'))`

// blastRadiusBound is blastRadius with the rules that set automerge for a
// name bound once by cel.bind, so the filter is not written twice.
const blastRadiusBound = `rules.exists(b, has(b.automerge) && b.automerge == 'false' && !has(b.matchUpdateTypes) && has(b.prPriority) && b.prPriority == '5' && has(b.matchPackageNames)) &&
rules.filter(b, has(b.automerge) && b.automerge == 'false' && !has(b.matchUpdateTypes) && has(b.prPriority) && b.prPriority == '5' && has(b.matchPackageNames)).all(b,
  b.matchPackageNames.all(n,
    cel.bind(setting, rules.filter(r, has(r.automerge) && has(r.matchPackageNames) && n in r.matchPackageNames),
      setting[size(setting) - 1].automerge != 'true')))`

// cel.bind names an intermediate value over a list of maps: the fold with
// its matching rules bound once gives the verdicts the fold without it
// gives. An extractor read only in the bound value counts as used, and a
// bound value of a map compared with a bool is refused as the value itself
// is. Planted break: the bindings library left out of the environment,
// which made cel.bind a load error.
func TestMapsBindAnIntermediateValue(t *testing.T) {
	early := obj(10, "matchPackageNames", seq(10, str("metallb", 10)), "automerge", str("true", 11))
	blast := obj(20, "matchPackageNames", seq(20, str("cert-manager", 20), str("metallb", 20)),
		"automerge", str("false", 21), "prPriority", str("5", 22))
	late := obj(40, "matchPackageNames", seq(40, str("cert-manager", 40)), "automerge", str("true", 41))
	if res := Run(context.Background(), ".", assertRule(t, blastRadiusBound, mapsOf("rules", early, blast))); res.Verdict != check.VerdictTrue {
		t.Errorf("no later rule turns automerge on: %v %s", res.Verdict, res.Message)
	}
	if res := Run(context.Background(), ".", assertRule(t, blastRadiusBound, mapsOf("rules", early, blast, late))); res.Verdict != check.VerdictFalse {
		t.Errorf("a later rule turns automerge on for cert-manager: %v %s", res.Verdict, res.Message)
	}
	if _, err := Compile("cel.bind(first, m[0], has(first.z))", []string{"m"}, Lists{Maps: []string{"m"}}); err != nil {
		t.Errorf("m read only in the bound value: %v", err)
	}
	if _, err := Compile("cel.bind(z, m[0].z, z != true)", []string{"m"}, Lists{Maps: []string{"m"}}); err == nil || !strings.Contains(err.Error(), "the bool true is compared") {
		t.Errorf("a bound value of a map compared with true: %v", err)
	}
}

// The bindings library is pinned at version 0, the cel.bind macro alone:
// cel.@block, which a later version declares, is not in assert's
// environment. Planted break: bindingsVersion 1, which declares it.
func TestBindingsAreTheMacroAlone(t *testing.T) {
	env, err := baseEnv()
	if err != nil {
		t.Fatal(err)
	}
	if env.HasFunction("cel.@block") {
		t.Error("cel.@block is declared")
	}
}

// A list of maps folds Renovate's packageRules last write wins: the
// never-automerge rule holds while no later rule turns automerge on for its
// charts, and a later rule that does is an erratum whose message names it at
// its line. A rule before it that turns automerge on loses, so the rule
// holds. Planted breaks: the maps handed to CEL in reverse order, which
// passed the later rule; a map's keys dropped, which made every rule
// unknown.
func TestMapsFoldPackageRulesLastWriteWins(t *testing.T) {
	early := obj(10, "matchPackageNames", seq(10, str("metallb", 10)), "automerge", str("true", 11))
	blast := obj(20, "matchPackageNames", seq(20, str("cert-manager", 20), str("metallb", 20)),
		"automerge", str("false", 21), "prPriority", str("5", 22))
	digests := obj(30, "matchUpdateTypes", seq(30, str("digest", 30)), "automerge", str("true", 31))
	if res := Run(context.Background(), ".", assertRule(t, blastRadius, mapsOf("rules", early, blast, digests))); res.Verdict != check.VerdictTrue {
		t.Fatalf("no later rule turns automerge on: %v %s", res.Verdict, res.Message)
	}
	late := obj(40, "matchPackageNames", seq(40, str("cert-manager", 40)), "automerge", str("true", 41))
	res := Run(context.Background(), ".", assertRule(t, blastRadius, mapsOf("rules", early, blast, digests, late)))
	want := `rules[3] = {"matchPackageNames": ["cert-manager"], "automerge": "true"} at renovate.json:40`
	if res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, want) {
		t.Errorf("a later rule turns automerge on for cert-manager: %v %s", res.Verdict, res.Message)
	}
	if res := Run(context.Background(), ".", assertRule(t, blastRadius, mapsOf("rules", early, digests))); res.Verdict != check.VerdictFalse {
		t.Errorf("no never-automerge rule: %v %s", res.Verdict, res.Message)
	}
}

// A mapping is a map(string, dyn): a nested mapping is a map, a sequence a
// list, and a scalar a string, the text the file wrote, so a number is
// compared as '5' and converted with int(). A map's keys come in file
// order, the same on every run. A key the mapping lacks is false under
// has() and makes the rule unknown when read. Planted breaks: a scalar
// handed over as a number, which failed the comparison with '5'; the
// keys in Go's map order, which changed the order of the keys from run to
// run; a missing key read as the empty string, which held.
func TestMapsTypeTheirMembers(t *testing.T) {
	d := obj(1, "z", str("5", 1), "a", obj(2, "b", seq(3, str("x", 3), obj(4, "c", str("true", 4)))))
	cases := map[string]check.Verdict{
		"m[0].z == '5' && int(m[0].z) == 5":                                       check.VerdictTrue,
		"m[0].a.b[0] == 'x' && m[0].a.b[1].c == 'true'":                           check.VerdictTrue,
		"type(m[0].a) == map && type(m[0].a.b) == list && type(m[0].z) == string": check.VerdictTrue,
		"m[0].map(k, k) == ['z', 'a'] && m[0].a.b.size() == 2":                    check.VerdictTrue,
		"has(m[0].a.b) && !has(m[0].y) && 'z' in m[0]":                            check.VerdictTrue,
		"m[0] == {'z': '5', 'a': {'b': ['x', {'c': 'true'}]}}":                    check.VerdictTrue,
		"m[0].y == ''":  check.VerdictUnknown,
		"m[0].z == 'x'": check.VerdictFalse,
	}
	for expr, want := range cases {
		for range 20 {
			if res := Run(context.Background(), ".", assertRule(t, expr, mapsOf("m", d))); res.Verdict != want {
				t.Fatalf("%s: %v, want %v: %s", expr, res.Verdict, want, res.Message)
			}
		}
	}
}

// An extractor with as: map is a list(map(string, dyn)) at load, so an
// expression that reads it as a list of strings does not load, and a flat
// extractor beside it stays a list(string). Planted break: every extractor
// declared a list of maps, which loaded v.startsWith('a').
func TestMapsAreTypedAtLoad(t *testing.T) {
	if _, err := Compile("m[0].startsWith('a')", []string{"m"}, Lists{Maps: []string{"m"}}); err == nil || !strings.Contains(err.Error(), "found no matching overload for 'startsWith'") {
		t.Errorf("a map read as a string: %v", err)
	}
	if _, err := Compile("m[0].a == v[0] && v[0].startsWith('a')", []string{"m", "v"}, Lists{Maps: []string{"m"}}); err != nil {
		t.Errorf("a flat extractor beside a map extractor: %v", err)
	}
	if _, err := Compile("v[0].a == 'x'", []string{"v"}, Lists{}); err == nil {
		t.Error("a flat extractor read as a map loaded")
	}
}

// by_file over an extractor with as: map is a map from each file to its
// maps, in the order the maps came. Planted break: by_file grouping the
// maps' text, which made by_file(m)[f][0].a a type error.
func TestMapsGroupByFile(t *testing.T) {
	a, b := obj(1, "a", str("1", 1)), obj(5, "a", str("2", 5))
	b.At.File = "other.json"
	c := assertRule(t, "by_file(m)['renovate.json'][0].a == '1' && by_file(m)['other.json'].map(x, x.a) == ['2']", mapsOf("m", a, b))
	if res := Run(context.Background(), ".", c); res.Verdict != check.VerdictTrue {
		t.Errorf("%v %s", res.Verdict, res.Message)
	}
}

// normalize applies to every scalar of a map, as it applies to a flat
// value, and leaves the keys as the file wrote them, since the expression
// names them. The message shows the map as assert saw it. Planted breaks:
// normalize left out of maps, which compared ' X '; the keys lowered too,
// which lost Name.
func TestMapsAreNormalized(t *testing.T) {
	c := assertRule(t, "m[0].Name == 'x' && m[0].L[0] == 'y' && m[0].Name == 'q'", mapsOf("m", obj(3, "Name", str(" X ", 3), "L", seq(4, str("Y", 4)))))
	c.Rule.Normalize = []check.Normalization{check.NormalizeTrim, check.NormalizeLower}
	res := Run(context.Background(), ".", c)
	if res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, `m[0] = {"Name": "x", "L": ["y"]} at renovate.json:3`) {
		t.Errorf("%v %s", res.Verdict, res.Message)
	}
}

// Maps that do not come one to one with their files are refused, so the
// rule is unknown rather than reading a value under the wrong file.
// Planted break: the count left unchecked, which read a list one map short.
func TestMapsRefuseValuesWithoutTheirFiles(t *testing.T) {
	a, err := Compile("size(m) == 1", []string{"m"}, Lists{Maps: []string{"m"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Eval(context.Background(), map[string][]string{"m": {"x", "y"}}, map[string][]string{"m": {"a", "a"}}, map[string][]*check.Data{"m": {str("x", 1)}})
	if err == nil || err.Error() != `extractor "m": 1 maps came with 2 files, so the values are not whole` {
		t.Errorf("got %v", err)
	}
	if ok, err := a.Eval(context.Background(), nil, nil, nil); err != nil || ok {
		t.Errorf("no map and no file: %v %v", ok, err)
	}
}

// A large list of maps is handed to CEL in time linear in its nodes:
// 100000, the most an extractor returns, read whole. A comprehension
// inside another over a list of maps is charged for every member it reads
// and stops at the cost limit, unknown and naming it, here a lowered one.
// cel-go's cost tracker makes a comprehension quadratic in its list's
// length whatever its members are, so a long comprehension over maps
// meets the timeout as one over strings does. Planted break: a map's
// conversion repeated for each member read, which ran the first case past
// the timeout.
func TestMapsOfALargeListStayInsideTheCostLimit(t *testing.T) {
	var ds []*check.Data
	for i := range 50000 {
		ds = append(ds, obj(i+1, "n", str(strconv.Itoa(i), i+1)))
	}
	if res := Run(context.Background(), ".", assertRule(t, "size(m) == 50000 && m[49999].n == '49999'", mapsOf("m", ds...))); res.Verdict != check.VerdictTrue {
		t.Fatalf("100000 nodes: %v %.200s", res.Verdict, res.Message)
	}
	a, err := compileWith("m.all(x, m.exists(y, y.n == x.n))", []string{"m"}, Lists{Maps: []string{"m"}}, limits{cost: 10000, timeout: EvalTimeout, grace: stopGrace})
	if err != nil {
		t.Fatal(err)
	}
	files := make([]string, 300)
	_, err = a.Eval(context.Background(), nil, map[string][]string{"m": files}, map[string][]*check.Data{"m": ds[:300]})
	if err == nil || err.Error() != "the evaluation exceeded the cost limit of 10000" {
		t.Errorf("a comprehension inside another: %v", err)
	}
}

// ==, != and in between a value CEL types dyn, as a map's value is, and an
// operand of a type such a value never equals do not load, on either side,
// at that operand, saying what to write. The pass goes by the operand's
// checked type, so a computed bool or number is refused as a literal is,
// and it looks inside list and map literals, and through a side whose type
// is a list or map of dyn. A string, a converted value, an explicit dyn(),
// an empty container and a flat value load. Planted breaks, each loading
// the cases named: the pass left out (all); the right operand left
// unchecked (5 == m[0].z); in left unchecked ('x' in [1, 'x']); a list of
// dyn values left unchecked (true in [m[0].z, m[0].y]); a literal's kind
// tested in place of its type (int('5'), !false, size(m), the ternaries);
// bytes, duration or timestamp left out of the types (b'5', duration('1s'),
// timestamp(...)); a list literal's elements left unchecked
// (m[0].a.b == [true]); a map literal's values left unchecked
// (m[0].a == {'b': true}); its keys left unchecked (m[0].a == {5: 'x'});
// the keys of a map literal after in left unchecked (m[0].z in {5: 1});
// a side of type map(string, dyn) or list(map(string, dyn)) not counted as
// dyn (m[0] == {'z': 5}, m == [{'z': 5}]). A break that refuses a list
// operand of any element type refuses m[0].a.b == [] and fails the loads.
// Round 3's breaks: a list's or a map's member type left unchecked
// (v.map(x, x == 'y'), the ternaries of maps); a cel.bind name not followed
// to its literal (the bound mixed record); a comprehension variable not
// followed to the literal it runs over ([{'name': 'x', ...}].exists(w, ...));
// the member type of in's right side left unchecked ([1, 2].map(x, x), the
// map ternary); a cel.bind name after in not followed (the bound [1, 'a']);
// a refused member not refused once (the member reached twice).
func TestMapsRefuseComparisonsTheyNeverEqual(t *testing.T) {
	const (
		never  = "is compared with a value CEL types dyn, such as one from a map; a map's scalars are always strings, so the two are never equal; "
		escape = "; if the other side does not come from a map, wrap it in dyn()"
		number = "compare with a string, or convert the value with int(), uint() or double()" + escape
		truth  = "compare with the string 'true' or 'false'" + escape
	)
	for expr, want := range map[string]string{
		"m.all(r, !has(r.automerge) || r.automerge != true)": "assert: the bool true " + never + "compare with the string 'true'" + escape + ", at line 1 column 46 of the expression:\n    m.all(r, !has(r.automerge) || r.automerge != true)\n                                                 ^",
		"5 == m[0].z":                    "the number 5 " + never + "compare with the string '5', or convert the value with int(), uint() or double()" + escape + ", at line 1 column 1",
		"m[0].z == 5u":                   "the number 5 is compared",
		"m[0].z != 2.5":                  "the number 2.5 is compared",
		"m[0].z == null":                 "null " + never + "a mapping holds null as the string 'null', so compare with 'null'" + escape,
		"m[0].z in [1, '1']":             "the number 1 " + never + "compare with the string '1', or convert the value with int(), uint() or double()" + escape + ", at line 1 column 12",
		"true in [m[0].z, m[0].y]":       "the bool true is compared",
		"false in m[0].a.b":              "the bool false is compared",
		"m[0].z == false || m[0].z == 1": "the bool false is compared",
		// Computed operands, by type.
		"m[0].t == !false":                            "an expression of type bool " + never + truth + ", at line 1 column 11",
		"m[0].t == (size(m) > 0 ? true : false)":      "an expression of type bool " + never + truth,
		"m[0].z == (true ? 5 : 6)":                    "an expression of type int " + never + number,
		"m[0].z == size(m)":                           "an expression of type int " + never + number,
		"m[0].z == int('5')":                          "an expression of type int " + never + number,
		"m[0].t == (1 == 1)":                          "an expression of type bool " + never + truth,
		"size(m) in m[0].a.b":                         "an expression of type int " + never + number,
		"m[0].z == b'5'":                              "an expression of type bytes " + never + "convert the value with bytes()" + escape,
		"m[0].z == duration('1s')":                    "an expression of type duration " + never + "convert the value with duration()" + escape,
		"m[0].z == timestamp('2026-01-01T00:00:00Z')": "an expression of type timestamp " + never + "convert the value with timestamp()" + escape,
		// Container literals, at the member.
		"m[0] == {'z': 5}":        "the number 5 is compared",
		"m == [{'z': 5}]":         "the number 5 is compared",
		"m[0].a == {'b': [true]}": "the bool true is compared",
		"m[0].a == {'b': true}":   "the bool true is compared",
		"m[0].a == {5: 'x'}":      "the number 5 is compared",
		"m[0].a.b == [true]":      "the bool true is compared",
		"{'b': [1]} in [m[0].a]":  "the number 1 is compared",
		"[true] in [m[0].a.b]":    "the bool true is compared",
		"{'z': 5} in m":           "the number 5 is compared",
		"m[0] in [{'z': 5}]":      "the number 5 is compared",
		"m[0].z in {5: 1}":        "the number 5 is compared",
		// A typed container that is not a literal, by its member type.
		"m[0].a.b == v.map(x, x == 'y')":               "an expression of type list(bool) is compared with a value CEL types dyn, such as one from a map; a map's scalars are always strings, so its members never equal a map's, and the two are equal only when both are empty; build it of strings, such as with string()" + escape,
		"m[0] == {'z': v.map(x, int(x))}":              "an expression of type list(int) is compared",
		"m[0].z in [1, 2].map(x, x)":                   "an expression of type list(int) is searched for a value CEL types dyn, such as one from a map; a map's scalars are always strings, so it never holds one; build it of strings, such as with string(); if the value does not come from a map, wrap it in dyn()",
		"m[0].z in v.map(x, int(x))":                   "an expression of type list(int) is searched",
		"cel.bind(ns, [1, 2], m[0].z in ns)":           "the number 1 is compared",
		"cel.bind(ns, v.map(x, int(x)), m[0].z in ns)": "an expression of type list(int) is searched",
		"m[0].z in {5: 'x'}.map(k, k)":                 "an expression of type list(int) is searched",
		// The literal a cel.bind name binds, or a comprehension variable
		// runs over, at its member, a mixed record included.
		"cel.bind(want, {'automerge': true}, m.exists(r, r == want))":                    "the bool true is compared",
		"cel.bind(want, {'name': 'x', 'automerge': true}, m.exists(r, r == want))":       "the bool true is compared with a value CEL types dyn, such as one from a map; a map's scalars are always strings, so the two are never equal; compare with the string 'true'" + escape + ", at line 1 column 43",
		"cel.bind(w, {'name': 'x', 'automerge': true}, cel.bind(want, w, m[0] == want))": "the bool true is compared",
		"[{'automerge': true}].exists(w, m[0] == w)":                                     "the bool true is compared",
		"[{'name': 'x', 'automerge': true}].exists(w, m[0] == w)":                        "the bool true is compared",
		"cel.bind(ports, [80, 443], m.all(s, s.z in ports))":                             "the number 80 is compared",
		"m[0].a == (true ? {'b': 1} : {'b': 2})":                                         "an expression of type map(string, int) is compared",
		"m[0].a == (true ? {1: 'x'} : {2: 'x'})":                                         "an expression of type map(int, string) is compared",
		"m[0].z in (true ? {5: 'x'} : {6: 'x'})":                                         "an expression of type map(int, string) is searched",
		"cel.bind(x, m[0], cel.bind(y, x.z, y == 1))":                                    "the number 1 is compared",
		"cel.bind(a, [1, 'x'], cel.bind(b, a, m[0].z in b))":                             "the number 1 is compared",
		"[[1, 'x']].exists(l, m[0].z in l)":                                              "the number 1 is compared",
		"cel.bind(mixed, [1, 'a'], m[0].z in mixed)":                                     "the number 1 is compared",
	} {
		// The refusal comes before the test that each extractor is used.
		_, err := Compile(expr, []string{"m", "v"}, Lists{Maps: []string{"m"}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", expr, err, want)
		}
	}
	if _, err := Compile("m[0].z == false || m[0].z == 1", []string{"m"}, Lists{Maps: []string{"m"}}); err == nil || strings.Count(err.Error(), "is compared") != 2 {
		t.Errorf("each literal refused: %v", err)
	}
	if _, err := Compile("cel.bind(want, {'automerge': true}, m.exists(r, r == want) && m.all(r, r != want))", []string{"m"}, Lists{Maps: []string{"m"}}); err == nil || strings.Count(err.Error(), "is compared") != 1 {
		t.Errorf("a bound member reached twice is refused once: %v", err)
	}
	for _, expr := range []string{
		"m[0].z == '5' && int(m[0].z) == 5 && 'x' in m[0].a.b",
		"dyn(m[0].z) == 5",
		"size(m[0]) == 2 && m[0].z in ['1', '2']",
		"v[0] == '5'",
		"has(m[0].t) == true && (m[0].t == 'true') == true && type(m[0].z) == string",
		"m[0].z in {'5': 1} && m[0].a.b == [] && m[0] != {} && m != [] && [] in [m[0].a.b]",
		"m[0] == {'z': '5'} && m[0].a == {'b': ['true']}",
		"duration(m[0].d) == duration('1s')",
		"m.map(r, int(r.z)) == [5] && m[0].z in [1, 2].map(x, string(x)) && m.map(r, r.z) == v",
		"cel.bind(n, size(m), m.all(r, int(r.z) < n))",
		"cel.bind(want, {'name': 'x', 'automerge': 'true'}, m.exists(r, r == want))",
		"cel.bind(want, dyn({'automerge': true}), m.exists(r, r == want))",
		"cel.bind(none, [], m[0].a.b == none) && [[]].exists(w, m[0].a.b == w)",
		"m.exists(r, r.z in ['1', '2'].map(x, x))",
		"type(m[0].z) == int && 'f' in by_file(m) && by_file(m).size() == 1 && m[0].z == string(5)",
	} {
		if _, err := Compile(expr+" && size(m) + size(v) >= 0", []string{"m", "v"}, Lists{Maps: []string{"m"}}); err != nil {
			t.Errorf("%s: %v", expr, err)
		}
	}
}

// visits is the number of nodes the refusal pass visits in expr, over m, a
// list of maps.
func visits(t *testing.T, expr string) int {
	t.Helper()
	base, err := baseEnv()
	if err != nil {
		t.Fatal(err)
	}
	env, err := base.Extend(cel.Variable("m", cel.ListType(mapType)))
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	_, n := refusals(expr, ast.NativeRep())
	return n
}

// chain is a name followed through depth levels, each naming the last k
// times: binds compared with ==, the same searched with in, and
// comprehension variables over list literals, compared and searched.
func chain(shape string, k, depth int) string {
	names := func(prefix string, i int) string {
		return strings.TrimSuffix(strings.Repeat(prefix+strconv.Itoa(i)+", ", k), ", ")
	}
	if shape == "iter" || shape == "in iter" {
		expr, first := "m[0].z == x"+strconv.Itoa(depth), "['x']"
		if shape == "in iter" {
			expr, first = "m[0].z in x"+strconv.Itoa(depth), "[['x']]"
		}
		for i := depth; i >= 1; i-- {
			expr = "[" + names("x", i-1) + "].exists(x" + strconv.Itoa(i) + ", " + expr + ")"
		}
		return first + ".exists(x0, " + expr + ")"
	}
	expr := "m[0].z == a" + strconv.Itoa(depth)
	if shape == "in" {
		expr = "m[0].z in a" + strconv.Itoa(depth)
	}
	for i := depth; i >= 1; i-- {
		expr = "cel.bind(a" + strconv.Itoa(i) + ", [" + names("a", i-1) + "], " + expr + ")"
	}
	return "cel.bind(a0, 'x', " + expr + ")"
}

// The refusal pass visits each node of a followed name's value once, so its
// work grows with the expression, not with the paths through it: a chain
// one level deeper adds the same few visits, where a pass that walked a
// value again at every read multiplied them by k, and a 400 byte chain
// took 56 s to load. Planted breaks: the set of compared nodes left out,
// and the set of searched nodes left out, each of which made a level
// multiply the visits by 8.
func TestMapsRefusalVisitsEachNodeOnce(t *testing.T) {
	for _, shape := range []string{"bind", "in", "iter", "in iter"} {
		shallow, deep := visits(t, chain(shape, 8, 5)), visits(t, chain(shape, 8, 6))
		t.Logf("%s: %d visits at depth 5, %d at depth 6", shape, shallow, deep)
		if deep >= 2*shallow {
			t.Errorf("%s: %d visits at depth 5, %d at depth 6", shape, shallow, deep)
		}
	}
}

// A dyn that does not come from a map, such as a member of a list literal
// that mixes types, is refused the same way, in a rule with no map
// extractor too, and the message names a value CEL types dyn and the way
// out: dyn() around it loads. Planted break: the message without its
// escape, which fails the wording.
func TestDynThatIsNoMapsValueIsRefusedWithItsWayOut(t *testing.T) {
	_, err := Compile("[1, 'a'][0] == 1 && size(v) >= 0", []string{"v"}, Lists{})
	if err == nil || !strings.Contains(err.Error(), "the number 1 is compared with a value CEL types dyn, such as one from a map;") || !strings.Contains(err.Error(), "; if the other side does not come from a map, wrap it in dyn()") {
		t.Errorf("a dyn of a list literal: %v", err)
	}
	for _, expr := range []string{"dyn([1, 'a'][0]) == 1", "1 in dyn([1, 'a'])"} {
		if _, err := Compile(expr+" && size(v) >= 0", []string{"v"}, Lists{}); err != nil {
			t.Errorf("%s: %v", expr, err)
		}
	}
}
