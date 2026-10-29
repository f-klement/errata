package regex

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

func value(text, file string, line int) check.Value {
	return check.Value{Text: text, At: check.Location{File: file, Line: line}}
}

// mustReference builds s, which must reference names.
func mustReference(t *testing.T, s Spec) check.Dependent {
	t.Helper()
	e, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := e.(check.Dependent)
	if !ok {
		t.Fatalf("Build gave %T, want a check.Dependent", e)
	}
	return d
}

// A pattern that references an extractor needs it, and searches for each
// of its values, quoted, in the reference's place: a.b matches no axb, a
// value two files gave is searched for once, and the values come in the
// order of the file. Planted breaks: the value not quoted; no distinct.
func TestReferenceQuotesEachValue(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"renovate.json": "--install a.b x\n--install axb x\n--install argo-cd x\n"})
	d := mustReference(t, Spec{File: "renovate.json", Pattern: `--install ((?{charts})) `})
	if got := d.Needs(); !slices.Equal(got, []string{"charts"}) {
		t.Fatalf("Needs = %q", got)
	}
	vs, err := d.ExtractWith(root, map[string][]check.Value{"charts": {
		value("argo-cd", "a.sh", 3), value("a.b", "a.sh", 5), value("argo-cd", "b.sh", 1),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := extracttest.Texts(vs); got != "a.b@1 argo-cd@3" {
		t.Errorf("got %q", got)
	}
}

// A repeat after the reference repeats the whole value, and an alternation
// in a value stays inside it. Planted break: the value put in with no
// group around it.
func TestReferenceKeepsTheValueWhole(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "x: abab\ny: a|b\n"})
	d := mustReference(t, Spec{File: "f", Pattern: `^\w: ((?{v})+)$`})
	vs, err := d.ExtractWith(root, map[string][]check.Value{"v": {value("ab", "g", 1), value("a|b", "g", 2)}})
	if err != nil || extracttest.Texts(vs) != "abab@1 a|b@2" {
		t.Fatalf("got %v, %v", vs, err)
	}
}

// An extractor that gave no value leaves the pattern nothing to search
// for: no value, with the reason, so allow_empty decides. Planted break:
// a search of the pattern with no value, which matches.
func TestReferenceOfNoValue(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "chart: \n"})
	d := mustReference(t, Spec{File: "f", Pattern: `chart: ((?{charts}))`})
	vs, err := d.ExtractWith(root, map[string][]check.Value{"charts": nil})
	var none *check.NoValue
	if !errors.As(err, &none) || none.Reason != `extractor "charts" gave no value to search for` || vs != nil {
		t.Fatalf("got %v, %v; want check.NoValue", vs, err)
	}
}

// Extract, which no rule calls on a Dependent, refuses.
func TestReferenceExtractRefuses(t *testing.T) {
	d := mustReference(t, Spec{File: "f", Pattern: `((?{charts}))`})
	_, err := d.Extract(t.TempDir())
	extracttest.WantError(t, err, `regex: the pattern references the values of extractor "charts", which only its rule gives it`)
}

// files searches every file for every value, each value at its own file,
// in the order of the files. Planted break: the first file read alone.
func TestReferenceOverFiles(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{"a/1.sh": "install b\ninstall a\n", "b/2.sh": "install a\n", "c.md": "install a\n"})
	d := mustReference(t, Spec{Files: []string{"**/*.sh"}, Pattern: `install ((?{names}))$`})
	vs, err := d.ExtractWith(root, map[string][]check.Value{"names": {value("a", "n", 1), value("b", "n", 2)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := extracttest.Texts(vs); got != "b@1 a@2 a@1" || vs[2].At.File != "b/2.sh" {
		t.Errorf("got %q at %v", got, vs)
	}
}

// values_as: pattern reads each value as RE2 syntax, as Renovate reads its
// matchStrings. Planted break: values_as ignored, so the value is quoted.
func TestReferenceValuesAsPattern(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"plans.yaml": "version: v1.36.4+k3s1\nversion: v1.36.4\n"})
	d := mustReference(t, Spec{File: "plans.yaml", Pattern: `((?{matchers}))`, ValuesAs: AsPattern})
	vs, err := d.ExtractWith(root, map[string][]check.Value{"matchers": {value(`version: (?<currentValue>v[\d.]+\+k3s\d+)`, "renovate.json", 4)}})
	if err != nil || extracttest.Texts(vs) != "version: v1.36.4+k3s1@1" {
		t.Fatalf("got %v, %v", vs, err)
	}
}

// Under values_as: pattern, a value that is no RE2 pattern by itself, one
// that ends inside \Q, and one that takes the pattern past its capture
// groups are refused, each naming the value and where it was found.
// Planted breaks: each check removed in turn, so the value runs on into
// the pattern or is refused without its place.
func TestReferenceRefusesAValue(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "a)\n"})
	d := mustReference(t, Spec{File: "f", Pattern: `((?{m}))`, ValuesAs: AsPattern})
	cases := []struct{ value, want string }{
		{`(?=x)`, `regex: the value "(?=x)" of extractor "m", at renovate.json:4, is no RE2 pattern, and values_as: pattern reads each value as one: error parsing regexp: invalid or unsupported Perl syntax: ` + "`(?=`"},
		{`a\Q`, `regex: the value "a\\Q" of extractor "m", at renovate.json:4, ends inside \Q, which would run on into the pattern around it; end it with \E`},
		{strings.Repeat("(a)", 32), `regex: the pattern with the value "(a)(a)`},
	}
	for _, tc := range cases {
		_, err := d.ExtractWith(root, map[string][]check.Value{"m": {value(tc.value, "renovate.json", 4)}})
		extracttest.WantError(t, err, tc.want)
	}
	_, err := d.ExtractWith(root, map[string][]check.Value{"m": {value(strings.Repeat("(a)", 32), "renovate.json", 4)}})
	extracttest.WantError(t, err, `of extractor "m", at renovate.json:4: regex: pattern`)
	extracttest.WantError(t, err, "it has 33 capture groups, over the limit of 32")
}

// A pattern's reference is refused at load where it cannot stand. Each
// case plants one break in a spec that loads.
func TestReferenceRefusals(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"two references", Spec{File: "f", Pattern: `((?{a}))((?{b}))`},
			`regex: pattern "((?{a}))((?{b}))" references values 2 times; a pattern references the values of one extractor, once`},
		{"no name", Spec{File: "f", Pattern: `((?{}))`},
			`regex: pattern "((?{}))" holds (?{ but no reference; write (?{name}), with the name of an extractor of the rule`},
		{"not closed", Spec{File: "f", Pattern: `((?{a)`},
			`regex: pattern "((?{a)" holds (?{ but no reference`},
		{"no capture group", Spec{File: "f", Pattern: `x(?{a})`},
			`regex: pattern "x(?:)" has no capture group; the extractor returns group 1 (read with (?{a}) as an empty value)`},
		{"in a character class", Spec{File: "f", Pattern: `([(?{a})])`},
			`regex: pattern "([(?{a})])": the reference (?{a}) stands inside a character class or \Q...\E, where it is text; move it out of it`},
		{"inside \\Q", Spec{File: "f", Pattern: `(x)\Q(?{a})`},
			`the reference (?{a}) stands inside a character class or \Q...\E`},
		{"escaped", Spec{File: "f", Pattern: `(x)\(?{a})`},
			`(read with (?{a}) as an empty value)`},
		{"a pattern before group 1", Spec{File: "f", Pattern: `(?{a})(x)`, ValuesAs: AsPattern},
			`regex: pattern "(?{a})(x)": with values_as: pattern the reference (?{a}) stands before capture group 1, so a value's own groups would come first; put it inside group 1 or after it`},
		{"values_as unknown", Spec{File: "f", Pattern: `((?{a}))`, ValuesAs: "regex"},
			`regex: values_as "regex" must be literal or pattern`},
		{"values_as with no reference", Spec{File: "f", Pattern: `(x)`, ValuesAs: Literal},
			`regex: values_as applies to a pattern that references an extractor's values, as (?{name}) does, and this pattern references none`},
		{"as record", Spec{File: "f", Pattern: `(?P<n>(?{a}))`, As: "record"},
			`regex: pattern "(?P<n>(?{a}))" references values, and as: record reads only a pattern that references none; leave as out to read group 1 of each value's matches`},
		{"file outside", Spec{File: "../f", Pattern: `((?{a}))`},
			`regex: file "../f" is outside the repository: write a path relative to the repository root that does not leave it`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Build(tc.spec)
			extracttest.WantError(t, err, tc.want)
			if e != nil {
				t.Errorf("a refusal returns the extractor %#v", e)
			}
		})
	}
	// The file's refusal is a file's, with no word of the reference.
	_, err := Build(Spec{File: "../f", Pattern: `((?{a}))`})
	if strings.Contains(err.Error(), "empty value") {
		t.Errorf("a file's refusal reads %q", err)
	}
	// Each loads: a literal reference before group 1, and a pattern
	// reference inside group 1 or after it.
	for _, s := range []Spec{
		{File: "f", Pattern: `(?{a})(x)`},
		{File: "f", Pattern: `((?{a}))`, ValuesAs: AsPattern},
		{File: "f", Pattern: `(x)(?{a})`, ValuesAs: AsPattern},
		{File: "f", Pattern: `((?{a}))`, ValuesAs: Literal},
		// A group of the name the check gives its own, which RE2 lets two
		// groups share. Planted break: the name not made unique, so the
		// check finds this group, group 1, and refuses.
		{File: "f", Pattern: `(?P<errata_reference>x)(?{a})`, ValuesAs: AsPattern},
	} {
		if _, err := Build(s); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

// errata list writes values_as out for a pattern that references values,
// and two specs that differ only by the default written out read one
// place. Planted breaks: Resolve for every pattern; Place keeping literal.
func TestResolveAndPlace(t *testing.T) {
	ref, plain := Spec{File: "f", Pattern: `((?{a}))`}, Spec{File: "f", Pattern: `(x)`}
	if got := Resolve(ref).ValuesAs; got != Literal {
		t.Errorf("Resolve gives values_as %q, want literal", got)
	}
	if got := Resolve(plain).ValuesAs; got != "" {
		t.Errorf("Resolve of a pattern with no reference gives values_as %q", got)
	}
	if got := Resolve(Spec{Pattern: ref.Pattern, ValuesAs: AsPattern}).ValuesAs; got != AsPattern {
		t.Errorf("Resolve changes values_as pattern to %q", got)
	}
	written := ref
	written.ValuesAs = Literal
	if !reflect.DeepEqual(Place(written), Place(ref)) {
		t.Errorf("Place(%+v) = %+v, want %+v", written, Place(written), Place(ref))
	}
	asPattern := ref
	asPattern.ValuesAs = AsPattern
	if reflect.DeepEqual(Place(asPattern), Place(ref)) {
		t.Errorf("a value read as a pattern and as text read one place")
	}
}

// A regex is described as reading the extractor its pattern references,
// and none for a pattern with none or one Build refuses; as: record gives
// records of the named groups, in the order the pattern opens them.
// Planted breaks: each condition of Describe negated.
func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want check.Description
	}{
		{Spec{Pattern: `((?{a}))`}, check.Description{Label: "regex", Reads: []string{"a"}}},
		{Spec{Pattern: `(x)`}, check.Description{Label: "regex"}},
		{Spec{Pattern: `((?{a})(?{b}))`}, check.Description{Label: "regex"}},
		{Spec{Pattern: `(?P<b>x)(?P<a>y)`, As: "record"}, check.Description{Label: "regex", Shape: check.ShapeRecord, Fields: []string{"b", "a"}}},
		{Spec{Pattern: `(?P<b>x)`, As: "map"}, check.Description{Label: "regex"}},
	} {
		if got := Describe(c.spec); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Describe(%+v) = %+v, want %+v", c.spec, got, c.want)
		}
	}
}

// Under values_as: pattern, ^ and $ in a value match at the start and end
// of the text, as in a JavaScript regex without the m flag, such as a
// Renovate matchString, though the pattern around the value keeps the m
// flag. Planted break: the value put in without (?-m:...), which matched
// both lines.
func TestReferenceValueAnchorsAtTheText(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"plans.yaml": "  version: v1\n  version: v2\n"})
	d := mustReference(t, Spec{File: "plans.yaml", Pattern: `((?{m}))`, ValuesAs: AsPattern})
	cases := []struct{ value, want string }{
		{`version: (?<v>\S+)$`, ""},
		{`^  version: (?<v>\S+)`, "  version: v1@1"},
		{`\s*version: (?<v>\S+)`, "  version: v1@1 \n  version: v2@1"},
	}
	for _, tc := range cases {
		vs, err := d.ExtractWith(root, map[string][]check.Value{"m": {value(tc.value, "renovate.json", 4)}})
		if err != nil || extracttest.Texts(vs) != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.value, extracttest.Texts(vs), err, tc.want)
		}
	}
	// The pattern around the value is the author's, with the m flag.
	d = mustReference(t, Spec{File: "plans.yaml", Pattern: `((?{m}))$`, ValuesAs: AsPattern})
	vs, err := d.ExtractWith(root, map[string][]check.Value{"m": {value(`version: \S+`, "renovate.json", 4)}})
	if err != nil || extracttest.Texts(vs) != "version: v1@1 version: v2@2" {
		t.Errorf("got %q, %v", extracttest.Texts(vs), err)
	}
}

// An empty value, which would let the pattern around it match alone, and a
// value past MaxReference, whose compiles would cost gigabytes, are
// refused in both modes, naming where the value was found. Planted breaks:
// each check removed in turn.
func TestReferenceRefusesEmptyAndLongValues(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "--install  x\n"})
	for _, valuesAs := range []string{"", AsPattern} {
		d := mustReference(t, Spec{File: "f", Pattern: `--install ((?{r})) `, ValuesAs: valuesAs})
		_, err := d.ExtractWith(root, map[string][]check.Value{"r": {value("", "a.sh", 2)}})
		extracttest.WantError(t, err, `regex: the value of extractor "r", at a.sh:2, is empty, and the pattern would match with nothing in the reference's place`)
		long := strings.Repeat("a", core.MaxReference+1)
		_, err = d.ExtractWith(root, map[string][]check.Value{"r": {value(long, "a.sh", 3)}})
		extracttest.WantError(t, err, `regex: the value of extractor "r", at a.sh:3, holds 4097 bytes, over the limit of 4096 for a value a pattern references`)
		if _, err := d.ExtractWith(root, map[string][]check.Value{"r": {value(long[1:], "a.sh", 3)}}); err != nil {
			t.Errorf("a value at the limit: %v", err)
		}
	}
}
