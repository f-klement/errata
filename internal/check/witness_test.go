package check

import (
	"context"
	"strings"
	"testing"
)

// The witness key loads one to eight names, each an id, none twice, on a
// check of scope repo, and refuses each other case by name. The planted
// breaks: each refusal removed in turn, the scope's, the name's, the
// count's and the repeat's.
func TestValidateWitnesses(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Check)
		wantErr string
	}{
		{"one", func(c *Check) { c.Witness = []string{"drift"} }, ""},
		{"eight", func(c *Check) { c.Witness = []string{"a", "b", "c", "d", "e", "f", "g", "h"} }, ""},
		{"nine", func(c *Check) { c.Witness = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} }, "witness names 9 fixtures, over the limit of 8"},
		{"slash", func(c *Check) { c.Witness = []string{"a/b"} }, `witness "a/b" must be lower-case words`},
		{"dots", func(c *Check) { c.Witness = []string{".."} }, `witness ".." must be lower-case words`},
		{"empty name", func(c *Check) { c.Witness = []string{""} }, `witness "" must be lower-case words`},
		{"twice", func(c *Check) { c.Witness = []string{"a", "a"} }, `witness names "a" twice`},
		{"live", func(c *Check) { c.Scope, c.Witness = ScopeLive, []string{"a"} }, `witness on scope "live"`},
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

// A path is a witness's when it is below a directory witness in a
// directory .errata, at the root or deeper, and no other. The planted
// break: only the root's, which kept sub/.errata/witness/x.
func TestWitnessed(t *testing.T) {
	for p, want := range map[string]bool{
		".errata/witness/a/x":     true,
		"sub/.errata/witness/a/x": true,
		".errata/witness":         false,
		".errata/other/x":         false,
		"x.errata/witness/a":      false,
		"a/.errata/witnesses/x":   false,
	} {
		if got := Witnessed(p); got != want {
			t.Errorf("Witnessed(%q) = %v, want %v", p, got, want)
		}
	}
}

// A run of a witness starts its programs in the real root, any other run
// in its own root. The planted break: WorkDir returning root always.
func TestWorkDir(t *testing.T) {
	if got := WorkDir(context.Background(), "/r"); got != "/r" {
		t.Errorf("a run starts in %q, want /r", got)
	}
	if got := WorkDir(OnWitness(context.Background(), "/r"), "/r/.errata/witness/a"); got != "/r" {
		t.Errorf("a witness starts in %q, want /r", got)
	}
}

// A witness's result is named after its check with the fixture, and the
// coverage counts it apart from the checks. The planted break: Coverage
// counting a witness as a check that ran.
func TestWitnessResults(t *testing.T) {
	c := valid()
	rep := Report{Results: []Result{
		{Check: c, Verdict: VerdictTrue},
		{Check: c, Verdict: VerdictTrue, Witness: "a"},
		{Check: c, Verdict: VerdictFalse, Witness: "b"},
	}}
	if ran, skipped := rep.Coverage(); ran != 1 || skipped != 0 {
		t.Errorf("coverage ran %d, skipped %d, want 1 and 0", ran, skipped)
	}
	if held, ran := rep.WitnessesHeld(); held != 1 || ran != 2 {
		t.Errorf("witnesses held %d of %d, want 1 of 2", held, ran)
	}
	if got := rep.Results[2].Name(); got != "docs-name-node [witness b]" {
		t.Errorf("name %q", got)
	}
	if got := rep.Results[0].Name(); got != "docs-name-node" {
		t.Errorf("name %q", got)
	}
}
