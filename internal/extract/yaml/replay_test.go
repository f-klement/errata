package yaml

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// laughs is n levels of lists of ten aliases of the level before.
func laughs(n int) string {
	return "a0: &a0 [" + extracttest.List(10, "x") + "]\n" + extracttest.RepeatLines(n, func(i int) string {
		l := strconv.Itoa(i + 1)
		return "a" + l + ": &a" + l + " [" + extracttest.List(10, "*a"+strconv.Itoa(i)) + "]\n"
	})
}

// mergeChain is n mappings, each anchored and merging the one before.
func mergeChain(n int) string {
	return "- &a0 {k0: 1}\n" + extracttest.RepeatLines(n-1, func(j int) string {
		i := strconv.Itoa(j + 1)
		return "- &a" + i + " {<<: *a" + strconv.Itoa(j) + ", k" + i + ": 1}\n"
	})
}

const aliasRefused = "aliases and merge keys expand to more than 1000000 nodes, the limit; write the repeated part out, or a regex extractor can still read the file"

// The YAML inputs that took quadratic time, that the earlier limits refused
// though they are ordinary, and that got past those limits. Times are
// linear, linear under -race, and the quadratic pass, measured with that pass
// put back. A quadratic case is full, at the size its times were measured
// at, and make slow runs it; make test runs the same shape at a tenth of the
// size, which reads within extracttest.OtherBound. One more is full, the
// merge chain, since no smaller input reaches its limit; another test
// reaches it in make test.
var replays = []extracttest.Replay[Spec]{
	// Repeated keys, quadratic in yaml.v3's decode.
	// Linear 0.30 s, 1.20 s under -race;
	// quadratic 3.67 s; bound 1.2 s.
	{Name: "YAML mapping of 25000 keys", Spec: Spec{File: "f", Path: "k24999"},
		Body: func() string { return extracttest.KeyLines(25000, ": ") }, Values: 1, Last: "1", Bound: 1200 * time.Millisecond, Full: true},
	{Name: "YAML mapping of 2500 keys", Spec: Spec{File: "f", Path: "k2499"},
		Body: func() string { return extracttest.KeyLines(2500, ": ") }, Values: 1, Last: "1"},
	// Linear 0.24 s, 1.51 s under -race;
	// quadratic 4.80 s; bound 1.6 s.
	{Name: "2 YAML documents of 20000 keys", Spec: Spec{File: "f", Path: "k19998", Kind: "X"},
		Body: func() string { return strings.Repeat("kind: X\n"+extracttest.KeyLines(19999, ": ")+"---\n", 2) }, Values: 2, Last: "1", Bound: 1600 * time.Millisecond, Full: true},
	{Name: "2 YAML documents of 2000 keys", Spec: Spec{File: "f", Path: "k1998", Kind: "X"},
		Body: func() string { return strings.Repeat("kind: X\n"+extracttest.KeyLines(1999, ": ")+"---\n", 2) }, Values: 2, Last: "1"},
	// A line index per document. Linear 0.07 s, 0.63 s under -race;
	// quadratic 3.61 s; bound 1.2 s.
	{Name: "7000 YAML documents of one key", Spec: Spec{File: "f", Path: "kind", Kind: "X"},
		Body: func() string { return strings.Repeat("kind: X\n---\n", 7000) }, Values: 7000, Last: "X", Bound: 1200 * time.Millisecond, Full: true},
	{Name: "700 YAML documents of one key", Spec: Spec{File: "f", Path: "kind", Kind: "X"},
		Body: func() string { return strings.Repeat("kind: X\n---\n", 700) }, Values: 700, Last: "X"},

	// The other shapes, each bounded by extracttest.OtherBound: none of them
	// replays a quadratic pass.
	{Name: "2000 YAML mappings of two keys", Spec: Spec{File: "f", Path: "[-1].y"},
		Body: func() string { return strings.Repeat("- {x: 1, y: 2}\n", 2000) }, Values: 1, Last: "2"},
	// Alias bombs. Four levels of ten aliases add 111110 nodes and are
	// read; six add over 1000000.
	{Name: "four levels of ten aliases", Spec: Spec{File: "f", Path: "a4[9][9][9][9][9]"},
		Body: func() string { return laughs(4) }, Values: 1, Last: "x"},
	{Name: "six levels of ten aliases", Spec: Spec{File: "f", Path: "a0[0]"},
		Body: func() string { return laughs(6) }, Refused: "f: line 6: " + aliasRefused},
	// A merge chain, which ran out of 4 GB at 56000 items, is refused at
	// its 817th. The count stops it; up to there the merges copy 334000
	// keys, so it takes longer than the other shapes. Slowest of three on a
	// loaded machine 0.62 s, 2.31 s under -race; 1.06 s at a load of 8.4 on
	// four CPUs, which failed extracttest.OtherBound; bound 3.5 s, over three
	// times that.
	// No shorter chain reaches the limit, and TestYAMLMergeChainIsCounted is
	// the same refusal in make test, without the bound.
	{Name: "merge chain of 1000 items", Spec: Spec{File: "f", Path: "[0].k0"},
		Body: func() string { return mergeChain(1000) }, Refused: "f: line 817: " + aliasRefused, Bound: 3500 * time.Millisecond, Full: true},
	{Name: "10000 aliases of 100 keys", Spec: Spec{File: "f", Path: "base.k0"},
		Body: func() string {
			return "base: &b\n" + strings.ReplaceAll(extracttest.KeyLines(100, ": "), "k", "  k") + "l:\n" + strings.Repeat("- *b\n", 10000)
		},
		Refused: "f: line 5078: " + aliasRefused},
	// Depth past the limit, as 100 KB of [.
	{Name: "100 KB of YAML [", Spec: Spec{File: "f", Path: "$"},
		Body: func() string { return strings.Repeat("[", 100000) }, Refused: "f: yaml: exceeded max depth of 10000"},
	// Ordinary YAML the size cap refused, at a size the cap allowed.
	{Name: "OpenAPI of 100 paths", Spec: Spec{File: "f", Path: "paths['/resource99'].delete.operationId"},
		Body: func() string { return extracttest.OpenAPI(100) }, Values: 1, Last: "deleteResource99"},
	{Name: "Helm index of 100 chart versions", Spec: Spec{File: "f", Path: "entries.chart9[*].version"},
		Body: func() string { return extracttest.HelmIndex(10, 10) }, Values: 10, Last: "9.0.0"},
	{Name: "30 flow mappings of 100 keys", Spec: Spec{File: "f", Path: "[-1].k99"},
		Body: func() string {
			return strings.Repeat("- {"+strings.TrimSuffix(strings.ReplaceAll(extracttest.KeyLines(100, ": "), "\n", ", "), ", ")+"}\n", 30)
		}, Values: 1, Last: "1"},
}

// extracttest.OtherBound is the bound of a case that replays no quadratic pass. The
// slowest of them takes 0.17 s, and 0.48 s under -race.
func TestReplayMeasuredInputs(t *testing.T) {
	extracttest.RunReplays(t, Build, replays, false)
}

// The full cases, at the sizes their times were measured at.
func TestReplayMeasuredInputsAtFullSize(t *testing.T) {
	slowOnly(t)
	extracttest.RunReplays(t, Build, replays, true)
}
