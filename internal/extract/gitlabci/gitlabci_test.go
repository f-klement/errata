package gitlabci

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// yamlParse is the parser the facade hands Build.
var yamlParse Parser = yaml.ParseNoted

// build is Build with that parser.
func build(s Spec) (check.Extractor, error) { return Build(s, yamlParse) }

// values lists what the job sees as "text@file:line", with a ! after a not
// literal value and the condition in brackets after one that has it.
func values(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		cond := ""
		if v.Condition != "" {
			cond = "[" + v.Condition + "]"
		}
		out = append(out, fmt.Sprintf("%s%s@%s:%d%s", v.Text, mark, v.At.File, v.At.Line, cond))
	}
	return strings.Join(out, " ")
}

// see returns what job sees of a variable, or of a key when name holds a
// keyword, in a repository of files whose .gitlab-ci.yml is the main file.
// A variable no file sets is "", as the rule reads check.NoValue.
func see(t *testing.T, files map[string]string, job, name string) string {
	t.Helper()
	got, err := extracttest.Run(t, build, spec(job, name), extracttest.Repo(t, files))
	if none := (*check.NoValue)(nil); errors.As(err, &none) {
		return ""
	}
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return values(got)
}

// refused returns the error the extraction gives.
func refused(t *testing.T, files map[string]string, job, name string) error {
	t.Helper()
	_, err := extracttest.Run(t, build, spec(job, name), extracttest.Repo(t, files))
	if err == nil {
		t.Fatal("no error, want one")
	}
	return err
}

// spec names a variable when name is in capitals, and a key otherwise.
func spec(job, name string) Spec {
	if strings.ToUpper(name) == name {
		return Spec{File: ".gitlab-ci.yml", Job: job, Variable: name}
	}
	return Spec{File: ".gitlab-ci.yml", Job: job, Key: name}
}

func main(body string) map[string]string { return map[string]string{".gitlab-ci.yml": body} }

func want(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// "The top-level of the .gitlab-ci.yml file: The variable is available as a
// default for all jobs in a pipeline, unless a job defines a variable with
// the same name. The job's variable takes precedence." (CI/CD variables,
// Define a CI/CD variable in the .gitlab-ci.yml file.) Planted break: varIn
// on the job skipped, which gave the top-level value to both jobs.
func TestJobVariableOverridesTopLevel(t *testing.T) {
	files := main("variables:\n  TAG: top\nbuild:\n  variables:\n    TAG: job\n  script: [make]\ntest:\n  script: [make]\n")
	want(t, see(t, files, "build", "TAG"), "job@.gitlab-ci.yml:5")
	want(t, see(t, files, "test", "TAG"), "top@.gitlab-ci.yml:2")
}

// The precedence list, "Variables in the .gitlab-ci.yml file, from highest
// to lowest precedence: rules:variables in the job. variables in the job.
// workflow:rules:variables. Default variables at the top level of the
// file." (CI/CD variable precedence.) Which rule matches is the pipeline's,
// so each rule that can match gives its value, and a rule that sets none
// gives the value below it. Planted break: rules on the job skipped, which
// gave only "job".
func TestRulesVariablesOverJobVariables(t *testing.T) {
	files := main(`variables:
  TAG: top
build:
  variables:
    TAG: job
  rules:
    - if: $CI_COMMIT_TAG
      variables:
        TAG: release
    - if: $CI_MERGE_REQUEST_ID
    - when: always
      variables:
        TAG: always
    - variables:
        TAG: unreachable
  script: [make]
`)
	want(t, see(t, files, "build", "TAG"), "release@.gitlab-ci.yml:9[if $CI_COMMIT_TAG] always@.gitlab-ci.yml:13 job@.gitlab-ci.yml:5")
}

// workflow:rules:variables sit between the job's variables and the top
// level's in the same precedence list: above "Default variables at the top
// level", below "variables in the job". Planted break: workflow's rules
// read after the job's variables, which gave "workflow" to build.
func TestWorkflowRulesVariablesBetweenJobAndTopLevel(t *testing.T) {
	files := main(`variables:
  TAG: top
  ONLY_TOP: top
workflow:
  rules:
    - if: $CI_COMMIT_TAG
      variables:
        TAG: workflow
        ONLY_TOP: workflow
    - when: always
build:
  variables:
    TAG: job
  script: [make]
`)
	want(t, see(t, files, "build", "TAG"), "job@.gitlab-ci.yml:13")
	want(t, see(t, files, "build", "ONLY_TOP"), "workflow@.gitlab-ci.yml:9[workflow if $CI_COMMIT_TAG] top@.gitlab-ci.yml:3")
}

// A rule with when: never stops the job when it matches, so its variables
// never reach it; "when no rule matches", from the rules reference, the
// job is not added, so a rules list with no catch-all gives no value below
// it. Planted break: when: never not checked, which gave "never" too.
func TestRuleWithWhenNeverGivesNothing(t *testing.T) {
	files := main(`build:
  variables:
    TAG: job
  rules:
    - if: $A
      when: never
      variables:
        TAG: never
    - if: $B
      variables:
        TAG: b
  script: [make]
`)
	want(t, see(t, files, "build", "TAG"), "b@.gitlab-ci.yml:11[if $B]")
}

// A variable set by some rules and by nothing else leaves the job with no
// value from the files when another rule matches; that is not one of the
// values, so the extraction is refused rather than giving the rule's alone.
func TestVariableSetOnlyBySomeRulesIsRefused(t *testing.T) {
	err := refused(t, main("build:\n  rules:\n    - if: $A\n      variables:\n        TAG: a\n    - when: always\n  script: [make]\n"), "build", "TAG")
	extracttest.WantError(t, err, ".gitlab-ci.yml:5: the value is set only when some rules or matrix instances match")
}

// "Use inherit:variables to control the inheritance of default variables
// keywords", true by default, false for none, or "list default variables to
// inherit on one line: variables: [VARIABLE1, VARIABLE2]" (inherit:variables
// reference). The workflow's rule variables count as default variables
// too: "workflow:rules:variables become default variables available in all
// jobs", and "Use inherit:variables in the trigger job and list the exact
// variables you want to forward" (workflow:rules:variables reference).
// Planted break: inherits ignored, which gave "top" to every job.
func TestInheritVariables(t *testing.T) {
	files := main(`variables:
  TAG: top
  OTHER: top
workflow:
  rules:
    - if: $A
      variables:
        WF: workflow
    - when: always
none:
  inherit:
    variables: false
  script: [make]
some:
  inherit:
    variables: [OTHER, WF]
  script: [make]
`)
	want(t, see(t, files, "none", "TAG"), "")
	want(t, see(t, files, "none", "WF"), "")
	want(t, see(t, files, "some", "TAG"), "")
	want(t, see(t, files, "some", "OTHER"), "top@.gitlab-ci.yml:3")
	err := refused(t, files, "some", "WF")
	extracttest.WantError(t, err, "set only when some rules or matrix instances match")

	// The boolean is Psych's: YAML 1.1 spells it yes, on or true, and no,
	// off or false, in any case, and "the value must be a boolean or an
	// array of strings" (inherit/variables.rb), so a quoted "false" or a
	// !!str one is refused. Planted breaks: only true and false taken,
	// which refused yes; the quoted false read as false.
	for flag, want1 := range map[string]string{"yes": "top@.gitlab-ci.yml:1", "On": "top@.gitlab-ci.yml:1", "!!bool TRUE": "top@.gitlab-ci.yml:1", "no": "", "Off": "", "False": ""} {
		want(t, see(t, main("variables: {TAG: top}\njob:\n  inherit: {variables: "+flag+"}\n  script: [x]\n"), "job", "TAG"), want1)
	}
	for _, flag := range []string{`"false"`, `'yes'`, `!!str false`} {
		extracttest.WantError(t, refused(t, main("variables: {TAG: top}\njob:\n  inherit: {variables: "+flag+"}\n  script: [x]\n"), "job", "TAG"), "as a string, quoted or tagged, which GitLab refuses")
	}
}

// "If the job already has a keyword defined, the job keyword takes
// precedence and the default keyword is not used" (default reference), and
// "Use inherit:default to control the inheritance of default keywords"
// (inherit:default reference). Planted break: the default read before the
// job's own keyword, which gave ruby:3.0 to rspec.
func TestDefaultKeyword(t *testing.T) {
	files := main(`default:
  image: ruby:3.0
  retry: 2
rspec:
  image: ruby:2.7
  script: [rspec]
lint:
  script: [lint]
alone:
  inherit:
    default: false
  script: [x]
some:
  inherit:
    default: [retry]
  script: [x]
`)
	want(t, see(t, files, "rspec", "image"), "ruby:2.7@.gitlab-ci.yml:5")
	want(t, see(t, files, "rspec", "retry"), "2@.gitlab-ci.yml:3")
	want(t, see(t, files, "lint", "image"), "ruby:3.0@.gitlab-ci.yml:2")
	want(t, see(t, files, "alone", "image"), "")
	want(t, see(t, files, "some", "image"), "")
	want(t, see(t, files, "some", "retry"), "2@.gitlab-ci.yml:3")
}

// "Using image at the top level, but not in the default section, is
// deprecated" (image reference): GitLab still reads it as the default, and
// stages, which default does not take, is no job's keyword. Planted break:
// every top-level key read as a default, which gave tags "top".
func TestTopLevelKeywordIsADefault(t *testing.T) {
	files := main("image: alpine\ntags: [top]\ndefault:\n  before_script: [default]\nbuild:\n  script: [make]\n")
	want(t, see(t, files, "build", "image"), "alpine@.gitlab-ci.yml:1")
	want(t, see(t, files, "build", "before_script"), "default@.gitlab-ci.yml:4")
	want(t, see(t, files, "build", "stages"), "")
	// tags is a default keyword, but not one the top level sets: the
	// top-level key is no default.
	want(t, see(t, files, "build", "tags"), "")
}

// extends "Performs a reverse deep merge based on the keys", over "up to
// eleven levels of inheritance", and with several parents "Keys from the
// last member always override anything defined on other levels" (extends
// reference and Optimize GitLab CI/CD configuration files). Hashes merge,
// "script does not merge", and "you must assign it to null" to unset a key.
// Planted break: the job's own keys left out of the merge, which gave the
// template's TAG.
func TestExtendsMergesDeeply(t *testing.T) {
	files := main(`.base:
  image: base
  variables:
    TAG: base
    KEEP: base
  script: [base1, base2]
.middle:
  extends: .base
  variables:
    TAG: middle
.other:
  image: other
  variables:
    GONE: other
rspec:
  extends: [.middle, .other]
  variables:
    TAG: rspec
    GONE: null
  script: [rspec]
`)
	want(t, see(t, files, "rspec", "TAG"), "rspec@.gitlab-ci.yml:18")
	want(t, see(t, files, "rspec", "KEEP"), "base@.gitlab-ci.yml:5")
	want(t, see(t, files, "rspec", "image"), "other@.gitlab-ci.yml:12")
	want(t, see(t, files, "rspec", "script"), "rspec@.gitlab-ci.yml:20")
	// A null value is the variable GitLab refuses, so it is not literal.
	want(t, see(t, files, "rspec", "GONE"), "null!@.gitlab-ci.yml:19")
}

// Eleven levels of extends load, and a twelfth is refused, as are a job
// that extends itself and a parent no file defines. Planted break:
// maxExtends at 12, which read the chain of 12.
func TestExtendsLimits(t *testing.T) {
	chain := func(n int) string {
		var b strings.Builder
		b.WriteString(".t0:\n  image: deep\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, ".t%d:\n  extends: .t%d\n", i, i-1)
		}
		fmt.Fprintf(&b, "job:\n  extends: .t%d\n  script: [x]\n", n-1)
		return b.String()
	}
	want(t, see(t, main(chain(11)), "job", "image"), "deep@.gitlab-ci.yml:2")
	extracttest.WantError(t, refused(t, main(chain(12)), "job", "image"), "more than 11 levels of extends")
	extracttest.WantError(t, refused(t, main(".a:\n  extends: .b\n.b:\n  extends: .a\njob:\n  extends: .a\n"), "job", "image"), "extends itself")
	extracttest.WantError(t, refused(t, main("job:\n  extends: .gone\n"), "job", "image"), `job "job" extends ".gone", which no file defines`)
}

// "You must define the anchor higher in the YAML file than any aliases that
// reference it", a merge key "merge[s] the given hash into the current
// one", and "Anchors are only valid in the file they were defined in"
// (YAML anchors for scripts and merge keys). A value an alias gives is at
// its anchor's line.
func TestAnchorsAndMergeKeys(t *testing.T) {
	files := main(`.defaults: &defaults
  image: ruby
  variables: &vars
    TAG: anchored
test1:
  <<: *defaults
  script: [one]
test2:
  <<: *defaults
  image: node
  variables: *vars
`)
	want(t, see(t, files, "test1", "image"), "ruby@.gitlab-ci.yml:2")
	want(t, see(t, files, "test1", "TAG"), "anchored@.gitlab-ci.yml:4")
	want(t, see(t, files, "test2", "image"), "node@.gitlab-ci.yml:10")
	want(t, see(t, files, "test2", "TAG"), "anchored@.gitlab-ci.yml:4")
	files = map[string]string{
		".gitlab-ci.yml": "include: a.yml\njob:\n  <<: *base\n",
		"a.yml":          ".base: &base\n  image: ruby\n",
	}
	extracttest.WantError(t, refused(t, files, "job", "image"), "unknown anchor 'base'")
}

// "Use the !reference custom YAML tag to select keyword configuration from
// other job sections", and "You can nest !reference tags up to 10 levels
// deep in script, before_script, and after_script sections", whose nested
// arrays the job runs as one ("the nested-references job runs all three
// echo commands"). A !reference is followed after extends is merged, so it
// selects what the job it names inherits. Planted break: flatten without
// its recursion into a nested sequence, which refused the nested script.
func TestReference(t *testing.T) {
	files := main(`.setup:
  script:
    - echo one
.parent:
  variables:
    TAG: inherited
.child:
  extends: .parent
.vars:
  variables:
    URL: example.com
    PORT: "80"
nested:
  script:
    - !reference [.setup, script]
    - [echo two, [echo three]]
  variables: !reference [.vars, variables]
pick:
  variables:
    TAG: !reference [.child, variables, TAG]
  script: [x]
`)
	want(t, see(t, files, "nested", "script"), "echo one@.gitlab-ci.yml:3 echo two@.gitlab-ci.yml:16 echo three@.gitlab-ci.yml:16")
	want(t, see(t, files, "nested", "PORT"), "80@.gitlab-ci.yml:12")
	want(t, see(t, files, "pick", "TAG"), "inherited@.gitlab-ci.yml:6")
}

// A !reference that selects nothing, one that leads back to itself, nests
// past 10 levels, or takes no list is refused, and so is a tag GitLab does
// not read.
func TestReferenceRefusals(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"missing key", ".a:\n  script: [x]\njob:\n  script: !reference [.a, after_script]\n", `has no key "after_script"`},
		{"missing job", "job:\n  script: !reference [.gone, script]\n", `!reference names ".gone", which no file defines`},
		{"loop", ".a:\n  script: !reference [.b, script]\n.b:\n  script: !reference [.a, script]\njob:\n  script: !reference [.a, script]\n", "leads back to itself"},
		{"scalar", "job:\n  script: !reference .a\n", "!reference takes a sequence"},
		{"other tag", "job:\n  script: !include x\n", `the tag "!include" is not one GitLab reads`},
		{"deep", nestedScripts(11), "nest more than 10 levels"},
	} {
		t.Run(c.name, func(t *testing.T) {
			extracttest.WantError(t, refused(t, main(c.body), "job", "script"), c.want)
		})
	}
	want(t, see(t, main(nestedScripts(10)), "job", "script"), "x@.gitlab-ci.yml:2")
}

// nestedScripts is a job whose script reaches one command through n
// templates, each a !reference to the one before inside a list.
func nestedScripts(n int) string {
	var b strings.Builder
	b.WriteString(".s0:\n  script: [x]\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, ".s%d:\n  script: [!reference [.s%d, script]]\n", i, i-1)
	}
	fmt.Fprintf(&b, "job:\n  script: [!reference [.s%d, script]]\n", n-1)
	return b.String()
}

// A merge never changes a mapping it did not make: .mid's extends merges
// its variables into a copy of .base's, so b, which extends .base and
// reaches .mid only through a !reference, sees no X. Planted break: the
// copy skipped, which merged X into .base's own variables and gave "mid".
func TestMergeCopiesSharedMappings(t *testing.T) {
	files := main(`.base:
  variables:
    KEEP: base
.mid:
  extends: .base
  variables:
    X: mid
  inherit: {variables: true}
b:
  extends: .base
  inherit: !reference [.mid, inherit]
  script: [x]
`)
	want(t, see(t, files, "b", "X"), "")
	want(t, see(t, files, "b", "KEEP"), "base@.gitlab-ci.yml:3")
}

// The env kind reads a file named .gitlab-ci.yml with gitlab-ci, in any
// directory. Planted break: a name that ends in .gitlab-ci.yml taken.
func TestEnvChooses(t *testing.T) {
	for file, want := range map[string]bool{
		".gitlab-ci.yml": true, "sub/.gitlab-ci.yml": true,
		"a.gitlab-ci.yml": false, ".gitlab-ci.yaml": false, "ci.yml": false,
	} {
		if got := Kind.Env.Chooses(file); got != want {
			t.Errorf("%s: %t, want %t", file, got, want)
		}
	}
}
