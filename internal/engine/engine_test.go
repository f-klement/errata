package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// The engine's SkipsFail reaches the report, which every output and the exit code read.
func TestExecuteCarriesSkipsFail(t *testing.T) {
	var ran []string
	for _, want := range []bool{false, true} {
		e := &Engine{Run: alwaysTrue(&ran), SkipsFail: want}
		rep := e.Execute(context.Background(), []check.Check{{ID: "b", Scope: check.ScopeLive}})
		if rep.SkipsFail != want || rep.Failed() != want {
			t.Fatalf("engine SkipsFail %v gave report SkipsFail %v and Failed %v", want, rep.SkipsFail, rep.Failed())
		}
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
	rep := e.Execute(context.Background(), []check.Check{{ID: "a", Scope: check.ScopeRepo, Kind: &check.Program{Run: []string{"false"}}}})
	if rep.Results[0].Verdict != check.VerdictFalse {
		t.Fatalf("verdict = %s, want false from exit 1", rep.Results[0].Verdict)
	}
}

type value string

func (v value) Extract(context.Context, string) ([]check.Value, error) {
	return []check.Value{{Text: string(v), At: check.Location{File: "f.yaml", Line: 1}}}, nil
}

func (v value) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return v.Extract(ctx, root)
}

func agreeing(id string, a, b value) check.Check {
	return check.Check{ID: id, Scope: check.ScopeRepo, Kind: &check.Rule{
		Extractors: []check.NamedExtractor{{Name: "a", Extractor: a}, {Name: "b", Extractor: b}},
		Agree:      []string{"a", "b"},
	}}
}

// One run holds program checks and rules: New sends each to its runner, and
// the report has one result per check and one coverage count.
func TestNewRunsRulesAndProgramChecks(t *testing.T) {
	e := New(t.TempDir(), nil)
	rep := e.Execute(context.Background(), []check.Check{
		{ID: "program", Scope: check.ScopeRepo, Kind: &check.Program{Run: []string{"false"}}},
		agreeing("agrees", "x", "x"),
		agreeing("differs", "x", "y"),
		{ID: "live-program", Scope: check.ScopeLive, Kind: &check.Program{Run: []string{"true"}}},
	})
	want := []check.Verdict{check.VerdictFalse, check.VerdictTrue, check.VerdictFalse, ""}
	for i, res := range rep.Results {
		if res.Verdict != want[i] {
			t.Errorf("%s: verdict %q, want %q (%s)", res.Check.ID, res.Verdict, want[i], res.Message)
		}
	}
	if r, s := rep.Coverage(); r != 3 || s != 1 {
		t.Fatalf("coverage = ran %d skipped %d, want 3 and 1", r, s)
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

// New sends a generated check to the generator runner: a generator whose
// output matches the committed file holds, where protocol.Run would have
// read its exit code, and one whose output differs is an erratum. Without a
// provider the check runs, since its scope is repo, so --fail-on-skip never
// sees it skipped. The planted break: Dispatch sending a generated check to
// protocol.Run, which ran nothing, since its Run is empty, and was unknown.
func TestNewRunsAGeneratedCheck(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "out.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := func(id, printed string) check.Check {
		return check.Check{ID: id, Scope: check.ScopeRepo, Timeout: 5 * time.Second, Kind: &check.Generated{
			File: "out.txt", Run: []string{"printf", printed}, MaxBytes: 1 << 10,
		}}
	}
	e := New(root, nil)
	e.SkipsFail = true
	rep := e.Execute(context.Background(), []check.Check{gen("current", `x\n`), gen("stale", `y\n`)})
	want := []check.Verdict{check.VerdictTrue, check.VerdictFalse}
	for i, res := range rep.Results {
		if res.Verdict != want[i] {
			t.Errorf("%s: verdict %q, want %q (%s)", res.Check.ID, res.Verdict, want[i], res.Message)
		}
	}
	if r, s := rep.Coverage(); r != 2 || s != 0 || rep.FailingSkips() != 0 {
		t.Fatalf("coverage = ran %d skipped %d, failing skips %d, want 2, 0 and 0", r, s, rep.FailingSkips())
	}
}
