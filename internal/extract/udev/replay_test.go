package udev

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// udevReplays are the densest shapes measured for the reader, built at
// size bytes: the shortest rules under a key none assigns, a rule joined
// over every line, which udev drops at its line size, a rule joined over
// lines of a \ alone, which add a part each and no byte, a GOTO and its LABEL
// after another with tests:, pairs of GOTOs whose LABELs cross, whose ways
// meet again at the second LABEL, and GOTOs nested to one LABEL, which the
// work budget refuses once they are some hundreds deep. None is
// quadratic within the budget, so each is bounded by
// extracttest.OtherBound. make test reads them at 16 KiB and make slow at
// 1 MiB.
func udevReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	matched := Spec{Where: core.Where{File: "r.rules"}, Key: "MODE", Tests: map[string]string{"KERNEL": "b"}}
	return []extracttest.Replay[Spec]{
		{Name: "short rules", Spec: one("r.rules", "OWNER"), Body: func() string { return fill("MODE=\"1\"\n") }},
		{Name: "one joined rule", Spec: one("r.rules", "MODE"), Body: func() string { return fill("MODE=\"1\",\\\n") }},
		{Name: "lines of a \\ alone", Spec: one("r.rules", "MODE"), Body: func() string { return "MODE=\"1\"\\\\\\\n" + fill("\\\n") + "\n\n\n" }, Values: 1, Last: "1"},
		{Name: "GOTO after GOTO", Spec: matched, Body: func() string { return fill("KERNEL!=\"a\",GOTO=\"e\"\nMODE=\"1\"\nLABEL=\"e\"\n") }},
		{Name: "crossing GOTOs", Spec: one("r.rules", "MODE"), Body: func() string {
			return fill("KERNEL==\"a\",GOTO=\"x\"\nKERNEL==\"b\",GOTO=\"y\"\nLABEL=\"x\"\nLABEL=\"y\",MODE=\"1\"\n")
		}, Values: size / 71, Last: "1"},
		{Name: "nested GOTOs", Spec: one("r.rules", "MODE"), Body: func() string { return fill("KERNEL==\"a\",GOTO=\"e\"\n") + "LABEL=\"e\"\n" },
			Refused: "r.rules: reading the files and matching the spec take more than"},
	}
}

// runUdevReplays reads each shape at size within extracttest.OtherBound.
func runUdevReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, udevReplays(size), func(s Spec) string { return s.File })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayUdevShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runUdevReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayUdevShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runUdevReplays(t, 1<<20)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; the slow suite runs it.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}
