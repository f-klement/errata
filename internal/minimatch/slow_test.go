package minimatch

import "testing"

// slowOnly skips t under -race and -short, and make slow runs it once,
// plain, from the Makefile's SLOW_TESTS, which scripts/slowtests lists from
// the tests that call it. It is for the cases the race detector and make
// test's coverage stretch past what the package's 2 minute timeout leaves
// on the slowest node: the brace sets at brace-expansion's depth bound took
// 50 s there under make test's flags, and 0.5 s plain. Unlike the slowOnly
// of other packages it does not skip under the mutation tag: these tests
// hold cases no other test holds, the depth bound among them, and Gremlins
// ran them before they moved here.
func slowOnly(t *testing.T) {
	t.Helper()
	if testing.Short() || raceEnabled {
		t.Skip("too slow under the race detector; make slow runs it")
	}
}

// raceSample is the share of an oracle table make test checks: every
// raceSample-th case, from the first. The test's slowOnly twin checks the
// rest, so each case runs on every push, and both run under mutation.
const raceSample = 8

// sampled is whether case i is among those make test checks.
func sampled(i int) bool { return i%raceSample == 0 }
