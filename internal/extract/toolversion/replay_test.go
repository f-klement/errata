package toolversion

import (
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

// toolVersionReplays are the densest shapes of each kind of file, built at
// size bytes: the line reader's lines and fields, and modfile's lines and
// tokens, each go.mod under its limit of lines and tokens up to 1 MiB.
// None is quadratic, so each is bounded by extracttest.OtherBound.
// make test reads them at 16 KiB and make slow at 1 MiB. Measured at 32 MiB
// on a cluster node, the README's tool_version section gives the figures.
func toolVersionReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	at := func(tool, file string) Spec { return Spec{Tool: tool, Files: []string{file}} }
	versions := size / 2
	many := extracttest.Replay[Spec]{Name: "one line of many versions", Spec: at("node", ".tool-versions"),
		Body: func() string { return "node" + fill(" 1") }, Values: versions, Last: "1"}
	if versions > 100_000 {
		many.Values, many.Refused = 0, ".tool-versions: the extractor selects more than 100000 values, the limit"
	}
	// A comma is a token of its own: past the go.mod limit, at 1 MiB, the
	// count refuses the file before modfile builds a token of each.
	commas := extracttest.Replay[Spec]{Name: "go.mod of commas", Spec: at("go", "d/go.mod"),
		Body: func() string { return "module m\ngo 1.22\nx " + fill(",") + "\n" }, Refused: "d/go.mod:3: unknown directive: x"}
	if size > (32<<20)/goModBytesPerNode {
		commas.Refused = "d/go.mod: the file can hold up to"
	}
	return []extracttest.Replay[Spec]{
		{Name: "lines of other tools", Spec: at("node", "a/.tool-versions"),
			Body: func() string { return fill("python 3\n") + "node 20\n" }, Values: 1, Last: "20"},
		{Name: "comment lines", Spec: at("node", ".nvmrc"),
			Body: func() string { return fill("#\n") + "20\n" }, Values: 1, Last: "20"},
		{Name: "blank lines", Spec: at("python", ".python-version"),
			Body: func() string { return fill("\n") + "3.12\n" }, Values: 1, Last: "3.12"},
		{Name: "one long version", Spec: at("node", ".node-version"),
			Body: func() string { return fill("1") }, Values: 1, Last: "111"},
		many,
		{Name: "sdkmanrc lines of other candidates", Spec: at("java", ".sdkmanrc"),
			Body: func() string { return fill("a=1\n") + "java=21\n" }, Values: 1, Last: "21"},
		{Name: "go.mod of requirements", Spec: at("go", "go.mod"),
			Body: func() string { return "module m\ngo 1.22\nrequire (\n" + fill("example.com/a v1.0.0\n") + ")\n" }, Values: 1, Last: "1.22"},
		{Name: "go.mod of comments", Spec: at("go", "b/go.mod"),
			Body: func() string { return "module m\n" + fill("// comment\n") + "go 1.22\n" }, Values: 1, Last: "1.22"},
		{Name: "go.mod of short tokens", Spec: at("go", "c/go.mod"),
			Body: func() string { return "module m\ngo 1.22\nretract (\n" + fill("v1.0.0\n") + ")\n" }, Values: 1, Last: "1.22"},
		commas,
		{Name: "package.json of a long array", Spec: at("node", "package.json"),
			Body: func() string { return `{"engines": {"node": "20"}, "a": [` + fill("1,") + "1]}" }, Values: 1, Last: "20"},
	}
}

// runToolVersionReplays reads each shape at size within
// extracttest.OtherBound.
func runToolVersionReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, build, toolVersionReplays(size), func(s Spec) string { return s.Files[0] })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayToolVersionShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runToolVersionReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayToolVersionShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runToolVersionReplays(t, 1<<20)
}
