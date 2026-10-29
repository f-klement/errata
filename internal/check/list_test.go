package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// brackets is a Quoter that marks what it wrote, so a test sees which
// values went through it.
type brackets struct{}

func (brackets) Value(s string) string { return "[" + s + "]" }
func (brackets) Spec(spec any) string  { return fmt.Sprintf("{%v}", spec) }

// fieldsJSON is f as errata list's encoder writes it.
func fieldsJSON(t *testing.T, f Fields) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The list methods of each kind, through Kind: the JSON keys in order, with
// [] for a list the kind sets none of, and the text lines with each value
// from the config written by the quoter. A rule's relation is agree with its
// compare, or assert, or {} when it has neither. The planted break:
// `len(r.Agree) > 0` flipped in ListFields, which wrote the assert shape for
// an agree rule.
func TestKindsList(t *testing.T) {
	c := Check{Timeout: 90 * time.Second}
	cases := []struct {
		name  string
		kind  Kind
		json  string
		lines []Line
	}{
		{"agree rule with every list",
			&Rule{
				Extractors: []NamedExtractor{{Name: "x", Spec: "sx"}},
				Agree:      []string{"x", "y"}, Compare: CompareSemver,
				Normalize: []Normalization{NormalizeTrim, NormalizeLower}, AllowEmpty: []string{"x"}, SkipNotLiteral: []string{"y"},
			},
			`{"extract":{"x":"sx"},"relation":{"agree":["x","y"],"compare":"semver"},"normalize":["trim","lower"],"allow_empty":["x"],"skip_not_literal":["y"]}`,
			[]Line{{"extract:", "[x]: {sx}"}, {"agree:", "[x], [y] (compare semver)"}, {"normalize:", "[trim], [lower]"}, {"allow_empty:", "[x]"}, {"skip_not_literal:", "[y]"}}},
		{"agree rule that names no compare",
			&Rule{Agree: []string{"x"}},
			`{"extract":{},"relation":{"agree":["x"],"compare":"text"},"normalize":[],"allow_empty":[],"skip_not_literal":[]}`,
			[]Line{{"extract:", ""}, {"agree:", "[x] (compare text)"}}},
		{"assert rule with no list",
			&Rule{Extractors: []NamedExtractor{{Name: "a", Spec: 1}, {Name: "b", Spec: 2}}, Assert: "a < b"},
			`{"extract":{"a":1,"b":2},"relation":{"assert":"a < b"},"normalize":[],"allow_empty":[],"skip_not_literal":[]}`,
			[]Line{{"extract:", "[a]: {1}\n[b]: {2}"}, {"assert:", "a < b"}}},
		{"rule with neither relation",
			&Rule{},
			`{"extract":{},"relation":{},"normalize":[],"allow_empty":[],"skip_not_literal":[]}`,
			[]Line{{"extract:", ""}, {"assert:", ""}}},
		{"program check",
			&Program{Run: []string{"sh", "a b"}},
			`{"run":["sh","a b"],"timeout":"1m30s"}`,
			[]Line{{"run:", "[sh] [a b]"}, {"timeout:", "1m30s"}}},
		{"program check with no run",
			&Program{},
			`{"run":[],"timeout":"1m30s"}`,
			[]Line{{"run:", ""}, {"timeout:", "1m30s"}}},
		{"generated check",
			&Generated{File: "a.md", Run: []string{"gen"}, MaxBytes: 4096},
			`{"generated":{"file":"a.md","run":["gen"],"max_bytes":4096},"timeout":"1m30s"}`,
			[]Line{{"file:", "[a.md]"}, {"run:", "[gen]"}, {"max_bytes:", "4096"}, {"timeout:", "1m30s"}}},
		{"generated check with no generator",
			&Generated{File: "a.md"},
			`{"generated":{"file":"a.md","run":[],"max_bytes":0},"timeout":"1m30s"}`,
			[]Line{{"file:", "[a.md]"}, {"run:", ""}, {"max_bytes:", "0"}, {"timeout:", "1m30s"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c.Kind = tc.kind
			if got := fieldsJSON(t, tc.kind.ListFields(c)); got != tc.json+"\n" {
				t.Errorf("ListFields = %s, want %s", got, tc.json)
			}
			if got := tc.kind.ListLines(c, brackets{}); !reflect.DeepEqual(got, tc.lines) {
				t.Errorf("ListLines = %q, want %q", got, tc.lines)
			}
		})
	}
}

// Each kind names itself for errata list. The planted break: a kind
// answering with another's name.
func TestKindsNameThemselves(t *testing.T) {
	for _, tc := range []struct {
		kind            Kind
		name, one, many string
	}{
		{&Program{}, "program", "program check", "program checks"},
		{&Rule{}, "rule", "rule", "rules"},
		{&Generated{}, "generated", "generated check", "generated checks"},
	} {
		one, many := tc.kind.Nouns()
		if tc.kind.Name() != tc.name || one != tc.one || many != tc.many {
			t.Errorf("%T: %q, %q, %q, want %q, %q, %q", tc.kind, tc.kind.Name(), one, many, tc.name, tc.one, tc.many)
		}
	}
}

// OrEmpty gives an empty list for nil, so the JSON holds [] and not null,
// and any other list as it is.
func TestOrEmpty(t *testing.T) {
	if got := OrEmpty(nil); got == nil || len(got) != 0 {
		t.Errorf("OrEmpty(nil) = %#v, want an empty list", got)
	}
	in := []string{"a"}
	if got := OrEmpty(in); &got[0] != &in[0] {
		t.Errorf("OrEmpty(%q) = %q, want the same list", in, got)
	}
}
