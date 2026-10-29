package report

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/check"
)

// listChecks are one check of each kind: a program check, an agree rule, an
// assert rule and a generated check.
func listChecks() []check.Check {
	spec := map[string]any{"regex": map[string]any{"file": "f", "pattern": "(.)", "max_bytes": 1}}
	return []check.Check{
		{ID: "p", Reason: "r", Scope: check.ScopeRepo, Run: []string{"true"}, Timeout: 90 * time.Second, At: check.Location{File: "errata.yaml", Line: 2}},
		{ID: "a", Reason: "r", Scope: check.ScopeRepo, Rule: &check.Rule{
			Extractors: []check.NamedExtractor{{Name: "x", Spec: spec}, {Name: "y", Spec: spec}},
			Agree:      []string{"y", "x"}, AllowEmpty: []string{"x"}, Normalize: []check.Normalization{check.NormalizeTrim},
		}},
		{ID: "s", Reason: "r", Scope: check.ScopeRepo, Rule: &check.Rule{
			Extractors: []check.NamedExtractor{{Name: "x", Spec: spec}}, Assert: "size(x) > 0",
		}},
		generatedCheck(),
	}
}

// listDocument is ListJSON's output for checks, decoded.
func listDocument(t *testing.T, checks []check.Check) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := (ListJSON{}).WriteList(&buf, checks, nil); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("%v in %s", err, buf.String())
	}
	return doc
}

// A rule that compares as semver lists as semver, in JSON and in text, and
// one that names no compare as text. The planted break: toListCheck printing
// "text" for every agree rule, as before #89, fails the first check.
func TestListCompare(t *testing.T) {
	checks := listChecks()
	checks[1].Rule.Compare = check.CompareSemver
	rel := listedCheck(t, checks, 1)["relation"].(map[string]any)
	if rel["compare"] != "semver" {
		t.Errorf("a semver rule lists compare %v, want semver", rel["compare"])
	}
	var buf bytes.Buffer
	if err := (ListText{}).WriteList(&buf, checks, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "y, x (compare semver)") {
		t.Errorf("the text listing does not say compare semver:\n%s", buf.String())
	}
	checks[1].Rule.Compare = ""
	if rel := listedCheck(t, checks, 1)["relation"].(map[string]any); rel["compare"] != "text" {
		t.Errorf("a rule with no compare lists compare %v, want text", rel["compare"])
	}
}

// The JSON holds each kind of check with every key its type has, the lists
// of a rule that sets none of them as [] rather than null or left out, and
// the relation as agree with compare or as assert.
func TestListJSON(t *testing.T) {
	doc := listDocument(t, listChecks())
	if doc["version"] != float64(ListVersion) {
		t.Errorf("version %v, want %d", doc["version"], ListVersion)
	}
	checks := doc["checks"].([]any)
	program, agree, assert := checks[0].(map[string]any), checks[1].(map[string]any), checks[2].(map[string]any)
	if program["type"] != "program" || program["timeout"] != "1m30s" || program["extract"] != nil || program["relation"] != nil {
		t.Errorf("program check: %v", program)
	}
	at := program["at"].(map[string]any)
	if at["file"] != "errata.yaml" || at["line"] != float64(2) || at["outside"] != false {
		t.Errorf("program check at %v", at)
	}
	if agree["type"] != "rule" || agree["run"] != nil || agree["timeout"] != nil {
		t.Errorf("agree rule: %v", agree)
	}
	rel := agree["relation"].(map[string]any)
	if !slices.Equal(rel["agree"].([]any), []any{"y", "x"}) || rel["compare"] != string(check.CompareText) || len(rel) != 2 {
		t.Errorf("agree relation %v", rel)
	}
	rel = assert["relation"].(map[string]any)
	if rel["assert"] != "size(x) > 0" || len(rel) != 1 {
		t.Errorf("assert relation %v", rel)
	}
	for _, key := range []string{"normalize", "allow_empty", "skip_not_literal"} {
		if l, ok := assert[key].([]any); !ok || len(l) != 0 {
			t.Errorf("assert rule %s is %#v, want []", key, assert[key])
		}
	}
	if !slices.Equal(agree["allow_empty"].([]any), []any{"x"}) || !slices.Equal(agree["normalize"].([]any), []any{"trim"}) {
		t.Errorf("agree rule lists: %v", agree)
	}
	x := agree["extract"].(map[string]any)["x"].(map[string]any)["regex"].(map[string]any)
	if x["max_bytes"] != float64(1) || x["file"] != "f" {
		t.Errorf("extractor x: %v", x)
	}
}

// The text listing prints each check under its labels, a multi-line value
// on lines of its own under the first, a rule's relation and the lists it
// sets and none it leaves empty, and the count with each noun in the number
// it takes.
func TestListText(t *testing.T) {
	checks := listChecks()
	checks[0].Reason = "first line\nsecond line"
	var b bytes.Buffer
	if err := (ListText{}).WriteList(&b, checks, nil); err != nil {
		t.Fatal(err)
	}
	want := `p (repo) program check
  why: first line
       second line
  run: true
  timeout: 1m30s

a (repo) rule
  why: r
  extract: x: regex file f, max_bytes 1, pattern (.)
           y: regex file f, max_bytes 1, pattern (.)
  agree: y, x (compare text)
  normalize: trim
  allow_empty: x

s (repo) rule
  why: r
  extract: x: regex file f, max_bytes 1, pattern (.)
  assert: size(x) > 0

g (repo) generated check
  why: r
  file: architecture.md
  run: python3 ` + "`scripts/build architecture.py`" + ` --stdout
  max_bytes: 1048576
  timeout: 30s

4 checks: 2 rules, 1 program check, 1 generated check.
`
	if b.String() != want {
		t.Errorf("text listing:\n%s\nwant:\n%s", b.String(), want)
	}

	b.Reset()
	one := checks[2]
	one.Rule.Extractors = []check.NamedExtractor{{Name: "x", Spec: make(chan int)}}
	if err := (ListText{}).WriteList(&b, []check.Check{one}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "  extract: x: (json: unsupported type: chan int)\n") || !strings.HasSuffix(b.String(), "\n1 check: 1 rule, 0 program checks.\n") {
		t.Errorf("text listing of a spec that does not marshal:\n%s", b.String())
	}
}

// A name, an argument and a string of an extractor are free text, so the
// text listing writes each so that it reads as one value: as it is when it
// holds nothing that separates values, between backquotes as written when it
// can, quoted with escapes otherwise. A name holding a newline and a fake
// spec, or a comma, would otherwise read as more extractors than the rule
// has (found in review: a rule of two read as four, one of them reading a
// file neither reads). The planted break: textValue returning s as it is,
// which prints the fake spec on a line of its own.
func TestListTextQuotesWhatCouldReadAsTwo(t *testing.T) {
	fake := "app json file package.json, path version\nchart"
	spec := map[string]any{"regex": map[string]any{"file": "r.txt", "pattern": `v(\d+)`}}
	c := check.Check{ID: "a", Reason: "r", Scope: check.ScopeRepo, Rule: &check.Rule{
		Extractors: []check.NamedExtractor{{Name: fake, Spec: spec}, {Name: "b, c", Spec: spec}},
		Agree:      []string{fake, "b, c"}, AllowEmpty: []string{"b, c"}, SkipNotLiteral: []string{"a`b"},
	}}
	p := check.Check{ID: "p", Reason: "r", Scope: check.ScopeRepo, Run: []string{"sh", "-c", `echo "a b"`, "", "x\ty"}}
	var b bytes.Buffer
	if err := (ListText{}).WriteList(&b, []check.Check{c, p}, nil); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"  extract: \"app json file package.json, path version\\nchart\": regex file r.txt, pattern v(\\d+)\n",
		"           `b, c`: regex file r.txt, pattern v(\\d+)\n",
		"  agree: \"app json file package.json, path version\\nchart\", `b, c` (compare text)\n",
		"  allow_empty: `b, c`\n",
		"  skip_not_literal: \"a`b\"\n",
		"  run: sh -c `echo \"a b\"` \"\" \"x\\ty\"\n",
	} {
		if !strings.Contains(b.String(), line) {
			t.Errorf("the listing has no line %q:\n%s", line, b.String())
		}
	}
	// A rune strconv.IsPrint does not name, which QuoteControls would
	// rewrite inside backquotes, makes the value quoted, so a real U+202E
	// or U+2028 never reads as the six characters that spell its escape.
	// The planted break: backquoting whatever strconv.CanBackquote accepts.
	for name, want := range map[string]string{
		"a\u202eb":  `"a\u202eb"`,
		`a\u202eb`:  `a\u202eb`,
		"a\u2028b":  `"a\u2028b"`,
		"caf\u00e9": "`caf\u00e9`",
		"a\x7fb":    `"a\x7fb"`,
	} {
		if got := textValue(name); got != want {
			t.Errorf("textValue(%q) = %s, want %s", name, got, want)
		}
	}
	if n := strings.Count(b.String(), "\n           "); n != 1 {
		t.Errorf("the listing has %d continuation lines of extract, want 1:\n%s", n, b.String())
	}
}

// specText writes a map further in between braces, a bool and a number as
// they are, and a list, which no spec holds yet, as its values in brackets.
func TestSpecText(t *testing.T) {
	spec := map[string]any{"xml": map[string]any{
		"file": "pom.xml", "namespaces": map[string]any{"m": "http://x/", "p": "a b"},
		"code": true, "max_bytes": 7, "list": []any{1, []any{2}, "a b", map[string]any{"k": "v"}}, "none": nil,
	}}
	want := "xml code true, file pom.xml, list [1, [2], `a b`, {k v}], max_bytes 7, namespaces {m http://x/, p `a b`}, none null"
	if got := specText(spec); got != want {
		t.Errorf("specText = %q, want %q", got, want)
	}
	if got := specText(map[string]any{"a": map[string]any{}, "b": map[string]any{"k": "v"}}); got != "a ; b k v" {
		t.Errorf("specText of two kinds = %q", got)
	}
}

// listedFields places every field of the types a loaded check is made of:
// the key of the JSON that prints it; "fields" for one whose own type is
// placed here field by field; or, for a field list leaves out, why. A field
// added to one of these types fails TestListPlacesEveryField until it is
// placed here, and one placed under a key fails until its value reaches
// that key. So list cannot go on printing a check the old way while check
// runs it a new way: compare on a rule (#89) was such a field, and is
// printed from the rule rather than as a constant.
var listedFields = map[reflect.Type]map[string]string{
	reflect.TypeFor[check.Check](): {
		"ID": "id", "Reason": "reason", "Scope": "scope", "Run": "run", "Timeout": "timeout",
		"At": "fields", "Rule": "fields", "Generated": "fields",
	},
	reflect.TypeFor[check.Generated](): {"File": "generated.file", "Run": "generated.run", "MaxBytes": "generated.max_bytes"},
	reflect.TypeFor[check.Location]():  {"File": "at.file", "Line": "at.line", "Outside": "at.outside"},
	reflect.TypeFor[check.Rule](): {
		"Extractors": "fields", "Agree": "relation.agree", "Compare": "relation.compare", "Assert": "relation.assert",
		"AllowEmpty": "allow_empty", "SkipNotLiteral": "skip_not_literal", "Normalize": "normalize",
		"Compiled": "left out: compiled from Assert, which is printed",
	},
	reflect.TypeFor[check.NamedExtractor](): {
		"Name": "extract", "Spec": "extract",
		"Kind":      "left out: the kind as a message names it, made from Spec",
		"Extractor": "left out: built from Spec",
		"Maps":      "left out: made from Spec, whose as: map is printed",
		"Fields":    "left out: made from Spec, whose as: record and fields, or regex pattern, are printed",
	},
}

// fieldOwner is, for a type of listedFields, the value of that type inside
// c, a check of listChecks: c itself, its place, its rule, or its rule's
// first extractor.
func fieldOwner(typ reflect.Type, c *check.Check) reflect.Value {
	switch typ {
	case reflect.TypeFor[check.Location]():
		return reflect.ValueOf(&c.At).Elem()
	case reflect.TypeFor[check.Rule]():
		return reflect.ValueOf(c.Rule).Elem()
	case reflect.TypeFor[check.NamedExtractor]():
		return reflect.ValueOf(&c.Rule.Extractors[0]).Elem()
	case reflect.TypeFor[check.Generated]():
		return reflect.ValueOf(c.Generated).Elem()
	default:
		return reflect.ValueOf(c).Elem()
	}
}

// setDistinct sets v to a value that no check of listChecks holds.
func setDistinct(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-distinct")
	case reflect.Int, reflect.Int64:
		v.SetInt(v.Int() + 4242)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		setDistinct(t, elem)
		v.Set(reflect.Append(v, elem))
	case reflect.Interface:
		v.Set(reflect.ValueOf(map[string]any{"distinct": map[string]any{"file": "g"}}))
	default:
		t.Fatalf("no distinct value for a field of kind %s", v.Kind())
	}
}

// at is the value at path, such as at.file, in a check of the JSON.
func at(c map[string]any, path string) any {
	var v any = c
	for _, k := range strings.Split(path, ".") {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	return v
}

// listedCheck is checks listed as JSON, decoded, the one at i.
func listedCheck(t *testing.T, checks []check.Check, i int) map[string]any {
	t.Helper()
	doc := listDocument(t, checks)
	return doc["checks"].([]any)[i].(map[string]any)
}

// Every field of a check, its rule, its place and its extractors is printed
// or left out on purpose, and each one printed reaches the key the table
// names: given a distinct value, the JSON at that key changes. The planted
// breaks: a field Compare added to check.Rule, which fails until the table
// places it; and placed as relation.compare without toListCheck reading it,
// which fails because the JSON there stays "text".
func TestListPlacesEveryField(t *testing.T) {
	for typ, fields := range listedFields {
		var names []string
		for f := range typ.Fields() {
			names = append(names, f.Name)
			placed, ok := fields[f.Name]
			switch {
			case !ok:
				t.Errorf("%s.%s is neither printed by errata list nor left out on purpose: place it in listedFields", typ, f.Name)
				continue
			case placed == "fields":
				elem := f.Type
				if k := elem.Kind(); k == reflect.Pointer || k == reflect.Slice {
					elem = elem.Elem()
				}
				if listedFields[elem] == nil {
					t.Errorf("%s.%s is placed field by field, but listedFields does not place %s", typ, f.Name, f.Type)
				}
				continue
			case strings.HasPrefix(placed, "left out: "):
				continue
			}
			// The program check holds the check's own fields, the assert
			// rule its assert, the generated check its generated key, and
			// the agree rule every other field of a rule.
			i := 1
			switch {
			case typ == reflect.TypeFor[check.Check]() || typ == reflect.TypeFor[check.Location]():
				i = 0
			case f.Name == "Assert":
				i = 2
			case typ == reflect.TypeFor[check.Generated]():
				i = 3
			}
			checks := listChecks()
			before := at(listedCheck(t, checks, i), placed)
			setDistinct(t, fieldOwner(typ, &checks[i]).FieldByName(f.Name))
			if after := at(listedCheck(t, checks, i), placed); reflect.DeepEqual(before, after) {
				t.Errorf("%s.%s is placed under %q, but a distinct value leaves the JSON there %v", typ, f.Name, placed, after)
			}
		}
		for name := range fields {
			if !slices.Contains(names, name) {
				t.Errorf("listedFields places %s.%s, which the type does not have", typ, name)
			}
		}
	}
}
