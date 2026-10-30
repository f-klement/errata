package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A glob of a real run leaves the fixtures of witnesses out, at the root
// and in any directory .errata, and a glob on a witness matches the
// fixture's tracked files with its path cut off, leaving out an untracked
// file of the fixture, the real tree's files and a fixture nested in it.
// Planted breaks: the real run's list without the DeleteFunc; the witness
// given the whole real index; the cut prefix without its slash, which
// matched a sibling fixture named a-b beside a.
func TestTrackedFilesOfAWitness(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"conf/a.yaml":                           "",
		".errata/witness/a/conf/a.yaml":         "",
		".errata/witness/a/.errata/witness/b/x": "",
		".errata/witness/a-b/conf/c.yaml":       "",
		"sub/.errata/witness/c/conf/d.yaml":     "",
		".errata/other.yaml":                    "",
	})
	if err := os.WriteFile(filepath.Join(root, ".errata/witness/a/conf/untracked.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := trackedFiles(t.Context(), root)
	if want := ".errata/other.yaml conf/a.yaml"; err != nil || strings.Join(got, " ") != want {
		t.Errorf("a real run lists %q, %v, want %q", got, err, want)
	}
	fixture := filepath.Join(root, ".errata", "witness", "a")
	got, err = trackedFiles(check.OnWitness(t.Context(), root), fixture)
	if want := "conf/a.yaml"; err != nil || strings.Join(got, " ") != want {
		t.Errorf("a witness lists %q, %v, want %q", got, err, want)
	}
}

// A witness whose root is not inside the real root is refused, not read
// as a list of every file. The planted break: the IsLocal test dropped,
// which cut ../ paths from nothing and listed none.
func TestTrackedFilesOfAWitnessOutside(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{"a": ""})
	_, err := trackedFiles(check.OnWitness(t.Context(), root), t.TempDir())
	extracttest.WantError(t, err, "is not inside the repository root")
}

// Only trackedFiles lists git's index, so every glob of a real run leaves
// the fixtures of witnesses out and every glob on a witness reads the cut
// index: no other function of the package calls readIndex. The planted
// break: Files.List calling readIndex(root) itself.
func TestOnlyTrackedFilesReadsTheIndex(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	callers := map[string]bool{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "readIndex" {
						callers[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	if len(callers) != 1 || !callers["trackedFiles"] {
		t.Errorf("readIndex is called by %v, want trackedFiles alone", slices.Sorted(maps.Keys(callers)))
	}
}
