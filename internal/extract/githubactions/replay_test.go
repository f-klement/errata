package githubactions

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

// costShape is an input whose read took time past its size, from the
// security review of #36 or from its fix rounds, at a size whose read
// with the defect planted again took seconds on a worker node, and the same
// bytes with the costly part where the extractor does not look, whose read
// is the linear cost of the file. The figures are that node's, on 2026-10-24.
type costShape struct {
	name           string
	body, baseline string
	spec           Spec
	want           string // a prefix of the values, or of the refusal
}

// manyVariables is a matrix of n variables of one value each, which a
// search of the earlier names for each made quadratic (security review,
// D1: 4.59 s at 40000 on the build node). At 20000 the planted search took 6.72 s
// against 63 ms for the same bytes; the fix reads it in 81 to 115 ms.
func manyVariables(n int, under string) string {
	var b strings.Builder
	b.WriteString("jobs:\n  j:\n    env: {V: x}\n" + under)
	for i := range n {
		fmt.Fprintf(&b, "        a%07d: [x]\n", i)
	}
	return b.String()
}

// longCompare is a value of l bytes compared with an exclude: entry's of
// the same length for each of 65536 combinations and four entries, one
// step each whatever its length (security review, D2: 2.40 s at 256 KiB
// on the build node). At 64 KiB the planted step took 1.95 s against 95 ms, and
// was refused only at the end, for its 65536 jobs; the fix is refused at
// the step limit in 87 to 99 ms.
func longCompare(l int, excludes string) string {
	big := strings.Repeat("x", l)
	var b strings.Builder
	b.WriteString("jobs:\n  j:\n    env: {V: x}\n    strategy:\n      matrix:\n")
	b.WriteString("        a: [" + big + "a]\n        b: [")
	for i := range 65536 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%d", i)
	}
	b.WriteString("]\n" + excludes + "          - &e {a: " + big + "b}\n          - *e\n          - *e\n          - *e\n")
	return b.String()
}

// aliasedScripts is a script of 1 MiB that does not name GITHUB_ENV, run
// again by m steps through an alias, which scanned it once a step
// (security review, D3: 11.09 s at 10000 steps on the build node). Before the
// step read in the same job, or in a job of its own. At 300 steps the
// planted scan took 19.8 s against 32 ms, since the match is in any case;
// the fix reads it in 98 to 100 ms.
func aliasedScripts(m int, sameJob bool) string {
	steps := "      - run: &r '" + strings.Repeat("GITHUB_EN ", 1<<20/10) + "'\n" + strings.Repeat("      - run: *r\n", m)
	target := "      - id: s\n        env: {V: x}\n"
	if sameJob {
		return "jobs:\n  j:\n    steps:\n" + steps + target
	}
	return "jobs:\n  other:\n    steps:\n" + steps + "  j:\n    steps:\n" + target
}

// aliasedTemplate is a value of a 1 MiB matrix reference that gives the
// empty string, listed m times through an alias, whose template was made
// again for each item with nothing counted. Found in round 1: at 200
// items it took 6.07 s against 24 ms; the fix reads it in 50 ms.
func aliasedTemplate(m int, key string) string {
	var b strings.Builder
	b.WriteString("jobs:\n  j:\n    strategy: {matrix: {a: [x]}}\n    " + key + ": [&t '${{ matrix.zz" + strings.Repeat(" ", 1<<20) + "}}'")
	for range m {
		b.WriteString(", *t")
	}
	b.WriteString("]\n")
	return b.String()
}

// longKeyCompare is longCompare with the long text a mapping's key, which
// a lookup in the entry hashed once for each comparison. Found in round 1;
// the fix is refused at the step limit, as longCompare is.
func longKeyCompare(l int, excludes string) string {
	big := strings.Repeat("x", l)
	var b strings.Builder
	b.WriteString("jobs:\n  j:\n    env: {V: x}\n    strategy:\n      matrix:\n")
	// YAML holds an implicit key to 1024 characters, so the key is
	// explicit.
	b.WriteString("        a:\n          - ? " + big + "\n            : 1\n        b: [")
	for i := range 65536 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%d", i)
	}
	b.WriteString("]\n" + excludes + "          - &e\n            a:\n              ? " + big + "\n              : 2\n          - *e\n          - *e\n          - *e\n")
	return b.String()
}

// nestedLookups is a matrix value of a mapping of k keys, and a value of k
// references to a key below it that it does not hold, each looked for
// among all k keys, quadratic in k with nothing counted. Found in round 1:
// at 20000 it took 2.70 s against 57 ms; the fix is refused at the step
// limit in 91 to 108 ms.
func nestedLookups(k int, key string) string {
	var b strings.Builder
	b.WriteString("jobs:\n  j:\n    strategy: {matrix: {n: [{")
	for i := range k {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "k%06d: x", i)
	}
	b.WriteString("}]}}\n    env: {V: x, " + key + ": '" + strings.Repeat("${{ matrix.n.zz }}", k) + "'}\n")
	return b.String()
}

// entryKeys is an exclude: entry of k keys the matrix does not hold before
// the variable key that disagrees, compared with each of 65536
// combinations, whose keys were walked once a combination with nothing
// counted (security delta review of round 1, S1: 3.47 s against 0.44 s at
// 100000 keys). The baseline puts the variable key first. Both are refused
// for more than 256 jobs.
func entryKeys(k int, variableFirst bool) string {
	var keys strings.Builder
	for i := range k {
		fmt.Fprintf(&keys, "k%06d, ", i)
	}
	var b strings.Builder
	b.WriteString("jobs:\n  j:\n    env: {V: x}\n    strategy:\n      matrix:\n        a: [y]\n        b: [")
	for i := range 65536 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%d", i)
	}
	b.WriteString("]\n        exclude:\n          - {")
	if variableFirst {
		b.WriteString("a: z, " + keys.String() + "k: 1}\n")
	} else {
		b.WriteString(keys.String() + "a: z}\n")
	}
	return b.String()
}

// emptyCombos is a value of r matrix references read in each of 256 jobs
// that empty include: entries make, where a lookup in a combination of no
// key counted nothing (security delta review of round 1, S2: 1.15 s at
// 200000). The references are short, so that the lookups weigh more than
// the parse. The baseline is the same references in a variable not read.
func emptyCombos(r int, key string) string {
	return "jobs:\n  j:\n    env: {V: x, " + key + ": '" + strings.Repeat("${{matrix.z}}", r) + "'}\n    strategy:\n      matrix:\n        include:\n" +
		strings.Repeat("          - {}\n", 256)
}

var costShapes = []costShape{
	{"many variables", manyVariables(20000, "    strategy:\n      matrix:\n"), manyVariables(20000, "    with:\n      matrix:\n"),
		spec("j", "", "V"), "x@3"},
	{"long compare", longCompare(64<<10, "        exclude:\n"), longCompare(64<<10, "        b2: [1]\n      x:\n"),
		spec("j", "", "V"), "ci.yml: the matrix takes more than 1000000 steps to build"},
	{"aliased scripts", aliasedScripts(300, true), aliasedScripts(300, false),
		spec("j", "s", "V"), "x@"},
	{"aliased template", aliasedTemplate(200, "runs-on"), aliasedTemplate(200, "services"),
		spec("j", "", "runs-on"), "@4[j (x)] @4[j (x)]"},
	{"long key compare", longKeyCompare(256<<10, "        exclude:\n"), longKeyCompare(256<<10, "        b2: [1]\n      x:\n"),
		spec("j", "", "V"), "ci.yml: the matrix takes more than 1000000 steps to build"},
	{"nested lookups", nestedLookups(20000, "W"), nestedLookups(20000, "X"),
		spec("j", "", "W"), "ci.yml: the matrix takes more than 1000000 steps to build"},
	{"entry keys", entryKeys(100000, false), entryKeys(100000, true),
		spec("j", "", "V"), `ci.yml:6: the matrix of job "j" gives more than 256 jobs`},
	{"empty combinations", emptyCombos(300000, "W"), emptyCombos(300000, "X"),
		spec("j", "", "W"), "ci.yml: the matrix takes more than 1000000 steps to build"},
}

// read is the fastest of three reads of body for s, and what the first
// gave. It stops after a read within limit, or one over ten times it,
// which no rerun brings under: a planted break would read three times.
func read(t *testing.T, s Spec, body string, limit time.Duration) (time.Duration, string) {
	t.Helper()
	root := extracttest.Repo(t, map[string]string{wf: body})
	e, err := build(s)
	if err != nil {
		t.Fatal(err)
	}
	var best time.Duration
	var got string
	for i := range 3 {
		start := time.Now()
		vs, err := e.Extract(t.Context(), root)
		took := time.Since(start)
		if i == 0 {
			got = values(vs)
			if err != nil {
				got = err.Error()
			}
		}
		if i == 0 || took < best {
			best = took
		}
		if limit > 0 && (best <= limit || best > 10*limit) {
			break
		}
	}
	return best, got
}

// Each shape reads in at most four times the time of the same bytes with
// the costly part where the extractor does not look, and a tenth of a
// second more for the timer. A ratio, not a time, so it holds on any
// node. make slow runs it, since under -race it takes about 50 s. Each
// shape is a few MiB, and all of them peaked at 379 MiB there.
// Planted breaks, each seen failing on its shape: the matrix names
// searched for each variable; a scalar comparison counted as one step;
// each run: scanned once a step; the template made again for each item; a
// scalar written whole into a job's name; a mapping's key looked up in the
// entry with nothing counted; a key below a matrix value looked for with
// nothing counted; a value compared with the first once a job; an entry's
// keys that name no variable walked for each combination; a lookup in a
// combination of no key counted as nothing.
func TestCostIsLinearInTheFile(t *testing.T) {
	slowOnly(t)
	for _, c := range costShapes {
		t.Run(c.name, func(t *testing.T) {
			base, _ := read(t, c.spec, c.baseline, 0)
			limit := 4*base + 100*time.Millisecond*extracttest.RaceSlowdown
			took, got := read(t, c.spec, c.body, limit)
			t.Logf("%s: %d bytes, %v, against %v", c.name, len(c.body), took, base)
			if !strings.Contains(got, c.want) {
				t.Errorf("got %.200q, want %q", got, c.want)
			}
			if took > limit {
				t.Errorf("took %v, over %v: four times %v, the read of the same bytes", took, limit, base)
			}
		})
	}
}
