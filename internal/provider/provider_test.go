package provider

import (
	"errors"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

type fake struct {
	name string
	caps Capabilities
}

func (f fake) Name() string               { return f.name }
func (f fake) Capabilities() Capabilities { return f.caps }
func (fake) Reconciler() ReconcilerModel  { return ReconcilerModel{} }

// faulty is a provider errata found and cannot run.
type faulty struct{ fake }

func (faulty) Fault() error { return errors.New("the build differs from its pin") }

func (faulty) Lacks(string) string { return "" }

// explained is a provider that gives its own reasons, as the plugin host's
// does.
type explained struct {
	fake
	why string
}

func (explained) Fault() error { return nil }

func (e explained) Lacks(op string) string {
	if e.caps.has(op) {
		return ""
	}
	return e.why
}

// A check reaches its scope when the provider it names is installed and
// has the operation the scope needs: none for repo, read for live and
// predict for applied; a provider that explains itself gives the gap's
// reason. Planted breaks: applied needing read; the name not compared, so
// any provider served every check; Lacks not asked.
func TestReach(t *testing.T) {
	k := func(caps Capabilities) []Provider { return []Provider{fake{"kube", caps}} }
	cases := []struct {
		ps    []Provider
		name  string
		scope check.Scope
		gap   string
	}{
		{nil, "", check.ScopeRepo, ""},
		{k(Capabilities{Read: true}), "kube", check.ScopeLive, ""},
		{k(Capabilities{Predict: true}), "kube", check.ScopeApplied, ""},
		{nil, "kube", check.ScopeLive, "no installed plugin provides read: no provider named kube was loaded"},
		{k(Capabilities{Read: true, Predict: true}), "other", check.ScopeApplied, "no installed plugin provides predict: no provider named other was loaded"},
		{k(Capabilities{Read: true}), "kube", check.ScopeApplied, "no installed plugin provides predict: provider kube provides read"},
		{k(Capabilities{Render: true, Predict: true}), "kube", check.ScopeLive, "no installed plugin provides read: provider kube provides render, predict"},
		{k(Capabilities{}), "kube", check.ScopeLive, "no installed plugin provides read: provider kube provides none of render, read and predict"},
		{[]Provider{explained{fake{"kube", Capabilities{Read: true}}, "it is away"}}, "kube", check.ScopeApplied, "no installed plugin provides predict: it is away"},
		{[]Provider{explained{fake{"kube", Capabilities{Read: true}}, "it is away"}}, "kube", check.ScopeLive, ""},
		{nil, "kube", "other", "unknown scope other"},
	}
	for _, tc := range cases {
		gap, fault := Reach(tc.ps, tc.name, tc.scope)
		if gap != tc.gap || fault != nil {
			t.Errorf("Reach(%v, %s, %s) = %q, %v, want %q", tc.ps, tc.name, tc.scope, gap, fault, tc.gap)
		}
	}
}

// A provider that says it cannot run makes Reach return its fault and no
// gap, whatever it would provide. Planted break: the Fault method not
// asked, which made a faulty provider a skip.
func TestReachReturnsAFault(t *testing.T) {
	gap, fault := Reach([]Provider{faulty{fake{"kube", Capabilities{Read: true}}}}, "kube", check.ScopeLive)
	if gap != "" || fault == nil || fault.Error() != "the build differs from its pin" {
		t.Fatalf("Reach = %q, %v", gap, fault)
	}
	if gap, fault := Reach([]Provider{faulty{fake{"kube", Capabilities{Read: true}}}}, "kube", check.ScopeRepo); gap != "" || fault != nil {
		t.Fatalf("a repo check reached %q, %v", gap, fault)
	}
}

func genCaps() *rapid.Generator[Capabilities] {
	return rapid.Custom(func(t *rapid.T) Capabilities {
		return Capabilities{rapid.Bool().Draw(t, "render"), rapid.Bool().Draw(t, "read"), rapid.Bool().Draw(t, "predict")}
	})
}

// Installing another provider never takes a scope away from a check, and a
// check reaches its scope exactly when the provider it names has the
// operation.
func TestPropertyAnotherProviderTakesNothingAway(t *testing.T) {
	names := []string{"a", "b", "c"}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, len(names)).Draw(t, "n")
		var ps []Provider
		for _, name := range names[:n] {
			ps = append(ps, fake{name, genCaps().Draw(t, "caps")})
		}
		name := rapid.SampledFrom(names).Draw(t, "name")
		scope := rapid.SampledFrom(check.Scopes).Draw(t, "scope")
		gap, _ := Reach(ps, name, scope)
		want := scope == check.ScopeRepo
		for _, p := range ps {
			if p.Name() == name {
				want = want || p.Capabilities().has(operations[scope])
			}
		}
		if (gap == "") != want {
			t.Fatalf("Reach(%v, %s, %s) = %q, want reached %v", ps, name, scope, gap, want)
		}
		more := slices.Concat(ps, []Provider{fake{"d", genCaps().Draw(t, "extra")}})
		if again, _ := Reach(more, name, scope); gap == "" && again != "" {
			t.Fatalf("adding a provider lost %s for %s: %q", scope, name, again)
		}
	})
}
