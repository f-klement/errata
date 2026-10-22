package engine

import (
	"context"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/provider"
)

type fakeProvider struct{ caps provider.Capabilities }

func (fakeProvider) Name() string                          { return "fake" }
func (f fakeProvider) Capabilities() provider.Capabilities { return f.caps }
func (fakeProvider) Reconciler() provider.ReconcilerModel  { return provider.ReconcilerModel{} }

// alwaysTrue records which checks the engine handed to the runner.
func alwaysTrue(ran *[]string) Runner {
	return func(_ context.Context, _ string, c check.Check) check.Result {
		*ran = append(*ran, c.ID)
		return check.Result{Check: c, Verdict: check.VerdictTrue}
	}
}

func TestExecuteSkipsUnreachableScopes(t *testing.T) {
	var ran []string
	e := &Engine{Root: "/repo", Run: alwaysTrue(&ran)}
	checks := []check.Check{
		{ID: "a", Scope: check.ScopeRepo},
		{ID: "b", Scope: check.ScopeLive},
		{ID: "c", Scope: check.ScopeApplied},
	}
	rep := e.Execute(context.Background(), checks)

	if len(ran) != 1 || ran[0] != "a" {
		t.Fatalf("runner saw %v, want only [a]", ran)
	}
	if r, s := rep.Coverage(); r != 1 || s != 2 {
		t.Fatalf("coverage = ran %d skipped %d, want 1 and 2", r, s)
	}
}

func TestExecutePassesRoot(t *testing.T) {
	var gotRoot string
	e := &Engine{Root: "/repo", Run: func(_ context.Context, root string, c check.Check) check.Result {
		gotRoot = root
		return check.Result{Check: c, Verdict: check.VerdictTrue}
	}}
	e.Execute(context.Background(), []check.Check{{ID: "a", Scope: check.ScopeRepo}})
	if gotRoot != "/repo" {
		t.Fatalf("root = %q", gotRoot)
	}
}

func TestNewUsesTheCheckProtocol(t *testing.T) {
	e := New(t.TempDir(), nil)
	rep := e.Execute(context.Background(), []check.Check{{ID: "a", Scope: check.ScopeRepo, Run: []string{"false"}}})
	if rep.Results[0].Verdict != check.VerdictFalse {
		t.Fatalf("verdict = %s, want false from exit 1", rep.Results[0].Verdict)
	}
}

// For any checks and providers: one result per check, in input order, and a
// check is skipped exactly when the providers cannot reach its scope.
func TestPropertyOneResultPerCheckInOrder(t *testing.T) {
	scopes := []check.Scope{check.ScopeRepo, check.ScopeLive, check.ScopeApplied}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(t, "n")
		checks := make([]check.Check, n)
		for i := range checks {
			checks[i] = check.Check{ID: string(rune('a' + i)), Scope: rapid.SampledFrom(scopes).Draw(t, "scope")}
		}
		caps := provider.Capabilities{Read: rapid.Bool().Draw(t, "read"), Predict: rapid.Bool().Draw(t, "predict")}

		var ran []string
		e := &Engine{Providers: []provider.Provider{fakeProvider{caps}}, Run: alwaysTrue(&ran)}
		rep := e.Execute(context.Background(), checks)

		if len(rep.Results) != n {
			t.Fatalf("%d results for %d checks", len(rep.Results), n)
		}
		for i, res := range rep.Results {
			if res.Check.ID != checks[i].ID {
				t.Fatalf("result %d is %s, want %s", i, res.Check.ID, checks[i].ID)
			}
			reachable, _ := provider.Reaches(caps, checks[i].Scope)
			if res.Skipped() == reachable {
				t.Fatalf("check %s scope %s caps %+v: skipped=%v", res.Check.ID, checks[i].Scope, caps, res.Skipped())
			}
		}
		_, skipped := rep.Coverage()
		if len(ran) != n-skipped {
			t.Fatalf("runner ran %d checks, want %d", len(ran), n-skipped)
		}
	})
}
