package shell

import (
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// shellReplays are the densest shapes measured for the reader, built at
// size bytes: the most statements, words, quotes, defaults and here-documents
// a file can hold, and nesting just under the guard's limit, again and again.
// None is quadratic, so each is bounded by extracttest.OtherBound, or by its
// own Bound where it was measured past that. make test reads them at 16 KiB
// and make slow at 1 MiB; the README's shell section gives the figures at
// 32 MiB.
func shellReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	x := Spec{File: "f.sh", Name: "X"}
	many := func(name, unit, last string) extracttest.Replay[Spec] {
		r := extracttest.Replay[Spec]{Name: name, Spec: x, Body: func() string { return fill(unit) }, Values: size / len(unit), Last: last}
		if r.Values > 100_000 {
			r.Values, r.Refused = 0, "f.sh: the extractor selects more than 100000 values, the limit"
		}
		return r
	}
	// $( ) 200 deep, about 2200 calls, ten times under the guard's limit.
	nest := "X=" + strings.Repeat("$(", 200) + "a" + strings.Repeat(")", 200) + "\n"
	// Defaults 3000 deep, under the guard's limit, return each default
	// whole: 27 MB of values from 18 KB. The walker stops reading values
	// once they pass every extractor's limit, so a file of them is refused
	// after a few, not after gigabytes.
	defaults := ": " + strings.Repeat("${X:-", 3000) + "a" + strings.Repeat("}", 3000) + "\n"
	// A pipeline is two levels of the syntax tree for each command, so past
	// half the walk's limit it is refused.
	pipeline := extracttest.Replay[Spec]{Name: "a pipeline", Spec: x, Body: func() string { return "a" + fill(" | a") }}
	if 2*size/len(" | a") > maxTreeDepth {
		pipeline.Refused = "f.sh: line 1: the script's syntax is more than 20000 levels deep, the limit"
	}
	nested := extracttest.Replay[Spec]{Name: "nested defaults just under the limit", Spec: x, Body: func() string { return fill(defaults) }}
	if size >= len(defaults) {
		nested.Refused = "f.sh: the values the extractor selects hold more than 33554432 bytes in all"
	}
	// Redirects with a default on one line and their here-documents after
	// it: the walk reads each document after its line, and a cursor that
	// went back for each value counted the file again and again (2.6 s for
	// 1.15 MB). The values' lines come from one pass over them sorted.
	docs := size / len(" <<A >${X:-a}${X:-b}\nA\n")
	heredocs := extracttest.Replay[Spec]{Name: "here-documents after their line", Spec: x, Values: 2 * docs, Last: "b",
		Body: func() string {
			return "cat" + strings.Repeat(" <<A >${X:-a}", docs) + "\n" + strings.Repeat("${X:-b}\nA\n", docs)
		}}
	// Assignments, references and arithmetic at the head of a pipeline 9000
	// commands deep, and here-documents there: the walk once looped over its
	// stack of 18000 nodes for each assignment and reference, which took a
	// file of 32 MiB 85 s, and copied the stack for each pending document,
	// which took 4000 of them 1232 MiB. They take longer than the other
	// shapes: assignments at 1 MiB, slowest of three on builder 0.30 s,
	// 0.67 s with three runs at once, and 1.06 s in CI beside two other
	// pipelines, which failed extracttest.OtherBound. Bound 2 s, three
	// times the loaded run; that walk put back takes them 4.4 s and 5.7 s.
	pipe := strings.Repeat("|a", 9000)
	headOfPipe := func(name, unit string) extracttest.Replay[Spec] {
		return extracttest.Replay[Spec]{Name: name, Spec: x, Body: func() string { return "{ " + fill(unit) + " }" + pipe + "\n" }, Bound: 2 * time.Second}
	}
	pending := size / len(" <<A") / 2
	deepDocs := extracttest.Replay[Spec]{Name: "here-documents under a deep pipeline", Spec: x, Body: func() string {
		return "{ cat" + strings.Repeat(" <<A", pending) + "; }" + pipe + "; :\n" + strings.Repeat("A\n", pending)
	}}
	return []extracttest.Replay[Spec]{
		headOfPipe("assignments at the head of a deep pipeline", "a=1;"),
		headOfPipe("references at the head of a deep pipeline", "a=$b;"),
		headOfPipe("arithmetic at the head of a deep pipeline", "((a));"),
		deepDocs,
		nested,
		heredocs,
		many("assignments", "X=1\n", "1"),
		many("assignments on one line", "X=1;", "1"),
		many("defaults", ": ${X:-a}\n", "a"),
		many("nested commands just under the limit", nest, "$($("),
		many("here-documents with a default", "cat <<E\n${X:=b}\nE\n", "b"),
		{Name: "words", Spec: x, Body: func() string { return "echo" + fill(" a") }},
		{Name: "a long quoted value", Spec: x, Body: func() string { return "X='" + fill("a") + "'" }, Values: 1, Last: "aaaa"},
		{Name: "a value of many quotes", Spec: x, Body: func() string { return "X=" + fill(`'a'"b"\c`) }, Values: 1, Last: "abc"},
		{Name: "an array", Spec: x, Body: func() string { return "X=(" + fill(" a") + ")" }, Values: 1, Last: "( a"},
		pipeline,
	}
}

// runShellReplays reads each shape at size within its Bound or extracttest.OtherBound.
func runShellReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, shellReplays(size), func(Spec) string { return "f.sh" })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayShellShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runShellReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayShellShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runShellReplays(t, 1<<20)
}
