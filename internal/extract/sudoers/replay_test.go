package sudoers

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// sudoersReplays are the densest shapes of the reader, built at size
// bytes: short Defaults lines of a setting none selects, a statement
// joined over many lines, whose line marks a search finds, a user
// specification of many commands, and a user list of many entries. None
// is quadratic, so each is bounded by extracttest.OtherBound. make test
// reads them at 16 KiB and make slow at 1 MiB.
func sudoersReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	return []extracttest.Replay[Spec]{
		{Name: "Defaults lines", Spec: defaults("sudoers", "z"), Body: func() string { return fill("Defaults a\n") }},
		{Name: "joined lines", Spec: user("sudoers", "z"), Body: func() string { return "a ALL = /x" + fill(", \\\n/x") + "\n" }},
		{Name: "commands", Spec: user("sudoers", "z"), Body: func() string { return "a ALL = /x" + fill(",/x") + "\n" }},
		{Name: "user list", Spec: user("sudoers", "a"), Body: func() string { return "b" + fill(",b") + " ALL = /x\n" }},
	}
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplaySudoersShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	extracttest.RunShapes(t, Build, sudoersReplays(16<<10), func(s Spec) string { return s.File })
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplaySudoersShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	extracttest.RunShapes(t, Build, sudoersReplays(1<<20), func(s Spec) string { return s.File })
}
