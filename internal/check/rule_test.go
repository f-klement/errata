package check

import (
	"strings"
	"testing"
	"time"
)

type noValues struct{}

func (noValues) Extract(string) ([]Value, error) { return nil, nil }
func (noValues) ExtractWith(string, map[string][]Value) ([]Value, error) {
	return nil, nil
}

// computing describes c's extractors as ones that can return a not literal
// value, as a shell or CI reader's are.
func computing(c *Check) {
	for i := range c.Rule().Extractors {
		c.Rule().Extractors[i].Description.NotLiteral = true
	}
}

func validRule() Check {
	return Check{
		ID:     "endpoint-agrees",
		Reason: "a reason",
		Scope:  ScopeRepo,
		Kind: &Rule{
			Extractors: []NamedExtractor{{Name: "secret", Extractor: noValues{}}, {Name: "service", Extractor: noValues{}}},
			Agree:      []string{"service", "secret"},
		},
	}
}

// record makes the rule's extractor secret return records of the fields
// name and group.
func record(c *Check) {
	c.Rule().Extractors[0].Description.Shape, c.Rule().Extractors[0].Description.Fields = ShapeRecord, []string{"name", "group"}
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
		{"valid assert", func(c *Check) { c.Rule().Agree = nil; c.Rule().Assert = "service == secret" }, ""},
		{"valid normalize", func(c *Check) { c.Rule().Normalize = []Normalization{NormalizeLower, NormalizeTrim} }, ""},
		{"valid allow empty", func(c *Check) { c.Rule().AllowEmpty = []string{"secret"} }, ""},
		{"agree names an extractor twice", func(c *Check) { c.Rule().Agree = []string{"service", "secret", "service"} }, `agree names "service" twice`},
		{"allow empty names an extractor twice", func(c *Check) { c.Rule().AllowEmpty = []string{"secret", "secret"} }, `allow_empty names "secret" twice`},
		{"allow empty names an undeclared extractor", func(c *Check) { c.Rule().AllowEmpty = []string{"configmap"} }, `allow_empty names "configmap", which extract does not declare`},
		{"valid skip not literal", func(c *Check) { computing(c); c.Rule().SkipNotLiteral = []string{"service", "secret"} }, ""},
		{"skip not literal names an extractor twice", func(c *Check) { computing(c); c.Rule().SkipNotLiteral = []string{"secret", "service", "secret"} }, `skip_not_literal names "secret" twice`},
		{"skip not literal names an undeclared extractor", func(c *Check) { computing(c); c.Rule().SkipNotLiteral = []string{"configmap"} }, `skip_not_literal names "configmap", which extract does not declare`},
		{"skip not literal names a literal only extractor", func(c *Check) {
			computing(c)
			c.Rule().Extractors[1].Description.NotLiteral = false
			c.Rule().SkipNotLiteral = []string{"service", "secret"}
		}, `skip_not_literal names "service", whose extractor returns only literal values, so it has nothing to leave out`},
		{"live rule", func(c *Check) { c.Scope = ScopeLive }, `scope "live": a rule reads files, so its scope is repo; a live extractor comes with a provider`},
		{"applied rule", func(c *Check) { c.Scope = ScopeApplied }, `scope "applied": a rule reads files, so its scope is repo`},
		{"timeout on a rule", func(c *Check) { c.Timeout = time.Second }, "timeout applies to a program check and a generated check"},
		{"no extractor", func(c *Check) { c.Rule().Extractors = nil; c.Rule().Agree = nil; c.Rule().Assert = "x" }, "extract is required for a rule"},
		{"neither agree nor assert", func(c *Check) { c.Rule().Agree = nil }, "a rule needs agree or assert"},
		{"empty agree", func(c *Check) { c.Rule().Agree = []string{} }, "a rule needs agree or assert"},
		{"blank assert", func(c *Check) { c.Rule().Agree = nil; c.Rule().Assert = " \n" }, "a rule needs agree or assert"},
		{"agree and assert", func(c *Check) { c.Rule().Assert = "true" }, "agree and assert are both set"},
		{"agree names an undeclared extractor", func(c *Check) { c.Rule().Agree = append(c.Rule().Agree, "configmap") }, `agree names "configmap", which extract does not declare`},
		{"agree names a map extractor", func(c *Check) { c.Rule().Extractors[0].Description.Shape = ShapeMap },
			`agree names "secret", whose extractor hands assert each mapping whole (as: map); agree compares flat values, so write an assert, or leave as: map out`},
		{"valid agree over a field of records", func(c *Check) { record(c); c.Rule().Agree = []string{"service", "secret.group"} }, ""},
		{"valid agree over two fields of records", func(c *Check) {
			record(c)
			c.Rule().Extractors = c.Rule().Extractors[:1]
			c.Rule().Agree = []string{"secret.name", "secret.group"}
		}, ""},
		{"agree names a record extractor whole", func(c *Check) { record(c) },
			`agree names "secret", whose extractor returns records (as: record); agree compares flat values, so name one field, such as secret.name, or write an assert`},
		{"agree names a field records lack", func(c *Check) { record(c); c.Rule().Agree = []string{"service", "secret.url"} },
			`agree names "secret.url", but the records of extractor "secret" have no field "url"; their fields are name, group`},
		{"agree names a field of a flat extractor", func(c *Check) { c.Rule().Agree = []string{"service", "secret.group"} },
			`agree names "secret.group", but extractor "secret" returns no record, so it has no field "group"`},
		{"agree names a field twice", func(c *Check) { record(c); c.Rule().Agree = []string{"service", "secret.name", "secret.name"} },
			`agree names "secret.name" twice`},
		{"an extractor's name with a dot that is no field wins", func(c *Check) {
			record(c)
			c.Rule().Extractors = append(c.Rule().Extractors, NamedExtractor{Name: "secret.url", Extractor: noValues{}})
			c.Rule().Agree = []string{"service", "secret.url", "secret.group"}
		}, ""},
		{"an extractor's name that is also a field of records", func(c *Check) {
			record(c)
			c.Rule().Extractors = append(c.Rule().Extractors, NamedExtractor{Name: "secret.name", Extractor: noValues{}})
			c.Rule().Agree = []string{"service", "secret.name", "secret.group"}
		}, `agree names "secret.name", which is both an extractor and field "name" of the records of extractor "secret"; rename the extractor, so each entry names one`},
		// A dot at the start of an entry still splits it: the extractor
		// named "" that errata.yaml's key "" declares has the field.
		{"a record extractor named the empty string", func(c *Check) {
			record(c)
			c.Rule().Extractors[0].Name = ""
			c.Rule().Agree = []string{"service", ".group"}
		}, ""},
		{"an extractor's name that is also a field of records named the empty string", func(c *Check) {
			record(c)
			c.Rule().Extractors[0].Name = ""
			c.Rule().Extractors = append(c.Rule().Extractors, NamedExtractor{Name: ".group", Extractor: noValues{}})
			c.Rule().Agree = []string{"service", ".group", ".name"}
		}, `agree names ".group", which is both an extractor and field "group" of the records of extractor ""`},
		{"a record extractor's name with a dot splits at the last dot", func(c *Check) {
			record(c)
			c.Rule().Extractors[0].Name = "a.b"
			c.Rule().Extractors[0].Description.Fields = []string{"c"}
			c.Rule().Agree = []string{"service", "a.b.c"}
		}, ""},
		{"valid assert over a map extractor", func(c *Check) {
			c.Rule().Extractors[0].Description.Shape = ShapeMap
			c.Rule().Agree = nil
			c.Rule().Assert = "x"
		}, ""},
		{"extractor agree leaves out", func(c *Check) { c.Rule().Agree = []string{"service"} }, `extractor "secret" is declared but agree does not name it`},
		{"valid compare semver", func(c *Check) { c.Rule().Compare = CompareSemver }, ""},
		{"valid compare text", func(c *Check) { c.Rule().Compare = CompareText }, ""},
		{"unknown compare", func(c *Check) { c.Rule().Compare = "version" }, `compare "version" must be one of text, semver`},
		{"compare on assert", func(c *Check) { c.Rule().Agree = nil; c.Rule().Assert = "x"; c.Rule().Compare = CompareText },
			`compare "text" applies to agree; assert compares in its expression, with semver() and semver_satisfies() for versions`},
		{"unknown normalize", func(c *Check) { c.Rule().Normalize = []Normalization{NormalizeTrim, "upper"} }, `normalize "upper" must be one of trim, lower`},
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
	c.Rule().Agree = nil
	c.Rule().Assert = "service == 'x'"
	if err := c.Validate(); err != nil {
		t.Fatalf("assert rule rejected: %v", err)
	}
}

func TestValidateRuleReportsEveryProblem(t *testing.T) {
	c := validRule()
	c.Timeout = time.Second
	c.Rule().Agree = []string{"configmap"}
	c.Rule().Normalize = []Normalization{"upper"}
	err := c.Validate()
	if err == nil {
		t.Fatal("broken rule validated")
	}
	for _, want := range []string{"timeout applies to a program check and a generated check", `agree names "configmap"`, `extractor "secret" is declared`, `extractor "service" is declared`, `normalize "upper"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// A rule with no compare compares as text; one with compare, by it.
func TestComparison(t *testing.T) {
	for _, c := range []struct{ set, want Compare }{{"", CompareText}, {CompareText, CompareText}, {CompareSemver, CompareSemver}} {
		if got := (Rule{Compare: c.set}).Comparison(); got != c.want {
			t.Errorf("Comparison() of compare %q = %q, want %q", c.set, got, c.want)
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
// skip_not_literal does not report it a second time, and one whose
// description says it returns literal values only is refused.
func TestValidateSkipNotLiteralKinds(t *testing.T) {
	c := validRule()
	c.Rule().Extractors[0].Extractor = nil
	c.Rule().SkipNotLiteral = []string{"secret"}
	if err := c.Validate(); err != nil {
		t.Fatalf("an extractor that did not build: %v", err)
	}
	c.Rule().Extractors[0].Extractor = noValues{}
	err := c.Validate()
	if want := `skip_not_literal names "secret", whose extractor returns only literal values`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	// With its kind, the message names it.
	c.Rule().Extractors[0].Description.Label = "ini, git dialect on .gitconfig"
	err = c.Validate()
	if want := `skip_not_literal names "secret", whose kind of extractor, ini, git dialect on .gitconfig, returns only literal values, so it has nothing to leave out`; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}
