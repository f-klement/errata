package extract

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// iniReplays are the densest shapes measured for each reader, and the ones
// the reviews found quadratic, built at size bytes; a name in them is a
// sixteenth of size. None is quadratic, so each is bounded by otherBound;
// the times and peaks measured at 32 MiB are in the README draft. tox, a key
// of each letter in each of a million sections, took 7.8 s and 846 MB while
// tox kept every section's keys for its repeated key check, and 3.5 s to
// 6.3 s and 133 MB to 172 MB once it kept one section's. make test reads
// them at 16 KiB and make slow at 1 MiB.
func iniReplays(size int) []replay {
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	name := strings.Repeat("a", size/16)
	range999 := "h" + strings.Repeat("[0:9]", 999) + " v=1\n"
	nines := "h[0:" + strings.Repeat("9", 4300) + "] v=1\n"
	return []replay{
		{name: "plain: a key, then blank lines in its value", spec: ini("f", "s", "k"),
			body: func() string { return "[s]\nk = 1\n" + fill("\n") }, values: 1, last: "1"},
		{name: "plain: a value of indented lines", spec: ini("f", "s", "k"),
			body: func() string { return "[s]\nk = 1\n" + fill("  x\n") }, values: 1, last: "1" + strings.Repeat("\nx", size/4)},
		{name: "plain: keys", spec: ini("f", "s", "b"),
			body: func() string { return "[s]\n" + fill("a=1\n") }},
		{name: "plain: a line of no-break spaces", spec: ini("f", "s", "k"),
			body: func() string { return "[s]\nk = 1" + fill(" ") + "\n" }, values: 1, last: "1"},
		{name: "git: keys without a value", spec: gitSpec(".gitconfig", "a", nil, "k"),
			body: func() string { return "[a]\n" + fill("b\n") }},
		{name: "git: a quoted value", spec: gitSpec(".gitconfig", "a", nil, "k"),
			body: func() string { return "[a]\nk = \"" + fill(" ;") + "\"\n" }, values: 1, last: fill(" ;")},
		{name: "ansible: hosts", spec: Spec{INI: &INI{File: "hosts", Section: "ungrouped", Key: "a"}},
			body: func() string { return fill("h\n") }},
		{name: "ansible: a host of many variables", spec: Spec{INI: &INI{File: "hosts", Section: "ungrouped", Key: "b"}},
			body: func() string { return "h" + fill(" a=1") + "\n" }},
		{name: "tox: a key of each letter in many sections", spec: ini("tox.ini", "0", "b"),
			body: func() string {
				var keys strings.Builder
				for c := 'a'; c <= 'z'; c++ {
					keys.WriteString(string(c) + "=1\n")
				}
				return repeatLines(size/(len(keys.String())+8), func(i int) string { return "[" + strconv.FormatInt(int64(i), 16) + "]\n" + keys.String() })
			}, values: 1, last: "1"},
		{name: "properties: a value joined over many lines", spec: props("a"),
			body: func() string { return "a=" + fill("x\\\n  ") }, values: 1, last: strings.Repeat("x", size/5)},
		{name: "properties: unicode escapes", spec: props("a"),
			body: func() string { return "a=" + fill("\\u0041") }, values: 1, last: strings.Repeat("A", size/6)},
		// A section or a host the spec names over keys it does not select:
		// compared once for each entry, the name cost its length for each,
		// 2.3 s to 2.8 s at 1 MiB and 58 s for git at 32 MiB.
		{name: "git: a long section over keys", spec: gitSpec(".gitconfig", name, nil, "k"),
			body: func() string { return "[" + name + "]\n" + fill("a\n") }},
		{name: "plain: a long section over keys", spec: ini("f", name, "k"),
			body: func() string { return "[" + name + "]\n" + fill("a=1\n") }},
		{name: "ansible: a long host with variables", spec: Spec{INI: &INI{File: "hosts", Section: "ungrouped", Host: name, Key: "b"}},
			body: func() string { return name + fill(" a=1") + "\n" }},
		{name: "ansible: a long group over hosts", spec: Spec{INI: &INI{File: "hosts", Section: name, Key: "b"}},
			body: func() string { return "[" + name + "]\n" + fill("h\n") }},
		{name: "php: a long section over keys", spec: ini("php.ini", name, "k"),
			body: func() string { return "[" + name + "]\n" + fill("a=1\n") }},
		{name: "tox: a long section over keys", spec: ini("tox.ini", name, "k"),
			body: func() string { return "[" + name + "]\n" + keyLines(size/7, " = ") }},
		// ansible ranges, which errata never expands: bounds of many digits,
		// whose conversion was quadratic in them (4 M digits took 79.5 s) and
		// which Python refuses past 4300, many ranges in one host, each of
		// which copied the host (38 s at 32 MiB), and many hosts of a range.
		{name: "ansible: a range of long bounds", spec: Spec{INI: &INI{File: "hosts", Section: "ungrouped"}},
			body: func() string {
				return "h[" + strings.Repeat("1", size/2-4) + ":" + strings.Repeat("1", size/2-4) + "]\n"
			},
			refused: "hosts: line 1: the host \"h[1111"},
		{name: "ansible: 1000 ranges and a long tail", spec: Spec{INI: &INI{File: "hosts", Section: "ungrouped", Key: "b"}},
			body: func() string { return strings.Repeat("[0:1]", 999) + fill("a") + "\n" }},
		{name: "ansible: hosts of one range", spec: Spec{INI: &INI{File: "hosts", Section: "ungrouped", Key: "b"}},
			body: func() string { return fill("h[0:1]\n") }},
		// The second security review's shapes, each now inside the one work
		// meter of its read: ranges a long name can start at many positions
		// of (about 350 s at 32 MiB under a cap per line), 999 ranges against
		// a name of 999 zeros (19.5 s), a large tox section then small ones,
		// whose one map was cleared at each header (90 s), a long host with a
		// port, which ran the address patterns (40 s), and a bound of 4300
		// digits on every line, selected.
		{name: "ansible: ten 17 digit ranges a line, a name of 1023 digits", spec: Spec{INI: &INI{File: "hosts", Section: "g", Host: "h" + strings.Repeat("1", 1023), Key: "v"}},
			body:    func() string { return "[g]\n" + fill("h"+strings.Repeat("[0:99999999999999999]", 10)+" v=1\n") },
			refused: "hosts: reading the file and matching the spec take more than 128 units of work"},
		{name: "ansible: 999 ranges a line, a name of 999 zeros", spec: Spec{INI: &INI{File: "hosts", Section: "g", Host: "h" + strings.Repeat("0", 999), Key: "v"}},
			body: func() string { return "[g]\n" + fill(range999) }, values: size / len(range999), last: "1"},
		{name: "ansible: 999 ranges a line, a name that misses at its end", spec: Spec{INI: &INI{File: "hosts", Section: "g", Host: "h" + strings.Repeat("0", 998) + "x", Key: "v"}},
			body: func() string { return "[g]\n" + fill(range999) }},
		{name: "tox: a large section, then sections of one key", spec: ini("tox.ini", "0", "a"),
			body: func() string {
				return "[big]\n" + keyLines(size/14, "=") + repeatLines(size/16, func(i int) string { return "[" + strconv.FormatInt(int64(i), 16) + "]\na=1\n" })
			}, values: 1, last: "1"},
		{name: "ansible: a long host with a port, no host selected", spec: Spec{INI: &INI{File: "hosts", Section: "g", Key: "v"}},
			body: func() string { return "[g]\nh[0:1]" + fill("a") + ":22 v=1\n" }, values: 1, last: "1"},
		{name: "ansible: a long host with a port, a host selected", spec: Spec{INI: &INI{File: "hosts", Section: "g", Host: "h0", Key: "v"}},
			body: func() string { return "[g]\nh[0:1]" + fill("a") + ":22 v=1\n" }},
		{name: "ansible: a bound of 4300 digits a line, selected", spec: Spec{INI: &INI{File: "hosts", Section: "g", Host: "h123456789", Key: "v"}},
			body: func() string { return "[g]\n" + fill(nines) }, values: size / len(nines), last: "1"},
		// php fallbacks nested in fallbacks, quadratic in their depth (74.8 s
		// at 917 KB), now refused past maxDepth; and a reference 1000 deep
		// around a long text, which is read.
		{name: "php: fallbacks nested past the limit", spec: ini("php.ini", "", "a"),
			body: func() string {
				n := size / 8
				return "a = " + strings.Repeat("${ab:-", n) + "x" + strings.Repeat("}", n) + "\n"
			}, refused: "php.ini: line 1: nested more than 1000 levels deep, the limit"},
		{name: "php: a reference 1000 deep around a long text", spec: ini("php.ini", "", "a"),
			body: func() string {
				return "a = " + strings.Repeat("${ab:-", maxDepth) + fill("x") + strings.Repeat("}", maxDepth) + "\n"
			}, values: 1, last: "${ab:-"},
	}
}

// runINIReplays reads each shape at size within otherBound.
func runINIReplays(t *testing.T, size int) {
	t.Helper()
	for _, c := range iniReplays(size) {
		t.Run(c.name, func(t *testing.T) {
			file := "a.properties"
			if c.spec.INI != nil {
				file = c.spec.INI.File
			}
			root := repo(t, map[string]string{file: c.body()})
			e, err := Build(c.spec)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			got, err := e.Extract(root)
			if took := time.Since(start); took > otherBound*raceSlowdown {
				t.Errorf("took %v, over %v", took, otherBound*raceSlowdown)
			}
			switch {
			case c.refused != "":
				if err == nil || !strings.HasPrefix(err.Error(), c.refused) {
					t.Errorf("error %v, want %q", err, c.refused)
				}
			case err != nil:
				t.Errorf("error %v", err)
			case len(got) != c.values || (c.values > 0 && !strings.HasPrefix(got[len(got)-1].Text, c.last)):
				t.Errorf("%d values, want %d", len(got), c.values)
			}
		})
	}
}

// In make test at 16 KiB, under -race and coverage too, where every loop
// costs about thirty times its plain time. The quadratic passes the
// reviews found have witnesses that do not depend on time:
// TestMatcherComparesEachSectionOnce, TestCheckRangesCopiesNothing,
// TestPHPRefusesDeepReferences and the digit limit's refusal.
func TestReplayINIShapes(t *testing.T) {
	if mutationRun {
		t.Skip("the bound is on a whole parse, which no mutant of an operator changes")
	}
	runINIReplays(t, 16<<10)
}

// The same at 1 MiB, where a quadratic pass passes the bound.
func TestReplayINIShapesAtFullSize(t *testing.T) {
	slowOnly(t)
	runINIReplays(t, 1<<20)
}
