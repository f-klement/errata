package report

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/config"
)

// fake is a kind of check errata does not have, as a plugin's would be: it
// lists a probe, and in text writes it with the listing's quoting.
type fake struct{ probe string }

func (*fake) Name() string                    { return "fake" }
func (*fake) Nouns() (one, many string)       { return "fake check", "fake checks" }
func (*fake) ScopeRefusal(check.Scope) string { return "" }
func (*fake) Validate(check.Check) []error    { return nil }
func (f *fake) ListFields(check.Check) check.Fields {
	return check.Fields{{Key: "probe", Value: f.probe}}
}
func (f *fake) ListLines(_ check.Check, q check.Quoter) []check.Line {
	return []check.Line{{Label: "probe:", Text: q.Value(f.probe)}}
}

// errata list lists a kind it has no code for from the kind itself: its
// type and keys in JSON, its noun, lines and count in text, after the
// rules and program checks the count always names. The planted breaks:
// the JSON's type left to a list of the kinds errata has, and the count
// leaving out a kind that is not first counted.
func TestListAKindFromTheKind(t *testing.T) {
	checks := []check.Check{
		{ID: "f", Reason: "r", Scope: check.ScopeLive, Kind: &fake{probe: "<a b>"}},
		{ID: "g", Reason: "r", Scope: check.ScopeLive, Kind: &fake{probe: "c"}},
	}
	doc := listDocument(t, checks)
	got := doc["checks"].([]any)[0].(map[string]any)
	if got["type"] != "fake" || got["probe"] != "<a b>" || len(got) != 6 {
		t.Errorf("JSON of a fake check = %v, want type fake, probe <a b> and the four keys every check has", got)
	}
	var b bytes.Buffer
	if err := (ListText{}).WriteList(&b, checks, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"f (live) fake check\n", "  probe: `<a b>`\n", "2 checks: 0 rules, 0 program checks, 2 fake checks.\n"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("text listing\n%s\nlacks %q", b.String(), want)
		}
	}
}

// The kinds the count always names are kinds a config loads, so a kind
// taken out of config is not still counted as none. The planted break:
// the rule's row in config naming a generated check as its kind.
func TestCountedFirstAreKindsAConfigLoads(t *testing.T) {
	for _, k := range countedFirst {
		if !slices.ContainsFunc(config.Kinds(), func(l check.Kind) bool { return l.Name() == k.Name() }) {
			t.Errorf("the count always names %s, which no config loads", k.Name())
		}
	}
}
