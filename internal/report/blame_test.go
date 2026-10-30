package report

import (
	"context"
	"strconv"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/rule"
)

// fixed is an extractor that returns its values.
type fixed []check.Value

func (f fixed) Extract(context.Context, string) ([]check.Value, error) { return f, nil }
func (f fixed) ExtractWith(context.Context, string, map[string][]check.Value) ([]check.Value, error) {
	return f, nil
}

// An agree rule with compare: semver over 20, 20.11.1 and 18 is false
// because of 18, and its SARIF result points there. The texts are three
// of one value each, so a report that grouped them itself took 20 as the
// majority and pointed at 20.11.1, which agrees. Planted break: SARIF's
// old disagreeing put back in place of the rule's blame (#208).
func TestSARIFPointsWhereSemverIsFalse(t *testing.T) {
	r := &check.Rule{Compare: check.CompareSemver}
	for i, v := range []string{"20", "20.11.1", "18"} {
		name := "e" + strconv.Itoa(i)
		r.Extractors = append(r.Extractors, check.NamedExtractor{Name: name, Extractor: fixed{val(v, "pins.txt", i+1)}})
		r.Agree = append(r.Agree, name)
	}
	res := rule.Run(context.Background(), "", check.Check{ID: "node", Reason: "r", Scope: check.ScopeRepo, Kind: r})
	if res.Verdict != check.VerdictFalse {
		t.Fatalf("verdict %s: %s", res.Verdict, res.Message)
	}
	out := decodeSARIF(t, writeSARIF(t, check.Report{Results: []check.Result{res}})).Runs[0].Results[0]
	if got := out.Locations[0].where(); got != "pins.txt:3" {
		t.Fatalf("primary location %s, want pins.txt:3, the 18", got)
	}
}
