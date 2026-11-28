package plugin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/plugin/wire"
)

// errataReads are the variables errata's code reads besides wire.ErrataEnv,
// by file and argument as the code writes them, and why a plugin may name
// each. A read the test finds that is in neither fails it: a new variable
// errata reads is a credential or setting of its own until it is listed
// here with its reason.
var errataReads = map[string]string{
	"internal/cli/plugin.go os.Getenv(SourceEnv)":             "SourceEnv is wire.SourceEnv",
	"internal/cli/plugin.go os.Getenv(plugin.TokenEnv)":       "TokenEnv is wire.TokenEnv",
	"internal/engine/width.go os.Getenv(\"GOMAXPROCS\")":      "a Go runtime variable, which every request gets",
	"internal/extract/compose/stage.go os.Getenv(\"TMPDIR\")": "the temporary directory, named in a message when it is relative; no credential",
	"internal/treecopy/treecopy.go os.LookupEnv(name)":        "git's steering variables, refused when set, never read for a value",
}

// Every variable errata's code reads is in wire.ErrataEnv, which env may
// not name, or in errataReads with the reason a plugin may name it; and
// docs/plugin-protocol.md lists each of wire.ErrataEnv. Planted break: a
// read of GITHUB_TOKEN written as a literal again.
func TestEveryVariableErrataReadsIsListed(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() && d.Name() == "testdata" {
				return orSkip(err)
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			for _, read := range envReads(t, path) {
				found[filepath.ToSlash(rel)+" "+read] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for read := range found {
		if _, ok := errataReads[read]; !ok {
			t.Errorf("%s reads a variable neither wire.ErrataEnv nor errataReads lists: add it to wire.ErrataEnv if it is errata's own credential or setting, else to errataReads with the reason a plugin may name it", read)
		}
	}
	for read := range errataReads {
		if !found[read] {
			t.Errorf("errataReads lists %s, which the code no longer reads", read)
		}
	}
	spec, err := os.ReadFile(filepath.Join(root, "docs", "plugin-protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range wire.ErrataEnv {
		if !strings.Contains(string(spec), "`"+name+"`") {
			t.Errorf("docs/plugin-protocol.md does not list %s, which env may not name", name)
		}
	}
}

// orSkip is err, or for a testdata directory fs.SkipDir.
func orSkip(err error) error {
	if err != nil {
		return err
	}
	return fs.SkipDir
}

// envReads are the calls of os.Getenv and os.LookupEnv in the file at
// path, each written as the source writes it.
func envReads(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == "os" && (sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv") {
			out = append(out, string(src[fset.Position(call.Pos()).Offset:fset.Position(call.End()).Offset]))
		}
		return true
	})
	return out
}
