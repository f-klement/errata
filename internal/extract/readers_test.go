package extract

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/f-klement/errata/internal/extract/"

// shared are the packages under internal/extract that are no reader: what the
// readers share, and what their tests share.
var shared = []string{"core", "extracttest", "coretest"}

// Each kind is a reader of its own, and no reader imports another: a reader
// imports core and nothing else of internal/extract, and core imports no
// reader. So a reader's mutant reruns its own tests alone, and a reader can
// become a program of its own (#103).
func TestReadersImportNoOtherReader(t *testing.T) {
	imports := map[string][]string{}
	dirs, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "testdata" {
			continue
		}
		files, err := filepath.Glob(filepath.Join(d.Name(), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, im := range parsed.Imports {
				p, _ := strconv.Unquote(im.Path.Value)
				imports[d.Name()] = append(imports[d.Name()], p)
			}
		}
	}
	// The readers are the packages of the kinds' specs, so a reader that
	// kinds does not list, and a kind whose package is not here, fail.
	readers := map[string]bool{}
	for _, k := range kinds.order {
		readers[path.Base(k.Spec.PkgPath())] = true
	}
	for pkg := range imports {
		if !readers[pkg] && !slices.Contains(shared, pkg) {
			t.Errorf("the package %s is no reader of a kind kinds lists, and not one the readers share", pkg)
		}
	}
	for pkg := range readers {
		if _, ok := imports[pkg]; !ok {
			t.Errorf("kinds lists a kind of the reader %s, which is no package here", pkg)
		}
	}
	for _, p := range readerImports(imports) {
		t.Error(p)
	}
}

// readerImports lists each import of a package of internal/extract by a
// package below it that it may not import: by a reader, any but core; by
// core, a reader.
func readerImports(imports map[string][]string) []string {
	var problems []string
	for pkg, ims := range imports {
		for _, im := range ims {
			other, ok := strings.CutPrefix(im, module)
			if !ok || other == pkg {
				continue
			}
			switch {
			case pkg == "core" && !slices.Contains(shared, other):
				problems = append(problems, fmt.Sprintf("core imports the reader %s; core is what the readers share and imports none", other))
			case !slices.Contains(shared, pkg) && other != "core":
				problems = append(problems, fmt.Sprintf("the reader %s imports %s; a reader imports core and no other package of internal/extract", pkg, other))
			}
		}
	}
	slices.Sort(problems)
	return problems
}

// The planted breaks: a reader that imports another reader, a reader that
// imports a test helper package, and core importing a reader.
func TestReaderImportsRefuses(t *testing.T) {
	got := readerImports(map[string][]string{
		"yaml": {module + "core", module + "json", "fmt"},
		"json": {module + "core", module + "extracttest"},
		"core": {module + "toml"},
		"toml": {module + "core"},
	})
	want := []string{
		"core imports the reader toml; core is what the readers share and imports none",
		"the reader json imports extracttest; a reader imports core and no other package of internal/extract",
		"the reader yaml imports json; a reader imports core and no other package of internal/extract",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
