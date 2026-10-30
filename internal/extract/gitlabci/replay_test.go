package gitlabci

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// shape is a configuration built at a size: small in make test, and at
// full, the size measured against its quadratic pass, in make slow, read
// within bound there.
type shape struct {
	name        string
	files       func(n int) map[string]string
	spec        Spec
	want        string // the values, or with refused a prefix of the refusal
	refused     bool
	small, full int
	bound       time.Duration
}

// repeatedKeys is a main file that sets again each of its include's n
// top-level keys: each is merged twice, which core.Node.Set made a search
// of the keys, quadratic in n.
func repeatedKeys(n int) map[string]string {
	var keys strings.Builder
	for i := range n {
		fmt.Fprintf(&keys, "k%07d: 1\n", i)
	}
	return map[string]string{
		"a.yml":          keys.String(),
		".gitlab-ci.yml": "include: a.yml\nbuild:\n  image: x\n" + keys.String(),
	}
}

// matrixKeys is a matrix entry of n keys of one value each, whose
// instance's values were copied once a key, quadratic in n.
func matrixKeys(n int) map[string]string {
	var b strings.Builder
	b.WriteString("build:\n  script: [x]\n  parallel:\n    matrix:\n      - {V: a")
	for i := range n {
		fmt.Fprintf(&b, ", K%d: a", i)
	}
	b.WriteString("}\n")
	return map[string]string{".gitlab-ci.yml": b.String()}
}

// matrixTimesRules is a matrix of 200 instances over a workflow of n
// rules, whose 199 n candidates were all built before the value limit.
func matrixTimesRules(n int) map[string]string {
	var b strings.Builder
	b.WriteString("workflow:\n  rules:\n")
	for i := range n {
		fmt.Fprintf(&b, "    - if: $A\n      variables: {V: v%d}\n", i)
	}
	b.WriteString("build:\n  script: [x]\n  parallel:\n    matrix:\n      - V: [x]\n      - W: [")
	for i := range 199 {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "w%d", i)
	}
	b.WriteString("]\n")
	return map[string]string{".gitlab-ci.yml": b.String()}
}

// rulesEachSet is a workflow of n rules that each set the variable, under
// a matrix of 199 instances that do not and one that does: 199 n + 1
// candidates of their own node and condition, which values deduplicated
// with a search of those it kept, quadratic in them. A file of n rules
// alone would make as many candidates only from 100000 rules, whose read
// takes too long beside the quadratic pass to bound it.
func rulesEachSet(n int) map[string]string {
	var b strings.Builder
	b.WriteString("workflow:\n  rules:\n")
	for i := range n {
		fmt.Fprintf(&b, "    - if: $A\n      variables: {V: v%d}\n", i)
	}
	b.WriteString("build:\n  script: [x]\n  parallel:\n    matrix:\n      - V: [x]\n      - W: [")
	for i := range 199 {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "w%d", i)
	}
	b.WriteString("]\n")
	return map[string]string{".gitlab-ci.yml": b.String()}
}

// shapes are the inputs the security review of #35 measured as quadratic.
// Each bound is at most a third of the quadratic pass's time at full size
// on the build node, and three times or more the slowest of three linear
// reads there (the report's fix rounds give the runs). Each full shape
// peaks well under 2 GiB, the smallest node the runner may choose.
var shapes = []shape{
	// The quadratic merge (core.Node.Set) took 150.6 s at 320000 keys on
	// the build node; three linear reads took 1.60, 1.34 and 1.39 s.
	{"repeated keys", repeatedKeys, Spec{File: ".gitlab-ci.yml", Job: "build", Key: "image"}, "x@.gitlab-ci.yml:3", false, 20000, 320000, 20 * time.Second},
	// The matrix entry's copy a key took 95 s at 100000 keys (security
	// review, D6), and a planted copy of the key lists 33.2 s on the build
	// node; three linear reads took 0.15, 0.14 and 0.16 s.
	{"matrix keys", matrixKeys, Spec{File: ".gitlab-ci.yml", Job: "build", Variable: "V"}, "a@.gitlab-ci.yml:5[build: [a, a, a, a", false, 5000, 100000, 5 * time.Second},
	// The candidates built before the value limit took 13.1 s and
	// 1074 MiB at 20000 rules (security review, D7); three linear reads
	// took 0.14, 0.13 and 0.13 s. The full shapes together peaked at
	// 802 MiB.
	// values deduplicating its 99899 candidates with a search of those it
	// kept took 5.40 s on the build node; three linear reads took 0.12,
	// 0.13 and 0.14 s (security review, round 2, N4).
	{"rules each set", rulesEachSet, Spec{File: ".gitlab-ci.yml", Job: "build", Variable: "V"}, "x@.gitlab-ci.yml:", false, 10, (core.MaxValues - 1) / 199, 1500 * time.Millisecond},
	{"matrix times rules", matrixTimesRules, Spec{File: ".gitlab-ci.yml", Job: "build", Variable: "V"}, ".gitlab-ci.yml: the extractor selects more than 100000 values", true, 1000, 20000, 4 * time.Second},
}

// runShapes reads each shape at its small size, or its full size, within
// bound, or its own bound at full size.
func runShapes(t *testing.T, full bool, bound time.Duration) {
	t.Helper()
	for _, s := range shapes {
		n := s.small
		if full {
			n, bound = s.full, s.bound
		}
		t.Run(s.name, func(t *testing.T) {
			root := extracttest.Repo(t, s.files(n))
			e, err := build(s.spec)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			got, err := e.Extract(t.Context(), root)
			took := time.Since(start)
			t.Logf("%s at %d: took %v, bound %v", s.name, n, took, bound*extracttest.RaceSlowdown)
			if took > bound*extracttest.RaceSlowdown {
				t.Errorf("took %v, over %v", took, bound*extracttest.RaceSlowdown)
			}
			switch {
			case s.refused && (err == nil || !strings.HasPrefix(err.Error(), s.want)):
				t.Errorf("error %v, want %q", err, s.want)
			case s.refused:
			case err != nil:
				t.Errorf("error %v", err)
			case !strings.HasPrefix(values(got), s.want):
				t.Errorf("got %.200q, want %q", values(got), s.want)
			}
		})
	}
}

// In make test at each shape's small size, under -race and coverage too.
func TestReplayGitLabCIShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole read, which no mutant of an operator changes")
	}
	runShapes(t, false, extracttest.OtherBound)
}

// The same at full size, where a quadratic pass passes its bound.
func TestReplayGitLabCIShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runShapes(t, true, 0)
}
