package gitlabci

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// "the configuration in the .gitlab-ci.yml file takes precedence over the
// included configuration" (include reference), "If parameters overlap, the
// last included file takes precedence", and the merge "is a deep merge,
// where hash maps are merged at any depth" while "you cannot add or modify
// individual items in an array" (Use CI/CD configuration from other files).
// Each value is at the file and line that set it. Planted breaks: the main
// file's own keys left out, which gave "a" for TAG, and merged before its
// includes, which gave "a" too.
func TestIncludeLocalPrecedence(t *testing.T) {
	files := map[string]string{
		".gitlab-ci.yml": "include:\n  - a.yml\n  - local: /ci/b.yml\nvariables:\n  TAG: main\nbuild:\n  script: [main]\n",
		"a.yml":          "variables:\n  TAG: a\n  FROM_A: a\n  OVER: a\nbuild:\n  image: a\n  script: [a1, a2]\n",
		"ci/b.yml":       "variables:\n  OVER: b\n",
	}
	want(t, see(t, files, "build", "TAG"), "main@.gitlab-ci.yml:5")
	want(t, see(t, files, "build", "FROM_A"), "a@a.yml:3")
	want(t, see(t, files, "build", "OVER"), "b@ci/b.yml:2")
	want(t, see(t, files, "build", "image"), "a@a.yml:6")
	want(t, see(t, files, "build", "script"), "main@.gitlab-ci.yml:7")
}

// "If an included file also uses include, that nested include
// configuration is merged first (recursively)": the file that includes it
// overrides it. A single string is "include: 'my-config.yml'". Planted
// break: a nested file merged after the file that includes it, which gave
// "nested".
func TestNestedIncludeMergesFirst(t *testing.T) {
	files := map[string]string{
		".gitlab-ci.yml": "include: 'a.yml'\njob:\n  script: [x]\n",
		"a.yml":          "include: [{local: n.yml}]\nvariables:\n  TAG: a\n",
		"n.yml":          "variables:\n  TAG: nested\n  DEEP: nested\n",
	}
	want(t, see(t, files, "job", "TAG"), "a@a.yml:3")
	want(t, see(t, files, "job", "DEEP"), "nested@n.yml:3")
}

// "The maximum number of nested included files for a pipeline is 150", by
// default; a file that includes itself reaches the limit. An include outside the repository does not load, nor
// one that is a sequence. Planted break: maxIncludes at 151, which read the
// loop to 151 files.
func TestIncludeRefusals(t *testing.T) {
	extracttest.WantError(t, refused(t, main("include: .gitlab-ci.yml\njob:\n  script: [x]\n"), "job", "script"), "includes more than 150 files, GitLab's limit")
	var b strings.Builder
	for i := range 150 {
		fmt.Fprintf(&b, "- f%d.yml\n", i)
	}
	files := map[string]string{".gitlab-ci.yml": "include:\n" + b.String() + "job:\n  script: [x]\n"}
	for i := range 150 {
		files[fmt.Sprintf("f%d.yml", i)] = fmt.Sprintf("variables:\n  V%d: x\n", i)
	}
	want(t, see(t, files, "job", "V149"), "x@f149.yml:2")
	extracttest.WantError(t, refused(t, main("include: ../up.yml\n"), "job", "script"), `include "../up.yml" is outside the repository`)
	extracttest.WantError(t, refused(t, main("include: [[a.yml]]\n"), "job", "script"), "an include is a file or a mapping")
	extracttest.WantError(t, refused(t, main("include: gone.yml\njob:\n  script: [x]\n"), "job", "script"), "read gone.yml")
}

// An include errata cannot read offline (a remote file, another project's,
// a template, a component, a file named by a wildcard or a variable, or one
// whose rules a pipeline decides) could set any key, so every value is
// unknown, naming the first and counting the rest.
func TestIncludeOutOfReach(t *testing.T) {
	for _, c := range []struct{ include, want string }{
		{"https://example.com/ci.yml", "the remote include at .gitlab-ci.yml:1"},
		{"{remote: 'https://example.com/ci.yml'}", "the remote include at .gitlab-ci.yml:1"},
		{"{project: group/p, file: ci.yml}", "the project include at .gitlab-ci.yml:1"},
		{"{template: Auto-DevOps.gitlab-ci.yml}", "the template include at .gitlab-ci.yml:1"},
		{"{component: $CI_SERVER_FQDN/g/c@1.0}", "the component include at .gitlab-ci.yml:1"},
		{"{local: 'configs/*.yml'}", `whose file "configs/*.yml" a wildcard or a variable names`},
		{"{local: '$DIR/a.yml'}", "a wildcard or a variable names"},
		{"{local: a.yml, rules: [{if: $X}]}", "whose rules: a pipeline decides"},
		{"[{template: a.yml}, https://e.com/b.yml]", "the template include at .gitlab-ci.yml:1, and 1 more, which errata cannot read offline"},
		{"{nothing: x}", "the unknown kind of include"},
	} {
		t.Run(c.include, func(t *testing.T) {
			err := refused(t, main("include: "+c.include+"\njob:\n  variables:\n    TAG: x\n"), "job", "TAG")
			extracttest.WantError(t, err, c.want)
			extracttest.WantError(t, err, "the job's values may come from")
		})
	}
}

// A trigger job passes its variables to a downstream pipeline, which
// errata does not read: "CI/CD variables defined in a top-level variables
// section (globally) or in the trigger job are forwarded" (trigger
// reference), and there they rank as pipeline variables.
func TestTriggerJobIsOutOfReach(t *testing.T) {
	files := main("child:\n  variables:\n    TAG: x\n  trigger:\n    include: child.yml\n")
	extracttest.WantError(t, refused(t, files, "child", "TAG"), "job \"child\" is a trigger job")
	want(t, see(t, files, "child", "trigger.include"), "child.yml@.gitlab-ci.yml:5")
}

// A file that declares inputs holds a spec: header document first, and its
// configuration after the ---. Any other second document is refused.
func TestSpecHeader(t *testing.T) {
	want(t, see(t, main("spec:\n  inputs:\n    tag:\n---\njob:\n  image: $[[ inputs.tag ]]\n"), "job", "image"), "$[[ inputs.tag ]]!@.gitlab-ci.yml:6")
	extracttest.WantError(t, refused(t, main("a: 1\n---\nb: 2\n"), "job", "image"), "holds 2 YAML documents")
	extracttest.WantError(t, refused(t, main("- a\n"), "job", "image"), "the configuration is a sequence")
	extracttest.WantError(t, refused(t, map[string]string{".gitlab-ci.yml": "include: e.yml\njob:\n  script: [x]\n", "e.yml": "# nothing\n"}, "job", "script"),
		"e.yml: the included file is empty, which GitLab refuses")
}
