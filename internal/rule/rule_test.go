package rule

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

// fake is an extractor that returns fixed values or a fixed error, and
// records the root it was given.
type fake struct {
	values []check.Value
	err    error
	root   *string
}

func (f fake) Extract(root string) ([]check.Value, error) {
	if f.root != nil {
		*f.root = root
	}
	return f.values, f.err
}

func at(text, file string, line int) check.Value {
	return check.Value{Text: text, At: check.Location{File: file, Line: line}}
}

func agreeRule(extractors ...check.NamedExtractor) check.Check {
	r := &check.Rule{Extractors: extractors}
	for _, e := range extractors {
		r.Agree = append(r.Agree, e.Name)
	}
	return check.Check{ID: "r", Reason: "r", Scope: check.ScopeRepo, Kind: r}
}

func named(name string, f fake) check.NamedExtractor {
	return check.NamedExtractor{Name: name, Extractor: f}
}

func TestAgreeTrue(t *testing.T) {
	c := agreeRule(
		named("service", fake{values: []check.Value{at("10.43.0.5", "svc.yaml", 3)}}),
		named("secret", fake{values: []check.Value{at("10.43.0.5", "secret.yaml", 9), at("10.43.0.5", "secret.yaml", 12)}}),
	)
	res := Run(context.Background(), "/repo", c)
	if res.Verdict != check.VerdictTrue || res.Message != "" {
		t.Fatalf("verdict %s message %q, want true and no message", res.Verdict, res.Message)
	}
	if res.Check.ID != "r" {
		t.Fatalf("result names check %q", res.Check.ID)
	}
}

type slow struct{}

func (slow) Extract(string) ([]check.Value, error) {
	time.Sleep(10 * time.Millisecond)
	return []check.Value{at("x", "f", 1)}, nil
}

func TestRunRecordsDuration(t *testing.T) {
	res := Run(context.Background(), "", agreeRule(check.NamedExtractor{Name: "a", Extractor: slow{}}))
	if res.Duration < 10*time.Millisecond {
		t.Fatalf("duration = %s, want at least 10ms", res.Duration)
	}
}

func TestAgreeFalseListsEveryDistinctValue(t *testing.T) {
	c := agreeRule(
		named("service", fake{values: []check.Value{at("10.43.0.5", "svc.yaml", 3)}}),
		named("secret", fake{values: []check.Value{at("s3.internal", "secret.yaml", 9), at("10.43.0.5", "secret.yaml", 12)}}),
	)
	res := Run(context.Background(), "/repo", c)
	want := `agree: the values differ
  "10.43.0.5" at svc.yaml:3 (service), secret.yaml:12 (secret)
  "s3.internal" at secret.yaml:9 (secret)`
	if res.Verdict != check.VerdictFalse || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant false and:\n%s", res.Verdict, res.Message, want)
	}
}

func TestAgreePassesRoot(t *testing.T) {
	var got string
	c := agreeRule(named("a", fake{values: []check.Value{at("x", "f", 1)}, root: &got}))
	Run(context.Background(), "/repo", c)
	if got != "/repo" {
		t.Fatalf("extractor got root %q, want /repo", got)
	}
}

func TestNormalize(t *testing.T) {
	values := func() []check.NamedExtractor {
		return []check.NamedExtractor{
			named("a", fake{values: []check.Value{at(" S3.Internal\n", "a.yaml", 1)}}),
			named("b", fake{values: []check.Value{at("s3.internal", "b.yaml", 1)}}),
		}
	}
	cases := []struct {
		normalize []check.Normalization
		want      check.Verdict
	}{
		{nil, check.VerdictFalse},
		{[]check.Normalization{check.NormalizeTrim}, check.VerdictFalse},
		{[]check.Normalization{check.NormalizeLower}, check.VerdictFalse},
		{[]check.Normalization{check.NormalizeTrim, check.NormalizeLower}, check.VerdictTrue},
		{[]check.Normalization{check.NormalizeLower, check.NormalizeTrim}, check.VerdictTrue},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.normalize), func(t *testing.T) {
			c := agreeRule(values()...)
			c.Rule().Normalize = tc.normalize
			if res := Run(context.Background(), "", c); res.Verdict != tc.want {
				t.Fatalf("verdict %s, want %s: %s", res.Verdict, tc.want, res.Message)
			}
		})
	}
}

// The message shows the normalised text, since that is what was compared.
func TestNormalizedTextInMessage(t *testing.T) {
	c := agreeRule(
		named("a", fake{values: []check.Value{at(" A ", "a.yaml", 1)}}),
		named("b", fake{values: []check.Value{at("b", "b.yaml", 2)}}),
	)
	c.Rule().Normalize = []check.Normalization{check.NormalizeTrim, check.NormalizeLower}
	res := Run(context.Background(), "", c)
	if !strings.Contains(res.Message, `"a" at a.yaml:1 (a)`) {
		t.Fatalf("message %q, want the normalised value", res.Message)
	}
}

// Fail closed: every way a rule cannot compare is unknown, with the reason.
func TestAgreeUnknown(t *testing.T) {
	one := []check.Value{at("x", "f", 1)}
	cases := []struct {
		name       string
		allowEmpty []string
		extractors []check.NamedExtractor
		want       []string
	}{
		{
			"extractor error",
			nil,
			[]check.NamedExtractor{named("a", fake{values: one}), named("b", fake{err: errors.New("open b.yaml: no such file")})},
			[]string{`extractor "b": open b.yaml: no such file`},
		},
		{
			"every extractor error is named",
			nil,
			[]check.NamedExtractor{named("a", fake{err: errors.New("boom a")}), named("b", fake{err: errors.New("boom b")})},
			[]string{`extractor "a": boom a`, `extractor "b": boom b`},
		},
		{
			"an error is unknown even when allow_empty lists the extractor",
			[]string{"b"},
			[]check.NamedExtractor{named("a", fake{values: one}), named("b", fake{err: errors.New("boom")})},
			[]string{`extractor "b": boom`},
		},
		{
			"no value",
			nil,
			[]check.NamedExtractor{named("a", fake{values: one}), named("b", fake{})},
			[]string{`extractor "b" found no value`},
		},
		{
			"no value, with the reason the extractor gives",
			nil,
			[]check.NamedExtractor{named("a", fake{values: one}), named("b", fake{err: &check.NoValue{Reason: "it may be set in project settings"}})},
			[]string{`extractor "b" found no value: it may be set in project settings`},
		},
		{
			"no value from an extractor allow_empty does not list",
			[]string{"a"},
			[]check.NamedExtractor{named("a", fake{values: one}), named("b", fake{})},
			[]string{`extractor "b" found no value`},
		},
		{
			"no value from any extractor, each listed in allow_empty",
			[]string{"a", "b"},
			[]check.NamedExtractor{named("a", fake{}), named("b", fake{})},
			[]string{"no extractor found a value, so agree has nothing to compare"},
		},
		{
			"one value from one extractor",
			nil,
			[]check.NamedExtractor{named("tags", fake{values: one})},
			[]string{`agree found one value, so it has nothing to compare: "x" at f:1 (tags)`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := agreeRule(tc.extractors...)
			c.Rule().AllowEmpty = tc.allowEmpty
			res := Run(context.Background(), "", c)
			if res.Verdict != check.VerdictUnknown {
				t.Fatalf("verdict %s, want unknown", res.Verdict)
			}
			for _, w := range tc.want {
				if !strings.Contains(res.Message, w) {
					t.Errorf("message %q does not contain %q", res.Message, w)
				}
			}
		})
	}
}

// One extractor that finds two values compares them: a regular expression
// that matches the same fact in two places.
func TestAgreeOneExtractorTwoValues(t *testing.T) {
	c := agreeRule(named("tags", fake{values: []check.Value{at("v1", "README.md", 3), at("v1", "README.md", 9)}}))
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s, want true: %s", res.Verdict, res.Message)
	}
	c = agreeRule(named("tags", fake{values: []check.Value{at("v1", "README.md", 3), at("v2", "README.md", 9)}}))
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictFalse {
		t.Fatalf("verdict %s, want false: %s", res.Verdict, res.Message)
	}
}

// allow_empty lets a listed extractor find nothing while the others still
// decide, even when one value is left.
func TestAllowEmptyComparesTheRest(t *testing.T) {
	c := agreeRule(
		named("a", fake{}),
		named("b", fake{values: []check.Value{at("x", "b.yaml", 1)}}),
		named("c", fake{values: []check.Value{at("y", "c.yaml", 1)}}),
	)
	c.Rule().AllowEmpty = []string{"a"}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictFalse {
		t.Fatalf("verdict %s, want false: %s", res.Verdict, res.Message)
	}
	c.Rule().Extractors[2] = named("c", fake{values: []check.Value{at("x", "c.yaml", 1)}})
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s, want true: %s", res.Verdict, res.Message)
	}
	c = agreeRule(named("a", fake{}), named("b", fake{values: []check.Value{at("x", "b.yaml", 1)}}))
	c.Rule().AllowEmpty = []string{"a"}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("one value beside a listed empty extractor: verdict %s, want true: %s", res.Verdict, res.Message)
	}
}

// The result carries the values the rule evaluated, by extractor in agree
// order and raw rather than normalised, so a report reads their locations
// without parsing the message.
func TestResultCarriesExtractedValues(t *testing.T) {
	svc := []check.Value{at(" X ", "deploy/service.yaml", 3)}
	cfg := []check.Value{at("y", "config/endpoints.yaml", 9), at("x", "config/endpoints.yaml", 12)}
	c := agreeRule(named("service", fake{values: svc}), named("config", fake{values: cfg}), named("spare", fake{}))
	c.Rule().AllowEmpty = []string{"spare"}
	c.Rule().Normalize = []check.Normalization{check.NormalizeTrim, check.NormalizeLower}
	res := Run(context.Background(), "", c)
	want := []check.Extracted{{Extractor: "service", Values: svc}, {Extractor: "config", Values: cfg}, {Extractor: "spare"}}
	if !reflect.DeepEqual(res.Extracted, want) {
		t.Fatalf("extracted = %+v, want %+v", res.Extracted, want)
	}

	errored := agreeRule(named("service", fake{values: svc}), named("config", fake{err: errors.New("boom")}))
	res = Run(context.Background(), "", errored)
	if want := []check.Extracted{{Extractor: "service", Values: svc}}; !reflect.DeepEqual(res.Extracted, want) {
		t.Fatalf("with an extractor error, extracted = %+v, want %+v", res.Extracted, want)
	}
}

func TestAgreeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := agreeRule(named("a", fake{values: []check.Value{at("x", "f", 1)}}))
	res := Run(ctx, "", c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, `cancelled before extractor "a" ran`) {
		t.Fatalf("verdict %s message %q, want unknown and the cancellation", res.Verdict, res.Message)
	}
}

// For any values from any extractors: agree is unknown when an extractor that
// allow_empty does not list finds nothing, or when fewer than two values are
// left and no listed extractor came back empty; otherwise it is true exactly
// when the set of normalised strings has one member. A false verdict's message
// names every location it was given, and the result carries every value.
func TestPropertyAgree(t *testing.T) {
	norms := []check.Normalization{check.NormalizeTrim, check.NormalizeLower}
	rapid.Check(t, func(t *rapid.T) {
		normalize := rapid.SliceOfDistinct(rapid.SampledFrom(norms), func(n check.Normalization) check.Normalization { return n }).Draw(t, "normalize")
		n := rapid.IntRange(1, 4).Draw(t, "extractors")
		var (
			extractors   []check.NamedExtractor
			allowEmpty   []string
			wantValues   []check.Extracted
			locations    []string
			set          = map[string]bool{}
			total        int
			unlistedNone bool
			listedNone   bool
		)
		for i := range n {
			name := fmt.Sprintf("e%d", i)
			listed := rapid.Bool().Draw(t, name+" listed")
			if listed {
				allowEmpty = append(allowEmpty, name)
			}
			values := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) check.Value {
				return at(
					rapid.SampledFrom([]string{"a", "A", " a", "b", "B ", "", " "}).Draw(t, "text"),
					rapid.StringMatching(`[a-z]{1,6}\.yaml`).Draw(t, "file"),
					rapid.IntRange(1, 500).Draw(t, "line"),
				)
			}), 0, 3).Draw(t, name)
			switch {
			case len(values) == 0 && listed:
				listedNone = true
			case len(values) == 0:
				unlistedNone = true
			}
			total += len(values)
			wantValues = append(wantValues, check.Extracted{Extractor: name, Values: values})
			for _, v := range values {
				locations = append(locations, fmt.Sprintf("%s:%d (%s)", v.At.File, v.At.Line, name))
				text := v.Text
				for _, nm := range normalize {
					if nm == check.NormalizeTrim {
						text = strings.TrimSpace(text)
					} else {
						text = strings.ToLower(text)
					}
				}
				set[text] = true
			}
			extractors = append(extractors, named(name, fake{values: values}))
		}
		c := agreeRule(extractors...)
		c.Rule().Normalize = normalize
		c.Rule().AllowEmpty = allowEmpty
		res := Run(context.Background(), "", c)

		if !reflect.DeepEqual(res.Extracted, wantValues) {
			t.Fatalf("extracted %+v, want %+v", res.Extracted, wantValues)
		}
		compared := !unlistedNone && total > 0 && (total >= 2 || listedNone)
		if !compared {
			if res.Verdict != check.VerdictUnknown {
				t.Fatalf("verdict %s with %d values, unlisted empty %v, listed empty %v; want unknown", res.Verdict, total, unlistedNone, listedNone)
			}
			return
		}
		if (res.Verdict == check.VerdictTrue) != (len(set) == 1) {
			t.Fatalf("verdict %s with %d distinct values %v", res.Verdict, len(set), set)
		}
		if len(set) > 1 {
			if res.Verdict != check.VerdictFalse {
				t.Fatalf("verdict %s, want false with values %v", res.Verdict, set)
			}
			for _, loc := range locations {
				if !strings.Contains(res.Message, loc) {
					t.Fatalf("message does not name %s:\n%s", loc, res.Message)
				}
			}
			for text := range set {
				if !strings.Contains(res.Message, fmt.Sprintf("%q at ", text)) {
					t.Fatalf("message does not name value %q:\n%s", text, res.Message)
				}
			}
		}
	})
}

// A message quotes at most maxQuoted bytes of a value, then ... and the
// value's length, so a false rule over long values writes a short message.
// The planted break is quoting the whole value.
func TestMessagesClipLongValues(t *testing.T) {
	long := strings.Repeat("v", 100_000)
	want := fmt.Sprintf("%q... (100000 bytes)", long[:maxQuoted])
	for name, c := range map[string]check.Check{
		"agree false": agreeRule(
			named("a", fake{values: []check.Value{at(long, "a.yaml", 1)}}),
			named("b", fake{values: []check.Value{at("w"+long[1:], "b.yaml", 2)}}),
		),
		"agree one value": agreeRule(named("a", fake{values: []check.Value{at(long, "a.yaml", 1)}})),
		"assert false":    assertRule(t, "size(a) == 0", named("a", fake{values: []check.Value{at(long, "a.yaml", 1)}})),
		"assert unknown":  assertRule(t, "int(a[0]) == 1", named("a", fake{values: []check.Value{at(long, "a.yaml", 1)}})),
	} {
		res := Run(context.Background(), "/repo", c)
		if !strings.Contains(res.Message, want) {
			t.Errorf("%s: the message does not quote the value clipped: %.300q", name, res.Message)
		}
		if len(res.Message) > 4*maxQuoted+500 {
			t.Errorf("%s: a message of %d bytes", name, len(res.Message))
		}
	}
}

// Two long values that agree in their first maxQuoted bytes still print
// differently: each is quoted from just before the first byte where it
// differs from the values it is compared with, with that offset and its
// length. The planted break is quoting the first maxQuoted bytes, which
// printed these two URLs alike.
func TestClipShowsWhereValuesDiffer(t *testing.T) {
	url := func(tag string) string {
		return "https://registry.example.com/" + strings.Repeat("a/", 129) + "ax?tag=" + tag
	}
	a, b := url("1.2.3"), url("1.2.4")
	if len(a) != 299 || len(b) != 299 {
		t.Fatalf("the URLs are %d and %d bytes", len(a), len(b))
	}
	for name, c := range map[string]check.Check{
		"agree":  agreeRule(named("a", fake{values: []check.Value{at(a, "a.yaml", 1)}}), named("b", fake{values: []check.Value{at(b, "b.json", 1)}})),
		"assert": assertRule(t, "a[0] == b[0]", named("a", fake{values: []check.Value{at(a, "a.yaml", 1)}}), named("b", fake{values: []check.Value{at(b, "b.json", 1)}})),
	} {
		res := Run(context.Background(), "/repo", c)
		if res.Verdict != check.VerdictFalse {
			t.Fatalf("%s: %s %s", name, res.Verdict, res.Message)
		}
		for _, tag := range []string{`tag=1.2.3" (from byte `, `tag=1.2.4" (from byte `} {
			if !strings.Contains(res.Message, tag) {
				t.Errorf("%s: the message does not show %s:\n%s", name, tag, res.Message)
			}
		}
		if !strings.Contains(res.Message, " of 299)") {
			t.Errorf("%s: the message does not give the length:\n%s", name, res.Message)
		}
	}
}

// A window starts and ends on a rune boundary, so a clipped value quotes
// whole characters and never half of one as an escaped byte. In "end", byte
// maxQuoted falls inside é, so the window ends before it; in "start", the
// byte quoteContext before the first difference falls inside é, so the
// window starts at it. The planted break is cutting at the byte counts,
// which quotes "\xa9" at the edge.
func TestWindowsKeepWholeRunes(t *testing.T) {
	end := strings.Repeat("a", maxQuoted-1) + "é" + strings.Repeat("a", 40)
	shared := strings.Repeat("a", 100) + "é" + strings.Repeat("b", quoteContext-1)
	x, y := shared+"x"+strings.Repeat("c", 200), shared+"y"+strings.Repeat("c", 200)
	for _, tc := range []struct {
		name   string
		values []string
		v      string
		want   string
	}{
		{"end", []string{end}, end,
			fmt.Sprintf("%q... (%d bytes)", end[:maxQuoted-1], len(end))},
		{"start", []string{x, y}, x,
			fmt.Sprintf("...%q (from byte 100 of %d)", x[100:], len(x))},
	} {
		if got := newQuoter(tc.values).quote(tc.v); got != tc.want {
			t.Errorf("%s: quoted\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}

// A value of exactly maxQuoted bytes is quoted whole, without marks, whether
// or not the quoter was given it; one byte more is clipped. A long value
// whose first byte does not start a rune, as a file that is not UTF-8 can
// hold, is quoted from byte 0. The planted breaks are < for <= in newQuoter,
// which marks the whole value with its length; >= for > in quote, which
// recurses without end on a value it was not given; and start >= 0 in the
// loop that moves a window's start back to a rune, which starts it at -1.
func TestQuotesAtMaxQuoted(t *testing.T) {
	exact := strings.Repeat("v", maxQuoted)
	over := exact + "v"
	invalid := "\x80" + over
	for _, tc := range []struct {
		name   string
		values []string
		v      string
		want   string
	}{
		{"exactly maxQuoted", []string{exact}, exact, strconv.Quote(exact)},
		{"exactly maxQuoted, not given", nil, exact, strconv.Quote(exact)},
		{"one byte over", []string{over}, over, fmt.Sprintf("%q... (%d bytes)", exact, len(over))},
		{"first byte not a rune start", []string{invalid}, invalid, fmt.Sprintf("%q... (%d bytes)", invalid[:maxQuoted], len(invalid))},
	} {
		if got := newQuoter(tc.values).quote(tc.v); got != tc.want {
			t.Errorf("%s: quoted\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}

// Different values never print alike, however long they are and however
// many share their prefixes: a window that two long values share is told
// apart by the fingerprint.
func TestPropertyQuotedValuesDiffer(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := strings.Repeat("a", rapid.IntRange(0, 600).Draw(t, "shared"))
		var values []string
		for range rapid.IntRange(2, 8).Draw(t, "values") {
			tail := rapid.StringOfN(rapid.SampledFrom([]rune("ab")), 0, 400, -1).Draw(t, "tail")
			values = append(values, base+tail)
		}
		q := newQuoter(values)
		printed := map[string]string{}
		for _, v := range values {
			p := q.quote(v)
			if other, ok := printed[p]; ok && other != v {
				t.Fatalf("%d and %d bytes print alike: %s", len(v), len(other), p)
			}
			printed[p] = v
			if len(p) > maxQuoted+100 {
				t.Fatalf("a value of %d bytes prints as %d", len(v), len(p))
			}
		}
	})
}

// Four values where windows match: x and y differ at byte 10 and agree
// after it, as u and w do, and each shares its first 511 bytes with another
// value, so x's and y's windows start at byte 479 and hold the same bytes,
// as u's and w's do. The planted break is printing no fingerprint, which
// prints x and y alike.
func TestSharedWindowsPrintAFingerprint(t *testing.T) {
	base, c := strings.Repeat("a", 10), strings.Repeat("c", 500)
	values := []string{base + "X" + c + "1", base + "X" + c + "2", base + "Y" + c + "1", base + "Y" + c + "2"}
	q := newQuoter(values)
	printed := map[string]bool{}
	for _, v := range values {
		p := q.quote(v)
		if printed[p] {
			t.Fatalf("two values print alike: %s", p)
		}
		printed[p] = true
		if !strings.Contains(p, "(from byte 479 of 512, fingerprint ") {
			t.Errorf("no fingerprint: %s", p)
		}
	}
	if p := newQuoter(values[:2]).quote(values[0]); strings.Contains(p, "fingerprint") {
		t.Errorf("two values print a fingerprint, though their windows differ: %s", p)
	}
}

// An extractor that returns check.NoValue found no value: allow_empty lets
// the rule pass on it, as for an empty list, and the values of another
// extractor carry their condition into the message. Planted break: NoValue
// read as an ordinary error, which made the allow_empty rule unknown.
func TestNoValueAndConditions(t *testing.T) {
	c := agreeRule(named("a", fake{values: []check.Value{at("x", "f", 1), at("x", "f", 2)}}), named("b", fake{err: &check.NoValue{Reason: "settings"}}))
	c.Rule().AllowEmpty = []string{"b"}
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue {
		t.Fatalf("verdict %s, %s; want true", res.Verdict, res.Message)
	}
	v := at("y", "f", 3)
	v.Condition = "if $A"
	c = agreeRule(named("a", fake{values: []check.Value{at("x", "f", 1), v}}))
	res := Run(context.Background(), "", c)
	if res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, `"y" at f:3 when "if $A" (a)`) {
		t.Fatalf("verdict %s, message %q; want false, naming the condition", res.Verdict, res.Message)
	}
}

// Every message that places a value names its origin and its condition
// after the place: the saw lines of a false assert, a not literal value,
// the one value agree found, and the places of a false agree. Planted
// breaks: ConditionNote dropped from saw, from notLiteral and from the one
// value message, each failing its case; Notes dropped from the places of a
// false agree.
func TestMessagesNameConditions(t *testing.T) {
	v := at("y", "f", 3)
	v.Condition = "if $A"
	v.Origin = `gitlab-ci job "b"`
	res := Run(context.Background(), "", assertRule(t, "a[0] == 'x'", named("a", fake{values: []check.Value{v}})))
	if !strings.Contains(res.Message, `a[0] = "y" at f:3 from gitlab-ci job "b" when "if $A"`) {
		t.Errorf("assert: %q names no condition", res.Message)
	}
	computed := v
	computed.NotLiteral = true
	res = Run(context.Background(), "", agreeRule(named("a", fake{values: []check.Value{at("x", "f", 1), computed}})))
	if !strings.Contains(res.Message, `"y" at f:3 from gitlab-ci job "b" when "if $A"`) {
		t.Errorf("not literal: %q names no condition", res.Message)
	}
	res = Run(context.Background(), "", agreeRule(named("a", fake{values: []check.Value{v}})))
	if !strings.Contains(res.Message, `nothing to compare: "y" at f:3 from gitlab-ci job "b" when "if $A" (a)`) {
		t.Errorf("one value: %q names no condition", res.Message)
	}
	res = Run(context.Background(), "", agreeRule(named("a", fake{values: []check.Value{at("x", "f", 1), v}})))
	if !strings.Contains(res.Message, `f:3 from gitlab-ci job "b" when "if $A" (a)`) {
		t.Errorf("false agree: %q names no origin", res.Message)
	}
}
