package provider

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

type fake struct{ caps Capabilities }

func (fake) Name() string                 { return "fake" }
func (f fake) Capabilities() Capabilities { return f.caps }
func (fake) Reconciler() ReconcilerModel  { return ReconcilerModel{} }

func TestReaches(t *testing.T) {
	cases := []struct {
		caps  Capabilities
		scope check.Scope
		want  bool
	}{
		{Capabilities{}, check.ScopeRepo, true},
		{Capabilities{}, check.ScopeLive, false},
		{Capabilities{Read: true}, check.ScopeLive, true},
		{Capabilities{Read: true}, check.ScopeApplied, false},
		{Capabilities{Predict: true}, check.ScopeApplied, false},
		{Capabilities{Read: true, Predict: true}, check.ScopeApplied, true},
		{Capabilities{Render: true, Read: true, Predict: true}, "other", false},
	}
	for _, tc := range cases {
		ok, gap := Reaches(tc.caps, tc.scope)
		if ok != tc.want {
			t.Errorf("Reaches(%+v, %s) = %v, want %v", tc.caps, tc.scope, ok, tc.want)
		}
		if ok == (gap != "") {
			t.Errorf("Reaches(%+v, %s): ok=%v but gap=%q", tc.caps, tc.scope, ok, gap)
		}
	}
}

func genCaps() *rapid.Generator[Capabilities] {
	return rapid.Custom(func(t *rapid.T) Capabilities {
		return Capabilities{rapid.Bool().Draw(t, "render"), rapid.Bool().Draw(t, "read"), rapid.Bool().Draw(t, "predict")}
	})
}

// Adding a provider never takes a scope away, and the union has a capability
// exactly when some provider has it.
func TestPropertyUnion(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		all := rapid.SliceOf(genCaps()).Draw(t, "caps")
		ps := make([]Provider, len(all))
		var want Capabilities
		for i, c := range all {
			ps[i] = fake{c}
			want.Render, want.Read, want.Predict = want.Render || c.Render, want.Read || c.Read, want.Predict || c.Predict
		}
		got := Union(ps)
		if got != want {
			t.Fatalf("Union = %+v, want %+v", got, want)
		}
		extra := genCaps().Draw(t, "extra")
		bigger := Union(append(ps, fake{extra}))
		for _, s := range []check.Scope{check.ScopeRepo, check.ScopeLive, check.ScopeApplied} {
			before, _ := Reaches(got, s)
			after, _ := Reaches(bigger, s)
			if before && !after {
				t.Fatalf("adding %+v lost scope %s", extra, s)
			}
		}
	})
}
