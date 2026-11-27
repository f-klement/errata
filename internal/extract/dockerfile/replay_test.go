package dockerfile

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// dockerfileReplays are the densest shapes for the reader, built at size
// bytes: the most instructions, words, continued lines, lines skipped
// inside an instruction, variables, expansions and stages a file can
// hold. None is quadratic, so each is bounded by extracttest.OtherBound.
// make test reads them at 16 KiB and make slow at fullSize.
func dockerfileReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	spec := func(in, key string) Spec {
		return Spec{OneFile: core.OneFile{File: "Dockerfile"}, Instruction: in, Key: key}
	}
	many := func(name string, s Spec, body func() string, values int, last string) extracttest.Replay[Spec] {
		r := extracttest.Replay[Spec]{Name: name, Spec: s, Body: body, Values: values, Last: last}
		if values > core.MaxValues {
			r.Values, r.Refused = 0, "Dockerfile: the extractor selects more than 100000 values, the limit"
		}
		return r
	}
	distinct := func() string {
		var b strings.Builder
		b.WriteString("FROM a\n")
		for i := 0; b.Len() < size; i++ {
			b.WriteString("ENV A" + strconv.Itoa(i) + "=1\n")
		}
		return b.String()
	}
	// Half the file a stage's variables, half stages FROM it, each copying
	// them: refused for work.
	inheriting := func() string {
		var b strings.Builder
		b.WriteString("FROM a AS s\n")
		for i := 0; b.Len() < size/2; i++ {
			b.WriteString("ENV A" + strconv.Itoa(i) + "=1\n")
		}
		return b.String() + fill("FROM s\n")[:size/2]
	}
	// Stages of distinct names, since docker build refuses a name twice.
	var nb strings.Builder
	for i := 0; nb.Len() < size; i++ {
		nb.WriteString("FROM a AS b" + strconv.Itoa(i) + "\n")
	}
	named := nb.String()
	work := "Dockerfile: reading the file and matching the spec take more than"
	return []extracttest.Replay[Spec]{
		many("instructions", spec("ENV", ""), func() string { return "FROM a\n" + fill("ENV A=1\n") }, size/8, "1"),
		many("pairs over continued lines", spec("ENV", "B"), func() string { return "FROM a\nENV " + fill("A=1 \\\n") + "B=2\n" }, 1, "2"),
		many("words over continued lines", spec("EXPOSE", ""), func() string { return "FROM a\nEXPOSE " + fill("1 \\\n") + "2\n" }, size/4+1, "2"),
		many("a word over continued lines", spec("ENV", ""), func() string { return "FROM a\nENV A=" + fill("a\\\n") + "b\n" }, 1, "aaaa"),
		many("comment lines in an instruction", spec("ENV", ""), func() string { return "FROM a\nENV A=1 \\\n" + fill("# c\n") + " B=2\n" }, 2, "2"),
		many("blank lines in an instruction", spec("ENV", ""), func() string { return "FROM a\nENV A=1 \\\n" + fill(" \n") + " B=2\n" }, 2, "2"),
		many("distinct variables", spec("ENV", "A1"), distinct, 1, "1"),
		many("expansions", spec("ENV", "Z"), func() string { return "FROM a\nENV A=xxxxxxxx\n" + fill("ENV B=$A$A$A$A\n") + "ENV Z=1\n" }, 1, "1"),
		many("stages", spec("FROM", ""), func() string { return fill("FROM a\n") }, size/7, "a"),
		many("named stages", spec("FROM", ""), func() string { return named }, strings.Count(named, "\n"), "a"),
		{Name: "stages copying variables", Spec: spec("ENV", "Z"), Body: inheriting, Refused: work},
		many("global args", spec("FROM", ""), func() string { return fill("ARG A=1\n") + "FROM a:$A\n" }, 1, "a:1"),
		many("heredocs", spec("ENV", ""), func() string { return "FROM a\nRUN <<EOF\n" + fill("ENV X=1\n") + "EOF\nENV A=1\n" }, 1, "1"),
		{Name: "a long line", Spec: spec("ENV", ""), Body: func() string { return "FROM a\nENV A=" + strings.Repeat("a", 1<<16) + "\n" },
			Refused: "Dockerfile: line 2: dockerfile line greater than max allowed size of 65535"},
	}
}

// runDockerfileReplays reads each shape at size within
// extracttest.OtherBound.
func runDockerfileReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, dockerfileReplays(size), func(s Spec) string { return s.File })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayDockerfileShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole read, which no mutant of an operator changes")
	}
	runDockerfileReplays(t, 16<<10)
}

// fullSize is the parse limit at the default max_bytes, 1 MiB, less a
// KiB for the lines a shape writes around its fill.
const fullSize = core.DefaultMaxBytes/parseShare - 1<<10

// The same at fullSize, where a quadratic pass passes the bound.
func TestReplayDockerfileShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runDockerfileReplays(t, fullSize)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; the Makefile's SLOW_TESTS runs it.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}
