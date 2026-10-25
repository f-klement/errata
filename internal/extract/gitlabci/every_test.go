package gitlabci

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// every is what Every reads of name in a repository of files, each value
// as values writes it and its origin after it.
func every(t *testing.T, files map[string]string, name string) (string, error) {
	t.Helper()
	x, err := Every(".gitlab-ci.yml", name, 0, yamlParse)
	if err != nil {
		t.Fatalf("Every: %v", err)
	}
	vs, err := x.Extract(extracttest.Repo(t, files))
	var out []string
	for _, v := range vs {
		out = append(out, values([]check.Value{v})+" "+v.Origin)
	}
	return strings.Join(out, "; "), err
}

// Every gives each job's value, in the order the file and its includes
// list the jobs, each named as its origin: a job's own over the top
// level's, one from an included file, a rule's with its condition, and a
// trigger job's, which it passes downstream. A hidden job, a global keyword
// and a job that sees no value give none. Planted breaks: the hidden job
// test dropped, which read .base and gave its value twice; the origin left
// out; a job with no value refused, as Build refuses it.
func TestEveryJob(t *testing.T) {
	got, err := every(t, map[string]string{
		".gitlab-ci.yml": `include: ci.yml
variables: {PORT: "80"}
.base: {variables: {PORT: "81"}}
build: {extends: .base, script: [b]}
test: {script: [t]}
release:
  rules:
    - if: $CI_COMMIT_TAG
      variables: {PORT: "82"}
    - when: on_success
  script: [r]
down: {trigger: {include: child.yml}}
lint: {inherit: {variables: false}, script: [l]}
`,
		"ci.yml": "deploy: {variables: {PORT: \"83\"}, script: [d]}\n",
	}, "PORT")
	if err != nil {
		t.Fatal(err)
	}
	want := `83@ci.yml:1 job "deploy"; 81@.gitlab-ci.yml:3 job "build"; 80@.gitlab-ci.yml:2 job "test"; ` +
		`82@.gitlab-ci.yml:9[if $CI_COMMIT_TAG] job "release"; 80@.gitlab-ci.yml:2 job "release"; 80@.gitlab-ci.yml:2 job "down"`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A file no job of which sees the variable gives no value, not an error,
// so allow_empty decides.
func TestEveryJobNone(t *testing.T) {
	got, err := every(t, map[string]string{".gitlab-ci.yml": "build: {script: [b]}\n"}, "PORT")
	if err != nil || got != "" {
		t.Errorf("got %q, %v; want no value", got, err)
	}
}

// What makes one job's value unknown makes Every's: a remote include, and
// a value only some rules set. Planted break: the error of a job skipped
// like a job with no value.
func TestEveryJobRefuses(t *testing.T) {
	_, err := every(t, map[string]string{".gitlab-ci.yml": "include: https://example.com/a.yml\nbuild: {script: [b]}\n"}, "PORT")
	extracttest.WantError(t, err, "which errata cannot read offline")
	_, err = every(t, map[string]string{".gitlab-ci.yml": "build:\n  rules:\n    - if: $A\n      variables: {PORT: \"1\"}\n    - when: always\n  script: [b]\n"}, "PORT")
	extracttest.WantError(t, err, "the value is set only when some rules or matrix instances match")
	_, err = Every(".gitlab-ci.yml", "true", 0, yamlParse)
	extracttest.WantError(t, err, "its YAML loader reads it as a boolean or null")
	_, err = Every("../x", "PORT", 0, yamlParse)
	extracttest.WantError(t, err, "is outside the repository")
}

// Every's values in all are held to core.MaxValues and to max_bytes, as
// one job's are: each job repeats what it inherits, and values of exactly
// either bound are kept. The value bound is witnessed at 100, a limit the
// test sets. Planted breaks: either bound dropped, which returned every
// value; the byte bound taken as >=; the value bound taken as one more.
func TestEveryJobBounds(t *testing.T) {
	// The value bound at 100 values, as the extractor holds it at
	// core.MaxValues: 100000 jobs would pass the mutation run's heap cap.
	var b strings.Builder
	b.WriteString("variables: {PORT: \"8080\"}\n")
	for i := range 100 {
		fmt.Fprintf(&b, "j%d: {script: [x]}\n", i)
	}
	small, err := Every(".gitlab-ci.yml", "PORT", 0, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	limited := small.(extractor)
	limited.valueLimit = 100
	root := extracttest.Repo(t, map[string]string{".gitlab-ci.yml": b.String()})
	if vs, err := limited.Extract(root); err != nil || len(vs) != 100 {
		t.Errorf("100 values under a limit of 100: %d, %v", len(vs), err)
	}
	b.WriteString("j100: {script: [x]}\n")
	_, err = limited.Extract(extracttest.Repo(t, map[string]string{".gitlab-ci.yml": b.String()}))
	extracttest.WantError(t, err, "more than "+strconv.Itoa(core.MaxValues)+" values")

	two := "variables: {PORT: \"" + strings.Repeat("8", 1000) + "\"}\na: {}\nb: {}\n"
	x, err := Every(".gitlab-ci.yml", "PORT", 2000, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Extract(extracttest.Repo(t, map[string]string{".gitlab-ci.yml": two})); err != nil {
		t.Errorf("values of exactly max_bytes: %v", err)
	}
	_, err = x.Extract(extracttest.Repo(t, map[string]string{".gitlab-ci.yml": two + "c: {}\n"}))
	extracttest.WantError(t, err, "the values of every job hold more than 2000 bytes in all")
}

// A job of a later stage gets the dotenv report of the first job, in the
// file's order, of any stage before its own, read from the stages read
// once, and a job of the first stage gets none. Planted breaks: the first
// job of the last stage before taken, which named q for z; a job's own
// stage counted as earlier, which refused r; the last job of a stage kept
// in place of its first, which named q for z, p2 being after it.
func TestEarlierReportIsTheFirstInTheFile(t *testing.T) {
	rep := "  artifacts: {reports: {dotenv: e.env}}\n  script: [s]\n"
	files := main("stages: [a, b, c]\np:\n  stage: a\n" + rep + "q:\n  stage: b\n" + rep + "p2:\n  stage: a\n" + rep +
		"r:\n  stage: a\n  variables: {TAG: one}\n  script: [s]\nz:\n  stage: c\n  variables: {TAG: one}\n  script: [s]\n")
	extracttest.WantError(t, refused(t, files, "z", "TAG"), `job "z" gets the dotenv report of job "p"`)
	want(t, see(t, files, "r", "TAG"), "one@.gitlab-ci.yml:16")
	// Every reads q first, which sets no TAG but may get one from p's.
	_, err := every(t, files, "TAG")
	extracttest.WantError(t, err, `job "q" gets the dotenv report of job "p"`)
}

// A trigger job counts only when it forwards its variables: with
// forward:yaml_variables false it gives no value, and one that is not a
// boolean, such as the string "false", is refused, as GitLab refuses it.
// A forward: that is not a mapping is refused too. Planted breaks: forward
// not read, which gave 9 for down; the boolean's test dropped, which read
// the string "false" as true; the mapping test dropped, which read
// forward: false as forwarding.
func TestEveryTriggerForwards(t *testing.T) {
	for _, c := range []struct{ trigger, want string }{
		{"{include: c.yml}", `9@.gitlab-ci.yml:2 job "down"`},
		{"{include: c.yml, forward: {yaml_variables: true}}", `9@.gitlab-ci.yml:2 job "down"`},
		{"{include: c.yml, forward: {pipeline_variables: true}}", `9@.gitlab-ci.yml:2 job "down"`},
		{"group/project", `9@.gitlab-ci.yml:2 job "down"`},
		{"{include: c.yml, forward: {yaml_variables: false}}", ""},
		{"{include: c.yml, forward: {yaml_variables: no}}", ""},
	} {
		got, err := every(t, map[string]string{".gitlab-ci.yml": "down:\n  variables: {PORT: \"9\"}\n  trigger: " + c.trigger + "\n"}, "PORT")
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.trigger, got, err, c.want)
		}
	}
	_, err := every(t, map[string]string{".gitlab-ci.yml": "down:\n  variables: {PORT: \"9\"}\n  trigger: {include: c.yml, forward: {yaml_variables: \"false\"}}\n"}, "PORT")
	extracttest.WantError(t, err, "trigger:forward:yaml_variables is true or false, which GitLab requires as a boolean")
	_, err = every(t, map[string]string{".gitlab-ci.yml": "down:\n  variables: {PORT: \"9\"}\n  trigger: {include: c.yml, forward: false}\n"}, "PORT")
	extracttest.WantError(t, err, "trigger:forward is a scalar; errata reads it only as a mapping")
}

// A Build walks the jobs before its own for their dotenv reports, as it
// always has, and reads no later job's report; only Every reads every
// job's, once. Planted break: the reports read once for a Build too, which
// spends the work budget on jobs it did not read before.
func TestBuildReadsNoLaterReport(t *testing.T) {
	data := []byte("deploy:\n  stage: build\n  variables: {TAG: one}\n  script: [x]\nlater:\n  stage: deploy\n  script: [x]\n  artifacts: {reports: {dotenv: e.env}}\n")
	for _, every := range []bool{false, true} {
		l := &loader{read: func(core.Source) ([]byte, error) { return data, nil }, parse: yamlParse, limit: 1 << 20, left: 1 << 20, info: map[*core.Node]nodeInfo{}}
		top, err := l.load(".gitlab-ci.yml", false)
		if err != nil {
			t.Fatal(err)
		}
		c := &config{loader: l, top: top, main: ".gitlab-ci.yml", jobs: map[string]resolvedJob{}, resolving: map[string]bool{},
			refs: map[*core.Node]followed{}, following: map[*core.Node]bool{}, every: every}
		if _, err := c.variable("deploy", "TAG"); err != nil {
			t.Fatal(err)
		}
		if read := c.staged != nil; read != every {
			t.Errorf("every %v: read every job's report: %v", every, read)
		}
	}
}

// ExtractRead counts the bytes of the file and of each file it includes,
// which the env extractor charges to its max_bytes. Planted break: the
// includes not counted.
func TestExtractReadCountsIncludes(t *testing.T) {
	files := map[string]string{".gitlab-ci.yml": "include: ci.yml\nb: {script: [x]}\n", "ci.yml": "a: {script: [x]}\n"}
	x, err := Every(".gitlab-ci.yml", "PORT", 0, yamlParse)
	if err != nil {
		t.Fatal(err)
	}
	_, read, err := x.(extractor).ExtractRead(extracttest.Repo(t, files))
	if want := int64(len(files[".gitlab-ci.yml"]) + len(files["ci.yml"])); err != nil || read != want {
		t.Errorf("read %d, %v; want %d", read, err, want)
	}
}

// Every returns at most core.MaxValues values, unless a test sets its
// limit. Planted break: another figure for no limit.
func TestEveryValueLimitIsMaxValues(t *testing.T) {
	if got := (extractor{}).maxValues(); got != core.MaxValues {
		t.Errorf("no limit set: %d, want %d", got, core.MaxValues)
	}
	if got := (extractor{valueLimit: 5}).maxValues(); got != 5 {
		t.Errorf("a limit of 5: %d", got)
	}
}

// What Every reads of every job's report and stage that no other test
// reaches: a variable of exactly maxName bytes loads; a needs: entry that
// names no job is refused; an earlier job whose report does not read
// refuses a later job, as Build's walk does; and a .post job, the last of
// the stages, is read. Planted breaks: the name's bound as >=; the needs:
// message's test inverted; a report's error not kept for the earlier
// stages; the stages' table one short, which panicked on the .post job.
func TestEveryReachesEachCheck(t *testing.T) {
	if _, err := Every(".gitlab-ci.yml", strings.Repeat("A", maxName), 0, yamlParse); err != nil {
		t.Errorf("a variable of %d bytes: %v", maxName, err)
	}
	_, err := every(t, main("a:\n  needs: [{artifacts: true}]\n  script: [x]\n"), "PORT")
	extracttest.WantError(t, err, "a needs: entry names a job: or a job by a string")
	_, err = every(t, main("a:\n  stage: build\n  script: [x]\n  artifacts: !reference [.nowhere, artifacts]\nb:\n  stage: test\n  variables: {PORT: \"1\"}\n  script: [x]\n"), "PORT")
	if err == nil || !strings.Contains(err.Error(), ".nowhere") {
		t.Errorf("an earlier report that does not read: %v", err)
	}
	got, err := every(t, main("last:\n  stage: .post\n  variables: {PORT: \"1\"}\n  script: [x]\n"), "PORT")
	if err != nil || got != `1@.gitlab-ci.yml:3 job "last"` {
		t.Errorf("a .post job: %q, %v", got, err)
	}
}
