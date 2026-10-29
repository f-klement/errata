package gitlabci

import (
	"errors"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// "GitLab expands job variable values recursively before sending them to
// the runner", and "The expanded part needs to be in a form of $variable,
// or ${variable} or %variable%" (Where variables can be used), so such a
// value is one GitLab computes: not literal. With expand: false, "The
// result of VAR3 is value3 $VAR1" (variables:expand reference). A script's $ is
// the command's own text, which the shell runs. Planted break: references
// always false, which made $CI_COMMIT_SHA literal.
func TestValuesGitLabExpands(t *testing.T) {
	files := main(`variables:
  A: $CI_COMMIT_SHA
  B: pre-${CI_JOB_ID}-post
  C: '%HOME%\x'
  D: cost$$
  E: {value: $X, expand: false}
  F: price $ 5
  G: 50%
job:
  image: registry/$IMAGE
  script: [echo $A]
`)
	for name, w := range map[string]string{
		"A": "$CI_COMMIT_SHA!@.gitlab-ci.yml:2", "B": "pre-${CI_JOB_ID}-post!@.gitlab-ci.yml:3",
		"C": `%HOME%\x!@.gitlab-ci.yml:4`, "D": "cost$$!@.gitlab-ci.yml:5", "E": "$X@.gitlab-ci.yml:6",
		"F": "price $ 5@.gitlab-ci.yml:7", "G": "50%@.gitlab-ci.yml:8",
		"image": "registry/$IMAGE!@.gitlab-ci.yml:10", "script": "echo $A@.gitlab-ci.yml:11",
	} {
		want(t, see(t, files, "job", name), w)
	}
}

// GitLab reads the file with Psych, a YAML 1.1 loader, and "The value must
// be a string" (variables reference): a plain scalar Psych types as a
// number, a boolean, a date or null reaches the job as Ruby prints that
// value, so 3.10 is 3.1 and 010 is 8. Such a value is not literal; quoted,
// it is the text. A decimal integer is the one number Ruby prints as it is
// written. Planted break: psychString true for every scalar, which made
// 3.10 literal.
func TestValuesYAMLRetypes(t *testing.T) {
	cases := map[string]bool{
		"3.10": false, "1.0": false, "010": false, "0x1F": false, "0b11": false, "1_000": false,
		"1,000": false, "1:30": false, "+5": false, "-0": false, ".inf": false, "-.Inf": false, ".NaN": false,
		"yes": false, "On": false, "FALSE": false, "~": false, "null": false, "": false, "2024-01-02": false,
		":sym": false, "20": true, "-3": true, "0": true, "1.2.3": true, "1e3": true, "v1.0": true,
		"y": true, "ruby:3.1": true, "1.": false, ".5": false,
	}
	for text, literal := range cases {
		if psychString(text) != literal {
			t.Errorf("%q: psychString is %v, want %v", text, !literal, literal)
		}
	}
	files := main("variables:\n  PLAIN: 3.10\n  QUOTED: '3.10'\n  BLOCK: |\n    3.10\n  TAGGED: !!str 3.10\n  INT: 20\n  EMPTY:\njob:\n  script: [x]\n")
	want(t, see(t, files, "job", "PLAIN"), "3.10!@.gitlab-ci.yml:2")
	want(t, see(t, files, "job", "QUOTED"), "3.10@.gitlab-ci.yml:3")
	want(t, see(t, files, "job", "BLOCK"), "3.10\n@.gitlab-ci.yml:4")
	want(t, see(t, files, "job", "TAGGED"), "3.10@.gitlab-ci.yml:6")
	want(t, see(t, files, "job", "INT"), "20@.gitlab-ci.yml:7")
	want(t, see(t, files, "job", "EMPTY"), "!@.gitlab-ci.yml:8")
}

// The long form: "Use the value keyword to define a pipeline-level
// (default) variable's value", and without one "the default value is an
// empty string" (variables:value and variables:description reference).
func TestVariableLongForm(t *testing.T) {
	files := main("variables:\n  A:\n    value: staging\n    description: where\n    options: [staging, prod]\n  B:\n    description: none\njob:\n  script: [x]\n")
	want(t, see(t, files, "job", "A"), "staging@.gitlab-ci.yml:3")
	want(t, see(t, files, "job", "B"), "@.gitlab-ci.yml:7")
	for body, msg := range map[string]string{
		"variables:\n  A: [x]\njob:\n  script: [x]\n":           "a variable's value is a string, not a sequence",
		"variables:\n  A: {value: [x]}\njob:\n  script: [x]\n":  "a variable's value is a string, not a sequence",
		"variables: [A]\njob:\n  script: [x]\n":                 "variables is a sequence, not a mapping",
		"job:\n  rules: {if: x}\n  script: [x]\n":               "rules is a mapping, not a list",
		"job:\n  rules: [x]\n  script: [x]\n":                   "a rule is a mapping, not a scalar",
		"job:\n  inherit: {variables: maybe}\n  script: [x]\n":  `inherit:variables is true, false or a list of names, not "maybe"`,
		"job:\n  inherit: {variables: {A: 1}}\n  script: [x]\n": "inherit:variables is true, false or a list of names, not a mapping",
		"job: [x]\n":               `job "job" is a sequence, not a mapping`,
		"job:\n  extends: [[a]]\n": "extends names a job by a string",
		"job:\n  variables:\n    A: !reference [.a, [b]]\n.a: {}\n": "!reference names a key by a string",
		"variables:\n  A: {value: x, expand: false}\njob: x\n":      `job "job" is a scalar`,
		"job:\n  variables:\n    A: !reference [.t, v]\n.t: 1\n":    `".t" is a scalar, not a mapping`,
	} {
		_, err := extracttest.Run(t, build, Spec{File: ".gitlab-ci.yml", Job: "job", Variable: "A"}, extracttest.Repo(t, main(body)))
		extracttest.WantError(t, err, msg)
	}
}

// A key names a keyword, or a key below one: a list gives each item, a
// nested list flattened; a mapping gives no value, and a key below a
// scalar is refused.
func TestKeyPaths(t *testing.T) {
	files := main("job:\n  image:\n    name: ruby:3\n    entrypoint: ['']\n  services: [{name: pg}]\n  tags: [a, [b]]\n  stage: deploy\n")
	want(t, see(t, files, "job", "image.name"), "ruby:3@.gitlab-ci.yml:3")
	want(t, see(t, files, "job", "tags"), "a@.gitlab-ci.yml:6 b@.gitlab-ci.yml:6")
	want(t, see(t, files, "job", "stage"), "deploy@.gitlab-ci.yml:7")
	want(t, see(t, files, "job", "image.missing"), "")
	extracttest.WantError(t, refused(t, files, "job", "image"), "selects a mapping, not a value; name a key below it")
	extracttest.WantError(t, refused(t, files, "job", "services"), "the list holds a mapping")
	extracttest.WantError(t, refused(t, files, "job", "stage.name"), `"stage" is a scalar, with no key "name" below it`)
}

// A job the files do not define is refused, since a renamed job would
// otherwise read as an empty value.
func TestMissingJob(t *testing.T) {
	extracttest.WantError(t, refused(t, main("build:\n  script: [x]\n"), "deploy", "TAG"), `no job "deploy" in the file or the files it includes`)
}

// Build refuses what no file could answer: no job, a hidden job or a
// global keyword as the job, both or neither of variable and key, a
// variable GitLab could not name, and a key with an empty part.
func TestBuildRefusals(t *testing.T) {
	long := strings.Repeat("a", maxName+1)
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{Job: "j", Key: "image"}, "gitlab-ci: file is required"},
		{Spec{File: "c.yml", Key: "image"}, "gitlab-ci: job is required"},
		{Spec{File: "c.yml", Job: ".hidden", Key: "image"}, `job ".hidden" is hidden`},
		{Spec{File: "c.yml", Job: "workflow", Key: "image"}, `"workflow" is a global keyword`},
		{Spec{File: "c.yml", Job: long, Key: "image"}, "over the limit of 1024"},
		{Spec{File: "c.yml", Job: "j"}, "set exactly one of variable"},
		{Spec{File: "c.yml", Job: "j", Variable: "A", Key: "image"}, "set exactly one of variable"},
		{Spec{File: "c.yml", Job: "j", Variable: "A-B"}, `variable "A-B" is no name GitLab accepts`},
		{Spec{File: "c.yml", Job: "j", Variable: long}, "is no name GitLab accepts"},
		{Spec{File: "c.yml", Job: "j", Key: "image..name"}, "has an empty part"},
		{Spec{File: "c.yml", Job: "j", Key: long}, "over the limit of 1024"},
		{Spec{File: "c.yml", Job: "j", Key: "variables.A"}, "write variable: NAME"},
		{Spec{File: "../c.yml", Job: "j", Key: "image"}, "outside the repository"},
	} {
		_, err := build(c.spec)
		extracttest.WantError(t, err, c.want)
	}
	x, err := build(Spec{File: "c.yml", Job: "j", Key: "image"})
	if err != nil || !check.MayReturnNotLiteral(x) {
		t.Fatalf("Build: %v, or the extractor does not declare its not literal values", err)
	}
}

// Place cleans the file and drops max_bytes, and keeps what it reads.
func TestPlace(t *testing.T) {
	p := Place(Spec{File: "./a/../c.yml", Job: "j", Variable: "V", MaxBytes: 9})
	if p != (Spec{File: "c.yml", Job: "j", Variable: "V"}) {
		t.Fatalf("Place is %+v", p)
	}
}

// An include over the bytes the files before it left is known by
// core.ErrTooLarge, which the note keeps, and not by the words of core's
// message, so the note survives a reworded message (#197). Planted break:
// load's errors.Is replaced by a match of "bytes, past the limit of",
// which is the old text match after core rewords its message.
func TestAnIncludeOverTheBytesLeftIsKnownByItsSentinel(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		".gitlab-ci.yml": "include: [a.yml]\njob:\n  script: [x]\n",
		"a.yml":          "variables:\n  A: " + strings.Repeat("a", 600) + "\n",
	})
	_, err := extracttest.Run(t, build, Spec{File: ".gitlab-ci.yml", Job: "job", Key: "script", MaxBytes: 300}, root)
	if !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("%v: is not core.ErrTooLarge", err)
	}
	if !strings.HasSuffix(err.Error(), "; the file and the files it includes hold at most max_bytes, 300, in all") {
		t.Fatalf("%v: lacks the note on the files' budget", err)
	}
}

// The first file over max_bytes, with nothing read before it, is refused
// with core's message alone: the note on the files' shared budget belongs
// to a file that the files before it left short. Planted break: load's
// l.left < l.limit taken as <=, which added the note to the first file.
func TestAFirstFileOverMaxBytesHasNoNoteOnTheBudget(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{".gitlab-ci.yml": "job:\n  script: [" + strings.Repeat("x", 600) + "]\n"})
	_, err := extracttest.Run(t, build, Spec{File: ".gitlab-ci.yml", Job: "job", Key: "script", MaxBytes: 300}, root)
	want := "read .gitlab-ci.yml: the file is 618 bytes, over the limit of 300 bytes; set max_bytes: on the extractor to read a larger file"
	if !errors.Is(err, core.ErrTooLarge) || err.Error() != want {
		t.Fatalf("%v: want core.ErrTooLarge saying %q, with no note on the budget", err, want)
	}
}

// max_bytes bounds the files read in all, the main file and every include
// together, and so does the node limit; the values hold at most max_bytes
// too, and !reference tags that repeat a list many times reach the work
// limit. Planted breaks: left not lowered by each file, which read b.yml,
// and the node count kept per file, which read the second dense file.
func TestBoundsCoverEveryFile(t *testing.T) {
	files := map[string]string{
		".gitlab-ci.yml": "include: [a.yml, b.yml]\njob:\n  script: [x]\n",
		"a.yml":          "variables:\n  A: " + strings.Repeat("a", 600) + "\n",
		"b.yml":          "variables:\n  B: " + strings.Repeat("b", 600) + "\n",
	}
	root := extracttest.Repo(t, files)
	_, err := extracttest.Run(t, build, Spec{File: ".gitlab-ci.yml", Job: "job", Key: "script", MaxBytes: 1000}, root)
	extracttest.WantError(t, err, "read b.yml: the file is 617 bytes, over the limit of 340 bytes; set max_bytes: on the extractor to read a larger file; the file and the files it includes hold at most max_bytes, 1000, in all")
	got := extracttest.MustRun(t, build, Spec{File: ".gitlab-ci.yml", Job: "job", Key: "script", MaxBytes: 1300}, root)
	want(t, values(got), "x@.gitlab-ci.yml:3")
	// Each dense file alone is under the node limit, and the two together
	// are over it.
	dense := "variables: [" + strings.Repeat("1,", 100) + "1]\n"
	files["a.yml"], files["b.yml"] = dense, dense
	root = extracttest.Repo(t, files)
	_, err = extracttest.Run(t, build, Spec{File: ".gitlab-ci.yml", Job: "job", Key: "script", MaxBytes: 2000}, root)
	extracttest.WantError(t, err, "b.yml: the files read so far can hold up to")
	files["b.yml"] = "variables: {}\n"
	got = extracttest.MustRun(t, build, Spec{File: ".gitlab-ci.yml", Job: "job", Key: "script", MaxBytes: 2000}, extracttest.Repo(t, files))
	want(t, values(got), "x@.gitlab-ci.yml:3")

	// Each list holds the one before it ten times, so the job's script
	// stands for 10^6 commands in a file of a few hundred bytes.
	var b strings.Builder
	b.WriteString(".l0:\n  script: [x]\n")
	for i := 1; i <= 6; i++ {
		b.WriteString(".l" + string(rune('0'+i)) + ":\n  script: [")
		for j := range 10 {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString("!reference [.l" + string(rune('0'+i-1)) + ", script]")
		}
		b.WriteString("]\n")
	}
	b.WriteString("job:\n  script: !reference [.l6, script]\n")
	err = refused(t, main(b.String()), "job", "script")
	extracttest.WantError(t, err, "take more than 1000000 steps to resolve, the limit")

	_, err = extracttest.Run(t, build, Spec{File: ".gitlab-ci.yml", Job: "job", Key: "script", MaxBytes: 300},
		extracttest.Repo(t, main(".t:\n  script: &s ["+strings.Repeat("y", 40)+"]\njob:\n  script: [*s, *s, *s, *s, *s, *s, *s, *s]\n")))
	extracttest.WantError(t, err, "the values hold more than 300 bytes in all")
	_ = core.MaxValues
}

// Psych keeps the text of a quoted, a block or a !!str scalar, and types
// any other by its text, a tagged one too: "!!float 3.10" is 3.1 and
// "!!int 010" is 8. A !!float is never its text, since Float of 3 is 3.0,
// nor a !!binary, which decodes. Planted break: the tag not read, which
// made !!float 3.10 literal.
func TestTaggedScalarsAreTyped(t *testing.T) {
	files := main("variables:\n  F: !!float 3.10\n  G: !!float 3\n  I: !!int 010\n  J: !!int 10\n  S: !!str 3.10\n  B: !!binary aGk=\n  Q: !!int '0x1F'\njob:\n  script: [x]\n")
	for name, w := range map[string]string{
		"F": "3.10!@.gitlab-ci.yml:2", "G": "3!@.gitlab-ci.yml:3", "I": "010!@.gitlab-ci.yml:4", "J": "10@.gitlab-ci.yml:5",
		"S": "3.10@.gitlab-ci.yml:6", "B": "aGk=!@.gitlab-ci.yml:7", "Q": "0x1F!@.gitlab-ci.yml:8",
	} {
		want(t, see(t, files, "job", name), w)
	}
}

// Each guard of the keyword lookup has a case: a keyword set to null is
// unset, so the default applies; a rule with only changes: or exists: can
// fail to match, so the rules after it still apply; a trigger inherited
// through extends makes a trigger job; stage is no default keyword, so
// default: does not give it. A mapping a merge copies keeps its file and
// line for a message. Planted breaks: the null check dropped, which gave
// "null"; changes and exists dropped from the test, which ended the rules
// at the first; trigger read from the job's own keys; every keyword read
// from default:; the copy's file dropped, which named the main file.
func TestKeywordGuards(t *testing.T) {
	want(t, see(t, main("default:\n  image: def\njob:\n  image: null\n  script: [x]\n"), "job", "image"), "def@.gitlab-ci.yml:2")
	for _, k := range []string{"changes: [a]", "exists: [b]"} {
		files := main("job:\n  rules:\n    - " + k + "\n      variables: {V: first}\n    - variables: {V: second}\n  script: [x]\n")
		want(t, see(t, files, "job", "V"), "first@.gitlab-ci.yml:4["+strings.Replace(k, ":", "", 1)+"] second@.gitlab-ci.yml:5")
	}
	files := main(".t:\n  trigger: {include: c.yml}\njob:\n  extends: .t\n  variables: {V: v}\n")
	extracttest.WantError(t, refused(t, files, "job", "V"), `job "job" is a trigger job`)
	want(t, see(t, main("default:\n  stage: deploy\njob:\n  script: [x]\n"), "job", "stage"), "")
	files = map[string]string{
		".gitlab-ci.yml": "include: a.yml\njob:\n  image: {entrypoint: ['']}\n",
		"a.yml":          "job:\n  image:\n    name: ruby\n",
	}
	extracttest.WantError(t, refused(t, files, "job", "image"), "a.yml:3: selects a mapping, not a value")
}

// What GitLab refuses, errata refuses where the check is cheap: a keyword
// set both at the top level and under default:, a long form in a rule's
// variables, a description on a job's variable, a variable named as YAML
// reads a boolean, an include that is not YAML, and an expand: that is not
// a boolean. A !reference of one item selects the job. Planted breaks: each
// check dropped in turn, which gave the value GitLab never runs.
func TestRefusedAsGitLabRefuses(t *testing.T) {
	for _, c := range []struct{ name, body, of, want string }{
		{"top and default", "image: top\ndefault:\n  image: def\njob:\n  script: [x]\n", "image", `"image" is set at the top level and under default:`},
		{"long form in a rule", "job:\n  rules: [{variables: {T: {value: r}}}]\n  script: [x]\n", "T", `takes no key "value", which GitLab refuses; it takes only a string`},
		{"description in a job", "job:\n  variables: {T: {value: v, description: d}}\n  script: [x]\n", "T", `takes no key "description", which GitLab refuses; it takes value, expand`},
		{"not YAML", "include: a.txt\njob:\n  script: [x]\n", "image", `include "a.txt" is not a .yml or .yaml file`},
		{"expand as a string", "variables: {T: {value: $X, expand: 'false'}}\njob:\n  script: [x]\n", "T", "expand is true or false, which GitLab requires as a boolean"},
		{"unknown tag merged", "include: a.yml\njob: !custom {image: x}\n", "image", `the tag "!custom" is not one GitLab reads`},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := main(c.body)
			files["a.yml"] = "job: {script: [x]}\n"
			extracttest.WantError(t, refused(t, files, "job", c.of), c.want)
		})
	}
	_, err := build(Spec{File: ".gitlab-ci.yml", Job: "job", Variable: "ON"})
	extracttest.WantError(t, err, `variable "ON" is no name GitLab accepts: its YAML loader reads it as a boolean or null`)
	want(t, see(t, main(".v:\n  T: x\njob:\n  variables: !reference [.v]\n  script: [x]\n"), "job", "T"), "x@.gitlab-ci.yml:2")
	want(t, see(t, main("variables: {T: {value: v, description: d, options: [v, w], expand: true}}\njob:\n  script: [x]\n"), "job", "T"), "v@.gitlab-ci.yml:1")
}
