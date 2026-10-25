package githubactions

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

const wf = ".github/workflows/ci.yml"

// values lists the values as "text@line", with a ! after a not literal
// value and the condition in brackets after one that has it.
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
		if v.At.File != wf {
			cond += "(in " + v.At.File + ")"
		}
		out = append(out, fmt.Sprintf("%s%s@%d%s", v.Text, mark, v.At.Line, cond))
	}
	return strings.Join(out, " ")
}

// spec reads job, and step when it is not "", for name: a variable when
// name is in capitals, and a key otherwise.
func spec(job, step, name string) Spec {
	s := Spec{File: wf, Job: job, Step: step, Key: name}
	if strings.ToUpper(name) == name {
		s.Key, s.Variable = "", name
	}
	return s
}

// see returns what the step sees of name in the workflow body. A variable
// no env: sets is "", as the rule reads check.NoValue.
func see(t *testing.T, body, job, step, name string) string {
	t.Helper()
	got, err := extracttest.Run(t, build, spec(job, step, name), extracttest.Repo(t, map[string]string{wf: body}))
	if none := (*check.NoValue)(nil); errors.As(err, &none) {
		return ""
	}
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return values(got)
}

// refused returns the error the extraction gives.
func refused(t *testing.T, body, job, step, name string) error {
	t.Helper()
	_, err := extracttest.Run(t, build, spec(job, step, name), extracttest.Repo(t, map[string]string{wf: body}))
	if err == nil {
		t.Fatal("no error, want one")
	}
	return err
}

func want(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// "When more than one environment variable is defined with the same name,
// GitHub uses the most specific variable. For example, an environment
// variable defined in a step will override job and workflow environment
// variables with the same name, while the step executes. An environment
// variable defined for a job will override a workflow variable with the
// same name, while the job executes." (Workflow syntax, env.) Planted
// breaks: the step's env: skipped, which gave job for the step; the job's
// skipped, which gave workflow for the job.
func TestStepOverJobOverWorkflow(t *testing.T) {
	body := `on: push
env:
  SERVER: workflow
jobs:
  test:
    env:
      SERVER: job
    steps:
      - id: own
        env:
          SERVER: step
        run: ./deploy
      - id: plain
        run: ./deploy
  other:
    steps:
      - id: plain
        run: ./deploy
`
	want(t, see(t, body, "test", "own", "SERVER"), "step@11")
	want(t, see(t, body, "test", "plain", "SERVER"), "job@7")
	want(t, see(t, body, "test", "", "SERVER"), "job@7")
	want(t, see(t, body, "other", "plain", "SERVER"), "workflow@3")
	want(t, see(t, body, "other", "", "SERVER"), "workflow@3")
}

// The documentation's own example of a step's env:, which sets every name
// it echoes (Workflow syntax, jobs.<job_id>.steps).
func TestDocumentedStepEnv(t *testing.T) {
	body := `name: Greeting from Mona

on: push

jobs:
  my-job:
    name: My Job
    runs-on: ubuntu-latest
    steps:
      - name: Print a greeting
        env:
          MY_VAR: Hi there! My name is
          FIRST_NAME: Mona
          MIDDLE_NAME: The
          LAST_NAME: Octocat
        run: |
          echo $MY_VAR $FIRST_NAME $MIDDLE_NAME $LAST_NAME.
`
	want(t, see(t, body, "my-job", "Print a greeting", "MY_VAR"), "Hi there! My name is@12")
	want(t, see(t, body, "my-job", "Print a greeting", "LAST_NAME"), "Octocat@15")
	want(t, see(t, body, "my-job", "", "runs-on"), "ubuntu-latest@8")
}

// A step is selected by its id, which is unique, and else by its name,
// which must then name one step. A parallel: group's steps are the job's.
// An id after two steps of its name is still found by the id. Planted
// breaks: the id compared after the name, which picked the step named
// build; the second name not refused; the group not entered; the second
// name refused before the id is looked for, which refused late.
func TestStepByIdOrName(t *testing.T) {
	body := `jobs:
  test:
    steps:
      - name: build
        env: {V: by-name}
      - id: build
        env: {V: by-id}
      - name: twice
      - name: twice
      - parallel:
          - id: inner
            env: {V: in-group}
      - name: late
      - name: late
      - id: late
        env: {V: late-id}
`
	want(t, see(t, body, "test", "build", "V"), "by-id@7")
	want(t, see(t, body, "test", "late", "V"), "late-id@16")
	want(t, see(t, body, "test", "inner", "V"), "in-group@12")
	extracttest.WantError(t, refused(t, body, "test", "twice", "V"), `lines 8 and 9: job "test" has two steps named "twice"; give the step an id`)
	extracttest.WantError(t, refused(t, body, "test", "none", "V"), `job "test" has no step whose id or name is "none"`)
	// A name that holds an expression matches only as written, and the
	// refusal says so. Planted break: the hint dropped.
	body = "jobs:\n  test:\n    steps:\n      - name: Build ${{ matrix.os }}\n        env: {V: v}\n"
	want(t, see(t, body, "test", "Build ${{ matrix.os }}", "V"), "v@5")
	extracttest.WantError(t, refused(t, body, "test", "Build linux", "V"),
		`has no step whose id or name is "Build linux"; a name that holds an expression, such as "Build ${{ matrix.os }}" at line 4, matches only as written`)
	extracttest.WantError(t, refused(t, body, "nojob", "", "V"), `jobs holds no job "nojob"`)
	extracttest.WantError(t, refused(t, "jobs:\n  test:\n    runs-on: x\n", "test", "a", "V"), `job "test" has no steps`)
	extracttest.WantError(t, refused(t, "jobs:\n  test:\n    steps: {a: 1}\n", "test", "a", "V"), "line 3: steps: is a mapping, not a sequence")
	extracttest.WantError(t, refused(t, "jobs:\n  test:\n    steps: [run]\n", "test", "a", "V"), "line 3: a step is a scalar, not a mapping")
}

// key: reads a key of the step, or of the job without step:, where
// versions are pinned: runs-on in each of its forms, uses, and a with:
// input. A key the file does not set gives no value. Planted break: the
// step's key read from the job, which gave nothing for uses.
func TestKeys(t *testing.T) {
	body := `jobs:
  test:
    runs-on: [self-hosted, linux]
    container: {image: node:20}
    steps:
      - id: setup
        uses: actions/setup-node@v7
        with:
          node-version: 20.11.1
          args: --x
  grouped:
    runs-on:
      group: ubuntu-runners
      labels: ubuntu-20.04-16core
`
	want(t, see(t, body, "test", "", "runs-on"), "self-hosted@3 linux@3")
	want(t, see(t, body, "test", "setup", "uses"), "actions/setup-node@v7@7")
	want(t, see(t, body, "test", "setup", "with.node-version"), "20.11.1@9")
	want(t, see(t, body, "test", "", "container.image"), "node:20@4")
	want(t, see(t, body, "test", "setup", "with.missing"), "")
	want(t, see(t, body, "grouped", "", "runs-on.labels"), "ubuntu-20.04-16core@14")
	extracttest.WantError(t, refused(t, body, "grouped", "", "runs-on"), "ci.yml:13: the key selects a mapping, not a value; name a key below it")
	extracttest.WantError(t, refused(t, body, "test", "setup", "uses.x"), `the key's part before "x" is a scalar, not a mapping`)
	extracttest.WantError(t, refused(t, "jobs:\n  t:\n    runs-on: [[a]]\n", "t", "", "runs-on"), "ci.yml:3: the list holds a sequence")
}

// A value with a ${{ }} expression is GitHub's to evaluate as the workflow
// runs: secrets, repository variables, inputs and the env context are not
// in the file. It is returned as written and not literal. A matrix
// reference in the workflow's env: is no job's, and stays as written too.
// Planted break: the expression check dropped, which gave the text as
// literal.
func TestExpressionsAreNotLiteral(t *testing.T) {
	body := `env:
  W: ${{ matrix.v }}
jobs:
  test:
    strategy:
      matrix: {v: [1]}
    env:
      S: ${{ secrets.TOKEN }}
      R: ${{ vars.REGISTRY }}
      I: v${{ inputs.tag }}
      E: ${{ env.OTHER }}
      O: ${{ matrix.v
    steps:
      - id: s
        with: {node-version: '${{ env.NODE }}'}
`
	for name, line := range map[string]int{"S": 8, "R": 9, "E": 11} {
		got := see(t, body, "test", "", name)
		if !strings.HasSuffix(got, fmt.Sprintf("}}!@%d", line)) {
			t.Fatalf("%s: got %q, want the expression as written, not literal", name, got)
		}
	}
	want(t, see(t, body, "test", "", "I"), "v${{ inputs.tag }}!@10")
	want(t, see(t, body, "test", "", "O"), "${{ matrix.v!@12")
	want(t, see(t, body, "test", "", "W"), "${{ matrix.v }}!@2")
	want(t, see(t, body, "test", "s", "with.node-version"), "${{ env.NODE }}!@15")
}

// GitHub reads YAML 1.2's core schema: a plain scalar it types as a number,
// a boolean or null is not the text the step sees. 3.10 is the number 3.1,
// 010 is 10, and a 16 digit integer loses its last digit as a double. A
// decimal integer as a double prints it, and true and false, are their text,
// and so is a quoted, a block or a !!str scalar. Planted breaks: coreString
// always true; the 15 digit bound dropped; !!str read as typed; any other
// tag read as the text.
func TestValuesYAMLRetypes(t *testing.T) {
	notLiteral := []string{"3.10", "010", "1e3", "0x1f", "0o7", "+5", "-0", "True", "FALSE", "null", "~", "", ".inf", "-.Inf", ".nan", "1.", "1234567890123456", "!!int 5", "!!float 1", "!!binary aGk="}
	literal := []string{"20", "-3", "0", "true", "false", "123456789012345", "'3.10'", `"010"`, "!!str 3.10", "1_000", "1.2.3", "v1", "yes", "on", "0b11", "1:30", "2026-10-24", "|\n        3.10"}
	for _, v := range notLiteral {
		got := see(t, "jobs:\n  t:\n    env:\n      V: "+v+"\n", "t", "", "V")
		if !strings.Contains(got, "!@") {
			t.Errorf("%s: got %q, want not literal", v, got)
		}
	}
	for _, v := range literal {
		got := see(t, "jobs:\n  t:\n    env:\n      V: "+v+"\n", "t", "", "V")
		if strings.Contains(got, "!@") || got == "" {
			t.Errorf("%s: got %q, want literal", v, got)
		}
	}
}

// "Reusable workflows are called directly within a job", and "Any
// environment variables set in an env context defined at the workflow
// level in the caller workflow are not propagated to the called workflow"
// (Reusing workflow configurations). A step or a variable of a job that
// calls one is the called file's: a local one names it, and one from
// another repository is out of reach offline. The call's own uses and
// with: inputs are the caller's, and read. Planted breaks: the call not
// checked, which read the caller's env:; $/ read as another repository.
func TestReusableWorkflowCalls(t *testing.T) {
	body := `env:
  V: caller
jobs:
  local:
    uses: ./.github/workflows/build.yml
    with: {username: mona}
  same:
    uses: $/.github/workflows/build.yml
  remote:
    uses: octo-org/another-repo/.github/workflows/workflow.yml@v1
`
	extracttest.WantError(t, refused(t, body, "local", "", "V"), `ci.yml:5: job "local" calls the workflow "./.github/workflows/build.yml", whose steps run with its own env:`)
	extracttest.WantError(t, refused(t, body, "local", "build", "uses"), "read that file with file: instead")
	extracttest.WantError(t, refused(t, body, "same", "", "V"), `calls the workflow "$/.github/workflows/build.yml"`)
	extracttest.WantError(t, refused(t, body, "remote", "", "V"), `ci.yml:10: job "remote" calls the reusable workflow "octo-org/another-repo/.github/workflows/workflow.yml@v1", from another repository, which errata cannot read offline`)
	want(t, see(t, body, "remote", "", "uses"), "octo-org/another-repo/.github/workflows/workflow.yml@v1@10")
	want(t, see(t, body, "local", "", "with.username"), "mona@6")
}

// A step before the one read that writes GITHUB_ENV can set any variable
// for the steps after it: "all subsequent steps in a job will have access"
// (Workflow commands). What it writes is known only once it runs, so the
// value is unknown, naming the step, whichever variable it writes: the
// script below names no TAG. The step itself, and the steps after it, do
// not change what it sees. Planted breaks: the check over the step itself
// too, which refused own; no check, which gave the job's value; the check
// also needing the variable's name in the script, which gave job@3 for TAG.
func TestGitHubEnvBeforeTheStep(t *testing.T) {
	body := `jobs:
  test:
    env: {V: job, TAG: job}
    steps:
      - id: own
        run: cat vars >> "$GITHUB_ENV"
      - id: next
        run: ./test
      - name: last
        run: echo "W=y" >> $GITHUB_ENV
`
	want(t, see(t, body, "test", "own", "TAG"), "job@3")
	extracttest.WantError(t, refused(t, body, "test", "next", "TAG"), `ci.yml:6: the step "own" names GITHUB_ENV in its run:, which can set TAG for the steps after it`)
	want(t, see(t, body, "test", "", "V"), "job@3")
	body = "jobs:\n  test:\n    steps:\n      - run: echo >> $GITHUB_ENV\n      - id: s\n        env: {V: s}\n"
	extracttest.WantError(t, refused(t, body, "test", "s", "V"), "the step at line 4 names GITHUB_ENV")
	body = "jobs:\n  test:\n    steps:\n      - name: setup\n        run: echo >> $GITHUB_ENV\n      - id: s\n"
	extracttest.WantError(t, refused(t, body, "test", "s", "V"), `the step "setup" names GITHUB_ENV`)
}

// The file is named in more ways than $GITHUB_ENV: PowerShell reads
// $env:github_env in any case, ${{ github.env }} is its path, a step's
// env: can hand the path to its script, and a shell: is a template the
// script runs in, the step's own or the job's or the workflow's
// defaults.run.shell. The same text in a step that runs no script, or
// that runs after the one read, changes nothing. Planted breaks: the
// match in one case, which missed the PowerShell step; github.env not
// matched; the index form not matched; the env: values not read; the
// shells not read, each of the three in turn; the job's env: not read; the
// workflow's env: not read; the job's env: read over the step's own run:.
func TestGitHubEnvSpellings(t *testing.T) {
	step := func(lines string) string {
		return "jobs:\n  t:\n    env: {V: job}\n    steps:\n      - " + lines + "\n      - id: s\n"
	}
	for _, c := range []struct{ body, want string }{
		{step(`{shell: pwsh, run: '"V=x" >> $env:github_env'}`), `ci.yml:5: the step at line 5 names GITHUB_ENV in its run:`},
		{step(`{id: e, run: 'echo V=x >> ${{ github.env }}'}`), `the step "e" names GITHUB_ENV in its run:`},
		{step(`{id: e, run: "echo V=x >> ${{ github['env'] }}"}`), `the step "e" names GITHUB_ENV in its run:`},
		{step(`{id: e, run: "echo V=x >> ${{ github [ 'ENV' ] }}"}`), `the step "e" names GITHUB_ENV in its run:`},
		{step(`{id: e, env: {F: '${{ github.env }}'}, run: 'echo V=x >> "$F"'}`), `the step "e" names GITHUB_ENV in its env:`},
		{step(`{id: e, shell: 'bash -c "echo V=1 >> $GITHUB_ENV; {0}"', run: ./x}`), `the step "e" names GITHUB_ENV in its shell:`},
		{"defaults: {run: {shell: 'bash -c \"echo V=1 >> $GITHUB_ENV; {0}\"'}}\n" + step("{id: e, run: ./x}"),
			`ci.yml:1: the step "e" names GITHUB_ENV in its shell, the workflow's defaults.run.shell`},
		{strings.Replace(step("{id: e, run: ./x}"), "    env:", "    defaults: {run: {shell: 'sh -c \"env >> $GITHUB_ENV; {0}\"'}}\n    env:", 1),
			`ci.yml:3: the step "e" names GITHUB_ENV in its shell, the job's defaults.run.shell`},
	} {
		extracttest.WantError(t, refused(t, c.body, "t", "s", "V"), c.want)
	}
	// The job's and the workflow's env: reach every run: step, so a value
	// there that names the file counts for each, as the step's own does.
	level := "jobs:\n  t:\n    env: {V: job, F: '${{ github.env }}'}\n    steps:\n      - uses: actions/checkout@v6\n      - run: echo V=x >> \"$F\"\n      - id: s\n"
	extracttest.WantError(t, refused(t, level, "t", "s", "V"), `ci.yml:3: the job's env: names GITHUB_ENV and reaches the run: of the step at line 6, which can set V`)
	want(t, see(t, strings.Replace(level, "      - run:", "      - id: s\n      - run:", 1), "t", "s", "V"), "job@3")
	// The run: scripts run in the job's container and see its env:.
	// Planted break: the container's env: not read, which gave job@4.
	inside := "jobs:\n  t:\n    container: {image: alpine, env: {F: '${{ github.env }}'}}\n    env: {V: job}\n    steps:\n      - run: echo V=x >> \"$F\"\n      - id: s\n"
	extracttest.WantError(t, refused(t, inside, "t", "s", "V"), `ci.yml:3: the job's container.env: names GITHUB_ENV and reaches the run: of the step at line 6`)
	top := "env: {F: '${{ github.env }}'}\n" + step(`{id: e, run: 'echo V=x >> "$F"'}`)
	extracttest.WantError(t, refused(t, top, "t", "s", "V"), `ci.yml:1: the workflow's env: names GITHUB_ENV and reaches the run: of the step "e"`)
	both := "env: {F: '${{ github.env }}'}\n" + step(`{id: e, run: 'echo V=x >> $GITHUB_ENV'}`)
	extracttest.WantError(t, refused(t, both, "t", "s", "V"), `ci.yml:6: the step "e" names GITHUB_ENV in its run:`)
	want(t, see(t, step(`{id: e, run: 'echo github.environment'}`), "t", "s", "V"), "job@3")
	want(t, see(t, step(`{id: e, uses: actions/checkout@v6, with: {path: '${{ github.env }}'}}`), "t", "s", "V"), "job@3")
	want(t, see(t, "defaults: {run: {shell: 'sh -c \"env >> $GITHUB_ENV; {0}\"'}}\n"+step("{id: e, uses: actions/checkout@v6}"), "t", "s", "V"), "job@4")
}

// An action can set a variable for the steps after it with
// core.exportVariable, which writes GITHUB_ENV, and errata reads no
// action's code. After a step that runs an action not on the list of those
// known to set none, a later step's variable is unknown, naming the action
// and its step. A local action and a Docker one are not on the list; the
// list matches the owner and the repository in any ASCII case, and the
// Kelvin sign is not k. Planted breaks: actions not checked, which gave
// job@3 after setup-node; the ref not cut, which refused checkout; the
// name matched in one case; the name folded with strings.ToLower; the
// actions/cache and actions/cache/save rows dropped, each in turn.
func TestActionsBeforeTheStep(t *testing.T) {
	body := `jobs:
  t:
    env: {V: job}
    steps:
      - uses: actions/checkout@v6
      - uses: Actions/Cache/Restore@v5
      - uses: actions/cache@v5
      - uses: actions/cache/save@v5
      - uses: actions/upload-artifact@v6
      - uses: actions/download-artifact@v7
      - id: quiet
      - id: setup
        uses: actions/setup-node@v7
      - id: after
`
	want(t, see(t, body, "t", "quiet", "V"), "job@3")
	want(t, see(t, body, "t", "setup", "V"), "job@3")
	extracttest.WantError(t, refused(t, body, "t", "after", "V"),
		`ci.yml:13: the step "setup" runs the action "actions/setup-node@v7", which can set V for the steps after it through GITHUB_ENV; errata reads no action's code`)
	for _, uses := range []string{"./.github/actions/checkout", "docker://alpine:3", "actions/checkout-fork@v1", "actions/chec\u212aout@v6"} {
		body := "jobs:\n  t:\n    env: {V: job}\n    steps:\n      - uses: " + uses + "\n      - id: s\n"
		extracttest.WantError(t, refused(t, body, "t", "s", "V"), `the step at line 5 runs the action "`+uses+`"`)
	}
	// A key is the file's, whatever an action exports.
	want(t, see(t, body, "t", "after", "uses"), "")
}

// A variable no env: sets is no value, and the reason names where errata
// looked and where else it may come from. Planted break: an empty list
// returned, which named nothing.
func TestVariableNoFileSets(t *testing.T) {
	body := "env: {A: 1}\njobs:\n  test:\n    container: node:20\n    steps:\n      - id: s\n"
	_, err := extracttest.Run(t, build, spec("test", "s", "TAG"), extracttest.Repo(t, map[string]string{wf: body}))
	var none *check.NoValue
	if !errors.As(err, &none) {
		t.Fatalf("got %v, want a NoValue", err)
	}
	want(t, none.Reason, `no env: of step "s", job "test", the workflow, its container sets TAG; it may come from a step before that writes GITHUB_ENV, from an action, from the runner, or from the repository's variables and secrets, which errata cannot read`)
}

// The env: of a job's container sets the variable in the container its
// steps run in. GitHub documents no order between it and the env: of the
// workflow, the job and the step, so a variable both set is unknown. One
// only the container sets is the container's. Planted breaks: the
// container not read; the conflict not refused, which gave the job's.
func TestContainerEnv(t *testing.T) {
	body := `jobs:
  test:
    container:
      image: node:20
      env: {NODE_ENV: development, V: c}
    env: {V: job}
    steps:
      - id: s
`
	want(t, see(t, body, "test", "s", "NODE_ENV"), "development@5")
	extracttest.WantError(t, refused(t, body, "test", "s", "V"), `ci.yml:6: the env: of job "test" and the job's container.env at line 5 both set V, and GitHub documents no order between them`)
}

// An env: GitHub refuses, or one an expression writes, does not resolve.
// A name that differs from the variable's only in case is one variable on
// a Windows runner, so the value hangs on the runner. Planted breaks:
// each check dropped in turn.
func TestEnvRefusals(t *testing.T) {
	for _, c := range []struct{ env, want string }{
		{"${{ fromJSON(needs.a.outputs.env) }}", `ci.yml:3: the env: of job "t" is an expression`},
		{"[a]", `ci.yml:3: the env: of job "t" is a sequence, not a mapping`},
		{"{V: {a: 1}}", `ci.yml:3: V in the env: of job "t" is a mapping; a variable's value is a string`},
		{"{v: 1}", `ci.yml:3: the env: of job "t" sets "v", which a Windows runner reads as V`},
	} {
		extracttest.WantError(t, refused(t, "jobs:\n  t:\n    env: "+c.env+"\n", "t", "", "V"), c.want)
	}
	extracttest.WantError(t, refused(t, "env:\n  V: [a]\njobs:\n  t: {}\n", "t", "", "V"), "ci.yml:2: V in the env: of the workflow is a sequence; a variable's value is a string")
}

// A workflow is one YAML document, a mapping with a jobs: mapping, and a
// job is a mapping. Planted break: each check dropped in turn.
func TestWorkflowShape(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"a: 1\n---\nb: 2\n", "holds 2 YAML documents; a workflow is one"},
		{"", "holds 0 YAML documents"},
		{"[a]\n", "line 1: the workflow is a sequence, not a mapping"},
		{"on: push\n", "has no jobs: mapping, which a workflow needs"},
		{"jobs: [a]\n", "has no jobs: mapping"},
		{"jobs:\n  t: x\n", `line 2: job "t" is a scalar, not a mapping`},
		{"jobs: {t: {}\n", "ci.yml: yaml:"},
	} {
		extracttest.WantError(t, refused(t, c.body, "t", "", "V"), c.want)
	}
}

// Build refuses a spec it cannot read. Planted break: each check dropped
// in turn.
func TestBuildRefusals(t *testing.T) {
	long := strings.Repeat("a", maxName+1)
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{Job: "t", Key: "runs-on"}, "github-actions: file is required"},
		{Spec{File: wf, Key: "runs-on"}, "github-actions: job is required"},
		{Spec{File: wf, Job: "t"}, "github-actions: set exactly one of variable"},
		{Spec{File: wf, Job: "t", Variable: "V", Key: "uses"}, "github-actions: set exactly one of variable"},
		{Spec{File: wf, Job: "t", Variable: "1V"}, `variable "1V" is no variable name`},
		{Spec{File: wf, Job: "t", Variable: "A-B"}, `variable "A-B" is no variable name`},
		{Spec{File: wf, Job: "t", Variable: long}, "is no variable name"},
		{Spec{File: wf, Job: "t", Variable: "GITHUB_SHA"}, "GitHub ignores an env: that sets a GITHUB_ or RUNNER_ name"},
		{Spec{File: wf, Job: "t", Variable: "runner_temp"}, `variable "runner_temp" is one the runner sets`},
		{Spec{File: wf, Job: "t", Key: "with..a"}, `key "with..a" has an empty part`},
		{Spec{File: wf, Job: "t", Key: "env"}, "key env: write variable: NAME"},
		{Spec{File: wf, Job: "t", Key: "env.A"}, "key env: write variable: NAME"},
		{Spec{File: wf, Job: "t", Key: long}, "bytes long, over the limit of 1024"},
		{Spec{File: wf, Job: long, Key: "uses"}, "github-actions: job \"aaaa"},
		{Spec{File: wf, Job: "t", Step: long, Key: "uses"}, "github-actions: step \"aaaa"},
		{Spec{File: "../ci.yml", Job: "t", Key: "uses"}, "outside the repository"},
	} {
		_, err := build(c.s)
		extracttest.WantError(t, err, c.want)
	}
	for _, ok := range []Spec{
		{File: wf, Job: "t", Variable: "CI"},
		{File: wf, Job: "t", Variable: "_A1"},
		{File: wf, Job: "t", Variable: strings.Repeat("A", maxName)},
		{File: wf, Job: strings.Repeat("a", maxName), Step: strings.Repeat("a", maxName), Key: "with." + strings.Repeat("a", maxName-5)},
	} {
		if _, err := build(ok); err != nil {
			t.Fatalf("%+v: %v", ok, err)
		}
	}
}

// Place cleans the file and drops max_bytes, and keeps what selects.
func TestPlace(t *testing.T) {
	got := Place(Spec{File: "./.github/workflows/../workflows/ci.yml", Job: "t", Step: "s", Variable: "V", MaxBytes: 10})
	if got != (Spec{File: wf, Job: "t", Step: "s", Variable: "V"}) {
		t.Fatalf("got %+v", got)
	}
}

// The extractor declares that it can return a not literal value, so a rule
// may list it in skip_not_literal.
func TestMayReturnNotLiteral(t *testing.T) {
	x, err := build(spec("t", "", "V"))
	if err != nil {
		t.Fatal(err)
	}
	if !check.MayReturnNotLiteral(x) {
		t.Fatal("MayReturnNotLiteral is false")
	}
}
