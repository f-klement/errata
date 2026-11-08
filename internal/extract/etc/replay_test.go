package etc

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// etcReplays are the densest shapes measured for the readers, built at
// size bytes: the shortest lines each dialect reads as a directive, under
// a key none sets, since a value for each would pass the value limit, a
// modprobe line joined over many, a chrony line of many words, and with
// match: an sshd file of one Match block and value after another, which
// asks each block once. None is quadratic, so each is bounded by
// extracttest.OtherBound. make test reads them at 16 KiB and make slow at
// 1 MiB.
func etcReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	return []extracttest.Replay[Spec]{
		{Name: "sshd directives", Spec: one("sshd_config", "z"), Body: func() string { return fill("a b\n") }},
		{Name: "sysctl directives", Spec: one("sysctl.conf", "z"), Body: func() string { return fill("a=b\n") }},
		{Name: "limits lines", Spec: one("limits.conf", "z"), Body: func() string { return fill("a b c d\n") }},
		{Name: "logrotate blocks", Spec: one("logrotate.conf", "z"), Body: func() string { return fill("/x {\na\n}\n") }},
		{Name: "modprobe joined lines", Spec: one("modprobe.d/m.conf", "options"), Body: func() string { return "options m " + fill("x\\\n") }, Values: 1, Last: "xxx"},
		{Name: "chrony words", Spec: one("chrony.conf", "a"), Body: func() string { return "a " + fill("b\t ") }, Values: 1, Last: "b b"},
		{Name: "sshd Match blocks with match:", Spec: matching("sshd_config", "a", map[string]string{"user": "u"}),
			Body: func() string { return fill("Match User x\na b\n") }, Values: 0},
	}
}

// runEtcReplays reads each shape at size within extracttest.OtherBound.
func runEtcReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, etcReplays(size), func(s Spec) string { return s.File })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayEtcShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runEtcReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayEtcShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runEtcReplays(t, 1<<20)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// shadowShape is the delta review's shape of n setters under Host blocks
// the connection does not match, then n Match lines of distinct users.
func shadowShape(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "Host nomatch%d\n  User x\n", i)
	}
	for i := range n {
		fmt.Fprintf(&b, "Match user u%d\n  IdentityFile /k%d\n", i, i)
	}
	return b.String()
}

// The Match decider decides each setter once, so a file of eight times the
// setters and Match lines takes about eight times as long, never the
// sixty-four a decision of every setter for every Match line takes. The
// bound is a ratio, so the shape stays small: 8000 of each is 500 KB.
// Planted break: shadow walking every setter before the block again, as
// the round-1 code did.
func TestMatchShadowIsLinear(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole read, which no mutant of an operator changes")
	}
	took := func(n int) time.Duration {
		root := extracttest.Repo(t, map[string]string{"ssh_config": shadowShape(n)})
		x, err := Build(matching("ssh_config", "identityfile", map[string]string{"user": "alice", "host": "h"}))
		if err != nil {
			t.Fatal(err)
		}
		best := time.Duration(1 << 62)
		for range 5 {
			start := time.Now()
			if _, err := x.Extract(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	small, large := took(1000), took(8000)
	t.Logf("1000 of each: %v, 8000 of each: %v, ratio %.1f", small, large, float64(large)/float64(small))
	if large > 24*small {
		t.Errorf("8000 of each took %v, over 24 times the %v of 1000", large, small)
	}
}
