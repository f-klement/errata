package rule

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

// computed is a value the file only computes, as a shell or CI reader marks
// $(cmd) or ${{ expr }}.
func computed(text, file string, line int) check.Value {
	v := at(text, file, line)
	v.NotLiteral = true
	return v
}

// A not literal value makes agree and assert unknown, and the message names
// the value and where it was found. The planted break is comparing the text:
// "$(git describe)" agrees with nothing, so agree would be false and assert
// would run.
func TestNotLiteralIsUnknown(t *testing.T) {
	shell := named("shell", fake{values: []check.Value{at("1.4.2", "build.sh", 2), computed("$(git describe)", "build.sh", 7)}})
	chart := named("chart", fake{values: []check.Value{at("1.4.2", "Chart.yaml", 3)}})
	want := `extractor "shell" returned 1 not literal value, which a rule does not compare as text, since the file only computes such a value; list "shell" in skip_not_literal to leave such values out
  "$(git describe)" at build.sh:7`
	for name, c := range map[string]check.Check{
		"agree":  agreeRule(chart, shell),
		"assert": assertRule(t, "chart[0] == shell[0]", chart, shell),
	} {
		res := Run(context.Background(), "", c)
		if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, want) {
			t.Errorf("%s: verdict %s message:\n%s\nwant unknown and:\n%s", name, res.Verdict, res.Message, want)
		}
		if strings.Contains(res.Message, "skip_not_literal left out") {
			t.Errorf("%s: an unlisted extractor left a value out:\n%s", name, res.Message)
		}
	}
}

// Every not literal value of an unlisted extractor is named, and none of its
// literal ones.
func TestNotLiteralNamesEachValue(t *testing.T) {
	c := agreeRule(
		named("ci", fake{values: []check.Value{computed("${{ env.TAG }}", ".github/workflows/ci.yml", 12), at("1.4.2", ".github/workflows/ci.yml", 20), computed("${{ inputs.tag }}", ".github/workflows/ci.yml", 31)}}),
		named("chart", fake{values: []check.Value{at("1.4.2", "Chart.yaml", 3)}}),
	)
	res := Run(context.Background(), "", c)
	want := `extractor "ci" returned 2 not literal values, which a rule does not compare as text, since the file only computes such a value; list "ci" in skip_not_literal to leave such values out
  "${{ env.TAG }}" at .github/workflows/ci.yml:12
  "${{ inputs.tag }}" at .github/workflows/ci.yml:31`
	if res.Verdict != check.VerdictUnknown || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant unknown and:\n%s", res.Verdict, res.Message, want)
	}
}

// skip_not_literal leaves the listed extractor's not literal values out, and
// the message counts them, on a rule that holds as on one that does not. The
// result keeps them apart from the values compared. The planted break is
// comparing them: "$(git describe)" differs from 1.4.2, so the true rule
// would be false.
func TestSkipNotLiteralLeavesValuesOut(t *testing.T) {
	left := computed("$(git describe)", "build.sh", 7)
	shellValues := []check.Value{at("1.4.2", "build.sh", 2), left}
	c := agreeRule(
		named("chart", fake{values: []check.Value{at("1.4.2", "Chart.yaml", 3)}}),
		named("shell", fake{values: shellValues}),
	)
	c.Rule().SkipNotLiteral = []string{"shell"}
	res := Run(context.Background(), "", c)
	if want := `skip_not_literal left out 1 not literal value of extractor "shell"`; res.Verdict != check.VerdictTrue || res.Message != want {
		t.Fatalf("verdict %s message %q, want true and %q", res.Verdict, res.Message, want)
	}
	want := []check.Extracted{
		{Extractor: "chart", Values: []check.Value{at("1.4.2", "Chart.yaml", 3)}},
		{Extractor: "shell", Values: []check.Value{at("1.4.2", "build.sh", 2)}, LeftOut: []check.Value{left}},
	}
	if !reflect.DeepEqual(res.Extracted, want) {
		t.Fatalf("extracted %+v, want %+v", res.Extracted, want)
	}
	if shellValues[1] != left {
		t.Fatalf("the extractor's own slice changed: %+v", shellValues)
	}

	c.Rule().Extractors[0] = named("chart", fake{values: []check.Value{at("1.5.0", "Chart.yaml", 3)}})
	res = Run(context.Background(), "", c)
	wantMsg := `agree: the values differ
  "1.5.0" at Chart.yaml:3 (chart)
  "1.4.2" at build.sh:2 (shell)
skip_not_literal left out 1 not literal value of extractor "shell"`
	if res.Verdict != check.VerdictFalse || res.Message != wantMsg {
		t.Fatalf("verdict %s message:\n%s\nwant false and:\n%s", res.Verdict, res.Message, wantMsg)
	}
}

// The message counts per extractor, in the order the rule ran them, and only
// the extractors that left a value out.
func TestSkipNotLiteralCountsPerExtractor(t *testing.T) {
	c := agreeRule(
		named("chart", fake{values: []check.Value{at("1.4.2", "Chart.yaml", 3)}}),
		named("ci", fake{values: []check.Value{computed("${{ env.TAG }}", "ci.yml", 1), at("1.4.2", "ci.yml", 2), computed("${{ inputs.tag }}", "ci.yml", 3)}}),
		named("shell", fake{values: []check.Value{at("1.4.2", "build.sh", 2), computed("$(git describe)", "build.sh", 7)}}),
		named("make", fake{values: []check.Value{at("1.4.2", "Makefile", 1)}}),
	)
	c.Rule().SkipNotLiteral = []string{"shell", "make", "ci"}
	res := Run(context.Background(), "", c)
	want := "skip_not_literal left out 2 not literal values of extractor \"ci\"\n" +
		"skip_not_literal left out 1 not literal value of extractor \"shell\""
	if res.Verdict != check.VerdictTrue || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant true and:\n%s", res.Verdict, res.Message, want)
	}
}

// assert sees only the values that were not left out.
func TestSkipNotLiteralAssertSeesWhatIsLeft(t *testing.T) {
	c := assertRule(t, "size(shell) == 1 && shell[0] == '1.4.2'",
		named("shell", fake{values: []check.Value{computed("$(git describe)", "build.sh", 7), at("1.4.2", "build.sh", 2)}}))
	c.Rule().SkipNotLiteral = []string{"shell"}
	res := Run(context.Background(), "", c)
	if want := `skip_not_literal left out 1 not literal value of extractor "shell"`; res.Verdict != check.VerdictTrue || res.Message != want {
		t.Fatalf("verdict %s message %q, want true and %q", res.Verdict, res.Message, want)
	}
}

// An extractor whose every value was left out counts as one that found no
// value: unknown unless allow_empty lists it. Listed, agree compares the one
// value left as it does beside an empty extractor, and assert decides on an
// empty list. The planted break is counting a left out value as found, which
// made the first rule true on 1.4.2 alone.
func TestSkipNotLiteralEveryValueLeftOut(t *testing.T) {
	onlyComputed := func() check.NamedExtractor {
		return named("shell", fake{values: []check.Value{computed("$(git describe)", "build.sh", 7), computed("${VERSION}", "build.sh", 9)}})
	}
	chart := named("chart", fake{values: []check.Value{at("1.4.2", "Chart.yaml", 3)}})

	c := agreeRule(chart, onlyComputed())
	c.Rule().SkipNotLiteral = []string{"shell"}
	res := Run(context.Background(), "", c)
	want := `extractor "shell" found no literal value
skip_not_literal left out all 2 not literal values of extractor "shell", so the rule compared nothing from it`
	if res.Verdict != check.VerdictUnknown || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant unknown and:\n%s", res.Verdict, res.Message, want)
	}

	// The planted break is the count of c8f93cc, which read as a partial
	// skip on a rule that held having compared nothing from shell.
	c.Rule().AllowEmpty = []string{"shell"}
	want = `skip_not_literal left out all 2 not literal values of extractor "shell", so the rule compared nothing from it`
	if res := Run(context.Background(), "", c); res.Verdict != check.VerdictTrue || res.Message != want {
		t.Fatalf("allow_empty lists it: verdict %s message %q, want true and %q", res.Verdict, res.Message, want)
	}

	a := assertRule(t, "size(shell) == 0", onlyComputed())
	a.Rule().SkipNotLiteral = []string{"shell"}
	if res := Run(context.Background(), "", a); res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, `extractor "shell" found no literal value`) {
		t.Fatalf("assert: verdict %s message %q, want unknown: found no literal value", res.Verdict, res.Message)
	}
	a.Rule().AllowEmpty = []string{"shell"}
	if res := Run(context.Background(), "", a); res.Verdict != check.VerdictTrue {
		t.Fatalf("assert with allow_empty: verdict %s, want true: %s", res.Verdict, res.Message)
	}
}

// agree's floor of two values applies to what is left: one extractor with a
// literal value and a left out one has one value to compare.
func TestSkipNotLiteralFloorCountsWhatIsLeft(t *testing.T) {
	c := agreeRule(named("shell", fake{values: []check.Value{at("1.4.2", "build.sh", 2), computed("$(git describe)", "build.sh", 7)}}))
	c.Rule().SkipNotLiteral = []string{"shell"}
	res := Run(context.Background(), "", c)
	want := `agree found one value, so it has nothing to compare: "1.4.2" at build.sh:2 (shell)
skip_not_literal left out 1 not literal value of extractor "shell"`
	if res.Verdict != check.VerdictUnknown || res.Message != want {
		t.Fatalf("verdict %s message:\n%s\nwant unknown and:\n%s", res.Verdict, res.Message, want)
	}
}

// A not literal value of a long text is quoted clipped, as every value in a
// message is.
func TestNotLiteralClipsLongValues(t *testing.T) {
	long := "$(" + strings.Repeat("x", 100_000) + ")"
	c := agreeRule(named("a", fake{values: []check.Value{computed(long, "a.sh", 1)}}), named("b", fake{values: []check.Value{at("x", "b", 1)}}))
	res := Run(context.Background(), "", c)
	if res.Verdict != check.VerdictUnknown || !strings.Contains(res.Message, "... (100003 bytes) at a.sh:1") || len(res.Message) > 1000 {
		t.Fatalf("verdict %s message of %d bytes: %.400q", res.Verdict, len(res.Message), res.Message)
	}
}

// For any values, literal or not, from any extractors, with any of them
// listed in skip_not_literal and allow_empty: a listed extractor's not
// literal values are left out and counted, an unlisted extractor with one is
// unknown, and what is left decides as it would with nothing left out.
func TestPropertySkipNotLiteral(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 4).Draw(t, "extractors")
		var (
			extractors       []check.NamedExtractor
			skip, allowEmpty []string
			want             []check.Extracted
			kept             = map[string]bool{}
			total            int
			unlisted         bool // an unlisted extractor returned a not literal value
			unlistedNone     bool
			listedNone       bool
			counts           []string
			textsOf          []string
		)
		for i := range n {
			name := fmt.Sprintf("e%d", i)
			skipped := rapid.Bool().Draw(t, name+" skip")
			allowed := rapid.Bool().Draw(t, name+" allow")
			if skipped {
				skip = append(skip, name)
			}
			if allowed {
				allowEmpty = append(allowEmpty, name)
			}
			values := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) check.Value {
				v := at(rapid.SampledFrom([]string{"a", "b"}).Draw(t, "text"), "f", rapid.IntRange(1, 9).Draw(t, "line"))
				v.NotLiteral = rapid.Bool().Draw(t, "not literal")
				return v
			}), 0, 3).Draw(t, name)
			x := check.Extracted{Extractor: name, Values: values}
			hasComputed := slices.ContainsFunc(values, func(v check.Value) bool { return v.NotLiteral })
			if skipped && hasComputed {
				x.Values = nil
				for _, v := range values {
					if v.NotLiteral {
						x.LeftOut = append(x.LeftOut, v)
					} else {
						x.Values = append(x.Values, v)
					}
				}
				switch {
				case len(x.Values) > 0:
					counts = append(counts, fmt.Sprintf("left out %d not literal value", len(x.LeftOut)))
				case len(x.LeftOut) == 1:
					counts = append(counts, fmt.Sprintf("left out the one value of extractor %q, which is not literal, so the rule compared nothing from it", name))
				default:
					counts = append(counts, fmt.Sprintf("left out all %d not literal values of extractor %q, so the rule compared nothing from it", len(x.LeftOut), name))
				}
			}
			if !skipped && hasComputed {
				unlisted = true
			}
			switch {
			case len(x.Values) == 0 && allowed:
				listedNone = true
			case len(x.Values) == 0:
				unlistedNone = true
			}
			for _, v := range x.Values {
				kept[v.Text] = true
				textsOf = append(textsOf, v.Text)
			}
			total += len(x.Values)
			want = append(want, x)
			extractors = append(extractors, named(name, fake{values: values}))
		}
		c := agreeRule(extractors...)
		c.Rule().SkipNotLiteral = skip
		c.Rule().AllowEmpty = allowEmpty
		res := Run(context.Background(), "", c)

		if !reflect.DeepEqual(res.Extracted, want) {
			t.Fatalf("extracted %+v, want %+v", res.Extracted, want)
		}
		for _, line := range counts {
			if !strings.Contains(res.Message, line) {
				t.Fatalf("message does not count %q:\n%s", line, res.Message)
			}
		}
		if len(counts) == 0 && strings.Contains(res.Message, "skip_not_literal left out") {
			t.Fatalf("nothing was left out, yet the message counts:\n%s", res.Message)
		}
		if strings.Contains(res.Message, "which a rule does not compare as text") != unlisted {
			t.Fatalf("unlisted not literal %v, message:\n%s", unlisted, res.Message)
		}
		compared := !unlisted && !unlistedNone && total > 0 && (total >= 2 || listedNone)
		if !compared {
			if res.Verdict != check.VerdictUnknown {
				t.Fatalf("verdict %s with %d values left, unlisted not literal %v, unlisted empty %v, listed empty %v; want unknown",
					res.Verdict, total, unlisted, unlistedNone, listedNone)
			}
			return
		}
		if (res.Verdict == check.VerdictTrue) != (len(kept) == 1) {
			t.Fatalf("verdict %s with the values %v left", res.Verdict, textsOf)
		}
	})
}
