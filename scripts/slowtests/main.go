// Command slowtests prints the slow tests of the tree, one a line, as make
// slow runs them: ./internal/<package>:<test>, sorted.
//
// Usage: slowtests <module root>
//
// A slow test is a test function whose body calls slowOnly, which skips it
// under -race, -short and gremlins' mutation tag, so make test skips it and
// make slow runs it once, plain (extracttest.SlowOnly says why). The Makefile
// reads this list into SLOW_TESTS, so a new slow test needs no edit there.
//
// It sees a call in the test's own body only. So it fails, and prints no
// list, when slowOnly is called anywhere else, such as in a helper a test
// calls, and when extracttest.SlowOnly is called outside a package's own
// slowOnly: a test that skipped that way would run in neither make test nor
// make slow. Directories named testdata are left out, since go test builds
// none of them.
package main

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
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: slowtests <module root>")
		os.Exit(2)
	}
	found, err := slowTests(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "slowtests:", err)
		os.Exit(1)
	}
	for _, t := range found {
		fmt.Fprintln(os.Stdout, t)
	}
}

// slowTests is every slow test under root's internal directory, sorted, or
// the first call it would miss.
func slowTests(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error { //nolint:gosec // the module root the caller names, as make slow passes it
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			calls, err := slowCalls(fset, fn)
			if err != nil {
				return err
			}
			if calls {
				found = append(found, "./"+filepath.ToSlash(rel)+":"+fn.Name.Name)
			}
		}
		return nil
	})
	slices.Sort(found)
	return found, err
}

// slowCalls reports whether fn is a test whose body calls slowOnly, and
// refuses a call this list would not see: slowOnly called by a function that
// is not a test, and extracttest.SlowOnly called by one that is not slowOnly.
func slowCalls(fset *token.FileSet, fn *ast.FuncDecl) (bool, error) {
	test := fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test")
	calls := false
	var refused error
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || refused != nil {
			return refused == nil
		}
		switch f := c.Fun.(type) {
		case *ast.Ident:
			if f.Name == "slowOnly" {
				if !test {
					refused = fmt.Errorf("%s: %s calls slowOnly, but only a test that calls it in its own body is listed, so make slow would not run the test that reaches it: call slowOnly in the test", fset.Position(c.Pos()), fn.Name.Name)
				}
				calls = true
			}
		case *ast.SelectorExpr:
			if f.Sel.Name == "SlowOnly" && fn.Name.Name != "slowOnly" {
				refused = fmt.Errorf("%s: %s calls %s.SlowOnly, but only a call of the package's own slowOnly is listed, so make slow would not run it: call slowOnly, which calls SlowOnly", fset.Position(c.Pos()), fn.Name.Name, exprName(f.X))
			}
		}
		return refused == nil
	})
	return calls && test, refused
}

func exprName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return "a package"
}
