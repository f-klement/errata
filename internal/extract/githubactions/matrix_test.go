package githubactions

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The documentation's multi-dimensional matrix: "The workflow will run six
// jobs, one for each combination of the os and version variables", and
// "The order of the variables in the matrix determines the order in which
// the jobs are created" (Workflow syntax, strategy.matrix). Each job's
// value is at the matrix's line that sets it, and carries the job's name.
// Planted breaks: the first variable varying fastest; the line of the
// value's template kept for a lone reference.
func TestMatrixCombinations(t *testing.T) {
	body := `jobs:
  example_matrix:
    strategy:
      matrix:
        os: [ubuntu-22.04, ubuntu-24.04]
        version: [10, 12, 14]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/setup-node@v7
        id: node
        with:
          node-version: ${{ matrix.version }}
`
	want(t, see(t, body, "example_matrix", "node", "with.node-version"),
		"10@6[example_matrix (ubuntu-22.04, 10)] 12@6[example_matrix (ubuntu-22.04, 12)] 14@6[example_matrix (ubuntu-22.04, 14)] "+
			"10@6[example_matrix (ubuntu-24.04, 10)] 12@6[example_matrix (ubuntu-24.04, 12)] 14@6[example_matrix (ubuntu-24.04, 14)]")
	got := see(t, body, "example_matrix", "", "runs-on")
	if !strings.HasPrefix(got, "ubuntu-22.04@5[example_matrix (ubuntu-22.04, 10)] ubuntu-22.04@5[") || strings.Count(got, "@5[") != 6 {
		t.Fatalf("got %q", got)
	}
	// A value that names no matrix variable is one value with no condition.
	want(t, see(t, body, "example_matrix", "node", "uses"), "actions/setup-node@v7@9")
}

// The documentation's include example: {color: green} goes into every
// combination, {color: pink, animal: cat} overwrites the added green
// where animal is cat, {fruit: apple, shape: circle} goes into both
// apples, and the two banana entries each overwrite an original value, so
// each is a job of its own; the second is not added to the first's. "the
// original matrix values will not be overwritten, but added matrix values
// can be overwritten" (Workflow syntax, strategy.matrix.include). A
// variable a job does not have is null, which a value writes as "".
// Planted breaks: include matched against the jobs it made, which added
// animal: cat to {fruit: banana}; an added value never overwritten.
func TestMatrixInclude(t *testing.T) {
	body := `jobs:
  j:
    strategy:
      matrix:
        fruit: [apple, pear]
        animal: [cat, dog]
        include:
          - color: green
          - color: pink
            animal: cat
          - fruit: apple
            shape: circle
          - fruit: banana
          - fruit: banana
            animal: cat
    env:
      ALL: ${{ matrix.fruit }}/${{ matrix.animal }}/${{ matrix.color }}/${{ matrix.shape }}
`
	want(t, see(t, body, "j", "", "ALL"),
		"apple/cat/pink/circle@17[j (apple, cat, pink, circle)] apple/dog/green/circle@17[j (apple, dog, green, circle)] "+
			"pear/cat/pink/@17[j (pear, cat, pink)] pear/dog/green/@17[j (pear, dog, green)] "+
			"banana///@17[j (banana)] banana/cat//@17[j (banana, cat)]")
	// The documented example cannot tell that a job an entry makes takes
	// no later entry, since its later entry sets animal, which the job
	// does not have. Here {a: 2, b: x} agrees with the job {a: 2} makes.
	body = "jobs:\n  j:\n    strategy:\n      matrix:\n        a: [1]\n        include: [{a: 2}, {a: 2, b: x}]\n    env: {V: '${{ matrix.a }}${{ matrix.b }}'}\n"
	want(t, see(t, body, "j", "", "V"), "1@7[j (1)] 2@7[j (2)] 2x@7[j (2, x)]")
}

// An include: entry that agrees with a combination sets only the keys the
// matrix's variables do not hold, so a variable's value stays at the
// matrix's line. Planted break: the first variable set from the entry too,
// which moved it to line 6.
func TestIncludeKeepsTheMatrixLine(t *testing.T) {
	body := "jobs:\n  j:\n    strategy:\n      matrix:\n        a: [1]\n        include: [{a: 1, b: x}]\n    env: {V: '${{ matrix.a }}'}\n"
	want(t, see(t, body, "j", "", "V"), "1@5[j (1, x)]")
}

// The documentation's two other include examples: an entry that agrees
// with one combination adds npm to it alone, and an entry whose version
// is in no combination is a job of its own, "10 jobs". A matrix of only
// include: entries runs one job for each. Planted break: an entry added
// where only some of its variables agree.
func TestMatrixIncludeExamples(t *testing.T) {
	body := `jobs:
  expand:
    strategy:
      matrix:
        os: [windows-latest, ubuntu-latest]
        node: [14, 16]
        include:
          - os: windows-latest
            node: 16
            npm: 6
    env: {NPM: "${{ matrix.npm }}"}
  add:
    strategy:
      matrix:
        os: [macos-latest, windows-latest, ubuntu-latest]
        version: [12, 14, 16]
        include:
          - os: windows-latest
            version: 17
    env: {V: "${{ matrix.version }}"}
  includes_only:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include:
          - site: "production"
            datacenter: "site-a"
          - site: "staging"
            datacenter: "site-b"
    env: {SITE: "${{ matrix.site }}-${{ matrix.datacenter }}"}
`
	want(t, see(t, body, "expand", "", "NPM"),
		"@11[expand (windows-latest, 14)] 6@10[expand (windows-latest, 16, 6)] @11[expand (ubuntu-latest, 14)] @11[expand (ubuntu-latest, 16)]")
	got := see(t, body, "add", "", "V")
	if n := strings.Count(got, "@"); n != 10 || !strings.HasSuffix(got, "17@19[add (windows-latest, 17)]") {
		t.Fatalf("got %d jobs: %q", n, got)
	}
	want(t, see(t, body, "includes_only", "", "SITE"),
		"production-site-a@30[includes_only (production, site-a)] staging-site-b@30[includes_only (staging, site-b)]")
}

// "An excluded configuration only has to be a partial match for it to be
// excluded", and "All include combinations are processed after exclude.
// This allows you to use include to add back combinations that were
// previously excluded." Planted breaks: exclude needing every variable to
// match; include before exclude.
func TestMatrixExclude(t *testing.T) {
	body := `jobs:
  j:
    strategy:
      matrix:
        os: [a, b]
        v: [1, 2]
        exclude:
          - os: b
        include:
          - os: b
            v: 2
    env: {X: "${{ matrix.os }}${{ matrix.v }}"}
`
	want(t, see(t, body, "j", "", "X"), "a1@12[j (a, 1)] a2@12[j (a, 2)] b2@12[j (b, 2)]")
	// An entry names a variable in any case: "The variable names are case
	// insensitive." Planted break: the entries' keys matched exactly, which
	// neither excluded b nor added the include to b's job.
	cased := strings.Replace(strings.Replace(body, "- os: b\n        include", "- OS: b\n        include", 1), "- os: b\n            v: 2", "- Os: b\n            V: 2", 1)
	want(t, see(t, cased, "j", "", "X"), "a1@12[j (a, 1)] a2@12[j (a, 2)] b2@12[j (b, 2)]")
	cased = "jobs:\n  j:\n    strategy:\n      matrix:\n        os: [a, b]\n        include: [{OS: b, extra: x}]\n    env: {X: '${{ matrix.os }}${{ matrix.extra }}'}\n"
	want(t, see(t, cased, "j", "", "X"), "a@7[j (a)] bx@7[j (b, x)]")
	extracttest.WantError(t, refused(t, strings.Replace(body, "- os: b\n        include", "- v: 1\n          - v: 2\n        excludes", 1), "j", "", "X"),
		"the matrix of job \"j\" gives no job: exclude: removes every combination")
	// A mapping matches a mapping of the same keys and values, at any
	// depth. Planted breaks: equalMappings always true, and always false; the lists' lengths not compared.
	body = "jobs:\n  j:\n    strategy:\n      matrix:\n        node: [{v: 14, o: [a]}, {v: 20, o: [a]}, {v: 14}]\n        exclude: [{node: {o: [a], v: 14}}, {node: {v: 20, o: [a, x]}}]\n    env: {V: '${{ matrix.node.v }}'}\n"
	want(t, see(t, body, "j", "", "V"), "20@5[j ({v: 20, o: [a]})] 14@5[j ({v: 14})]")
}

// A matrix value may be a mapping, read with a dotted reference, as the
// documentation's node.version example has it. The names match in any
// case: "The variable names are case insensitive." A value that is not a
// lone reference stays at the template's line. A reference to a mapping,
// or a reference beside any other expression, is the value as written.
// Planted breaks: the names compared exactly; the dotted path not
// followed.
func TestMatrixReferences(t *testing.T) {
	body := `jobs:
  j:
    strategy:
      matrix:
        os: [linux]
        node:
          - version: 14
          - version: 20
            env: NODE_OPTIONS=--openssl-legacy-provider
    env:
      V: ${{ matrix.Node.Version }}
      O: ${{ matrix.node.env }}
      P: node-${{ matrix.OS }}
      M: ${{ matrix.node }}
      X: ${{ matrix.os || 'x' }}
`
	// The second name is over 64 bytes, so linux, which both share, is
	// written as ....
	names := []string{"[j (..., {version: 14})]", "[j (..., {version: 20, env: NODE_OPTIONS=--openssl-legacy-provider})]"}
	want(t, see(t, body, "j", "", "V"), "14@7"+names[0]+" 20@8"+names[1])
	want(t, see(t, body, "j", "", "O"), "@12"+names[0]+" NODE_OPTIONS=--openssl-legacy-provider@9"+names[1])
	want(t, see(t, body, "j", "", "P"), "node-linux@13"+names[0]+" node-linux@13"+names[1])
	want(t, see(t, body, "j", "", "M"), "${{ matrix.node }}!@14"+names[0]+" ${{ matrix.node }}!@14"+names[1])
	want(t, see(t, body, "j", "", "X"), "${{ matrix.os || 'x' }}!@15")
	// A matrix value that is an expression is GitHub's to evaluate, so the
	// value it gives is as written and not literal. Planted break: the
	// expression check on the substituted value dropped, which gave it as
	// literal.
	body = "jobs:\n  j:\n    strategy:\n      matrix:\n        v: ['${{ secrets.X }}', b]\n    env: {V: '${{ matrix.v }}'}\n"
	want(t, see(t, body, "j", "", "V"), "${{ secrets.X }}!@5[j (${{ secrets.X }})] b@5[j (b)]")
}

// A matrix value GitHub's YAML retypes, such as 3.10, gives a value that is
// not literal; a quoted one is its text. Planted break: the retype check
// skipped for a substituted value.
func TestMatrixValuesRetype(t *testing.T) {
	body := "jobs:\n  j:\n    strategy:\n      matrix:\n        py: [3.10, '3.12']\n    env: {PY: '${{ matrix.py }}'}\n"
	want(t, see(t, body, "j", "", "PY"), "3.10!@5[j (3.10)] 3.12@5[j (3.12)]")
}

// What GitHub refuses, and what only an expression decides, does not
// resolve for a value that names the matrix. A matrix with no variable
// and no include: entry is named for what it lacks. Planted breaks: each
// check dropped in turn; the check for no variable dropped, which blamed
// an exclude: the matrix does not have.
func TestMatrixRefusals(t *testing.T) {
	for _, c := range []struct{ strategy, want string }{
		{"${{ fromJSON(x) }}", `the strategy of job "j" is an expression`},
		{"[a]", `the strategy of job "j" is a sequence, not a mapping`},
		{"{matrix: '${{ fromJSON(needs.a.outputs.m) }}'}", `the matrix of job "j" is an expression, "${{ fromJSON(needs.a.outputs.m) }}"`},
		{"{matrix: [a]}", `the matrix of job "j" is a sequence, not a mapping`},
		{"{matrix: {v: '${{ github.event.client_payload.versions }}'}}", `the matrix variable "v" of job "j" is an expression`},
		{"{matrix: {v: 1}}", `the matrix variable "v" is a scalar, not a list of values`},
		{"{matrix: {v: []}}", `the matrix variable "v" holds no value, which GitHub refuses`},
		{"{matrix: {os: [a], OS: [b]}}", `has the variables "os" and "OS", which GitHub reads as one`},
		{"{matrix: {v: [1], include: '${{ x }}'}}", `the matrix's include: of job "j" is an expression`},
		{"{matrix: {v: [1], exclude: {v: 1}}}", `the matrix's exclude: is a mapping, not a list`},
		{"{matrix: {v: [1], include: ['${{ x }}']}}", `an entry of the matrix's include: of job "j" is an expression`},
		{"{matrix: {v: [1], include: [a]}}", `an entry of the matrix's include: is a scalar, not a mapping`},
		{"{matrix: {}}", `the matrix of job "j" has no variable and no include: entry, so it gives no job`},
		{"{matrix: {exclude: [{v: 1}]}}", `has no variable and no include: entry`},
	} {
		body := "jobs:\n  j:\n    strategy: " + c.strategy + "\n    env: {V: '${{ matrix.v }}'}\n"
		extracttest.WantError(t, refused(t, body, "j", "", "V"), c.want)
	}
	// A strategy without a matrix is no matrix.
	want(t, see(t, "jobs:\n  j:\n    strategy: {fail-fast: false}\n    env: {V: '${{ matrix.v }}'}\n", "j", "", "V"), "${{ matrix.v }}!@4")
}

// vector is a matrix variable of n values.
func vector(name string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "        %s: [", name)
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s%d", name, i)
	}
	b.WriteString("]\n")
	return b.String()
}

// "A matrix will generate a maximum of 256 jobs per workflow run": 256
// resolve, 257 do not, whether the variables or include: entries make
// them. Before exclude:, the product holds at most 65536 combinations.
// include: removes no job, so more than 256 after exclude: is refused
// before the include: entries are matched. Planted breaks: the limit at
// 257; the check on entries that make jobs dropped; the product's bound
// dropped, which built 65536 times 2; the check after exclude: dropped,
// which matched 20 entries against 65536 combinations to the step limit.
func TestMatrixLimits(t *testing.T) {
	head := "jobs:\n  j:\n    env: {V: '${{ matrix.a }}'}\n    strategy:\n      matrix:\n"
	got := see(t, head+vector("a", 16)+vector("b", 16), "j", "", "V")
	if n := strings.Count(got, "@"); n != 256 {
		t.Fatalf("got %d values, want 256", n)
	}
	extracttest.WantError(t, refused(t, head+vector("a", 257), "j", "", "V"), `ci.yml:6: the matrix of job "j" gives more than 256 jobs, GitHub's limit`)
	var inc strings.Builder
	inc.WriteString("        include:\n")
	for i := range 2 {
		fmt.Fprintf(&inc, "          - a: new%d\n", i)
	}
	extracttest.WantError(t, refused(t, head+vector("a", 255)+inc.String(), "j", "", "V"), "gives more than 256 jobs")
	inc.Reset()
	inc.WriteString("        include:\n          - a: new\n")
	got = see(t, head+vector("a", 255)+inc.String(), "j", "", "V")
	if n := strings.Count(got, "@"); n != 256 || !strings.HasSuffix(got, "new@8[j (new)]") {
		t.Fatalf("got %d values, ending %q; want 256, the last new", n, got[max(0, len(got)-20):])
	}
	// An include-only matrix is refused at its 257th entry.
	inc.Reset()
	inc.WriteString("        include:\n")
	for i := range 257 {
		fmt.Fprintf(&inc, "          - {a: %d}\n", i)
	}
	extracttest.WantError(t, refused(t, head+inc.String(), "j", "", "V"), `ci.yml:6: the matrix of job "j" gives more than 256 jobs, GitHub's limit`)
	// Building the product is a step for each value of each combination:
	// 65536 combinations of 16 variables pass the step limit.
	var vars strings.Builder
	for i := range 16 {
		fmt.Fprintf(&vars, "        v%d: [0, 1]\n", i)
	}
	extracttest.WantError(t, refused(t, head+vars.String(), "j", "", "V"), "the matrix takes more than 1000000 steps to build")
	extracttest.WantError(t, refused(t, head+vector("a", 256)+vector("b", 256)+vector("c", 2), "j", "", "V"),
		"has more than 65536 combinations before exclude:, errata's limit")
	inc.Reset()
	inc.WriteString("        include:\n" + strings.Repeat("          - {a: a0, x: 1}\n", 20))
	extracttest.WantError(t, refused(t, head+vector("a", 256)+vector("b", 256)+inc.String(), "j", "", "V"), `ci.yml:6: the matrix of job "j" gives more than 256 jobs`)
	exc := "        exclude:\n          - b: b1\n"
	got = see(t, head+vector("a", 128)+vector("b", 2)+exc, "j", "", "V")
	if n := strings.Count(got, "@"); n != 128 {
		t.Fatalf("got %d values, want 128", n)
	}
}

// A job an expression may remove or make is not certain, so GitHub's limit
// is decided on the certain jobs. When the jobs pass 256 only through
// uncertain ones, the count hangs on the expression: a value that names
// the matrix is unknown, naming it, and runs-on is read. An include: entry
// that agrees only with a combination exclude: may have removed is a job
// of its own only in that combination's place, so it adds no job to the
// count. Planted breaks: every job kept counted as certain; an entry that
// may join a job counted as a certain job of its own; an entry that joins
// a combination counted as a job of its own; the limit on all jobs at >=.
func TestMatrixLimitHangsOnExpressions(t *testing.T) {
	head := "jobs:\n  t:\n    runs-on: ubuntu-latest\n    env: {V: '${{ matrix.a }}'}\n    strategy:\n      matrix:\n"
	hangs := `whether the matrix of job "t" gives more than 256 jobs, GitHub's limit, hangs on "${{ vars.X }}"`
	for _, c := range []struct{ matrix, line string }{
		{vector("a", 17) + vector("b", 16) + "        exclude: [{a: '${{ vars.X }}'}]\n", "ci.yml:9: "},
		{vector("a", 16) + vector("b", 16) + "        include: [{a: '${{ vars.X }}', c: 1}]\n", "ci.yml:9: "},
	} {
		body := head + c.matrix
		want(t, see(t, body, "t", "", "runs-on"), "ubuntu-latest@3")
		extracttest.WantError(t, refused(t, body, "t", "", "V"), c.line+hangs)
	}
	// 256 jobs whatever exclude: does: if it removes (a0, 1), the entry is
	// a job in its place. The job is named.
	in := head + vector("a", 256) + "        b: [1]\n        exclude: [{a: a0, b: '${{ vars.X }}'}]\n        include: [{a: a0, c: 1}]\n"
	want(t, see(t, in, "t", "", "runs-on"), "ubuntu-latest@3")
	extracttest.WantError(t, refused(t, in, "t", "", "V"), `ci.yml:9: whether job "t (a0, 1, 1)" runs, and with which values, hangs on "${{ vars.X }}"`)
	// 256 jobs in all, one of them uncertain: the job is named.
	body := head + vector("a", 16) + vector("b", 16) + "        exclude: [{a: a0, b: '${{ vars.X }}'}]\n"
	extracttest.WantError(t, refused(t, body, "t", "", "V"), `whether job "t (a0, b0)" runs`)
	// 257 certain jobs and one uncertain are refused for every value.
	body = head + vector("a", 258) + "        b: [1]\n        exclude: [{a: a0, b: '${{ vars.X }}'}]\n"
	extracttest.WantError(t, refused(t, body, "t", "", "runs-on"), `the matrix of job "t" gives more than 256 jobs, GitHub's limit`)
}

// A matrix whose jobs pass 256 only through uncertain ones gives no value
// that names it, so errata keeps no job of an include: entry past the
// 256th: a long list of such entries holds 256 jobs, not one for each.
// Planted breaks: the bound on the append dropped, which held 301; the
// bound at <, which held 255.
func TestMatrixHoldsNoJobPastTheLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("jobs:\n  t:\n    strategy:\n      matrix:\n        a: [1]\n        include:\n")
	for i := range 300 {
		fmt.Fprintf(&b, "          - {a: '${{ vars.Y }}', n: %d}\n", i)
	}
	docs, _, err := yamlParse([]byte(b.String()), 0, func(*core.Node, string, bool) {})
	if err != nil {
		t.Fatal(err)
	}
	w := &workflow{file: "ci.yml", info: map[*core.Node]nodeInfo{}, ids: map[string]int{}, templates: map[*core.Node]madeTemplate{}}
	_, err = w.matrix("t", docs[0].Field["jobs"].Field["t"])
	extracttest.WantError(t, err, `whether the matrix of job "t" gives more than 256 jobs, GitHub's limit, hangs on "${{ vars.Y }}"`)
	if w.held != maxJobs {
		t.Fatalf("the matrix held %d jobs, want %d", w.held, maxJobs)
	}
}

// Each include: and exclude: entry is matched against every combination,
// so the steps that takes count against core.MaxExpanded. Planted break:
// the count dropped, which matched 5000 entries against 256 combinations.
func TestMatrixWorkLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("jobs:\n  j:\n    env: {V: '${{ matrix.a }}'}\n    strategy:\n      matrix:\n")
	b.WriteString(vector("a", 16) + vector("b", 16))
	b.WriteString("        include:\n")
	for i := range 5000 {
		fmt.Fprintf(&b, "          - {x: %d}\n", i)
	}
	extracttest.WantError(t, refused(t, b.String(), "j", "", "V"), "the matrix takes more than 1000000 steps to build, the limit")
	// An empty entry compares no key and agrees with every combination,
	// and its match is a step all the same. Planted break: the step of a
	// match dropped, which matched 5000 empty entries against 256
	// combinations.
	empty := "jobs:\n  j:\n    env: {V: '${{ matrix.a }}'}\n    strategy:\n      matrix:\n" + vector("a", 16) + vector("b", 16) +
		"        include:\n" + strings.Repeat("          - {}\n", 5000)
	extracttest.WantError(t, refused(t, empty, "j", "", "V"), "the matrix takes more than 1000000 steps to build, the limit")
	// Looking a key up in a combination is a step, and one more for each
	// key it holds, even in a combination of no key. Planted breaks: the
	// step of the lookup dropped, in get and in set in turn; the keys
	// subtracted.
	refs := func(n int) string { return strings.Repeat("${{ matrix.zz }}", n) }
	head := "jobs:\n  j:\n    strategy:\n      matrix:\n"
	keyed := head + vector("a", 16) + vector("b", 16) + "    env: {V: '" + refs(1500) + "'}\n"
	extracttest.WantError(t, refused(t, keyed, "j", "", "V"), "the matrix takes more than 1000000 steps to build, the limit")
	keyless := head + "        include:\n" + strings.Repeat("          - {}\n", 256) + "    env: {V: '" + refs(4000) + "'}\n"
	extracttest.WantError(t, refused(t, keyless, "j", "", "V"), "the matrix takes more than 1000000 steps to build, the limit")
	var sets strings.Builder
	sets.WriteString(head + vector("a", 16) + vector("b", 16) + "        include:\n")
	for i := range 900 {
		fmt.Fprintf(&sets, "          - {x: %d}\n", i)
	}
	sets.WriteString("    env: {V: '${{ matrix.a }}'}\n")
	extracttest.WantError(t, refused(t, sets.String(), "j", "", "V"), "the matrix takes more than 1000000 steps to build, the limit")
	// A nested key that misses its exact match is looked for in any case
	// among the other side's keys, a step for each. Planted break: that
	// search uncounted, which compared 4000 keys in each of 256
	// combinations.
	keys := func(p string) string {
		var b strings.Builder
		for i := range 4000 {
			fmt.Fprintf(&b, "%s%d: 1, ", p, i)
		}
		return "{" + strings.TrimSuffix(b.String(), ", ") + "}"
	}
	nested := head + "        a: [" + keys("k") + "]\n" + vector("b", 256) + "        exclude: [{a: " + keys("j") + "}]\n    env: {V: '${{ matrix.a }}'}\n"
	extracttest.WantError(t, refused(t, nested, "j", "", "V"), "the matrix takes more than 1000000 steps to build, the limit")
}

// A report quotes 64 bytes of a condition, so a longer name writes the
// values every job shares as ..., and a name past 256 bytes is cut on a
// character's start. A long name: is cut, not the values. Planted breaks:
// no elision; a shared value kept; a name of 64 bytes elided; the name
// written whole; its room off by the "..." it takes; a name that fits cut;
// one of 16 bytes cut.
func TestMatrixLongNames(t *testing.T) {
	long := strings.Repeat("monitoring-", 6)
	body := "jobs:\n  j:\n    env: {V: '${{ matrix.a }}'}\n    strategy:\n      matrix:\n        x: [" + long + "]\n        a: [one, two]\n"
	want(t, see(t, body, "j", "", "V"), "one@7[j (..., one)] two@7[j (..., two)]")
	// A name of exactly 64 bytes is not long.
	exact := strings.Repeat("s", 55)
	body = "jobs:\n  j:\n    env: {V: '${{ matrix.a }}'}\n    strategy:\n      matrix:\n        x: [" + exact + "]\n        a: [one, two]\n"
	want(t, see(t, body, "j", "", "V"), "one@7[j ("+exact+", one)] two@7[j ("+exact+", two)]")
	// A long name: is cut so that the values fit in the 64 bytes a report
	// quotes, and to no fewer than 16 bytes.
	named := func(name, vals string) string {
		return "jobs:\n  j:\n    name: " + name + "\n    env: {V: '${{ matrix.a }}'}\n    strategy:\n      matrix:\n        a: [" + vals + "]\n"
	}
	n100, n59 := strings.Repeat("N", 100), strings.Repeat("N", 59)
	want(t, see(t, named(n100, "18, 20"), "j", "", "V"), "18@7["+n100[:56]+"... (18)] 20@7["+n100[:56]+"... (20)]")
	want(t, see(t, named(n59, "18"), "j", "", "V"), "18@7["+n59+" (18)]")
	v60 := strings.Repeat("v", 60)
	want(t, see(t, named(n100[:16], v60), "j", "", "V"), v60+"@7["+n100[:16]+" ("+v60+")]")
	want(t, see(t, named(n100[:17], v60), "j", "", "V"), v60+"@7["+n100[:16]+"... ("+v60+")]")
	huge := strings.Repeat("é", 200)
	body = "jobs:\n  j:\n    env: {V: '${{ matrix.a }}'}\n    strategy:\n      matrix:\n        a: [" + huge + "]\n"
	got := extracttest.MustRun(t, build, spec("j", "", "V"), extracttest.Repo(t, map[string]string{wf: body}))
	if c := got[0].Condition; !strings.HasSuffix(c, "...") || len(c) > maxCondition+3 || !utf8.ValidString(c) {
		t.Fatalf("got %d bytes: %q", len(c), c)
	}
}

// clipCondition keeps 256 bytes, backed off to a character's start.
func TestClipCondition(t *testing.T) {
	s := strings.Repeat("a", 255) + "€"
	if got := clipCondition(s); got != strings.Repeat("a", 255)+"..." {
		t.Fatalf("got %q", got)
	}
	if got := clipCondition(strings.Repeat("a", 256)); got != strings.Repeat("a", 256) {
		t.Fatalf("got %q", got)
	}
	if got := clipCondition(strings.Repeat("a", 257)); got != strings.Repeat("a", 256)+"..." {
		t.Fatalf("got %q", got)
	}
}

// GitHub names a job of a matrix by the job's name:, else its id, and
// appends the combination's values unless the name references the matrix
// itself (Workflow syntax, jobs.<job_id>.name: "The name of the job
// displayed in the GitHub UI"). An expression of any other kind stays as
// written in the name. Planted breaks: the id kept over name:; the values
// appended to a name that references the matrix; a matrix reference in the
// name left as written.
func TestMatrixJobName(t *testing.T) {
	job := func(name string) string {
		return "jobs:\n  t:\n    name: " + name + "\n    strategy: {matrix: {n: [18, 20], os: [linux]}}\n    env: {V: '${{ matrix.n }}'}\n"
	}
	want(t, see(t, job("Unit tests"), "t", "", "V"), "18@4[Unit tests (18, linux)] 20@4[Unit tests (20, linux)]")
	want(t, see(t, job("'Unit ${{ matrix.n }} on ${{ matrix.OS }}'"), "t", "", "V"), "18@4[Unit 18 on linux] 20@4[Unit 20 on linux]")
	want(t, see(t, job("'Build ${{ github.ref }}'"), "t", "", "V"), "18@4[Build ${{ github.ref }} (18, linux)] 20@4[Build ${{ github.ref }} (20, linux)]")
	want(t, see(t, job("\"${{ matrix.n == 18 && 'old' || 'new' }}\""), "t", "", "V"),
		"18@4[${{ matrix.n == 18 && 'old' || 'new' }}] 20@4[${{ matrix.n == 18 && 'old' || 'new' }}]")
	want(t, see(t, job("'Unit ${{ matrix.n }} on linux'"), "t", "", "V"), "18@4[Unit 18 on linux] 20@4[Unit 20 on linux]")
	// Only the matrix context counts as a reference to it, not a word
	// that holds "matrix".
	for _, other := range []string{"env.MATRIX_LABEL", "inputs.matrix-size", "env.matrix", "matrix_label"} {
		want(t, see(t, job("'B ${{ "+other+" }}'"), "t", "", "V"),
			"18@4[B ${{ "+other+" }} (18, linux)] 20@4[B ${{ "+other+" }} (20, linux)]")
	}
	want(t, see(t, job("'B ${{ toJSON(matrix) }}'"), "t", "", "V"), "18@4[B ${{ toJSON(matrix) }}] 20@4[B ${{ toJSON(matrix) }}]")
	// A name of 256 bytes before a reference is cut there, with "...".
	long := strings.Repeat("a", maxCondition)
	want(t, see(t, job("'"+long+"${{ matrix.n }}'"), "t", "", "V"), "18@4["+long+"...] 20@4["+long+"...]")
	// A name: that is no scalar is not GitHub's to show; the id stands.
	want(t, see(t, job("[a]"), "t", "", "V"), "18@4[t (18, linux)] 20@4[t (20, linux)]")
}

// A matrix an expression writes, such as fromJSON of an earlier job's
// output, has jobs only GitHub knows. A value that names the matrix is
// unknown, naming the expression; a value that does not is read as in any
// job. Planted break: the matrix's refusal returned for every value, which
// refused runs-on.
func TestDynamicMatrix(t *testing.T) {
	for _, strategy := range []string{
		"{matrix: '${{ fromJSON(needs.a.outputs.m) }}'}",
		"{matrix: {os: '${{ fromJSON(needs.a.outputs.os) }}'}}",
		"{matrix: {os: [a], include: '${{ fromJSON(needs.a.outputs.i) }}'}}",
	} {
		body := "jobs:\n  t:\n    runs-on: ubuntu-latest\n    strategy: " + strategy + "\n    env: {V: x, M: 'v${{ matrix.os }}', S: '${{ secrets.S }}'}\n"
		want(t, see(t, body, "t", "", "runs-on"), "ubuntu-latest@3")
		want(t, see(t, body, "t", "", "V"), "x@5")
		want(t, see(t, body, "t", "", "S"), "${{ secrets.S }}!@5")
		extracttest.WantError(t, refused(t, body, "t", "", "M"), "ci.yml:4: the matrix")
		extracttest.WantError(t, refused(t, body, "t", "", "M"), "is an expression")
	}
}

// GitHub evaluates an expression in an include: or exclude: entry, or in a
// value of a matrix variable, before it compares it, so errata never
// compares one as text. A comparison that meets one decides nothing, and a
// job whose existence or values hang on it makes a value that names the
// matrix unknown, naming the job and the expression. A comparison that
// another key already decides needs no expression's value. Planted breaks:
// an expression compared as text, which gave v-windows for a job
// SKIP_OS may exclude, and in turn the exclude:, the include: and the
// variable's value read that way.
func TestMatrixExpressionsInEntries(t *testing.T) {
	head := "jobs:\n  j:\n    env: {V: 'v-${{ matrix.os }}', W: '${{ matrix.v }}', L: x}\n    strategy:\n      matrix:\n"
	for _, c := range []struct{ matrix, want string }{
		{"        os: [linux, windows]\n        exclude: [{os: '${{ vars.SKIP_OS }}'}]\n",
			`ci.yml:7: whether job "j (linux)" runs, and with which values, hangs on "${{ vars.SKIP_OS }}", an expression GitHub evaluates`},
		{"        os: [linux]\n        include: [{os: '${{ vars.EXTRA }}', v: 1}]\n", `whether job "j (linux)" runs`},
		{"        os: ['${{ vars.OS }}', linux]\n        v: [1, 2]\n        exclude: [{os: linux, v: 2}]\n", `whether job "j (${{ vars.OS }}, 2)" runs`},
		{"        os: [linux]\n        v: [[1, '${{ vars.V }}']]\n        exclude: [{v: [1, 2]}]\n", `hangs on "${{ vars.V }}"`},
		{"        os: [linux]\n        v: [{a: '${{ vars.V }}'}]\n        include: [{v: {a: 2}, x: 1}]\n", `hangs on "${{ vars.V }}"`},
	} {
		body := head + c.matrix
		extracttest.WantError(t, refused(t, body, "j", "", "V"), c.want)
		want(t, see(t, body, "j", "", "L"), "x@3")
	}
	// The other key disagrees, so the expression decides nothing here.
	body := head + "        os: [linux, windows]\n        v: [1]\n        exclude: [{os: mac, v: '${{ vars.V }}'}, {os: linux, v: 2}]\n        include: [{os: windows, v: 3}]\n"
	want(t, see(t, body, "j", "", "V"), "v-linux@3[j (linux, 1)] v-windows@3[j (windows, 1)] v-windows@3[j (windows, 3)]")
	// A list of another length, or another shape, disagrees whatever the
	// expression in it gives.
	body = head + "        os: [linux]\n        v: [[1, 2]]\n        exclude: [{v: ['${{ vars.V }}']}, {v: {a: '${{ vars.V }}'}}]\n"
	want(t, see(t, body, "j", "", "V"), "v-linux@3[j (linux, [1, 2])]")
}

// Matrix keys are one when the runner's StringComparer.OrdinalIgnoreCase
// holds them one: each rune's simple upper case, except that the dotless i
// and the long s stay apart from I and S. The Kelvin sign is its own upper
// case, so it is not k. fold, for quiet's ASCII names, makes ASCII capitals
// small and nothing else. Planted breaks: foldKey and equalKey as ASCII
// folding; the dotless i, then the long s, not excepted; a byte that starts
// no rune read as utf8.RuneError; equalKey without its check that both
// sides end; fold as strings.ToLower.
func TestFold(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"os", "OS", true}, {"Os", "oS", true}, {"AZ", "az", true}, {"MATRIX_1", "matrix_1", true}, {"", "", true},
		{"é", "É", true}, {"σ", "Σ", true}, {"\u0250", "\u2c6f", true}, {"x\u0250y", "X\u2c6fY", true},
		{"@[", "`{", false}, {"k", "\u212a", false}, {"s", "\u017f", false}, {"i", "\u0131", false}, {"é", "e", false},
		{"\xff", "\xfe", false}, {"\xff", "\ufffd", false}, {"a", "ab", false}, {"ab", "a", false}, {"\u0250", "\u2c6fx", false},
	} {
		if got := foldKey(c.a) == foldKey(c.b); got != c.same {
			t.Errorf("%q, %q: foldKey alike %v, want %v", c.a, c.b, got, c.same)
		}
		if got := equalKey(c.a, c.b); got != c.same {
			t.Errorf("%q, %q: equalKey %v, want %v", c.a, c.b, got, c.same)
		}
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"actions/Cache", "ACTIONS/cache", true}, {"AZ", "az", true}, {"@[", "`{", false}, {"k", "\u212a", false}, {"é", "É", false},
	} {
		if got := fold(c.a) == fold(c.b); got != c.same {
			t.Errorf("%q, %q: fold alike %v, want %v", c.a, c.b, got, c.same)
		}
	}
}

// A matrix key and an include: entry's key that are one only outside
// ASCII are one key, as the runner holds them: É overwrites the variable
// é, so the entry is a job of its own, and é beside É is refused as os
// beside OS is. Planted break: the keys folded in ASCII alone.
func TestMatrixKeysFoldOutsideASCII(t *testing.T) {
	head := "jobs:\n  t:\n    env: {V: '${{ matrix.x }}'}\n    strategy:\n      matrix:\n"
	want(t, see(t, head+"        a: [1]\n        é: [1]\n        include:\n          - {a: 1, É: 2, x: new}\n", "t", "", "V"), "@3[t (1, 1)] new@9[t (1, 2, new)]")
	extracttest.WantError(t, refused(t, head+"        é: [1]\n        É: [2]\n", "t", "", "V"), `ci.yml:7: the matrix of job "t" has the variables "é" and "É", which GitHub reads as one`)
}

// GitHub reads a mapping's keys in any case and does not document how it
// compares them, so an entry whose nested keys match a value's only in
// another case leaves the job unknown, naming it, when the values agree.
// When they disagree, they do in either reading; an exact match decides
// alone. Planted breaks: the folded match dropped, which gave 1@6 2@6;
// the verdict left agree; the values not compared once folded; the key
// matched only folded.
func TestMatrixNestedKeysInAnotherCase(t *testing.T) {
	head := "jobs:\n  t:\n    env: {V: '${{ matrix.a.X }}'}\n    strategy:\n      matrix:\n        a: [{X: 1}, {X: 2}]\n"
	extracttest.WantError(t, refused(t, head+"        exclude:\n          - {a: {x: 1}}\n", "t", "", "V"),
		`ci.yml:8: whether job "t ({X: 1})" runs, and with which values, hangs on the case of this mapping's keys`)
	want(t, see(t, head+"        exclude:\n          - {a: {x: 3}}\n", "t", "", "V"), "1@6[t ({X: 1})] 2@6[t ({X: 2})]")
	want(t, see(t, head+"        exclude:\n          - {a: {X: 1}}\n", "t", "", "V"), "2@6[t ({X: 2})]")
	want(t, see(t, head+"        exclude:\n          - {a: {Y: 1}}\n", "t", "", "V"), "1@6[t ({X: 1})] 2@6[t ({X: 2})]")
	extracttest.WantError(t, refused(t, head+"        include:\n          - {a: {x: 2}, b: 3}\n", "t", "", "V"),
		`ci.yml:8: whether job "t ({X: 2})" runs, and with which values, hangs on the case of this mapping's keys`)
}

// The steps the matrix takes are counted exactly: core.MaxExpanded of them
// are allowed and one more is refused, a comparison of text is a step and
// one more for each 64 bytes of the shorter side, and countBytes counts
// one for each 64 bytes. Planted breaks: the limit at >=; countBytes
// multiplying; eqFold's step subtracted, then its bytes multiplied.
func TestMatrixStepCount(t *testing.T) {
	w := &workflow{file: "ci.yml"}
	if err := w.count(core.MaxExpanded); err != nil {
		t.Fatalf("count(MaxExpanded): %v", err)
	}
	extracttest.WantError(t, w.count(1), "ci.yml: the matrix takes more than 1000000 steps to build")
	w = &workflow{}
	if err := w.countBytes(130); err != nil || w.work != 2 {
		t.Fatalf("countBytes(130): work %d, %v; want 2", w.work, err)
	}
	same, err := w.eqFold(strings.Repeat("a", 129), strings.Repeat("A", 200))
	if err != nil || same || w.work != 5 {
		t.Fatalf("eqFold: %v, work %d, %v; want false, 5", same, w.work, err)
	}
}

// clipName and clipCondition back off to a character's start, and stop at
// the string's first byte. Planted break: the loop let past the first
// byte, which indexed before it.
func TestClipBacksOffToTheStart(t *testing.T) {
	cont := strings.Repeat("\x80", 300)
	if got := clipCondition(cont); got != "..." {
		t.Errorf("clipCondition: got %q", got)
	}
	var b strings.Builder
	clipName(&b, cont, 10)
	if b.String() != "..." {
		t.Errorf("clipName: got %q", b.String())
	}
}

// inline stops a few bytes past maxCondition, the separators and the
// brackets it closes, however long a value or a key, so a job's name costs
// no more than that whatever the matrix holds; the name is cut after. Planted break: the text written whole,
// which wrote a value of 1 MiB into the builder.
func TestInlineStopsPastTheLimit(t *testing.T) {
	long := &core.Node{Shape: core.Scalar, Text: strings.Repeat("x", 1<<20)}
	for _, n := range []*core.Node{
		long,
		{Shape: core.Sequence, Items: []*core.Node{long, long}},
		{Shape: core.Mapping, Keys: []string{strings.Repeat("k", 1<<20)}, Vals: []*core.Node{long}},
	} {
		var b strings.Builder
		b.WriteString("j (")
		inline(&b, n)
		if b.Len() > maxCondition+8 {
			t.Errorf("%s: wrote %d bytes", shapeName(n), b.Len())
		}
	}
}
