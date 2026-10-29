package check

import (
	"strings"
	"testing"
)

func validGenerated() Check {
	return Check{ID: "architecture-is-current", Reason: "a reason", Scope: ScopeRepo,
		Generated: &Generated{File: "architecture.md", Run: []string{"python3", "build.py", "--stdout"}, MaxBytes: 1 << 20}}
}

// A generated check validates with a file, a generator and a positive
// bound, at scope repo, and with no run or rule beside it. Each refusal is
// asserted by its message. The planted breaks: each if of
// Generated.validate dropped in turn, which loaded the check it refuses,
// and Validate's last if reading only Rule, which refused every generated
// check as one with no run.
func TestValidateGenerated(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Check)
		wantErr string
	}{
		{"valid", func(*Check) {}, ""},
		{"run beside it", func(c *Check) { c.Run = []string{"true"} }, "run is set beside generated"},
		{"rule beside it", func(c *Check) { c.Rule = &Rule{} }, "a rule (extract) or a generated check (generated), never both"},
		{"live", func(c *Check) { c.Scope = ScopeLive }, `scope "live": a generated check compares a file of the repository`},
		{"no file", func(c *Check) { c.Generated.File = "" }, "generated.file is required"},
		{"no generator", func(c *Check) { c.Generated.Run = nil }, "generated.run lists no argument"},
		{"no bound", func(c *Check) { c.Generated.MaxBytes = 0 }, "generated.max_bytes is 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validGenerated()
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !c.IsGenerated() || c.IsRule() {
					t.Fatalf("IsGenerated %v, IsRule %v", c.IsGenerated(), c.IsRule())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// An unknown scope is refused once, by Validate, and not again as one that
// is not repo.
func TestValidateGeneratedUnknownScopeOnce(t *testing.T) {
	c := validGenerated()
	c.Scope = "cluster"
	err := c.Validate()
	if err == nil || strings.Contains(err.Error(), "so its scope is repo") {
		t.Fatalf("error = %v, want the unknown scope alone", err)
	}
}
