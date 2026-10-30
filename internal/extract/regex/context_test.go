package regex

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// provisioning is node-provisioning.sh's shape: commands that change the
// cluster, one that only asks Argo to refresh, one that applies the k3s
// upgrade Plans by hand, and one in a comment.
const provisioning = `#!/bin/sh
# kubectl apply -f old.yaml was the old way
kubectl -n argocd annotate app x argocd.argoproj.io/refresh=hard
kubectl apply -f infra/k3s-upgrade/plans.yaml
kubectl apply -f staging.yaml
helm upgrade --install x y
`

// A match without what the line must not hold is kept, and one with it
// dropped, as provisioning-writes-git-not-the-cluster asks: without on
// the match's own line is a lookahead RE2 lacks. Planted break: Build
// leaving the context off the search.
func TestContextWithoutOnTheLine(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"p.sh": provisioning})
	s := Spec{File: "p.sh", Pattern: `\b(kubectl\s+(?:-n\s+\S+\s+)?(?:apply|annotate|delete)|helm\s+(?:upgrade|install))\b`,
		Context: &core.Context{Without: `^\s*#|argocd\.argoproj\.io/refresh|infra/k3s-upgrade/`}}
	if got := extracttest.Texts(extracttest.MustRun(t, Build, s, root)); got != "kubectl apply@5 helm upgrade@6" {
		t.Errorf("got %q", got)
	}
}

// A file set keeps the context for each file, and each file's lines are
// its own: a line before the first of one file is none of the file before
// it. Planted break: the set's searches built without the context.
func TestContextOverFiles(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{"a.txt": "ok\nv=1\n", "b.txt": "v=2\nok\n"})
	s := Spec{Files: []string{"*.txt"}, Pattern: `v=(\d)`, Context: &core.Context{Before: 1, With: `^ok$`}}
	if got := extracttest.Texts(extracttest.MustRun(t, Build, s, root)); got != "1@2" {
		t.Errorf("got %q", got)
	}
}

// A pattern that references values keeps, for every value, only the
// matches with the context. Planted break: the Each given no context.
func TestContextWithAReference(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"renovate.json": "--install a x\n// managed\n--install b x\n--install a y\n"})
	d := mustReference(t, Spec{File: "renovate.json", Pattern: `--install ((?{charts})) `,
		Context: &core.Context{Before: 1, With: `managed`}})
	vs, err := d.ExtractWith(t.Context(), root, map[string][]check.Value{"charts": {value("a", "s", 1), value("b", "s", 2)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := extracttest.Texts(vs); got != "b@3" {
		t.Errorf("got %q", got)
	}
}

// A search for records keeps only the matches with the context, as one
// for group 1 does: each record is built from a match as it is found, so
// it asks the context of that match. Planted break: records building a
// record of every match.
func TestContextKeepsRecords(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"p.sh": provisioning})
	s := Spec{File: "p.sh", Pattern: `kubectl (?P<verb>apply|annotate) -f (?P<file>\S+)`, As: "record",
		Context: &core.Context{Without: `^\s*#|infra/k3s-upgrade/`}}
	vs := extracttest.MustRun(t, Build, s, root)
	if len(vs) != 1 || vs[0].At.Line != 5 || vs[0].Data == nil {
		t.Fatalf("got %q, want the record of line 5 alone", extracttest.Texts(vs))
	}
	if f := vs[0].Data.Field("file"); f == nil || f.Text != "staging.yaml" {
		t.Errorf("file field %+v, want staging.yaml", f)
	}
}

// A search for records counts every match toward the limits on values and
// bytes before the context drops any, as one for group 1 does (ADR 0012),
// so a pattern that matches too often is refused however few records the
// context keeps. Planted break: records asking the context before they
// count the match.
func TestContextRecordsCountEveryMatch(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"many": strings.Repeat("a\n", core.MaxValues+1)})
	s := Spec{File: "many", Pattern: `(?P<v>a)`, As: "record", Context: &core.Context{With: `^ok$`}}
	e, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Extract(t.Context(), root)
	extracttest.WantError(t, err, fmt.Sprintf("many: the records the pattern matches hold more than %d nodes in all", core.MaxValues))
}

// A context that does not load leaves no extractor, for a pattern with a
// reference or without one. Planted break: the context checked only for
// a pattern without a reference.
func TestContextRefusedAtBuild(t *testing.T) {
	for _, pattern := range []string{`(v)`, `((?{v}))`} {
		e, err := Build(Spec{File: "f", Pattern: pattern, Context: &core.Context{After: 2}})
		extracttest.WantError(t, err, "regex: context needs with, without or both")
		if e != nil {
			t.Errorf("%s: a refusal returns the extractor %#v", pattern, e)
		}
	}
}
