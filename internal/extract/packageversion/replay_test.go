package packageversion

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// packageVersionReplays are the densest shapes of each manifest, built at
// size bytes: VERSION's lines and fields, read in one pass, and the JSON and
// TOML manifests under their format's node limit. None is quadratic, so each
// is bounded by extracttest.OtherBound. make test reads them at 16 KiB and
// make slow at 1 MiB.
func packageVersionReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	return []extracttest.Replay[Spec]{
		{Name: "VERSION of comment lines", Spec: one("VERSION"),
			Body: func() string { return fill("#\n") + "1.0.0\n" }, Values: 1, Last: "1.0.0"},
		{Name: "VERSION of blank lines", Spec: one("a/VERSION"),
			Body: func() string { return fill("\r\n") + "1.0.0\n" }, Values: 1, Last: "1.0.0"},
		{Name: "VERSION of one line of many fields", Spec: one("b/VERSION"),
			Body: func() string { return fill("1 ") }, Values: 1, Last: "1"},
		{Name: "VERSION of one long version", Spec: one("c/VERSION"),
			Body: func() string { return fill("1") }, Values: 1, Last: "111"},
		{Name: "package.json of a long array", Spec: one("package.json"),
			Body: func() string { return `{"version": "1.0.0", "a": [` + fill("1,") + "1]}" }, Values: 1, Last: "1.0.0"},
		{Name: "Cargo.toml of many tables", Spec: one("Cargo.toml"),
			Body: func() string {
				return "[package]\nversion = \"1.0.0\"\n" + extracttest.RepeatLines(size/32, func(i int) string {
					return "[dependencies.d" + strconv.Itoa(i) + "]\nversion = \"1\"\n"
				})
			}, Values: 1, Last: "1.0.0"},
		{Name: "pyproject.toml of a long dynamic list", Spec: one("pyproject.toml"),
			Body: func() string { return "[project]\ndynamic = [" + fill(`"a",`) + "\"version\"]\n" }, Values: 1, Last: "dynamic"},
	}
}

// runPackageVersionReplays reads each shape at size within
// extracttest.OtherBound.
func runPackageVersionReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, build, packageVersionReplays(size), func(s Spec) string { return s.Files[0] })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayPackageVersionShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runPackageVersionReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayPackageVersionShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runPackageVersionReplays(t, 1<<20)
}
