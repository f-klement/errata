package rule

import "testing"

// slowOnly skips t under -race, -short and Gremlins' mutation tag, and make
// slow runs it once, plain, from the Makefile's SLOW_TESTS, which
// scripts/slowtests lists from the tests that call it. It is for a test
// whose point is wall time, which the race detector and make test's
// coverage stretch: each about ten times for a test that matches globs.
func slowOnly(t *testing.T) {
	t.Helper()
	if testing.Short() || raceEnabled || mutationRun {
		t.Skip("its point is wall time; make slow runs it")
	}
}
