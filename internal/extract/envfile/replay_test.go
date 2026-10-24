package envfile

import (
	"runtime"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// envReplays are the densest shapes measured for the reader, built at size
// bytes. Each is bounded by extracttest.OtherBound. Words nested in words
// are read again at each level, so their work grows as the square of the
// nesting, and the shape is refused for work. make test reads them at 16 KiB and make slow
// at 1 MiB.
func envReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	return []extracttest.Replay[Spec]{
		{Name: "a key on each line", Spec: Spec{File: ".env", Name: "a"},
			Body: func() string { return fill("k=1\n") + "a=1\n" }, Values: 1, Last: "1"},
		{Name: "every key", Spec: Spec{File: ".env"},
			Body: func() string { return fill("a=" + strings.Repeat("x", 30) + "\n") }, Values: size / 33, Last: "xxx"},
		{Name: "a quoted value across lines", Spec: Spec{File: ".env", Name: "A"},
			Body: func() string { return `A="` + fill("x\\n\n") + `"` }, Values: 1, Last: "x\n\n"},
		{Name: "comments", Spec: Spec{File: ".env", Name: "A"},
			Body: func() string { return fill("#\n") + "A=1\n" }, Values: 1, Last: "1"},
		{Name: "substitutions", Spec: Spec{File: ".env", Name: "B"},
			Body: func() string { return "A=x\nB=" + fill("${A}") }, Values: 1, Last: "xxx"},
		{Name: "words after a closing brace", Spec: Spec{File: ".env", Name: "B"},
			Body: func() string { return "A=\nB=" + fill("${A:-}") + "}" }, Values: 1, Last: "}"},
		{Name: "unset names", Spec: Spec{File: ".env", Name: "B"},
			Body: func() string { return "B=" + fill("${Z:-x}") }, Values: 1, Last: "${Z:-x}"},
		{Name: "words nested in words", Spec: Spec{File: ".env", Name: "B"},
			Body: func() string {
				n := size / 6
				return "A=\nB=" + strings.Repeat("${A:-", n) + strings.Repeat("}", n)
			}, Refused: ".env: reading the file and matching the spec take more than 128 units of work"},
	}
}

// runEnvReplays reads each shape at size within extracttest.OtherBound.
func runEnvReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, envReplays(size), func(s Spec) string { return s.File })
}

// In make test at 16 KiB, under -race and coverage too.
func TestReplayEnvfileShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runEnvReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayEnvfileShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runEnvReplays(t, 1<<20)
}

// A read allocates in proportion to the file, which the 16 KiB replay's
// time bound cannot see: a copy of the rest of the value at each
// substitution costs a few milliseconds there and seconds at 1 MiB. Each
// shape read in full may allocate 16 bytes for each byte of the file; the
// copy allocates hundreds.
func TestReadAllocatesInProportionToTheFile(t *testing.T) {
	for _, c := range envReplays(16 << 10) {
		if c.Refused != "" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			data := []byte(c.Body())
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := readEnv(data, func(*core.Entry) error { return nil })
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			if grew := after.TotalAlloc - before.TotalAlloc; grew > 16*uint64(len(data)) {
				t.Errorf("a file of %d bytes allocated %d bytes, over 16 for each", len(data), grew)
			}
		})
	}
}
