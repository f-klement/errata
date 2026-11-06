package extract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A reader that marks a template's values (core.TemplateNotLiteral) also
// reads through the block scan (core.TemplateRead or core.TemplateBlocks),
// so a value inside one of the engine's blocks is marked in every such
// reader, not only in those that remembered to (#119's review found three
// that had not). The test reads each reader's source, and holds itself to
// the readers known to mark templates, so it cannot pass on reading none.
// The planted break is TemplateRead left out of jenkins.
func TestTemplateReadersScanBlocks(t *testing.T) {
	dirs, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var marking []string
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "testdata" || d.Name() == "core" {
			continue
		}
		calls := map[string]bool{}
		files, err := filepath.Glob(filepath.Join(d.Name(), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(parsed, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "core" {
						calls[sel.Sel.Name] = true
					}
				}
				return true
			})
		}
		if !calls["TemplateNotLiteral"] {
			continue
		}
		marking = append(marking, d.Name())
		if !calls["TemplateRead"] && !calls["TemplateBlocks"] {
			t.Errorf("%s marks a template's values with core.TemplateNotLiteral and reads through neither core.TemplateRead nor core.TemplateBlocks, so a value inside a template block is literal there", d.Name())
		}
	}
	for _, want := range []string{"envfile", "ini", "jenkins", "properties", "shell"} {
		if !slices.Contains(marking, want) {
			t.Errorf("%s no longer calls core.TemplateNotLiteral, or the test no longer reads it; found %v", want, marking)
		}
	}
}
