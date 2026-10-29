package rule

import (
	"context"
	"strconv"
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// blamed is the value res blames, as file:line, or "none".
func blamed(t *testing.T, res check.Result) string {
	t.Helper()
	b := res.Blamed
	if b == nil {
		return "none"
	}
	if b.Extractor < 0 || b.Extractor >= len(res.Extracted) || b.Value < 0 || b.Value >= len(res.Extracted[b.Extractor].Values) {
		t.Fatalf("blamed %+v is outside the result's values %+v", *b, res.Extracted)
	}
	return res.Extracted[b.Extractor].Values[b.Value].At.String()
}

// Under compare: semver an agree rule blames the value semver.Agree found
// not to agree with the values before it, never the value outside a
// majority of texts: over 20, 20.11.1 and 18 that is 18, where the texts
// are three of one value each and the first value outside the largest
// group would be 20.11.1, which agrees. A version that does not satisfy a
// range before it is blamed, and so is a range a version before it does
// not satisfy. Ranges alone that do not overlap blame none, since no pair
// is to blame. Planted break: the pair's first value blamed, which named
// 20.11.1 (#208).
func TestAgreeSemverBlamesTheValueItIsFalseFor(t *testing.T) {
	for _, c := range []struct {
		name   string
		values []string
		want   string
	}{
		{"versions", []string{"20", "20.11.1", "18"}, "v.txt:3"},
		{"a version under a range", []string{">=20", "20.1.0", "18.19.0"}, "v.txt:3"},
		{"a range a version misses", []string{"18.19.0", ">=20"}, "v.txt:2"},
		{"ranges apart", []string{">=21", "~> 20.1"}, "none"},
		{"holds", []string{"20", "20.11.1"}, "none"},
		{"unknown", []string{"20", "latest"}, "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var extractors []check.NamedExtractor
			for i, v := range c.values {
				extractors = append(extractors, named("e"+strconv.Itoa(i), fake{values: []check.Value{at(v, "v.txt", i+1)}}))
			}
			res := Run(context.Background(), "", semverRule(extractors...))
			if got := blamed(t, res); got != c.want {
				t.Fatalf("verdict %s blames %s, want %s:\n%s", res.Verdict, got, c.want, res.Message)
			}
		})
	}
}

// A value semver blames that was found at several places is blamed at
// the first of them, in the order the rule ran its extractors.
func TestAgreeSemverBlamesAValueAtItsFirstPlace(t *testing.T) {
	res := Run(context.Background(), "", semverRule(
		named("a", fake{values: []check.Value{at(">=20", "a.txt", 1)}}),
		named("b", fake{values: []check.Value{at("18.19.0", "b.txt", 1), at("18.19.0", "b.txt", 2)}}),
	))
	if got := blamed(t, res); got != "b.txt:1" {
		t.Fatalf("blames %s, want b.txt:1", got)
	}
}

// Under text an agree rule blames the first value outside the majority:
// the largest group of values with one normalised text, the earliest group
// on a tie. A rule that holds blames none. These cases were SARIF's while
// it chose the value itself. Planted breaks: the last group counted taken
// on a tie, which blamed the first y of x, y, x, y, z; the raw text
// grouped, which split A from a under normalize.
func TestAgreeBlamesTheFirstValueOutsideTheMajority(t *testing.T) {
	for _, c := range []struct {
		name      string
		values    []string
		normalize []check.Normalization
		want      string
	}{
		{"minority last", []string{"a", "a", "b"}, nil, "f.yaml:3"},
		{"minority first", []string{"b", "a", "a"}, nil, "f.yaml:1"},
		{"tie goes to the first group", []string{"a", "b"}, nil, "f.yaml:2"},
		{"tie goes to the group seen first, not the last one counted", []string{"x", "y", "x", "y", "z"}, nil, "f.yaml:2"},
		{"a later larger group wins", []string{"a", "b", "b"}, nil, "f.yaml:1"},
		{"normalized before grouping", []string{"b", "A", " a "}, []check.Normalization{check.NormalizeTrim, check.NormalizeLower}, "f.yaml:1"},
		{"raw text when no normalize", []string{"A", "b", "b", " a "}, nil, "f.yaml:1"},
		{"all agree", []string{"x", "x"}, nil, "none"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var extractors []check.NamedExtractor
			for i, v := range c.values {
				extractors = append(extractors, named("e"+strconv.Itoa(i), fake{values: []check.Value{at(v, "f.yaml", i+1)}}))
			}
			r := agreeRule(extractors...)
			r.Rule().Normalize = c.normalize
			res := Run(context.Background(), "", r)
			if got := blamed(t, res); got != c.want {
				t.Fatalf("verdict %s blames %s, want %s", res.Verdict, got, c.want)
			}
		})
	}
}

// agree over name.field blames the field of the record that differs, at
// the field's own line, as an index into the entry name.field of the
// result. Planted break: the field's place taken from its record, which
// blamed renovate.json:7.
func TestAgreeOnAFieldBlamesTheField(t *testing.T) {
	c := agreeRule(recordsOf(rec(3, "a", "external"), rec(7, "b", "internal"), rec(11, "c", "external")))
	c.Rule().Agree = []string{"gatus.group"}
	res := Run(context.Background(), "", c)
	if got := blamed(t, res); got != "renovate.json:8" || res.Extracted[res.Blamed.Extractor].Extractor != "gatus.group" {
		t.Fatalf("verdict %s blames %s in %+v, want renovate.json:8 under gatus.group", res.Verdict, got, res.Blamed)
	}
}

// A false assert has no majority, so it blames the first value of the
// first extractor that returned one; a true one blames none.
func TestAssertBlamesTheFirstValue(t *testing.T) {
	empty := named("empty", fake{})
	x := named("x", fake{values: []check.Value{at("1", "x.yaml", 3), at("1", "x.yaml", 5)}})
	c := assertRule(t, "size(empty) == 0 && size(x) == 3", empty, x)
	c.Rule().AllowEmpty = []string{"empty"}
	res := Run(context.Background(), "", c)
	if got := blamed(t, res); res.Verdict != check.VerdictFalse || got != "x.yaml:3" {
		t.Fatalf("verdict %s blames %s, want false at x.yaml:3: %s", res.Verdict, got, res.Message)
	}
	c = assertRule(t, "size(empty) == 0 && size(x) == 2", empty, x)
	c.Rule().AllowEmpty = []string{"empty"}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue || res.Blamed != nil {
		t.Fatalf("verdict %s blames %+v", res.Verdict, res.Blamed)
	}
}

// Each value an extractor handed over whole is shown in the result as the
// rule's message shows it, after the rule's normalize; a flat value has
// no shown form, since a report quotes its text. Planted break: the
// mapping shown before normalize, which kept " X ".
func TestAResultShowsAWholeValueAfterNormalize(t *testing.T) {
	m := mapsOf("m", obj(3, "a", str(" X ", 3)))
	flat := named("flat", fake{values: []check.Value{at("v", "f.yaml", 1)}})
	c := assertRule(t, "size(m) == 2 && size(flat) == 1", flat, m)
	c.Rule().Normalize = []check.Normalization{check.NormalizeTrim, check.NormalizeLower}
	res := Run(context.Background(), "", c)
	if len(res.Extracted) != 2 || res.Extracted[0].Values[0].Shown != "" || res.Extracted[1].Values[0].Shown != `{"a": "x"}` {
		t.Fatalf("extracted %+v", res.Extracted)
	}
}

// A whole value kept beside a not literal value that skip_not_literal
// leaves out keeps its shown form. Planted break: the kept values taken
// from the extractor's own slice, which dropped Shown.
func TestAWholeValueKeptBySkipNotLiteralKeepsItsShownForm(t *testing.T) {
	m := mapsOf("m", obj(3, "a", str(" X ", 3)))
	values := m.Extractor.(fake).values
	m.Extractor = fake{values: append(values, check.Value{Text: "$(x)", At: check.Location{File: "f.sh", Line: 1}, NotLiteral: true})}
	c := assertRule(t, "size(m) == 2", m)
	c.Rule().Normalize = []check.Normalization{check.NormalizeTrim, check.NormalizeLower}
	c.Rule().SkipNotLiteral = []string{"m"}
	res := Run(context.Background(), "", c)
	if len(res.Extracted) != 1 || len(res.Extracted[0].Values) != 1 || len(res.Extracted[0].LeftOut) != 1 || res.Extracted[0].Values[0].Shown != `{"a": "x"}` {
		t.Fatalf("extracted %+v", res.Extracted)
	}
}

// Under semver the blame goes by position alone, however many places hold
// each value, as the README says: 18 in one file and 20.1.0 in two after
// it blame 20.1.0, where the text rule blames 18, the minority.
func TestSemverBlamesByPositionAndTextByMajority(t *testing.T) {
	extractors := func() []check.NamedExtractor {
		return []check.NamedExtractor{
			named("a", fake{values: []check.Value{at("18", "a.txt", 1)}}),
			named("b", fake{values: []check.Value{at("20.1.0", "b.txt", 1), at("20.1.0", "c.txt", 1)}}),
		}
	}
	if got := blamed(t, Run(context.Background(), "", semverRule(extractors()...))); got != "b.txt:1" {
		t.Errorf("semver blames %s, want b.txt:1", got)
	}
	if got := blamed(t, Run(context.Background(), "", agreeRule(extractors()...))); got != "a.txt:1" {
		t.Errorf("text blames %s, want a.txt:1", got)
	}
}
