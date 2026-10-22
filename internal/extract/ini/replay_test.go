package ini

import (
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// iniReplays are the densest shapes measured for each reader, and the ones
// the reviews found quadratic, built at size bytes; a name in them is a
// sixteenth of size. None is quadratic, so each is bounded by extracttest.OtherBound;
// the times and peaks measured at 32 MiB are in the README draft. tox, a key
// of each letter in each of a million sections, took 7.8 s and 846 MB while
// tox kept every section's keys for its repeated key check, and 3.5 s to
// 6.3 s and 133 MB to 172 MB once it kept one section's. make test reads
// them at 16 KiB and make slow at 1 MiB.
func iniReplays(size int) []extracttest.Replay[Spec] {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	name := strings.Repeat("a", size/16)
	range999 := "h" + strings.Repeat("[0:9]", 999) + " v=1\n"
	nines := "h[0:" + strings.Repeat("9", 4300) + "] v=1\n"
	return []extracttest.Replay[Spec]{
		{Name: "plain: a key, then blank lines in its value", Spec: ini("f", "s", "k"),
			Body: func() string { return "[s]\nk = 1\n" + fill("\n") }, Values: 1, Last: "1"},
		{Name: "plain: a value of indented lines", Spec: ini("f", "s", "k"),
			Body: func() string { return "[s]\nk = 1\n" + fill("  x\n") }, Values: 1, Last: "1" + strings.Repeat("\nx", size/4)},
		{Name: "plain: keys", Spec: ini("f", "s", "b"),
			Body: func() string { return "[s]\n" + fill("a=1\n") }},
		{Name: "plain: a line of no-break spaces", Spec: ini("f", "s", "k"),
			Body: func() string { return "[s]\nk = 1" + fill(" ") + "\n" }, Values: 1, Last: "1"},
		{Name: "git: keys without a value", Spec: gitSpec(".gitconfig", "a", nil, "k"),
			Body: func() string { return "[a]\n" + fill("b\n") }},
		{Name: "git: a quoted value", Spec: gitSpec(".gitconfig", "a", nil, "k"),
			Body: func() string { return "[a]\nk = \"" + fill(" ;") + "\"\n" }, Values: 1, Last: fill(" ;")},
		{Name: "ansible: hosts", Spec: Spec{File: "hosts", Section: "ungrouped", Key: "a"},
			Body: func() string { return fill("h\n") }},
		{Name: "ansible: a host of many variables", Spec: Spec{File: "hosts", Section: "ungrouped", Key: "b"},
			Body: func() string { return "h" + fill(" a=1") + "\n" }},
		{Name: "tox: a key of each letter in many sections", Spec: ini("tox.ini", "0", "b"),
			Body: func() string {
				var keys strings.Builder
				for c := 'a'; c <= 'z'; c++ {
					keys.WriteString(string(c) + "=1\n")
				}
				return extracttest.RepeatLines(size/(len(keys.String())+8), func(i int) string { return "[" + strconv.FormatInt(int64(i), 16) + "]\n" + keys.String() })
			}, Values: 1, Last: "1"},
		// A section or a host the spec names over keys it does not select:
		// compared once for each entry, the name cost its length for each,
		// 2.3 s to 2.8 s at 1 MiB and 58 s for git at 32 MiB.
		{Name: "git: a long section over keys", Spec: gitSpec(".gitconfig", name, nil, "k"),
			Body: func() string { return "[" + name + "]\n" + fill("a\n") }},
		{Name: "plain: a long section over keys", Spec: ini("f", name, "k"),
			Body: func() string { return "[" + name + "]\n" + fill("a=1\n") }},
		{Name: "ansible: a long host with variables", Spec: Spec{File: "hosts", Section: "ungrouped", Host: name, Key: "b"},
			Body: func() string { return name + fill(" a=1") + "\n" }},
		{Name: "ansible: a long group over hosts", Spec: Spec{File: "hosts", Section: name, Key: "b"},
			Body: func() string { return "[" + name + "]\n" + fill("h\n") }},
		{Name: "php: a long section over keys", Spec: ini("php.ini", name, "k"),
			Body: func() string { return "[" + name + "]\n" + fill("a=1\n") }},
		{Name: "tox: a long section over keys", Spec: ini("tox.ini", name, "k"),
			Body: func() string { return "[" + name + "]\n" + extracttest.KeyLines(size/7, " = ") }},
		// ansible ranges, which errata never expands: bounds of many digits,
		// whose conversion was quadratic in them (4 M digits took 79.5 s) and
		// which Python refuses past 4300, many ranges in one host, each of
		// which copied the host (38 s at 32 MiB), and many hosts of a range.
		{Name: "ansible: a range of long bounds", Spec: Spec{File: "hosts", Section: "ungrouped"},
			Body: func() string {
				return "h[" + strings.Repeat("1", size/2-4) + ":" + strings.Repeat("1", size/2-4) + "]\n"
			},
			Refused: "hosts: line 1: the host \"h[1111"},
		{Name: "ansible: 1000 ranges and a long tail", Spec: Spec{File: "hosts", Section: "ungrouped", Key: "b"},
			Body: func() string { return strings.Repeat("[0:1]", 999) + fill("a") + "\n" }},
		{Name: "ansible: hosts of one range", Spec: Spec{File: "hosts", Section: "ungrouped", Key: "b"},
			Body: func() string { return fill("h[0:1]\n") }},
		// The second security review's shapes, each now inside the one work
		// meter of its read: ranges a long name can start at many positions
		// of (about 350 s at 32 MiB under a cap per line), 999 ranges against
		// a name of 999 zeros (19.5 s), a large tox section then small ones,
		// whose one map was cleared at each header (90 s), a long host with a
		// port, which ran the address patterns (40 s), and a bound of 4300
		// digits on every line, selected.
		{Name: "ansible: ten 17 digit ranges a line, a name of 1023 digits", Spec: Spec{File: "hosts", Section: "g", Host: "h" + strings.Repeat("1", 1023), Key: "v"},
			Body:    func() string { return "[g]\n" + fill("h"+strings.Repeat("[0:99999999999999999]", 10)+" v=1\n") },
			Refused: "hosts: reading the file and matching the spec take more than 128 units of work"},
		{Name: "ansible: 999 ranges a line, a name of 999 zeros", Spec: Spec{File: "hosts", Section: "g", Host: "h" + strings.Repeat("0", 999), Key: "v"},
			Body: func() string { return "[g]\n" + fill(range999) }, Values: size / len(range999), Last: "1"},
		{Name: "ansible: 999 ranges a line, a name that misses at its end", Spec: Spec{File: "hosts", Section: "g", Host: "h" + strings.Repeat("0", 998) + "x", Key: "v"},
			Body: func() string { return "[g]\n" + fill(range999) }},
		{Name: "tox: a large section, then sections of one key", Spec: ini("tox.ini", "0", "a"),
			Body: func() string {
				return "[big]\n" + extracttest.KeyLines(size/14, "=") + extracttest.RepeatLines(size/16, func(i int) string { return "[" + strconv.FormatInt(int64(i), 16) + "]\na=1\n" })
			}, Values: 1, Last: "1"},
		{Name: "ansible: a long host with a port, no host selected", Spec: Spec{File: "hosts", Section: "g", Key: "v"},
			Body: func() string { return "[g]\nh[0:1]" + fill("a") + ":22 v=1\n" }, Values: 1, Last: "1"},
		{Name: "ansible: a long host with a port, a host selected", Spec: Spec{File: "hosts", Section: "g", Host: "h0", Key: "v"},
			Body: func() string { return "[g]\nh[0:1]" + fill("a") + ":22 v=1\n" }},
		{Name: "ansible: a bound of 4300 digits a line, selected", Spec: Spec{File: "hosts", Section: "g", Host: "h123456789", Key: "v"},
			Body: func() string { return "[g]\n" + fill(nines) }, Values: size / len(nines), Last: "1"},
		// php fallbacks nested in fallbacks, quadratic in their depth (74.8 s
		// at 917 KB), now refused past maxDepth; and a reference 1000 deep
		// around a long text, which is read.
		{Name: "php: fallbacks nested past the limit", Spec: ini("php.ini", "", "a"),
			Body: func() string {
				n := size / 8
				return "a = " + strings.Repeat("${ab:-", n) + "x" + strings.Repeat("}", n) + "\n"
			}, Refused: "php.ini: line 1: nested more than 1000 levels deep, the limit"},
		{Name: "php: a reference 1000 deep around a long text", Spec: ini("php.ini", "", "a"),
			Body: func() string {
				return "a = " + strings.Repeat("${ab:-", core.MaxDepth) + fill("x") + strings.Repeat("}", core.MaxDepth) + "\n"
			}, Values: 1, Last: "${ab:-"},
	}
}

// runINIReplays reads each shape at size within extracttest.OtherBound.
func runINIReplays(t *testing.T, size int) {
	t.Helper()
	extracttest.RunShapes(t, Build, iniReplays(size), func(s Spec) string { return s.File })
}

// In make test at 16 KiB, under -race and coverage too, where every loop
// costs about thirty times its plain time. The quadratic passes the
// reviews found have witnesses that do not depend on time:
// TestMatcherComparesEachSectionOnce, TestCheckRangesCopiesNothing,
// TestPHPRefusesDeepReferences and the digit limit's refusal.
func TestReplayINIShapes(t *testing.T) {
	if extracttest.MutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runINIReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayINIShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runINIReplays(t, 1<<20)
}
