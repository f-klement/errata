package regex

import (
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Build returns the search, and a refusal names the regex kind and leaves no
// extractor; core's tests hold the search itself. The planted break for the
// second is returning the empty search with the error, which is an
// extractor that is not nil.
func TestBuild(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f": "v=1\nv=2\n"})
	if got := extracttest.Texts(extracttest.MustRun(t, Build, Spec{File: "f", Pattern: `v=(\d)`}, root)); got != "1@1 2@2" {
		t.Errorf("got %q", got)
	}
	e, err := Build(Spec{File: "f"})
	extracttest.WantError(t, err, "regex: pattern is required")
	if e != nil {
		t.Errorf("a refusal returns the extractor %#v", e)
	}
}

// files reads each file a glob names with the one pattern, each value with
// its own file, and refuses file beside files. Planted break: the pattern
// of the set's reader not the spec's.
func TestBuildFiles(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{"a/f.txt": "v=1\n", "b/c/f.txt": "v=2\nv=3\n", "g.md": "v=4\n"})
	vs := extracttest.MustRun(t, Build, Spec{Files: []string{"**/f.txt"}, Pattern: `v=(\d)`}, root)
	if got := extracttest.Texts(vs); got != "1@1 2@1 3@2" || vs[0].At.File != "a/f.txt" || vs[2].At.File != "b/c/f.txt" {
		t.Errorf("got %q at %v", got, vs)
	}
	_, err := Build(Spec{File: "g.md", Files: []string{"*.md"}, Pattern: `(v)`})
	extracttest.WantError(t, err, "regex: file and files are both set")
	_, err = Build(Spec{Files: []string{"*.md"}, Pattern: `v`})
	extracttest.WantError(t, err, "regex: pattern \"v\" has no capture group")
}
