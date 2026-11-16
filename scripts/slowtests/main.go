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
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func main() { os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr)) }

// cli runs the command line args, which follow the program's name, and
// returns the status to exit with: 2 on a misuse, 1 on an error.
func cli(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: slowtests <module root>")
		return 2
	}
	found, err := slowTests(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "slowtests:", err)
		return 1
	}
	for _, t := range found {
		fmt.Fprintln(stdout, t)
	}
	return 0
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
		tests, err := fileSlowTests(root, path)
		found = append(found, tests...)
		return err
	})
	slices.Sort(found)
	return found, err
}

// fileSlowTests is the slow tests of the test file at path under root, or
// the first call it would miss.
func fileSlowTests(root, path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	var found []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		calls, err := slowCalls(fset, fn)
		if err != nil {
			return found, err
		}
		if calls {
			found = append(found, "./"+filepath.ToSlash(rel)+":"+fn.Name.Name)
		}
	}
	return found, nil
}

// slowCalls reports whether fn is a test whose body calls slowOnly, and
// refuses a call this list would not see: slowOnly called by a function that
// is not a test, and extracttest.SlowOnly called by one that is not slowOnly.
func slowCalls(fset *token.FileSet, fn *ast.FuncDecl) (bool, error) {
	v := &slowVisit{fset: fset, fn: fn, test: fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test")}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if ok && v.refused == nil {
			v.call(c)
		}
		return v.refused == nil
	})
	return v.calls && v.test, v.refused
}

// slowVisit is slowCalls' walk of fn's body: whether fn is a test, whether
// the body calls slowOnly, and the first call refused.
type slowVisit struct {
	fset        *token.FileSet
	fn          *ast.FuncDecl
	test, calls bool
	refused     error
}

// call reads one call of the body.
func (v *slowVisit) call(c *ast.CallExpr) {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		if f.Name != "slowOnly" {
			return
		}
		if !v.test {
			v.refused = fmt.Errorf("%s: %s calls slowOnly, but only a test that calls it in its own body is listed, so make slow would not run the test that reaches it: call slowOnly in the test", v.fset.Position(c.Pos()), v.fn.Name.Name)
		}
		v.calls = true
	case *ast.SelectorExpr:
		if f.Sel.Name == "SlowOnly" && v.fn.Name.Name != "slowOnly" {
			v.refused = fmt.Errorf("%s: %s calls %s.SlowOnly, but only a call of the package's own slowOnly is listed, so make slow would not run it: call slowOnly, which calls SlowOnly", v.fset.Position(c.Pos()), v.fn.Name.Name, exprName(f.X))
		}
	}
}

func exprName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return "a package"
}
