package extracttest

import (
	"testing"
	"time"
)

// RaceSlowdown is how much the race detector slows a case; its bound is
// that many times longer under -race.
var RaceSlowdown = map[bool]time.Duration{false: 1, true: 10}[RaceEnabled]

// SlowOnly skips t under -race, -short and Gremlins' mutation tag: t reads
// a large input, which the race detector slows about ninefold and which
// make test's two minute timeout cannot hold, and which costs seconds for
// every mutant. make slow runs it once, plain, from the Makefile's
// SLOW_TESTS. Each package calls it through a slowOnly of its own, which is
// the call TestEverySlowTestIsInMakeSlow looks for in a test's body. A small
// input that reaches the same limit stays in make test beside each one.
func SlowOnly(t *testing.T) {
	t.Helper()
	if testing.Short() || RaceEnabled || MutationRun {
		t.Skip("reads a large input; make slow runs it")
	}
}
