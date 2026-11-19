package engine

import (
	"testing"

	"github.com/f-klement/errata/internal/check"
)

// Only a rule leaves what the run kept: every other kind runs a program in
// the repository.
func TestMayWrite(t *testing.T) {
	for _, tc := range []struct {
		kind check.Kind
		want bool
	}{{&check.Rule{}, false}, {&check.Program{}, true}, {&check.Generated{}, true}} {
		if got := mayWrite(check.Check{Kind: tc.kind}); got != tc.want {
			t.Errorf("%T: mayWrite %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// Only a program check and a generated check start a program, and so run
// in a copy; a rule reads in the repository and a kind with no runner runs
// nothing. Planted break: runsProgram true for every kind, which copied the
// tree for a check with no kind.
func TestRunsProgram(t *testing.T) {
	for _, tc := range []struct {
		kind check.Kind
		want bool
	}{{&check.Rule{}, false}, {&check.Program{}, true}, {&check.Generated{}, true}, {nil, false}} {
		if got := runsProgram(check.Check{Kind: tc.kind}); got != tc.want {
			t.Errorf("%T: runsProgram %v, want %v", tc.kind, got, tc.want)
		}
	}
}
