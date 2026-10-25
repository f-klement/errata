package githubactions

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// run extracts s from a repository of the workflow body.
func run(t *testing.T, s Spec, body string) error {
	t.Helper()
	_, err := extracttest.Run(t, build, s, extracttest.Repo(t, map[string]string{wf: body}))
	return err
}

// The workflow is held to max_bytes, and to the nodes that size allows,
// before it is parsed. Planted break: the node check dropped, which
// parsed the dense list.
func TestFileBounds(t *testing.T) {
	s := spec("t", "", "V")
	s.MaxBytes = 100
	extracttest.WantError(t, run(t, s, "jobs:\n  t:\n    env: {V: "+strings.Repeat("x", 100)+"}\n"), "over the limit of 100 bytes")
	s.MaxBytes = 7000
	dense := "jobs:\n  t:\n    env: {V: x}\n    runs-on: [" + strings.Repeat("1,", 2000) + "1]\n"
	extracttest.WantError(t, run(t, s, dense), "nodes, over the limit of 1000, one for every 7 bytes of max_bytes")
}

// The nodes YAML aliases add count against core.MaxExpanded, from a count
// of 0 for the one file. The bound is the yaml reader's; this pins that
// the extractor parses through it. Planted break: a start of -1000000,
// which read the file.
func TestAliasBound(t *testing.T) {
	body := "x: &a [" + strings.Repeat("1, ", 999) + "1]\njobs:\n  t:\n    env: {V: v}\n    runs-on: [" + strings.Repeat("*a, ", 1001) + "*a]\n"
	extracttest.WantError(t, run(t, spec("t", "", "V"), body), "aliases and merge keys expand to more than 1000000 nodes, the limit")
}

// A matrix repeats a value for each of its jobs, so the values are held to
// core.MaxValues in count and to max_bytes in all, as they are made, and
// one value to max_bytes while it is written. Planted breaks: each check
// dropped in turn, which made 102400 values, 512 KB of values, and one
// value of 120000 bytes; each of the two byte limits at >=, which refused
// a value of exactly max_bytes.
func TestValueBounds(t *testing.T) {
	head := "jobs:\n  j:\n    strategy:\n      matrix:\n" + vector("a", 16) + vector("b", 16)
	many := head + "    runs-on: [" + strings.Repeat("'${{ matrix.a }}', ", 399) + "'${{ matrix.a }}']\n"
	extracttest.WantError(t, run(t, spec("j", "", "runs-on"), many), "the extractor selects more than 100000 values, the limit")

	long := head + "    env: {V: '${{ matrix.a }}" + strings.Repeat("x", 2000) + "'}\n"
	s := spec("j", "", "V")
	s.MaxBytes = 65536
	extracttest.WantError(t, run(t, s, long), "the values hold more than 65536 bytes in all, the extractor's max_bytes")

	one := "jobs:\n  j:\n    strategy:\n      matrix:\n        a: [" + strings.Repeat("y", 60000) + "]\n    env: {V: '${{ matrix.a }}${{ matrix.a }}'}\n"
	s.MaxBytes = 100000
	extracttest.WantError(t, run(t, s, one), "a value holds more than 100000 bytes, the extractor's max_bytes")

	// A value of exactly max_bytes, which is all the values hold, is read.
	exact := "jobs:\n  j:\n    strategy:\n      matrix:\n        a: [" + strings.Repeat("y", 200) + "]\n    env: {V: '${{ matrix.a }}${{ matrix.a }}'}\n"
	s.MaxBytes = 400
	if err := run(t, s, exact); err != nil {
		t.Fatalf("a value of max_bytes: %v", err)
	}
}
