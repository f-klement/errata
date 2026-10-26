package extracttest

import (
	"testing"
	"time"
)

// A case measured past OtherBound carries its own Bound, and both runners
// read it. RunShapes once took OtherBound for every case, whatever its
// Bound, and shell's deep pipelines need 2 s on a loaded runner.
func TestBoundIsTheCasesOwnOrOtherBound(t *testing.T) {
	if got := boundOf(Replay[struct{}]{}); got != OtherBound {
		t.Errorf("a case with no Bound: %v, want OtherBound, %v", got, OtherBound)
	}
	if got := boundOf(Replay[struct{}]{Bound: 2 * time.Second}); got != 2*time.Second {
		t.Errorf("a case with a Bound of 2 s: %v", got)
	}
}
