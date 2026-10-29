package engine

import (
	"context"
	"maps"
	"reflect"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/config"
)

// fake is a kind of check errata does not have, as a plugin's would be.
type fake struct{ said string }

func (*fake) Name() string                                     { return "fake" }
func (*fake) Nouns() (one, many string)                        { return "fake check", "fake checks" }
func (*fake) ScopeRefusal(check.Scope) string                  { return "" }
func (*fake) Validate(check.Check) []error                     { return nil }
func (*fake) ListFields(check.Check) check.Fields              { return nil }
func (*fake) ListLines(check.Check, check.Quoter) []check.Line { return nil }

// A kind is run by its row of the dispatch table: a table with a row added
// for fake runs a fake check by it, and runs the kinds errata has as
// before. The planted break: Dispatch keying the table on the check's
// value rather than its kind's type, which found no row.
func TestDispatchRunsAKindByItsRow(t *testing.T) {
	table := maps.Clone(kinds)
	table[reflect.TypeFor[*fake]()] = func(_ context.Context, _ string, c check.Check) check.Result {
		return check.Result{Check: c, Verdict: check.VerdictFalse, Message: c.Kind.(*fake).said}
	}
	c := check.Check{ID: "f", Scope: check.ScopeRepo, Kind: &fake{said: "the fake runner ran"}}
	if res := table.Dispatch(context.Background(), t.TempDir(), c); res.Verdict != check.VerdictFalse || res.Message != "the fake runner ran" {
		t.Fatalf("fake check: %q %q, want false from its runner", res.Verdict, res.Message)
	}
	p := check.Check{ID: "p", Scope: check.ScopeRepo, Kind: &check.Program{Run: []string{"true"}}}
	if res := table.Dispatch(context.Background(), t.TempDir(), p); res.Verdict != check.VerdictTrue {
		t.Fatalf("program check beside it: %q %q, want true", res.Verdict, res.Message)
	}
}

// A kind with no row is unknown: errata did not run the check, so it
// cannot say the claim holds (ADR 0001). The planted break: Dispatch
// returning a result with no verdict for a kind with no row.
func TestDispatchOfAKindWithNoRowIsUnknown(t *testing.T) {
	res := Dispatch(context.Background(), t.TempDir(), check.Check{ID: "f", Scope: check.ScopeRepo, Kind: &fake{}})
	if res.Verdict != check.VerdictUnknown || res.Message != "errata has no runner for a check of kind *engine.fake" {
		t.Errorf("%q %q, want unknown with no runner", res.Verdict, res.Message)
	}
}

// The dispatch table has a row for each kind a config can load, and no
// other, so a kind given a config row and no runner fails here and not as
// an unknown in every run. The planted break: the generated check's row
// left out of the table.
func TestTheTableRunsEveryKindAConfigLoads(t *testing.T) {
	loads := map[reflect.Type]bool{}
	for _, k := range config.Kinds() {
		loads[reflect.TypeOf(k)] = true
		if _, ok := kinds[reflect.TypeOf(k)]; !ok {
			t.Errorf("config loads a %T, and the dispatch table has no row for it", k)
		}
	}
	for typ := range kinds {
		if !loads[typ] {
			t.Errorf("the dispatch table has a row for %s, which no config loads", typ)
		}
	}
}
