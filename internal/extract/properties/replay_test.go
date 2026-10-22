package properties

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// propertiesReplays are the densest shapes measured for the reader, built at
// size bytes. Neither is quadratic, so each is bounded by
// extracttest.OtherBound. make test reads them at 16 KiB and make slow at
// 1 MiB. TestReplayINIShapes is the same for the ini dialects.
func propertiesReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	return []extracttest.Replay[Spec]{
		{Name: "a value joined over many lines", Spec: props("a"),
			Body: func() string { return "a=" + fill("x\\\n  ") }, Values: 1, Last: strings.Repeat("x", size/5)},
		{Name: "unicode escapes", Spec: props("a"),
			Body: func() string { return "a=" + fill("\\u0041") }, Values: 1, Last: strings.Repeat("A", size/6)},
	}
}

// runPropertiesReplays reads each shape at size within
// extracttest.OtherBound.
func runPropertiesReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, propertiesReplays(size), func(Spec) string { return "a.properties" })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayPropertiesShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runPropertiesReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayPropertiesShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runPropertiesReplays(t, 1<<20)
}
