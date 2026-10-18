package check

import (
	"strings"
	"testing"
	"time"
)

type noValues struct{}

func (noValues) Extract(string) ([]Value, error) { return nil, nil }

// computes is an extractor whose kind declares that it can return a not
// literal value, as a shell or CI reader will.
type computes struct{ noValues }

func (computes) MayReturnNotLiteral() bool { return true }

// computing makes c's extractors ones that can return a not literal value.
func computing(c *Check) {
	for i := range c.Rule.Extractors {
		c.Rule.Extractors[i].Extractor = computes{}
	}
}

func validRule() Check {
	return Check{
		ID:     "endpoint-agrees",
		Reason: "a reason",
		Scope:  ScopeRepo,
		Rule: &Rule{
			Extractors: []NamedExtractor{{Name: "secret", Extractor: noValues{}}, {Name: "service", Extractor: noValues{}}},
			Agree:      []string{"service", "secret"},
		},
	}
}

// Each case plants one break in an otherwise valid rule and names the refusal
// it must produce.
func TestValidateRule(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Check)
		wantErr string
	}{
		{"valid agree", func(*Check) {}, ""},
		{"valid assert", func(c *Check) { c.Rule.Agree = nil; c.Rule.Assert = "service == secret" }, ""},
		{"valid normalize", func(c *Check) { c.Rule.Normalize = []Normalization{NormalizeLower, NormalizeTrim} }, ""},
		{"valid allow empty", func(c *Check) { c.Rule.AllowEmpty = []string{"secret"} }, ""},
		{"agree names an extractor twice", func(c *Check) { c.Rule.Agree = []string{"service", "secret", "service"} }, `agree names "service" twice`},
		{"allow empty names an extractor twice", func(c *Check) { c.Rule.AllowEmpty = []string{"secret", "secret"} }, `allow_empty names "secret" twice`},
		{"allow empty names an undeclared extractor", func(c *Check) { c.Rule.AllowEmpty = []string{"configmap"} }, `allow_empty names "configmap", which extract does not declare`},
		{"valid skip not literal", func(c *Check) { computing(c); c.Rule.SkipNotLiteral = []string{"service", "secret"} }, ""},
		{"skip not literal names an extractor twice", func(c *Check) { computing(c); c.Rule.SkipNotLiteral = []string{"secret", "service", "secret"} }, `skip_not_literal names "secret" twice`},
		{"skip not literal names an undeclared extractor", func(c *Check) { computing(c); c.Rule.SkipNotLiteral = []string{"configmap"} }, `skip_not_literal names "configmap", which extract does not declare`},
		{"skip not literal names a literal only extractor", func(c *Check) {
			computing(c)
			c.Rule.Extractors[1].Extractor = noValues{}
			c.Rule.SkipNotLiteral = []string{"service", "secret"}
		}, `skip_not_literal names "service", whose extractor returns only literal values, so it has nothing to leave out`},
		{"live rule", func(c *Check) { c.Scope = ScopeLive }, `scope "live": a rule reads files, so its scope is repo; a live extractor comes with a provider`},
		{"applied rule", func(c *Check) { c.Scope = ScopeApplied }, `scope "applied": a rule reads files, so its scope is repo`},
		{"run and extract", func(c *Check) { c.Run = []string{"true"} }, "a check is a program check (run) or a rule (extract), never both"},
		{"timeout on a rule", func(c *Check) { c.Timeout = time.Second }, "timeout applies to a program check only"},
		{"no extractor", func(c *Check) { c.Rule.Extractors = nil; c.Rule.Agree = nil; c.Rule.Assert = "x" }, "extract is required for a rule"},
		{"neither agree nor assert", func(c *Check) { c.Rule.Agree = nil }, "a rule needs agree or assert"},
		{"empty agree", func(c *Check) { c.Rule.Agree = []string{} }, "a rule needs agree or assert"},
		{"blank assert", func(c *Check) { c.Rule.Agree = nil; c.Rule.Assert = " \n" }, "a rule needs agree or assert"},
		{"agree and assert", func(c *Check) { c.Rule.Assert = "true" }, "agree and assert are both set"},
		{"agree names an undeclared extractor", func(c *Check) { c.Rule.Agree = append(c.Rule.Agree, "configmap") }, `agree names "configmap", which extract does not declare`},
		{"extractor agree leaves out", func(c *Check) { c.Rule.Agree = []string{"service"} }, `extractor "secret" is declared but agree does not name it`},
		{"unknown normalize", func(c *Check) { c.Rule.Normalize = []Normalization{NormalizeTrim, "upper"} }, `normalize "upper" must be one of trim, lower`},
		{"missing reason", func(c *Check) { c.Reason = "" }, "reason is required"},
		{"bad id", func(c *Check) { c.ID = "Garage" }, "lower-case words"},
		{"unknown scope", func(c *Check) { c.Scope = "cluster" }, `scope "cluster"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validRule()
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
			if !strings.Contains(err.Error(), `check "`+c.ID+`"`) {
				t.Fatalf("error = %v, want it to name the check", err)
			}
		})
	}
}

// With assert, an extractor that no agree names is not left out: the
// expression refers to it. The unused-extractor refusal is agree's alone.
func TestValidateAssertNeedsNoAgreeCoverage(t *testing.T) {
	c := validRule()
	c.Rule.Agree = nil
	c.Rule.Assert = "service == 'x'"
	if err := c.Validate(); err != nil {
		t.Fatalf("assert rule rejected: %v", err)
	}
}

func TestValidateRuleReportsEveryProblem(t *testing.T) {
	c := validRule()
	c.Run = []string{"true"}
	c.Rule.Agree = []string{"configmap"}
	c.Rule.Normalize = []Normalization{"upper"}
	err := c.Validate()
	if err == nil {
		t.Fatal("broken rule validated")
	}
	for _, want := range []string{"never both", `agree names "configmap"`, `extractor "secret" is declared`, `extractor "service" is declared`, `normalize "upper"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestIsRule(t *testing.T) {
	if valid().IsRule() {
		t.Error("program check reported as a rule")
	}
	if !validRule().IsRule() {
		t.Error("rule not reported as a rule")
	}
}

// A scope that is not a scope at all is one mistake, so a rule reports it
// once, not also as a scope other than repo.
func TestValidateRuleReportsABadScopeOnce(t *testing.T) {
	for _, scope := range []Scope{"", "Repo", "cluster"} {
		c := validRule()
		c.Scope = scope
		err := c.Validate()
		if err == nil {
			t.Fatalf("scope %q validated", scope)
		}
		if n := strings.Count(err.Error(), "scope "); n != 1 {
			t.Errorf("scope %q: %d scope errors in %q, want 1", scope, n, err)
		}
		if !strings.Contains(err.Error(), "must be one of repo, live, applied") {
			t.Errorf("scope %q: error %q does not name the scopes", scope, err)
		}
	}
}

// Normalized is the one home of the normalisation: the rule runner groups
// values with it, and a report that groups Extracted must get the same groups.
func TestNormalized(t *testing.T) {
	cases := []struct {
		normalize []Normalization
		in, want  string
	}{
		{nil, " S3.Internal\n", " S3.Internal\n"},
		{[]Normalization{NormalizeTrim}, " S3.Internal\n", "S3.Internal"},
		{[]Normalization{NormalizeLower}, " S3.Internal\n", " s3.internal\n"},
		{[]Normalization{NormalizeTrim, NormalizeLower}, " S3.Internal\n", "s3.internal"},
		{[]Normalization{NormalizeLower, NormalizeTrim}, " S3.Internal\n", "s3.internal"},
		{[]Normalization{NormalizeTrim}, "\u00a0x\t", "x"},
		{[]Normalization{NormalizeLower}, "ÄB", "äb"},
	}
	for _, tc := range cases {
		r := Rule{Normalize: tc.normalize}
		if got := r.Normalized(tc.in); got != tc.want {
			t.Errorf("Normalize %v: Normalized(%q) = %q, want %q", tc.normalize, tc.in, got, tc.want)
		}
	}
}

// An extractor that did not build is reported where it failed, so
// skip_not_literal does not report it a second time, and one that declares
// it cannot return a not literal value is refused as one that does not say.
func TestValidateSkipNotLiteralKinds(t *testing.T) {
	c := validRule()
	c.Rule.Extractors[0].Extractor = nil
	c.Rule.SkipNotLiteral = []string{"secret"}
	if err := c.Validate(); err != nil {
		t.Fatalf("an extractor that did not build: %v", err)
	}
	c.Rule.Extractors[0].Extractor = declaresLiteral{}
	err := c.Validate()
	if want := `skip_not_literal names "secret", whose extractor returns only literal values`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	// With its kind, the message names it.
	c.Rule.Extractors[0].Kind = "ini, git dialect on .gitconfig"
	err = c.Validate()
	if want := `skip_not_literal names "secret", whose kind of extractor, ini, git dialect on .gitconfig, returns only literal values, so it has nothing to leave out`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}

type declaresLiteral struct{ noValues }

func (declaresLiteral) MayReturnNotLiteral() bool { return false }
