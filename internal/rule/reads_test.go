package rule

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// reader is an extractor that reads the values of the extractors it needs,
// records what it was given and in which order the extractors ran, and
// returns fixed values.
type reader struct {
	needs  []string
	values []check.Value
	got    *map[string][]check.Value
	ran    *[]string
	name   string
}

func (reader) Extract(string) ([]check.Value, error) {
	return nil, errors.New("Extract called on an extractor that reads values")
}

func (r reader) ExtractWith(_ string, values map[string][]check.Value) ([]check.Value, error) {
	*r.got = values
	*r.ran = append(*r.ran, r.name)
	return r.values, nil
}

// reading is the extractor name, r, described as reading what r needs.
func reading(name string, r reader) check.NamedExtractor {
	return check.NamedExtractor{Name: name, Extractor: r, Description: check.Description{Reads: r.needs}}
}

// ranFake is fake that records that it ran.
type ranFake struct {
	fake
	ran  *[]string
	name string
}

func (f ranFake) Extract(root string) ([]check.Value, error) {
	*f.ran = append(*f.ran, f.name)
	return f.fake.Extract(root)
}

func (f ranFake) ExtractWith(root string, _ map[string][]check.Value) ([]check.Value, error) {
	return f.Extract(root)
}

func compiledRule(t *testing.T, expr string, extractors ...check.NamedExtractor) check.Check {
	t.Helper()
	a, err := Compile(expr, extractors)
	if err != nil {
		t.Fatal(err)
	}
	return check.Check{ID: "r", Reason: "r", Scope: check.ScopeRepo,
		Kind: &check.Rule{Extractors: extractors, Assert: expr, Compiled: a}}
}

// An extractor that reads another's values runs after it, though its name
// comes first, and is given the values the rule compares: the literal ones,
// without what skip_not_literal leaves out.
func TestReaderRunsAfterWhatItReads(t *testing.T) {
	var got map[string][]check.Value
	var ran []string
	names := at("argo-cd", "charts.yaml", 3)
	computed := at("$(chart)", "charts.yaml", 4)
	computed.NotLiteral = true
	c := compiledRule(t, "a == ['argo-cd'] && z == ['argo-cd']",
		reading("a", reader{
			needs: []string{"z"}, values: []check.Value{at("argo-cd", "renovate.json", 9)}, got: &got, ran: &ran, name: "a",
		}),
		check.NamedExtractor{Name: "z", Extractor: ranFake{fake{values: []check.Value{names, computed}}, &ran, "z"}},
	)
	c.Rule().SkipNotLiteral = []string{"z"}
	res := Run(context.Background(), "/repo", c)
	if res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s: %s", res.Verdict, res.Message)
	}
	if !slices.Equal(ran, []string{"z", "a"}) {
		t.Fatalf("ran %q, want z before a", ran)
	}
	if len(got) != 1 || !slices.Equal(got["z"], []check.Value{names}) {
		t.Fatalf("a was given %v, want z's literal value alone", got)
	}
	if res.Extracted[0].Extractor != "z" || res.Extracted[1].Extractor != "a" {
		t.Fatalf("extracted %v, want z then a, in the order they ran", res.Extracted)
	}
}

// A not literal value that skip_not_literal does not leave out makes the
// rule unknown, and the extractor that reads its values is not given it.
func TestReaderIsNotGivenNotLiteralValues(t *testing.T) {
	var got map[string][]check.Value
	var ran []string
	computed := at("$(chart)", "charts.yaml", 4)
	computed.NotLiteral = true
	c := compiledRule(t, "size(a) + size(z) > 0",
		reading("a", reader{needs: []string{"z"}, values: []check.Value{at("x", "f", 1)}, got: &got, ran: &ran}),
		check.NamedExtractor{Name: "z", Extractor: fake{values: []check.Value{at("argo-cd", "charts.yaml", 3), computed}}},
	)
	res := Run(context.Background(), "/repo", c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, `extractor "z" returned 1 not literal value`) {
		t.Fatalf("verdict %s: %s, want unknown for z's not literal value", res.Verdict, res.Message)
	}
	if !slices.Equal(got["z"], []check.Value{at("argo-cd", "charts.yaml", 3)}) {
		t.Fatalf("a was given %v, want z's literal value alone", got)
	}
}

// An extractor whose values another reads fails, so that one cannot run:
// the rule is unknown, naming both.
func TestReaderOfAFailedExtractor(t *testing.T) {
	var got map[string][]check.Value
	var ran []string
	c := compiledRule(t, "size(a) + size(z) > 0",
		reading("a", reader{needs: []string{"z"}, got: &got, ran: &ran}),
		check.NamedExtractor{Name: "z", Extractor: fake{err: errors.New("charts.yaml: no such file or directory")}},
	)
	res := Run(context.Background(), "/repo", c)
	want := "extractor \"z\": charts.yaml: no such file or directory\n" +
		"extractor \"a\": it reads the values of extractor \"z\", which could not be read"
	if res.Verdict != check.VerdictUnknown || res.Message != want {
		t.Fatalf("verdict %s message %q, want unknown with %q", res.Verdict, res.Message, want)
	}
	if ran != nil {
		t.Fatalf("a ran: %q", ran)
	}
}

// An agree rule runs its extractors in dependency order too.
func TestAgreeRunsWhatIsReadFirst(t *testing.T) {
	var got map[string][]check.Value
	var ran []string
	c := agreeRule(
		reading("b", reader{needs: []string{"c"}, values: []check.Value{at("1", "b", 1)}, got: &got, ran: &ran, name: "b"}),
		check.NamedExtractor{Name: "c", Extractor: ranFake{fake{values: []check.Value{at("1", "c", 1)}}, &ran, "c"}},
	)
	res := Run(context.Background(), "/repo", c)
	if res.Verdict != check.VerdictTrue || !slices.Equal(ran, []string{"c", "b"}) {
		t.Fatalf("verdict %s, ran %q: %s", res.Verdict, ran, res.Message)
	}
}

// An extractor whose values another reads need not be named in the assert,
// since what reads it shapes the verdict; any other unnamed one is still
// refused, and so is one only an extractor that did not build reads, as
// before the description said what it reads. Planted breaks: read ignored;
// the reads of one that did not build counted.
func TestCompileLetsAReadExtractorGoUnnamed(t *testing.T) {
	tracked := check.NamedExtractor{Name: "tracked", Extractor: fake{}, Description: check.Description{Reads: []string{"releases"}}}
	releases := check.NamedExtractor{Name: "releases", Extractor: fake{}}
	if _, err := Compile("size(tracked) > 0", []check.NamedExtractor{releases, tracked}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	other := check.NamedExtractor{Name: "other", Extractor: fake{}}
	_, err := Compile("size(tracked) > 0", []check.NamedExtractor{releases, tracked, other})
	if err == nil || err.Error() != `extractor "other" is declared but assert does not use it` {
		t.Fatalf("Compile: %v, want other refused alone", err)
	}
	tracked.Extractor = nil
	_, err = Compile("size(tracked) > 0", []check.NamedExtractor{releases, tracked})
	if err == nil || err.Error() != `extractor "releases" is declared but assert does not use it` {
		t.Fatalf("Compile: %v, want releases refused", err)
	}
}
