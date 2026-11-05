package dependency

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says. The Makefile's SLOW_TESTS lists the tests that
// call it, read by scripts/slowtests, which refuses a direct call of
// extracttest.SlowOnly.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// dependencyReplays are the densest shapes of each kind of file, built at
// size bytes: the line readers' lines, the Gemfile's strings and comments
// read through the lexer, and the JSON, TOML and YAML files under their
// format's node limit. None is quadratic, so each is bounded by
// extracttest.OtherBound. make test reads them at 16 KiB and make slow at
// 1 MiB.
func dependencyReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	return []extracttest.Replay[Spec]{
		{Name: "requirements of one long continued line", Spec: of("x", "requirements.txt"),
			Body: func() string { return "x==1 " + fill("\\\n") + "\n" }, Values: 1, Last: "1"},
		{Name: "requirements of other packages", Spec: of("x", "a/requirements.txt"),
			Body: func() string { return fill("y==1\n") + "x==2\n" }, Values: 1, Last: "2"},
		{Name: "Gemfile of strings", Spec: of("x", "Gemfile"),
			Body: func() string { return "gem \"x\", \"1\"\nfoo " + fill(`"a", `) + "\n" }, Values: 1, Last: "==1"},
		{Name: "Gemfile of comments inside a statement", Spec: of("x", "a/Gemfile"),
			Body: func() string { return "gem \"x\", " + fill("# c\n") + "\"2\"\n" }, Values: 1, Last: "==2"},
		{Name: "Gemfile of comments before a constant at the end", Spec: of("x", "b/Gemfile"),
			Body: func() string { return "gem \"x\", " + fill("# c\n") + "V\n" }, Values: 1, Last: "V"},
		{Name: "Gemfile.lock of specs", Spec: of("x", "Gemfile.lock"),
			Body: func() string { return "GEM\n  specs:\n" + fill("    y (1.0)\n") + "    x (2.0)\n" }, Values: 1, Last: "2.0"},
		{Name: "yarn.lock of many descriptors", Spec: of("x", "yarn.lock"),
			Body: func() string { return fill("y@^1, ") + "x@^1:\n  version \"1.0.0\"\n" }, Values: 1, Last: "1.0.0"},
		{Name: "package-lock.json of nested dependencies", Spec: of("x", "package-lock.json"),
			Body: func() string {
				n := min(size/48, 450)
				return `{"dependencies": ` + strings.Repeat(`{"x": {"version": "1", "dependencies": `, n) + "{}" + strings.Repeat("}}", n) + "}"
			}, Values: min(size/48, 450), Last: "1"},
		{Name: "Cargo.toml of many tables", Spec: of("x", "Cargo.toml"),
			Body: func() string {
				return extracttest.RepeatLines(size/32, func(i int) string {
					return "[target.t" + strconv.Itoa(i) + ".dependencies]\ny = \"1\"\n"
				}) + "[dependencies]\nx = \"2\"\n"
			}, Values: 1, Last: "^2"},
		{Name: "pnpm-lock.yaml of many packages", Spec: of("x", "pnpm-lock.yaml"),
			Body: func() string {
				return "lockfileVersion: '9.0'\npackages:\n" + extracttest.RepeatLines(size/16, func(i int) string { return "  y" + strconv.Itoa(i) + "@1: {}\n" }) + "  x@2.0.0: {}\n"
			}, Values: 1, Last: "2.0.0"},
	}
}

// runDependencyReplays reads each shape at size within
// extracttest.OtherBound.
func runDependencyReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, build, dependencyReplays(size), func(s Spec) string { return s.Files[0] })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayDependencyShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runDependencyReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayDependencyShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runDependencyReplays(t, 1<<20)
}
