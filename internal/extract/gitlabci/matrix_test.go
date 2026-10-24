package gitlabci

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Each parallel:matrix instance is a job of its own, named by its values,
// and GitLab merges the instance's variables over the job's, so an
// instance that sets the variable gives its value under its name and one
// that does not gives the job's. A rule's variable outranks both. "The
// matrix can generate a maximum of 200 jobs" (parallel:matrix reference).
// Planted breaks: the matrix skipped, which gave "job", a value no instance
// sees; joinCond keeping the instance's condition alone; the product
// checked only after every key, which wrapped 2^64 to 0 jobs.
func TestMatrixInstances(t *testing.T) {
	files := main(`build:
  variables:
    TAG: job
  parallel:
    matrix:
      - TAG: [a, b]
        OS: linux
      - OS: [mac]
  rules:
    - if: $RELEASE
      variables:
        TAG: release
    - when: on_success
  script: [x]
`)
	want(t, see(t, files, "build", "TAG"),
		"release@.gitlab-ci.yml:12[if $RELEASE] a@.gitlab-ci.yml:6[build: [a, linux]] b@.gitlab-ci.yml:6[build: [b, linux]] job@.gitlab-ci.yml:3[build: [mac]]")
	want(t, see(t, files, "build", "OS"), "linux@.gitlab-ci.yml:7[build: [a, linux]] linux@.gitlab-ci.yml:7[build: [b, linux]] mac@.gitlab-ci.yml:8[build: [mac]]")
	// An instance that sets nothing sees what is below the job: here a
	// workflow rule's value, under both conditions.
	files = main("variables: {T: top}\nworkflow:\n  rules:\n    - if: $W\n      variables: {T: w}\n    - when: always\nbuild:\n  parallel:\n    matrix: [{OS: [a]}, {T: [m]}]\n  script: [x]\n")
	want(t, see(t, files, "build", "T"), "w@.gitlab-ci.yml:5[build: [a]; workflow if $W] top@.gitlab-ci.yml:1[build: [a]] m@.gitlab-ci.yml:9[build: [m]]")
	// A variable no instance sets is the job's, for every instance.
	want(t, see(t, main("build:\n  variables: {V: v}\n  parallel:\n    matrix: [{OS: [a, b]}]\n  script: [x]\n"), "build", "V"), "v@.gitlab-ci.yml:2")

	var many strings.Builder
	many.WriteString("build:\n  parallel:\n    matrix:\n      - A: [")
	for i := range 201 {
		if i > 0 {
			many.WriteString(", ")
		}
		fmt.Fprintf(&many, "v%d", i)
	}
	many.WriteString("]\n  script: [x]\n")
	extracttest.WantError(t, refused(t, main(many.String()), "build", "A"), "parallel:matrix makes more than 200 jobs, GitLab's limit")
	extracttest.WantError(t, refused(t, main("build:\n  parallel: {matrix: [{A: {x: 1}}]}\n"), "build", "A"), "a parallel:matrix value is a string, not a mapping")
	// 64 keys of two values each make 2^64 jobs, which wraps an int to 0:
	// the product is checked before each key multiplies it.
	var wide strings.Builder
	wide.WriteString("build:\n  script: [x]\n  parallel:\n    matrix:\n      - {A: [x, y]")
	for i := range 63 {
		fmt.Fprintf(&wide, ", K%d: [a, b]", i)
	}
	wide.WriteString("}\n")
	extracttest.WantError(t, refused(t, main(wide.String()), "build", "A"), "parallel:matrix makes more than 200 jobs")
	extracttest.WantError(t, refused(t, main("build:\n  parallel: {matrix: [x]}\n"), "build", "A"), "a parallel:matrix entry is a mapping")
	extracttest.WantError(t, refused(t, main("build:\n  parallel: {matrix: x}\n"), "build", "A"), "parallel:matrix is a scalar, not a list")
	extracttest.WantError(t, refused(t, main("build:\n  parallel: {matrix: [{A: {value: x}}]}\n"), "build", "A"), "a parallel:matrix value is a string")
}

// "Variables from dotenv reports in jobs listed in dependencies or needs"
// rank above every variable of the file (CI/CD variable precedence). The
// report is written as the pipeline runs, so a job that gets one is
// unknown, naming the job that writes it: one it needs, one dependencies:
// lists, or with neither one of an earlier stage, a report under default:
// too. A needs: entry with artifacts: false gets none, nor does a job of a
// later stage. Planted breaks: dotenv not read, which gave "one"; the
// default stage build, which put deploy beside mk; .post left out of the
// order, which put it first.
func TestDotenvReportsAreOutOfReach(t *testing.T) {
	mk := "mk:\n  stage: build\n  script: [x]\n  artifacts: {reports: {dotenv: b.env}}\n"
	for _, c := range []struct{ name, deploy, want string }{
		{"needs", "  needs: [mk]\n", `job "deploy" gets the dotenv report of job "mk"`},
		{"dependencies", "  dependencies: [mk]\n", `gets the dotenv report of job "mk"`},
		{"earlier stage", "  stage: deploy\n", `gets the dotenv report of job "mk"`},
		// "If stage is not defined, the job uses the test stage by default."
		{"no stage is test", "", `gets the dotenv report of job "mk"`},
		{"needs in another pipeline", "  needs: [{pipeline: $UP, job: mk}]\n", "needs a job of another pipeline"},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := main(mk + "deploy:\n" + c.deploy + "  variables: {TAG: one}\n  script: [x]\n")
			extracttest.WantError(t, refused(t, files, "deploy", "TAG"), c.want)
		})
	}
	for _, c := range []struct{ name, deploy string }{
		{"needs without artifacts", "  needs: [{job: mk, artifacts: false}]\n"},
		{"no dependencies", "  dependencies: []\n"},
		{"the same stage", "  stage: build\n"},
		{"needs another job", "  needs: [other]\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := main(mk + "other:\n  stage: build\n  script: [x]\ndeploy:\n" + c.deploy + "  variables: {TAG: one}\n  script: [x]\n")
			want(t, see(t, files, "deploy", "TAG"), "one@.gitlab-ci.yml:"+strconv.Itoa(strings.Count(files[".gitlab-ci.yml"], "\n")-1))
		})
	}
	// A .post job runs after every stage, so it is no source.
	want(t, see(t, map[string]string{".gitlab-ci.yml": "last:\n  stage: .post\n  script: [x]\n  artifacts: {reports: {dotenv: p.env}}\ndeploy:\n  stage: deploy\n  variables: {TAG: one}\n  script: [x]\n"}, "deploy", "TAG"), "one@.gitlab-ci.yml:7")
	files := main("stages: [first, second]\ndefault:\n  artifacts: {reports: {dotenv: d.env}}\na:\n  stage: first\n  script: [x]\nb:\n  stage: second\n  variables: {TAG: one}\n  script: [x]\n")
	extracttest.WantError(t, refused(t, files, "b", "TAG"), `gets the dotenv report of job "a"`)
}

// A variable no file sets for the job is check.NoValue, which names where
// it may be set instead, so the rule's message says why it is unknown and
// allow_empty still decides.
func TestVariableNoFileSets(t *testing.T) {
	_, err := extracttest.Run(t, build, spec("build", "TAG"), extracttest.Repo(t, main("build:\n  script: [x]\n")))
	var none *check.NoValue
	if !errors.As(err, &none) || !strings.Contains(none.Reason, "no file gives the job TAG; it may come from the project's, group's or instance's settings") {
		t.Fatalf("error %v, want check.NoValue naming the settings", err)
	}
	// A keyword the job does not set is an empty list, with no reason.
	want(t, see(t, main("build:\n  script: [x]\n"), "build", "image"), "")
}

// A rule's condition names each test it has, joined with "and", a
// changes: of paths: by its paths, and an if: through its !reference.
// Planted breaks: "or" for "and"; the paths: branch dropped, which gave
// []; the if: read without its !reference, which gave "if [.r, if]".
func TestRuleConditions(t *testing.T) {
	files := main(`.r:
  if: $FROM_REF
job:
  rules:
    - if: $A
      changes: [a.go]
      variables: {V: both}
    - changes: {paths: [b.go], compare_to: main}
      variables: {V: paths}
    - if: !reference [.r, if]
      variables: {V: ref}
  script: [x]
`)
	want(t, see(t, files, "job", "V"), "both@.gitlab-ci.yml:7[if $A and changes [a.go]] paths@.gitlab-ci.yml:9[changes [b.go]] ref@.gitlab-ci.yml:11[if $FROM_REF]")
}

// A condition is cut at 256 bytes where it is read, on a character's
// start, since each matrix instance copies it: a 1 MB if: under 199
// instances allocates a few MB, not 199 MB. Planted break: clipCondition
// returning s whole, which kept 199 copies until the condition budget
// refused them.
func TestConditionsAreCut(t *testing.T) {
	var b strings.Builder
	b.WriteString("workflow:\n  rules:\n    - if: '$A == \"" + strings.Repeat("a", 1<<20) + "é\"'\n      variables: {V: v}\n")
	b.WriteString("build:\n  script: [x]\n  parallel:\n    matrix:\n      - V: [x]\n      - W: [")
	for i := range 199 {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "w%d", i)
	}
	b.WriteString("]\n")
	root := extracttest.Repo(t, main(b.String()))
	var got []check.Value
	var err error
	allocated := extracttest.Allocated(func() { got, err = extracttest.Run(t, build, spec("build", "V"), root) })
	if err != nil || len(got) != 200 {
		t.Fatalf("%d values, %v; want 200", len(got), err)
	}
	for _, v := range got {
		if len(v.Condition) > maxCondition+3 {
			t.Fatalf("a condition of %d bytes", len(v.Condition))
		}
	}
	if allocated > 64<<20 {
		t.Fatalf("allocated %d MB", allocated>>20)
	}
}

// The candidates are counted against the value limit, and their
// conditions against max_bytes, as they are made, so a matrix times a
// workflow's rules is refused before it builds them all: 199 instances
// times 2000 rules would be 398000 candidates. Planted breaks: add without
// its count, which built them all and allocated 233 MB; the conditions not
// charged, which read the small max_bytes case.
func TestCandidatesCountedAsMade(t *testing.T) {
	matrix := func(rules int) string {
		var b strings.Builder
		b.WriteString("workflow:\n  rules:\n")
		for i := range rules {
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
		return b.String()
	}
	root := extracttest.Repo(t, main(matrix(2000)))
	var err error
	allocated := extracttest.Allocated(func() { _, err = extracttest.Run(t, build, spec("build", "V"), root) })
	extracttest.WantError(t, err, "selects more than 100000 values")
	if allocated > 64<<20 {
		t.Fatalf("allocated %d MB before the refusal", allocated>>20)
	}
	_, err = extracttest.Run(t, build, Spec{File: ".gitlab-ci.yml", Job: "build", Variable: "V", MaxBytes: 6000}, extracttest.Repo(t, main(matrix(10))))
	extracttest.WantError(t, err, "the conditions of the values hold more than 6000 bytes in all")
}

// An entry's instances run in the order of Ruby's product, the last key
// varying fastest, and each is named by its values as GitLab writes them,
// Array(value).map(&:to_s) (entry/product/variables.rb) of what Psych
// loads: the octal 010 is 8 and yes is true. Planted breaks: the first key
// varying fastest; each value named by its text, which gave [010]; 1,
// read as Psych's integer 1.
func TestMatrixInstanceNames(t *testing.T) {
	want(t, see(t, main("build:\n  parallel:\n    matrix:\n      - {A: [1, 2], B: [x, y]}\n  script: [s]\n"), "build", "A"),
		"1@.gitlab-ci.yml:4[build: [1, x]] 1@.gitlab-ci.yml:4[build: [1, y]] 2@.gitlab-ci.yml:4[build: [2, x]] 2@.gitlab-ci.yml:4[build: [2, y]]")
	want(t, see(t, main("build:\n  parallel:\n    matrix:\n      - V: [010, '1', yes, 0x1f, -0b11, +1_000, \"010\", 3.10, !!str 07]\n  script: [s]\n"), "build", "V"),
		"010!@.gitlab-ci.yml:4[build: [8]] 1@.gitlab-ci.yml:4[build: [1]] yes!@.gitlab-ci.yml:4[build: [true]] 0x1f!@.gitlab-ci.yml:4[build: [31]] -0b11!@.gitlab-ci.yml:4[build: [-3]] +1_000!@.gitlab-ci.yml:4[build: [1000]] 010@.gitlab-ci.yml:4[build: [010]] 3.10!@.gitlab-ci.yml:4[build: [3.10]] 07@.gitlab-ci.yml:4[build: [07]]")
	// An integer Ruby's Integer() refuses keeps its text, and so does one
	// longer than a condition keeps, which the name would cut anyway.
	octal := "0" + strings.Repeat("7", maxCondition-1)
	eight, _ := new(big.Int).SetString(octal[1:], 8)
	want(t, see(t, main("build:\n  parallel:\n    matrix:\n      - V: [0b_, -0, "+octal+", "+octal+"7]\n  script: [s]\n"), "build", "V"),
		"0b_!@.gitlab-ci.yml:4[build: [0b_]] -0!@.gitlab-ci.yml:4[build: [0]] "+octal+"!@.gitlab-ci.yml:4[build: ["+clipCondition(eight.String())+"]] "+octal+"7!@.gitlab-ci.yml:4[build: ["+octal[:maxCondition-len("build: [")]+"...]")
	// Psych's decimal integer has a digit after each comma or underscore,
	// so 1, is a string, named as written; its value is not literal still,
	// since psychInt is wider than Psych's pattern.
	want(t, see(t, main("build:\n  parallel:\n    matrix:\n      - V:\n          - 1,\n          - 1_0\n  script: [s]\n"), "build", "V"),
		"1,!@.gitlab-ci.yml:5[build: [1,]] 1_0!@.gitlab-ci.yml:6[build: [10]]")
}

// A report quotes 64 bytes of a condition (check.Value.ConditionNote), and
// GitLab's name puts the varying values last, so two instances of a long
// name could print the same note. Such a name writes the values no
// instance differs in as "...". Planted breaks: no value elided, which
// printed "build: [aws, europe-west-1-a-long-zone-name, monitoring-and-aler"
// twice; aws elided though another entry sets gcp; a key of two values
// elided; the name of a lone instance elided.
func TestMatrixLongNamesTellInstancesApart(t *testing.T) {
	notes := func(body string) []string {
		got, err := extracttest.Run(t, build, spec("build", "V"), extracttest.Repo(t, main(body)))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range got {
			out = append(out, v.Condition)
		}
		return out
	}
	const one = "build:\n  variables: {V: v}\n  parallel:\n    matrix:\n      - {PROVIDER: aws, REGION: [europe-west-1-a-long-zone-name], STACK: [monitoring-and-alerting-one, monitoring-and-alerting-two]}\n      - {V: [w]}\n"
	got := notes(one + "  script: [s]\n")
	if strings.Join(got, " | ") != "build: [..., ..., monitoring-and-alerting-one] | build: [..., ..., monitoring-and-alerting-two] | build: [w]" {
		t.Errorf("conditions %q", got)
	}
	if a, b := (check.Value{Condition: got[0]}).ConditionNote(), (check.Value{Condition: got[1]}).ConditionNote(); a == b {
		t.Errorf("both instances print %s", a)
	}
	// An entry that sets PROVIDER apart keeps it in every name, and a
	// short name is GitLab's whole.
	got = notes(one + "      - {PROVIDER: gcp, REGION: europe-west-1-a-long-zone-name, STACK: monitoring-and-alerting-one}\n  script: [s]\n")
	if strings.Join(got, " | ") != "build: [aws, ..., monitoring-and-alerting-one] | build: [aws, ..., monitoring-and-alerting-two] | build: [w] | build: [gcp, ..., monitoring-and-alerting-one]" {
		t.Errorf("conditions %q", got)
	}
	got = notes("build:\n  variables: {V: v}\n  parallel:\n    matrix:\n      - {P: aws, S: [a, b]}\n      - {V: [w]}\n  script: [s]\n")
	if strings.Join(got, " | ") != "build: [aws, a] | build: [aws, b] | build: [w]" {
		t.Errorf("conditions %q", got)
	}
	// One instance has none to be told apart from, so its long name is
	// GitLab's whole too.
	got = notes("build:\n  parallel:\n    matrix:\n      - {V: aws, REGION: europe-west-1-a-long-zone-name, STACK: monitoring-and-alerting-one}\n  script: [s]\n")
	if strings.Join(got, " | ") != "build: [aws, europe-west-1-a-long-zone-name, monitoring-and-alerting-one]" {
		t.Errorf("conditions %q", got)
	}
}

// A condition is cut at maxCondition bytes where a character starts, as a
// report quotes it from its start. Planted break: the cut at byte 256
// whatever it holds, which split the € across it.
func TestClipCondition(t *testing.T) {
	a := strings.Repeat("a", maxCondition+1)
	for _, c := range []struct{ in, want string }{
		{a[:maxCondition], a[:maxCondition]},
		{a, a[:maxCondition] + "..."},
		{a[:maxCondition-2] + "€", a[:maxCondition-2] + "..."},
		{a[:maxCondition-3] + "€b", a[:maxCondition-3] + "€..."},
	} {
		if got := clipCondition(c.in); got != c.want {
			t.Errorf("%d bytes: %q, want %q", len(c.in), got, c.want)
		}
	}
	got, err := extracttest.Run(t, build, spec("build", "V"), extracttest.Repo(t, main("build:\n  rules:\n    - if: $A == \""+a[:maxCondition-6]+"€\"\n      variables: {V: v}\n  script: [s]\n")))
	if err != nil || len(got) != 1 || !utf8.ValidString(got[0].Condition) || !strings.HasSuffix(got[0].Condition, "...") {
		t.Errorf("got %s, %v; want one value under a cut condition of valid UTF-8", values(got), err)
	}
}
