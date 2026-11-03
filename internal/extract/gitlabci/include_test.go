package gitlabci

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
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
// a template, a component, a file named by a variable, wildcard or not, or
// one whose rules a pipeline decides, wildcard or not) could set any key, so every value is
// unknown, naming the first and counting the rest.
func TestIncludeOutOfReach(t *testing.T) {
	for _, c := range []struct{ include, want string }{
		{"https://example.com/ci.yml", "the remote include at .gitlab-ci.yml:1"},
		{"{remote: 'https://example.com/ci.yml'}", "the remote include at .gitlab-ci.yml:1"},
		{"{project: group/p, file: ci.yml}", "the project include at .gitlab-ci.yml:1"},
		{"{template: Auto-DevOps.gitlab-ci.yml}", "the template include at .gitlab-ci.yml:1"},
		{"{component: $CI_SERVER_FQDN/g/c@1.0}", "the component include at .gitlab-ci.yml:1"},
		{"{local: '$DIR/a.yml'}", `whose file "$DIR/a.yml" a variable names`},
		{"{local: '$DIR/*.yml'}", `whose file "$DIR/*.yml" a variable names`},
		{"{local: 'ci/*.yml', rules: [{if: $X}]}", "whose rules: a pipeline decides"},
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

// GitLab's wildcard becomes core globs that together match what its regular
// expression matches: **/ as zero or more directories, a core ** segment
// at the start of a name and nothing or a crossing inside one, any other
// ** within a name or across slashes, * within one name, ?, [ and \
// literal, stars in a run read ** first. Planted breaks: **/ read as any
// other **, which wrote configs/**/*.yml as two globs that need a
// directory; ** written as one glob, which lost what crosses a slash; the
// star after a star kept, which made a ** segment of a run of stars.
func TestWildcardGlobs(t *testing.T) {
	for name, want := range map[string]string{
		"configs/*.yml":    "configs/*.yml",
		"configs/**.yml":   "configs/*.yml configs/*/**/*.yml",
		"configs/**/*.yml": "configs/**/*.yml",
		"**/*.md":          "**/*.md",
		"a**/b.yml":        "a*/**/b.yml ab.yml",
		"a?[b]\\*.yml":     `a\?\[b]\\*.yml`,
		"a/***.yml":        "a/*.yml a/*/**/*.yml",
		"***/b.yml":        "*/**/b.yml *b.yml",
	} {
		got, err := wildcardGlobs(name)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%s: %q, %v; want %s", name, got, err, want)
		}
	}
	if _, err := wildcardGlobs(strings.Repeat("x**", 6) + ".yml"); err != nil {
		t.Errorf("6 **: %v", err)
	}
	_, err := wildcardGlobs(strings.Repeat("x**", 7) + ".yml")
	extracttest.WantError(t, err, "holds more than 6 **")
}

// GitLab's own spec cases for search_files_by_wildcard_path, over a
// repository of README.md, files/markdown/ruby-style-guide.md,
// files/ruby/popen.rb and files/ruby/version_info.rb: **/*.md matches the
// root's file too, files/**/*.md and files/**.md the files below files,
// */*.rb none, since no .rb is one directory down, and **/*.rb both. The
// files are included in git's order. Planted break: **/ read as any other
// **, which left README.md out of **/*.md.
func TestIncludeLocalWildcardAsGitLabsSpec(t *testing.T) {
	files := map[string]string{
		"README.md":                          "",
		"files/markdown/ruby-style-guide.md": "",
		"files/ruby/popen.rb":                "",
		"files/ruby/version_info.rb":         "",
	}
	root := extracttest.Tracked(t, files)
	for pattern, want := range map[string]string{
		"**/*.md":       "README.md files/markdown/ruby-style-guide.md",
		"files/**/*.md": "files/markdown/ruby-style-guide.md",
		"files/**.md":   "files/markdown/ruby-style-guide.md",
		"*/*.rb":        "",
		"**/*.rb":       "files/ruby/popen.rb files/ruby/version_info.rb",
	} {
		l := &loader{list: core.NewTracked(t.Context(), root).List}
		got, err := l.wildcard(".gitlab-ci.yml", pattern)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("%s: %q, %v; want %q", pattern, got, err, want)
		}
	}
}

// wildcardFiles is a tracked repository whose main file includes include,
// and whose configs each set A to their own name, so the value of A is the
// last file included, and set a variable of their own, so each file's is
// there only when it is included. configs/u.yml is not tracked.
func wildcardFiles(t *testing.T, include string) string {
	t.Helper()
	files := map[string]string{
		".gitlab-ci.yml":            "include: '" + include + "'\njob:\n  script: [x]\n",
		"configs/a.yml":             "variables:\n  A: a\n  IN_A: x\n",
		"configs/sub/b.yml":         "variables:\n  A: b\n  IN_B: x\n",
		"configs/sub/deep/c.yml":    "variables:\n  A: c\n  IN_C: x\n",
		"other/d.yml":               "variables:\n  A: d\n  IN_D: x\n",
		"configs/sub/notes.yml.txt": "x",
	}
	root := extracttest.Tracked(t, files)
	if err := os.WriteFile(filepath.Join(root, "configs", "u.yml"), []byte("variables:\n  IN_U: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// The GitLab documentation's examples of include:local with wildcards
// (Use CI/CD configuration from other files, Use include:local with
// wildcard file paths): configs/*.yml "adds all .yml files in the configs
// folder", configs/**.yml all of them "in configs/ and any subfolder in
// it", and configs/**/*.yml all of them "only in subfolders of configs".
// That last is narrower than GitLab's code, which reads **/ as zero or
// more directories and so includes configs/a.yml too; errata follows the
// code (TestIncludeLocalWildcardAsGitLabsSpec). The files are included in
// git's order and never an untracked one.
// Planted break: a wildcard sent to the unreachable list again, which
// made each unknown.
func TestIncludeLocalWildcard(t *testing.T) {
	for _, c := range []struct {
		include, last string
		in            []string
	}{
		{"configs/*.yml", "a", []string{"IN_A"}},
		{"/configs/**.yml", "c", []string{"IN_A", "IN_B", "IN_C"}},
		{"configs/**/*.yml", "c", []string{"IN_A", "IN_B", "IN_C"}},
	} {
		root := wildcardFiles(t, c.include)
		var got []string
		for _, v := range []string{"IN_A", "IN_B", "IN_C", "IN_D", "IN_U"} {
			vs, err := extracttest.Run(t, build, spec("job", v), root)
			if none := (*check.NoValue)(nil); errors.As(err, &none) {
				continue
			}
			if err != nil {
				t.Fatalf("%s: %s: %v", c.include, v, err)
			}
			if len(vs) > 0 {
				got = append(got, v)
			}
		}
		if !slices.Equal(got, c.in) {
			t.Errorf("%s: includes the files of %q, want %q", c.include, got, c.in)
		}
		vs, err := extracttest.Run(t, build, spec("job", "A"), root)
		if err != nil || len(vs) != 1 || vs[0].Text != c.last {
			t.Errorf("%s: A is %v, %v; want %s, the last file in git's order", c.include, vs, err, c.last)
		}
	}
}

// A wildcard include reads under the reader's bounds: each file it
// matches counts against GitLab's 150, a file it matches that is no .yml or
// .yaml is refused as GitLab refuses it, one that matches nothing includes
// nothing, and a glob over a sparse checkout is refused, naming it.
// Planted break: the extension check on the name only, which read
// configs/x.txt.
func TestIncludeLocalWildcardBounds(t *testing.T) {
	files := map[string]string{".gitlab-ci.yml": "include: 'configs/*'\njob:\n  script: [x]\n", "configs/x.txt": "x"}
	_, err := extracttest.Run(t, build, spec("job", "script"), extracttest.Tracked(t, files))
	extracttest.WantError(t, err, `include "configs/x.txt" is not a .yml or .yaml file, which GitLab refuses`)

	files = map[string]string{".gitlab-ci.yml": "include: 'configs/*.yml'\njob:\n  script: [x]\n"}
	for i := range 151 {
		files[fmt.Sprintf("configs/f%03d.yml", i)] = "variables:\n  V: x\n"
	}
	_, err = extracttest.Run(t, build, spec("job", "script"), extracttest.Tracked(t, files))
	extracttest.WantError(t, err, "includes more than 150 files, GitLab's limit")

	files = map[string]string{".gitlab-ci.yml": "include: 'none/*.yml'\njob:\n  script: [x]\n"}
	vs, err := extracttest.Run(t, build, spec("job", "script"), extracttest.Tracked(t, files))
	if err != nil || len(vs) != 1 {
		t.Errorf("a wildcard that matches nothing: %v, %v", vs, err)
	}

	files = map[string]string{".gitlab-ci.yml": "include: 'ci/*.yml'\njob:\n  script: [x]\n", "ci/a.yml": "variables:\n  V: x\n"}
	for _, index := range []bool{false, true} {
		_, err = extracttest.Run(t, build, spec("job", "script"), extracttest.Sparse(t, files, index))
		extracttest.WantError(t, err, `include "ci/*.yml" matches files errata cannot list: include: files lists the glob "ci/*.yml", which`)
		extracttest.WantError(t, err, "sparse checkout")
	}
}

// A wildcard leaves out the file that includes it, as GitLab's expansion
// does ("Prevent self-inclusion"), nested and in the main file, so a
// pipeline split into ci/*.yml with one of them including the rest reads.
// Planted break: the including file kept, which looped to the 150 limit.
func TestAWildcardLeavesOutTheIncludingFile(t *testing.T) {
	files := map[string]string{
		".gitlab-ci.yml": "include: ci/main.yml\njob:\n  script: [x]\n",
		"ci/main.yml":    "include: 'ci/*.yml'\nvariables:\n  MAIN: x\n",
		"ci/a.yml":       "variables:\n  A: a\n",
	}
	got, err := extracttest.Run(t, build, spec("job", "A"), extracttest.Tracked(t, files))
	if err != nil || values(got) != "a@ci/a.yml:2" {
		t.Errorf("nested: %v, %v", got, err)
	}
	files = map[string]string{
		".gitlab-ci.yml": "include: '*.yml'\njob:\n  script: [x]\n",
		"a.yml":          "variables:\n  A: a\n",
	}
	got, err = extracttest.Run(t, build, spec("job", "A"), extracttest.Tracked(t, files))
	if err != nil || values(got) != "a@a.yml:2" {
		t.Errorf("the main file: %v, %v", got, err)
	}
}

// GitLab strips only the leading slashes of a wildcard name, so one with a
// . or .. name or an empty one matches no path, and includes none, where
// core's cleaning would read configs/a.yml. Planted break: the name
// cleaned, which included configs/a.yml for each.
func TestAWildcardThatIsNoCleanPathIncludesNone(t *testing.T) {
	for _, include := range []string{"./configs/*.yml", "configs//*.yml", "x/../configs/*.yml"} {
		files := map[string]string{
			".gitlab-ci.yml": "include: '" + include + "'\njob:\n  script: [x]\n",
			"configs/a.yml":  "variables:\n  A: a\n",
		}
		got, err := extracttest.Run(t, build, spec("job", "A"), extracttest.Tracked(t, files))
		if none := (*check.NoValue)(nil); !errors.As(err, &none) {
			t.Errorf("%s: %v, %v; want no value", include, got, err)
		}
	}
}
