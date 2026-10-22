package extract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Every test that calls slowOnly, in any package, is named in SLOW_TESTS,
// and SLOW_TESTS names only such tests: a slow test the Makefile leaves out
// would run nowhere, since make test skips it, and a name the Makefile keeps
// after a rename fails make slow, but only there. It sees only a call to
// slowOnly in the test's own body: a test that skips through a helper that
// calls slowOnly, or skips on its own condition, is not found, so a slow
// test calls slowOnly itself.
func TestEverySlowTestIsInMakeSlow(t *testing.T) {
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	found, err := slowTests("../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 2 {
		t.Fatalf("found %v, want the slow tests of report and extract", found)
	}
	if err := sameSlowTests(slowTestsListed(string(makefile)), found); err != nil {
		t.Error(err)
	}
}

// The planted breaks: a slow test the Makefile leaves out, and a name the
// Makefile keeps that no slow test has.
func TestSameSlowTestsRefuses(t *testing.T) {
	both := []string{"./internal/a:TestX", "./internal/b:TestY"}
	if err := sameSlowTests(both, both); err != nil {
		t.Errorf("the same tests: %v", err)
	}
	extracttest.WantError(t, sameSlowTests(both[:1], both), "./internal/b:TestY calls slowOnly, so make test skips it, and SLOW_TESTS in the Makefile leaves it out: add it there")
	extracttest.WantError(t, sameSlowTests(both, both[1:]), "SLOW_TESTS in the Makefile names ./internal/a:TestX, which is no test that calls slowOnly: rename it there, or drop it")
	listed := slowTestsListed("A := 1\nSLOW_TESTS := ./internal/a:TestX \\\n\t./internal/b:TestY\nslow:\n")
	if !slices.Equal(listed, both) {
		t.Errorf("listed %v, want %v", listed, both)
	}
}

// slowTestsListed is SLOW_TESTS in the Makefile's text, its continuation
// lines joined.
func slowTestsListed(makefile string) []string {
	_, after, ok := strings.Cut(makefile, "\nSLOW_TESTS :=")
	if !ok {
		return nil
	}
	var fields []string
	for line := range strings.Lines(after) {
		line = strings.TrimSpace(line)
		more := strings.HasSuffix(line, `\`)
		fields = append(fields, strings.Fields(strings.TrimSuffix(line, `\`))...)
		if !more {
			break
		}
	}
	return fields
}

// slowTests is every test under root's internal directory whose body calls
// slowOnly, as the Makefile writes one: ./internal/<package>:<test>.
func slowTests(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			if callsSlowOnly(fn.Body) {
				found = append(found, "./"+filepath.ToSlash(rel)+":"+fn.Name.Name)
			}
		}
		return nil
	})
	return found, err
}

func callsSlowOnly(body *ast.BlockStmt) bool {
	calls := false
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "slowOnly" {
				calls = true
			}
		}
		return !calls
	})
	return calls
}

// sameSlowTests refuses a slow test that SLOW_TESTS leaves out, and a name
// in SLOW_TESTS that is no slow test.
func sameSlowTests(listed, found []string) error {
	for _, f := range found {
		if !slices.Contains(listed, f) {
			return fmt.Errorf("%s calls slowOnly, so make test skips it, and SLOW_TESTS in the Makefile leaves it out: add it there", f)
		}
	}
	for _, l := range listed {
		if !slices.Contains(found, l) {
			return fmt.Errorf("SLOW_TESTS in the Makefile names %s, which is no test that calls slowOnly: rename it there, or drop it", l)
		}
	}
	return nil
}
