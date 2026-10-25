package jenkins

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// jenkinsReplays are the densest shapes measured for the reader, built at
// size bytes: the most assignments, tokens, strings, interpolations, line
// ends and stages a file can hold, and nesting just under the depth limit,
// again and again. None is quadratic, so each is bounded by
// extracttest.OtherBound. make test reads them at 16 KiB and make slow at
// 1 MiB; the README's Jenkins section gives the figures at 32 MiB.
func jenkinsReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	x := Spec{File: "Jenkinsfile", Variable: "X"}
	staged := Spec{File: "Jenkinsfile", Variable: "X", Stage: "s"}
	many := func(name, unit, last string) extracttest.Replay[Spec] {
		r := extracttest.Replay[Spec]{Name: name, Spec: x, Body: func() string { return fill(unit) }, Values: size / len(unit), Last: last}
		if r.Values > 100_000 {
			r.Values, r.Refused = 0, "Jenkinsfile: the extractor selects more than 100000 values, the limit"
		}
		return r
	}
	none := "Jenkinsfile: no file sets X"
	// Blocks and strings each nested just under the depth limit.
	blocks := strings.Repeat("a{", 999) + "env.X = 1;" + strings.Repeat("}", 999) + "\n"
	gstrings := "a = " + strings.Repeat(`"${`, 499) + strings.Repeat(`}"`, 499) + "\n"
	return []extracttest.Replay[Spec]{
		many("assignments", "env.X = 1\n", "1"),
		many("assignments on one line", "def X=1;", "1"),
		many("blocks just under the depth limit", blocks, "1"),
		{Name: "strings just under the depth limit", Spec: x, Body: func() string { return fill(gstrings) }, Refused: none},
		{Name: "a statement of names", Spec: x, Body: func() string { return "env.X = a" + fill("+a") }, Values: 1, Last: "a+a"},
		{Name: "a statement over many lines", Spec: x, Body: func() string { return "env.X = a" + fill("+\n.a") }, Values: 1, Last: "a+"},
		{Name: "line ends", Spec: x, Body: func() string { return fill("\n") }, Refused: none},
		{Name: "comments", Spec: x, Body: func() string { return fill("//a\n/*b*/") }, Refused: none},
		{Name: "operators", Spec: x, Body: func() string { return fill(">>>=") }, Refused: none},
		{Name: "single bytes", Spec: x, Body: func() string { return fill("a(") }, Refused: "Jenkinsfile: line 1: a ( or [ is not closed at the end of the file"},
		{Name: "a long string", Spec: x, Body: func() string { return "env.X = '" + fill("a") + "'" }, Values: 1, Last: "aaaa"},
		{Name: "a string of escapes", Spec: x, Body: func() string { return "env.X = '" + fill(`\'`) + "'" }, Values: 1, Last: "''''"},
		{Name: "interpolations", Spec: x, Body: func() string { return "env.X = \"" + fill("${a}") + "\"" }, Values: 1, Last: `"${a}`},
		{Name: "slashy strings", Spec: x, Body: func() string { return fill("/a/\n") }, Refused: none},
		{Name: "stages", Spec: staged, Body: func() string { return "pipeline{stages{stage('s'){}" + fill("stage('b'){environment{X=''}}") + "}}" }, Refused: "Jenkinsfile: no file sets X"},
		{Name: "stages that nest", Spec: staged, Body: func() string {
			return "pipeline{stages{" + fill("stage('b'){stages{") + "stage('s'){}"
		}, Refused: "Jenkinsfile: line 1: nested more than 1000 levels deep"},
		{Name: "libraries", Spec: x, Body: func() string { return fill("@Library('a') _\n") }, Refused: "Jenkinsfile: line 1: the shared library"},
		{Name: "withEnv", Spec: x, Body: func() string { return fill("withEnv(['X=1']){}\n") }, Refused: "Jenkinsfile: line 1: withEnv sets X"},
		{Name: "parameters", Spec: x, Body: func() string { return "pipeline{parameters{" + fill("string(name:'X')\n") + "}}" }, Refused: "Jenkinsfile: line 1: X is a parameter"},
	}
}

// runJenkinsReplays reads each shape at size within extracttest.OtherBound.
func runJenkinsReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, jenkinsReplays(size), func(s Spec) string { return s.File })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayJenkinsShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole read, which no mutant of an operator changes")
	}
	runJenkinsReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayJenkinsShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runJenkinsReplays(t, 1<<20)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; the Makefile's SLOW_TESTS runs it.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}
