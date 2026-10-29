package check

import (
	"slices"
	"strings"
	"testing"
)

// reads is an extractor that reads the values of the extractors it names.
type reads []string

func (reads) Extract(string) ([]Value, error) { return nil, nil }
func (r reads) Needs() []string               { return r }
func (reads) ExtractWith(string, map[string][]Value) ([]Value, error) {
	return nil, nil
}

func ne(name string, x Extractor) NamedExtractor { return NamedExtractor{Name: name, Extractor: x} }

// ruleOf is an assert rule whose extractors, in name order, are given as
// name and extractor pairs.
func ruleOf(extractors ...NamedExtractor) Check {
	return Check{ID: "r", Reason: "r", Scope: ScopeRepo, Kind: &Rule{Extractors: extractors, Assert: "true"}}
}

// Each case plants one break in the dependencies of a rule that loads, and
// names the refusal: an extractor read that extract does not declare, and
// a cycle of one, two or three extractors.
func TestValidateDependencies(t *testing.T) {
	cases := []struct {
		name       string
		extractors []NamedExtractor
		wantErr    string
	}{
		{"one reads another", []NamedExtractor{ne("a", noValues{}), ne("b", reads{"a"})}, ""},
		{"a diamond is no cycle", []NamedExtractor{
			ne("a", reads{"b", "c"}), ne("b", reads{"c"}), ne("c", noValues{}), ne("d", reads{"a", "c"}),
		}, ""},
		{"an extractor that did not build reads nothing", []NamedExtractor{ne("a", nil), ne("b", reads{"a"})}, ""},
		{"reads an undeclared extractor", []NamedExtractor{ne("a", noValues{}), ne("b", reads{"charts"})},
			`extractor "b" reads the values of "charts", which extract does not declare`},
		{"reads itself", []NamedExtractor{ne("a", reads{"a"})},
			`extractors read each other's values in a cycle, so none of them can run first: "a" reads "a"`},
		{"two read each other", []NamedExtractor{ne("a", reads{"b"}), ne("b", reads{"a"})},
			`cycle, so none of them can run first: "a" reads "b" reads "a"`},
		{"a cycle below an extractor on none", []NamedExtractor{
			ne("a", reads{"b"}), ne("b", reads{"c"}), ne("c", reads{"d"}), ne("d", reads{"b"}),
		}, `cycle, so none of them can run first: "b" reads "c" reads "d" reads "b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ruleOf(tc.extractors...).Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate: %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// A cycle is named once, though every extractor on it starts a search.
func TestValidateNamesACycleOnce(t *testing.T) {
	err := ruleOf(ne("a", reads{"b"}), ne("b", reads{"a"})).Validate()
	if n := strings.Count(err.Error(), "cycle"); n != 1 {
		t.Fatalf("Validate: %v, names a cycle %d times, want 1", err, n)
	}
}

func TestOrder(t *testing.T) {
	r := ruleOf(
		ne("a", reads{"c"}), ne("b", noValues{}),
		ne("c", reads{"d"}), ne("d", noValues{}),
	).Rule()
	cases := []struct {
		name  string
		names []string
		want  []string
	}{
		{"what an extractor reads runs first", []string{"a", "b", "c", "d"}, []string{"d", "c", "a", "b"}},
		{"names keep their order otherwise", []string{"d", "b", "c", "a"}, []string{"d", "b", "c", "a"}},
		{"only the names given", []string{"b", "a"}, []string{"b", "a"}},
		{"a name twice runs once", []string{"b", "b"}, []string{"b"}},
		{"a name of no extractor keeps its place", []string{"x", "a", "c"}, []string{"x", "c", "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.Order(tc.names); !slices.Equal(got, tc.want) {
				t.Fatalf("Order(%q) = %q, want %q", tc.names, got, tc.want)
			}
		})
	}
}

// A cycle, which validation refuses, still ends, with each name once.
func TestOrderEndsOnACycle(t *testing.T) {
	r := ruleOf(ne("a", reads{"b"}), ne("b", reads{"a"})).Rule()
	if got := r.Order([]string{"a", "b"}); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("Order = %q, want [b a]", got)
	}
}
