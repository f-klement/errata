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
