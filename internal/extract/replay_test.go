package extract

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// raceSlowdown is how much the race detector slows a case; its bound is
// that many times longer under -race.
var raceSlowdown = map[bool]time.Duration{false: 1, true: 10}[raceEnabled]

// replay is one input measured against the extractors' earlier limits,
// rebuilt by its shape: what reading it must give.
type replay struct {
	name string
	spec Spec // its file is f, with the extension of its kind
	body func() string
	// Either values is the count of values and last the text of the last
	// One, or refused is the whole message.
	values  int
	last    string
	refused string
	// timeLimit replaces regexTimeLimit, so a refusal on time comes fast.
	timeLimit time.Duration
	// bound is the most time reading the input may take, without -race.
	// A case that replays a quadratic pass sets it at no more than a third
	// of the time that pass takes on the input, and at three times or more
	// the slowest of three linear runs on a loaded machine, under -race too;
	// its comment gives the three times.
	bound time.Duration
	// full marks a case at the size measured against its quadratic pass,
	// which only make slow runs: -race slows it past make test's time. A
	// case of the same shape at a tenth of the size stays in make test.
	full bool
}

// repeatLines repeats line(i) for i from 0 to n-1.
func repeatLines(n int, line func(i int) string) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(line(i))
	}
	return b.String()
}

func keyLines(n int, sep string) string {
	return repeatLines(n, func(i int) string { return "k" + strconv.Itoa(i) + sep + "1\n" })
}

func list(n int, item string) string {
	return strings.TrimSuffix(strings.Repeat(item+", ", n), ", ")
}

// laughs is n levels of lists of ten aliases of the level before.
func laughs(n int) string {
	return "a0: &a0 [" + list(10, "x") + "]\n" + repeatLines(n, func(i int) string {
		l := strconv.Itoa(i + 1)
		return "a" + l + ": &a" + l + " [" + list(10, "*a"+strconv.Itoa(i)) + "]\n"
	})
}

func mergeChain(n int) string {
	return "- &a0 {k0: 1}\n" + repeatLines(n-1, func(j int) string {
		i := strconv.Itoa(j + 1)
		return "- &a" + i + " {<<: *a" + strconv.Itoa(j) + ", k" + i + ": 1}\n"
	})
}

// openAPI is an OpenAPI document of n paths of three operations each.
func openAPI(n int) string {
	op := func(i, m string) string {
		return "    " + m + ":\n      summary: Operation " + m + " on resource " + i + "\n      operationId: " + m + "Resource" + i +
			"\n      parameters:\n        - name: limit\n          in: query\n          required: false\n          schema:\n            type: integer\n            format: int32\n" +
			"      responses:\n        '200':\n          description: A paged array of resources\n          content:\n            application/json:\n              schema:\n                $ref: '#/components/schemas/Resource'\n        default:\n          description: unexpected error\n"
	}
	return "openapi: 3.0.0\ninfo:\n  title: x\n  version: 1.0.0\npaths:\n" + repeatLines(n, func(i int) string {
		s := strconv.Itoa(i)
		return "  /resource" + s + ":\n" + op(s, "get") + op(s, "put") + op(s, "delete")
	})
}

// helmIndex is a Helm chart repository's index.yaml: charts of n versions.
func helmIndex(charts, n int) string {
	return "apiVersion: v1\nentries:\n" + repeatLines(charts, func(c int) string {
		return fmt.Sprintf("  chart%d:\n", c) + repeatLines(n, func(v int) string {
			return fmt.Sprintf(`  - annotations:
      artifacthub.io/license: Apache-2.0
    apiVersion: v2
    appVersion: v%[2]d.0.0
    created: "2026-01-01T00:00:00.000000000Z"
    dependencies:
    - condition: crds.enabled
      name: crds
      repository: ""
      version: 0.0.0
    description: A chart that runs one service of the cluster
    digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    home: https://example.com/chart%[1]d
    keywords:
    - monitoring
    - metrics
    maintainers:
    - email: someone@example.com
      name: someone
    name: chart%[1]d
    sources:
    - https://example.com/src
    type: application
    urls:
    - https://example.com/charts/chart%[1]d-%[2]d.0.0.tgz
    version: %[2]d.0.0
`, c, v)
		})
	}) + "generated: \"2026-10-17T00:00:00Z\"\n"
}

// uvLock is a uv.lock of n packages of w wheels.
func uvLock(n, w int) string {
	return "version = 1\nrequires-python = \">=3.9\"\n\n" + repeatLines(n, func(i int) string {
		return fmt.Sprintf("[[package]]\nname = \"p%d\"\nversion = \"1.0\"\nsource = { registry = \"https://pypi.org/simple\" }\n"+
			"dependencies = [\n    { name = \"a\" },\n    { name = \"b\" },\n]\nsdist = { url = \"https://x/p.tar.gz\", hash = \"sha256:ab\", size = 1 }\nwheels = [\n", i) +
			repeatLines(w, func(j int) string {
				return fmt.Sprintf("    { url = \"https://x/p%d.whl\", hash = \"sha256:ab\", size = %d },\n", j, j)
			}) +
			"]\n\n[package.optional-dependencies]\nx = [\n    { name = \"c\" },\n]\n\n"
	})
}

// poetryLock is a poetry.lock of format 1.1: n packages, then one array of
// files for each.
func poetryLock(n, files int) string {
	return repeatLines(n, func(i int) string {
		return fmt.Sprintf("[[package]]\nname = \"p%d\"\nversion = \"1.0\"\n\n[package.dependencies]\nx = \">=1\"\n\n", i)
	}) + "[metadata]\nlock-version = \"1.1\"\n\n[metadata.files]\n" + repeatLines(n, func(i int) string {
		return fmt.Sprintf("p%d = [\n", i) + repeatLines(files, func(j int) string {
			return fmt.Sprintf("    {file = \"p%d-%d.whl\", hash = \"sha256:abcdef\"},\n", i, j)
		}) + "]\n"
	})
}

const (
	deepRefused   = "f: line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file"
	valuesRefused = "f: the extractor selects more than 100000 values, the limit; narrow the path or the pattern so it selects fewer"
	aliasRefused  = "aliases and merge keys expand to more than 1000000 nodes, the limit; write the repeated part out, or a regex extractor can still read the file"
)

// The inputs that took quadratic time, that the earlier limits refused
// though they are ordinary, and that got past those limits. Times are
// linear, linear under -race, and the quadratic pass, measured on this
// package with that pass put back. A quadratic case is full, at the size its
// times were measured at, and make slow runs it; make test runs the same
// shape at a tenth of the size, which reads, within otherBound. Two more are
// full, the merge chain and the TOML list, since no smaller input reaches
// their limit; another test reaches it in make test.
var replays = []replay{
	// Repeated keys, quadratic in yaml.v3's and go-toml's decode.
	// Linear 0.30 s, 1.20 s under -race;
	// quadratic 3.67 s; bound 1.2 s.
	{name: "YAML mapping of 25000 keys", spec: Spec{YAML: &YAML{File: "f", Path: "k24999"}},
		body: func() string { return keyLines(25000, ": ") }, values: 1, last: "1", bound: 1200 * time.Millisecond, full: true},
	{name: "YAML mapping of 2500 keys", spec: Spec{YAML: &YAML{File: "f", Path: "k2499"}},
		body: func() string { return keyLines(2500, ": ") }, values: 1, last: "1"},
	// Linear 0.08 s, 0.23 s under -race;
	// quadratic 2.74 s; bound 0.9 s.
	{name: "TOML table of 30000 keys", spec: Spec{TOML: &TOML{File: "f", Path: "k29999"}},
		body: func() string { return keyLines(30000, " = ") }, values: 1, last: "1", bound: 900 * time.Millisecond, full: true},
	{name: "TOML table of 3000 keys", spec: Spec{TOML: &TOML{File: "f", Path: "k2999"}},
		body: func() string { return keyLines(3000, " = ") }, values: 1, last: "1"},
	// Linear 0.13 s, 0.39 s under -race;
	// quadratic 3.96 s; bound 1.3 s.
	{name: "30000 TOML tables of one key", spec: Spec{TOML: &TOML{File: "f", Path: "t29999.k"}},
		body: func() string {
			return repeatLines(30000, func(i int) string { return "[t" + strconv.Itoa(i) + "]\nk = 1\n" })
		}, values: 1, last: "1", bound: 1300 * time.Millisecond, full: true},
	{name: "3000 TOML tables of one key", spec: Spec{TOML: &TOML{File: "f", Path: "t2999.k"}},
		body: func() string {
			return repeatLines(3000, func(i int) string { return "[t" + strconv.Itoa(i) + "]\nk = 1\n" })
		}, values: 1, last: "1"},
	// Linear 0.09 s, 0.42 s under -race;
	// quadratic 2.79 s; bound 0.9 s.
	{name: "nested array tables, then 30000 keys", spec: Spec{TOML: &TOML{File: "f", Path: "a[1].k29999"}},
		body: func() string {
			return "[[a]]\n" + strings.Repeat("[[a.b]]\n", 20000) + "[[a]]\n" + keyLines(30000, " = ")
		},
		values: 1, last: "1", bound: 900 * time.Millisecond, full: true},
	{name: "nested array tables, then 3000 keys", spec: Spec{TOML: &TOML{File: "f", Path: "a[1].k2999"}},
		body: func() string {
			return "[[a]]\n" + strings.Repeat("[[a.b]]\n", 2000) + "[[a]]\n" + keyLines(3000, " = ")
		},
		values: 1, last: "1"},
	// Linear 0.24 s, 1.51 s under -race;
	// quadratic 4.80 s; bound 1.6 s.
	{name: "2 YAML documents of 20000 keys", spec: Spec{YAML: &YAML{File: "f", Path: "k19998", Kind: "X"}},
		body: func() string { return strings.Repeat("kind: X\n"+keyLines(19999, ": ")+"---\n", 2) }, values: 2, last: "1", bound: 1600 * time.Millisecond, full: true},
	{name: "2 YAML documents of 2000 keys", spec: Spec{YAML: &YAML{File: "f", Path: "k1998", Kind: "X"}},
		body: func() string { return strings.Repeat("kind: X\n"+keyLines(1999, ": ")+"---\n", 2) }, values: 2, last: "1"},
	// A line index per document. Linear 0.07 s, 0.63 s under -race;
	// quadratic 3.61 s; bound 1.2 s.
	{name: "7000 YAML documents of one key", spec: Spec{YAML: &YAML{File: "f", Path: "kind", Kind: "X"}},
		body: func() string { return strings.Repeat("kind: X\n---\n", 7000) }, values: 7000, last: "X", bound: 1200 * time.Millisecond, full: true},
	{name: "700 YAML documents of one key", spec: Spec{YAML: &YAML{File: "f", Path: "kind", Kind: "X"}},
		body: func() string { return strings.Repeat("kind: X\n---\n", 700) }, values: 700, last: "X"},
	// Linear 0.23 s, 0.94 s under -race;
	// quadratic 3.42 s; bound 1.1 s.
	{name: "20000 TOML sections of four keys", spec: Spec{TOML: &TOML{File: "f", Path: "s19999.d"}},
		body: func() string {
			return repeatLines(20000, func(i int) string { return fmt.Sprintf("[s%d]\na = 1\nb = \"x\"\nc = true\nd = 2\n", i) })
		}, values: 1, last: "2", bound: 1100 * time.Millisecond, full: true},
	{name: "2000 TOML sections of four keys", spec: Spec{TOML: &TOML{File: "f", Path: "s1999.d"}},
		body: func() string {
			return repeatLines(2000, func(i int) string { return fmt.Sprintf("[s%d]\na = 1\nb = \"x\"\nc = true\nd = 2\n", i) })
		}, values: 1, last: "2"},
	// A regex whose optional tail scans the line searches it again after
	// every match. The time limit, 100 ms here, stops it. Linear 0.10 s, 0.11 s under -race;
	// quadratic 4.59 s; bound 1.5 s.
	{name: "regex whose tail scans the line", spec: Spec{Regex: &Regex{File: "f", Pattern: `(\d)(?:.*x)?`}},
		body: func() string { return strings.Repeat("1 ", 10000) }, timeLimit: 100 * time.Millisecond,
		refused: "f: matching took more than 100ms, the limit, and stopped after", bound: 1500 * time.Millisecond},
	// Linear 0.10 s, 0.12 s under -race;
	// quadratic 5.87 s; bound 1.5 s.
	{name: "the same with (?s) over many lines", spec: Spec{Regex: &Regex{File: "f", Pattern: `(?s)(\d)(?:.*x)?`}},
		body: func() string { return strings.Repeat("1\n", 10000) }, timeLimit: 100 * time.Millisecond,
		refused: "f: matching took more than 100ms, the limit, and stopped after", bound: 1500 * time.Millisecond},
	// One search that finds nothing: a counted repeat keeps a thousand
	// threads alive at each byte. The limit stops it inside the search.
	// Stopped 0.12 s, 0.17 s under -race; the search unclocked, with the
	// limit checked only between searches, 3.3 s, 58.6 s under -race;
	// bound 1 s.
	{name: "one search of a counted repeat", spec: Spec{Regex: &Regex{File: "f", Pattern: `([ab]{0,1000}c)`}},
		body: func() string { return strings.Repeat("b", 64<<10) }, timeLimit: 100 * time.Millisecond,
		refused: "f: matching took more than 100ms, the limit, and stopped after 0 matches", bound: time.Second},

	// The other shapes, each bounded by otherBound: none of them replays a
	// quadratic pass.
	{name: "2000 YAML mappings of two keys", spec: Spec{YAML: &YAML{File: "f", Path: "[-1].y"}},
		body: func() string { return strings.Repeat("- {x: 1, y: 2}\n", 2000) }, values: 1, last: "2"},
	{name: "10000 TOML array tables", spec: Spec{TOML: &TOML{File: "f", Path: "a[-1].x"}},
		body: func() string { return strings.Repeat("[[a]]\nx = 1\n", 10000) }, values: 1, last: "1"},
	// Values unbounded: a regex (\d*) matched at every byte, which
	// TestExtractValueLimit replays at the limit, and a TOML list gave
	// every item. No shorter list reaches the limit, and JSON stands for
	// TOML in TestExtractValueLimit, so the list is full.
	{name: "TOML list of 100001 items", spec: Spec{TOML: &TOML{File: "f", Path: "a[*]"}},
		body: func() string { return "a = [" + list(100001, "1") + "]\n" }, refused: valuesRefused, full: true},
	// TOML dotted keys and headers that nothing bounded the depth of.
	{name: "TOML dotted key of 100000 parts", spec: Spec{TOML: &TOML{File: "f", Path: "a"}},
		body: func() string { return strings.Repeat("a.", 100000) + "b = 1\n" }, refused: deepRefused},
	{name: "TOML header of 100000 parts", spec: Spec{TOML: &TOML{File: "f", Path: "a"}},
		body: func() string { return "[" + strings.Repeat("a.", 100000) + "b]\n" }, refused: deepRefused},
	// Alias bombs. Four levels of ten aliases add 111110 nodes and are
	// read; six add over 1000000.
	{name: "four levels of ten aliases", spec: Spec{YAML: &YAML{File: "f", Path: "a4[9][9][9][9][9]"}},
		body: func() string { return laughs(4) }, values: 1, last: "x"},
	{name: "six levels of ten aliases", spec: Spec{YAML: &YAML{File: "f", Path: "a0[0]"}},
		body: func() string { return laughs(6) }, refused: "f: line 6: " + aliasRefused},
	// A merge chain, which ran out of 4 GB at 56000 items, is refused at
	// its 817th. The count stops it; up to there the merges copy 334000
	// keys, so it takes longer than the other shapes. Slowest of three on a
	// loaded machine 0.62 s, 2.31 s under -race; 1.06 s at a load of 8.4 on
	// four CPUs, which failed otherBound; bound 3.5 s, over three times that.
	// No shorter chain reaches the limit, and TestYAMLMergeChainIsCounted is
	// the same refusal in make test, without the bound.
	{name: "merge chain of 1000 items", spec: Spec{YAML: &YAML{File: "f", Path: "[0].k0"}},
		body: func() string { return mergeChain(1000) }, refused: "f: line 817: " + aliasRefused, bound: 3500 * time.Millisecond, full: true},
	{name: "10000 aliases of 100 keys", spec: Spec{YAML: &YAML{File: "f", Path: "base.k0"}},
		body: func() string {
			return "base: &b\n" + strings.ReplaceAll(keyLines(100, ": "), "k", "  k") + "l:\n" + strings.Repeat("- *b\n", 10000)
		},
		refused: "f: line 5078: " + aliasRefused},
	// Depth, at the limit and past it, and 100 KB of [ in each format.
	{name: "JSON 1000 levels deep", spec: Spec{JSON: &JSON{File: "f", Path: "$" + strings.Repeat("[0]", 1000)}},
		body: func() string { return nest(1000, `"x"`) }, values: 1, last: "x"},
	{name: "JSON 1001 levels deep", spec: Spec{JSON: &JSON{File: "f", Path: "$"}},
		body: func() string { return nest(1001, "1") }, refused: deepRefused},
	{name: "100 KB of JSON [", spec: Spec{JSON: &JSON{File: "f", Path: "$"}},
		body: func() string { return strings.Repeat("[", 100000) }, refused: deepRefused},
	{name: "100 KB of YAML [", spec: Spec{YAML: &YAML{File: "f", Path: "$"}},
		body: func() string { return strings.Repeat("[", 100000) }, refused: "f: yaml: exceeded max depth of 10000"},
	{name: "100 KB of TOML [", spec: Spec{TOML: &TOML{File: "f", Path: "a"}},
		body: func() string { return "a = " + strings.Repeat("[", 100000) }, refused: "f: toml: line 1: arrays and inline tables are nested more than the maximum of 10000 levels deep"},
	{name: "JSON 999 levels around 20000 items", spec: Spec{JSON: &JSON{File: "f", Path: "$" + strings.Repeat("[0]", 998) + "[-1]"}},
		body: func() string { return strings.Repeat("[", 999) + list(20000, "1") + strings.Repeat("]", 999) }, values: 1, last: "1"},
	// Ordinary TOML the step model refused. TestNodeLimitLeavesRoom checks
	// the node limit against the full sizes of the ordinary files.
	{name: "500 TOML tables of 20 keys", spec: Spec{TOML: &TOML{File: "f", Path: "t499.k19"}},
		body: func() string {
			return repeatLines(500, func(i int) string { return "[t" + strconv.Itoa(i) + "]\n" + keyLines(20, " = ") })
		}, values: 1, last: "1"},
	{name: "2000 TOML tables of 10 keys", spec: Spec{TOML: &TOML{File: "f", Path: "t1999.k9"}},
		body: func() string {
			return repeatLines(2000, func(i int) string { return "[t" + strconv.Itoa(i) + "]\n" + keyLines(10, " = ") })
		}, values: 1, last: "1"},
	{name: "10000 translation tables", spec: Spec{TOML: &TOML{File: "f", Path: "msg9999.other"}},
		body: func() string {
			return repeatLines(10000, func(i int) string { return fmt.Sprintf("[msg%d]\nother = \"text %d\"\n\n", i, i) })
		}, values: 1, last: "text 9999"},
	{name: "poetry.lock of 200 packages", spec: Spec{TOML: &TOML{File: "f", Path: "metadata.files.p199[-1].file"}},
		body: func() string { return poetryLock(200, 30) }, values: 1, last: "p199-29.whl"},
	{name: "uv.lock of 200 packages", spec: Spec{TOML: &TOML{File: "f", Path: "package[*].wheels[-1].size"}},
		body: func() string { return uvLock(200, 20) }, values: 200, last: "19"},
	// Ordinary YAML the size cap refused, at a size the cap allowed.
	{name: "OpenAPI of 100 paths", spec: Spec{YAML: &YAML{File: "f", Path: "paths['/resource99'].delete.operationId"}},
		body: func() string { return openAPI(100) }, values: 1, last: "deleteResource99"},
	{name: "Helm index of 100 chart versions", spec: Spec{YAML: &YAML{File: "f", Path: "entries.chart9[*].version"}},
		body: func() string { return helmIndex(10, 10) }, values: 10, last: "9.0.0"},
	{name: "30 flow mappings of 100 keys", spec: Spec{YAML: &YAML{File: "f", Path: "[-1].k99"}},
		body: func() string {
			return strings.Repeat("- {"+strings.TrimSuffix(strings.ReplaceAll(keyLines(100, ": "), "\n", ", "), ", ")+"}\n", 30)
		}, values: 1, last: "1"},
}

// otherBound is the bound of a case that replays no quadratic pass. The
// slowest of them takes 0.17 s, and 0.48 s under -race.
const otherBound = time.Second

func TestReplayMeasuredInputs(t *testing.T) {
	replayCases(t, false)
}

// The full cases, at the sizes their times were measured at.
func TestReplayMeasuredInputsAtFullSize(t *testing.T) {
	slowOnly(t)
	replayCases(t, true)
}

// replayCases runs the cases whose full is full.
func replayCases(t *testing.T, full bool) {
	t.Helper()
	for _, c := range replays {
		if c.full != full {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			if mutationRun && c.timeLimit == 0 {
				// Under Gremlins only the cases of the regex clock run: the
				// others bound the time of a whole parse, which no mutant of
				// an operator was seen to change, and cost a second a mutant.
				t.Skip("make test runs it; Gremlins runs the cases that stop on the clock")
			}
			root := repo(t, map[string]string{"f": c.body()})
			e, err := Build(c.spec)
			if err != nil {
				t.Fatal(err)
			}
			if r, ok := e.(regexExtractor); ok && c.timeLimit > 0 {
				r.timeLimit = c.timeLimit
				e = r
			}
			start := time.Now()
			got, err := e.Extract(root)
			bound := c.bound
			if bound == 0 {
				bound = otherBound
			}
			if took := time.Since(start); took > bound*raceSlowdown {
				t.Errorf("took %v, over %v", took, bound*raceSlowdown)
			}
			switch {
			case c.refused != "":
				if err == nil || !strings.HasPrefix(err.Error(), c.refused) {
					t.Errorf("error %v, want %q", err, c.refused)
				}
			case err != nil:
				t.Errorf("error %v, want %d values", err, c.values)
			case len(got) != c.values || got[len(got)-1].Text != c.last:
				t.Errorf("%d values, %s; want %d, the last %q", len(got), texts(got[max(0, len(got)-3):]), c.values, c.last)
			}
		})
	}
}
