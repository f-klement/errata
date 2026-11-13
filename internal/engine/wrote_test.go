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
