package extract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// No source of internal/extract outside its test helpers calls a strings
// function with x.Error() as an argument, the form of the text matches
// that #197 replaced: a reader that matched "bytes, over the limit of"
// would have lost its note the day core reworded the message, and no test
// would have failed. A reader asks errors.Is of a sentinel such as
// core.ErrTooLarge. The test refuses only that form: it does not follow
// err.Error() through a variable, nor see ==, a switch, or regexp and
// bytes calls, and it refuses a strings call that only builds a message.
// The shared test helpers compare messages, which is what a test does.
// Planted break: env's errors.Is put back as
// strings.Contains(err.Error(), ...).
func TestNoReaderDecidesByAnErrorsText(t *testing.T) {
	var problems []string
	files := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == "extracttest" || d.Name() == "coretest") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isStringsCall(call) && readsErrorText(call) {
				problems = append(problems, fmt.Sprintf("%s: a strings function reads an error's text; ask errors.Is of a sentinel", fset.Position(call.Pos())))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 50 {
		t.Fatalf("read %d files, want every reader's", files)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// isStringsCall is whether call is strings.F(...).
func isStringsCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "strings"
}

// readsErrorText is whether an argument of call is x.Error().
func readsErrorText(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		inner, ok := arg.(*ast.CallExpr)
		if !ok || len(inner.Args) != 0 {
			continue
		}
		if sel, ok := inner.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" {
			return true
		}
	}
	return false
}
