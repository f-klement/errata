package hcl

import (
	"slices"
	"strings"
	"testing"
)

// slowOnly skips t under -race, -short and Gremlins' mutation tag, and make
// slow runs it once, plain, from the Makefile's SLOW_TESTS, which
// scripts/slowtests lists from the tests that call it. It is for a test
// whose file lexes to millions of tokens: about 500 MiB plain, several
// times that under the race detector, and five times that again when the
// review checks run a package under five shuffle seeds at once.
func slowOnly(t *testing.T) {
	t.Helper()
	if testing.Short() || raceEnabled || mutationRun {
		t.Skip("it lexes millions of tokens; make slow runs it")
	}
}

// The review's template, 65535 blank heredoc lines and then 507000 ${a}
// on one line, 2093546 bytes, makes 2093547 counted tokens, under the
// default limit of 2097152, and costs 3.8e10 to join. It took 65 s to
// parse, past errata's 30 s, before the cost counted the parts after a
// join; now it is refused before it is parsed. Its blank lines alone cost
// 4.6e9, past 2^32, so the interpolations are witnessed by the template
// test's rows instead. Planted break: read never checking the cost.
func TestTheReviewsTwoMegabyteTemplateIsRefused(t *testing.T) {
	slowOnly(t)
	data := "x = <<E\n" + strings.Repeat("\n", 65535) + strings.Repeat("${a}", 507000) + "\nE\n"
	want := []string{templateError(1, 32<<20)}
	if got := shown(extractPath(t, "x", file("f.tf", data))); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
