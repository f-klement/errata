package check

import (
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func valid() Check {
	return Check{ID: "docs-name-node", Reason: "a reason", Scope: ScopeRepo, Kind: &Program{Run: []string{"true"}}}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Check)
		wantErr string
	}{
		{"valid", func(*Check) {}, ""},
		{"empty reason", func(c *Check) { c.Reason = "" }, "reason is required"},
		{"blank reason", func(c *Check) { c.Reason = " \n\t" }, "reason is required"},
		// Found by rapid: RE2's \S does not class \v as space, TrimSpace does.
		{"vertical tab reason", func(c *Check) { c.Reason = "\v" }, "reason is required"},
		{"no-break space reason", func(c *Check) { c.Reason = "\u00a0" }, "reason is required"},
		{"bad id", func(c *Check) { c.ID = "Docs_Node" }, "lower-case words"},
		{"trailing hyphen", func(c *Check) { c.ID = "docs-" }, "lower-case words"},
		{"double hyphen", func(c *Check) { c.ID = "a--b" }, "lower-case words"},
		{"unknown scope", func(c *Check) { c.Scope = "cluster" }, `scope "cluster"`},
		{"no run", func(c *Check) { c.Kind = &Program{} }, "run or extract is required"},
		{"no kind", func(c *Check) { c.Kind = nil }, "run or extract is required"},
		{"negative timeout", func(c *Check) { c.Timeout = -time.Second }, "timeout must not be negative"},
		{"zero timeout", func(c *Check) { c.Timeout = 0 }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	err := Check{}.Validate()
	if err == nil {
		t.Fatal("empty check validated")
	}
	for _, want := range []string{"id", "reason", "scope", "run or extract"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// Generators shared by the properties below.

func genScope() *rapid.Generator[Scope] {
	return rapid.SampledFrom([]Scope{ScopeRepo, ScopeLive, ScopeApplied})
}

func genID() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-z0-9]{1,8}(-[a-z0-9]{1,8}){0,3}`)
}

func genResult() *rapid.Generator[Result] {
	return rapid.Custom(func(t *rapid.T) Result {
		r := Result{Check: Check{ID: genID().Draw(t, "id"), Scope: genScope().Draw(t, "scope")}}
		if rapid.Bool().Draw(t, "skipped") {
			r.Gap = rapid.StringMatching(`[a-z ]{1,20}`).Draw(t, "skip")
		} else {
			r.Verdict = rapid.SampledFrom([]Verdict{VerdictTrue, VerdictFalse, VerdictUnknown}).Draw(t, "verdict")
		}
		return r
	})
}

func TestPropertyWellFormedCheckValidates(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := Check{
			ID:      genID().Draw(t, "id"),
			Reason:  rapid.StringMatching(`[A-Za-z0-9].{0,40}`).Draw(t, "reason"),
			Scope:   genScope().Draw(t, "scope"),
			Kind:    &Program{Run: rapid.SliceOfN(rapid.String(), 1, 4).Draw(t, "run")},
			Timeout: time.Duration(rapid.Int64Min(0).Draw(t, "timeout")),
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("well-formed check rejected: %v", err)
		}
	})
}

func TestPropertyReasonIsRequired(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := valid()
		c.Reason = rapid.StringOfN(rapid.SampledFrom([]rune{' ', '\t', '\n', '\r', '\v', '\f', '\u0085', '\u00a0', '\u2028', '\u3000'}), 0, 10, -1).Draw(t, "blank")
		if c.Validate() == nil {
			t.Fatalf("check with blank reason %q loaded", c.Reason)
		}
	})
}

func TestResultFailed(t *testing.T) {
	cases := []struct {
		res  Result
		want bool
	}{
		{Result{Verdict: VerdictTrue}, false},
		{Result{Verdict: VerdictFalse}, true},
		{Result{Verdict: VerdictUnknown}, true},
		{Result{Gap: "no provider can read a live system"}, false},
	}
	for _, tc := range cases {
		if got := tc.res.Failed(); got != tc.want {
			t.Errorf("%+v.Failed() = %v, want %v", tc.res, got, tc.want)
		}
	}
}

// A skip fails the run only when the report says skips fail; a run with no
// skip passes either way.
func TestReportSkipsFail(t *testing.T) {
	skip := Result{Gap: "no provider can read a live system"}
	pass := Result{Verdict: VerdictTrue}
	cases := []struct {
		rep  Report
		want bool
	}{
		{Report{Results: []Result{pass, skip}}, false},
		{Report{Results: []Result{pass, skip}, SkipsFail: true}, true},
		{Report{Results: []Result{pass}, SkipsFail: true}, false},
		{Report{SkipsFail: true}, false},
	}
	for _, tc := range cases {
		if got := tc.rep.Failed(); got != tc.want {
			t.Errorf("%+v.Failed() = %v, want %v", tc.rep, got, tc.want)
		}
	}
}

func TestPropertyReportCountsAddUp(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rep := Report{Results: rapid.SliceOf(genResult()).Draw(t, "results"), SkipsFail: rapid.Bool().Draw(t, "skips fail")}
		ran, skipped := rep.Coverage()
		if ran+skipped != len(rep.Results) {
			t.Fatalf("ran %d + skipped %d != %d results", ran, skipped, len(rep.Results))
		}
		errata, unknowns := rep.Errata(), rep.Unknowns()
		if len(errata)+len(unknowns) > ran {
			t.Fatalf("%d errata and %d unknown but only %d checks ran", len(errata), len(unknowns), ran)
		}
		// An unknown is never an erratum: its claim was not shown false.
		for _, e := range errata {
			if e.Verdict != VerdictFalse {
				t.Fatalf("errata contains %+v", e)
			}
		}
		for _, u := range unknowns {
			if u.Verdict != VerdictUnknown {
				t.Fatalf("unknowns contains %+v", u)
			}
		}
		var wantErrata, wantUnknown int
		for _, r := range rep.Results {
			switch r.Verdict {
			case VerdictFalse:
				wantErrata++
			case VerdictUnknown:
				wantUnknown++
			default: // VerdictTrue, or a skip with no verdict: neither fails the run
			}
		}
		if len(errata) != wantErrata || len(unknowns) != wantUnknown {
			t.Fatalf("errata %d unknown %d, want %d and %d", len(errata), len(unknowns), wantErrata, wantUnknown)
		}
		// The run fails on either, on a skip when skips fail, and only on these.
		wantSkips := 0
		if rep.SkipsFail {
			wantSkips = skipped
		}
		if rep.FailingSkips() != wantSkips {
			t.Fatalf("FailingSkips() = %d, want %d (SkipsFail %v, skipped %d)", rep.FailingSkips(), wantSkips, rep.SkipsFail, skipped)
		}
		if rep.Failed() != (wantErrata+wantUnknown+wantSkips > 0) {
			t.Fatalf("Failed() = %v with %d errata, %d unknown, %d skipped and SkipsFail %v", rep.Failed(), wantErrata, wantUnknown, skipped, rep.SkipsFail)
		}
	})
}
