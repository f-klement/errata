package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/config"
	"github.com/f-klement/errata/internal/extract"
)

// The scripts in testdata/script are the API tests: each one runs the errata
// binary as a user or CI would and asserts its output and exit code. The
// exit codes and output formats are the contract, so these tests guard it.
//
// errata-computed is errata with a fake reader in front of every extractor:
// a value holding $( or ${ is marked not literal, as a shell or CI reader
// marks a command substitution or an expression. No extractor errata has yet
// returns such a value, so the scripts run the not literal paths with it.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"errata": func() { os.Exit(Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) },
		"errata-computed": func() {
			os.Exit(execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr, computedLoad))
		},
	})
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{Dir: "testdata/script"})
}

// computedLoad is config.Load with each extractor behind computedReader.
func computedLoad(root, path string) (config.Config, error) {
	return config.LoadWith(root, path, func(s extract.Spec) (check.Extractor, check.Description, error) {
		x, d, err := config.BuiltIn(s)
		if err != nil {
			return nil, d, err
		}
		d.NotLiteral = true
		return computedReader{x}, d, nil
	})
}

// computedReader marks each value of the extractor it wraps that holds $(
// or ${ as not literal, which computedLoad describes it as returning, so a
// rule may list it in skip_not_literal.
type computedReader struct{ check.Extractor }

func (r computedReader) ExtractWith(ctx context.Context, root string, values map[string][]check.Value) ([]check.Value, error) {
	return r.marked(r.Extractor.ExtractWith(ctx, root, values))
}

func (r computedReader) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return r.marked(r.Extractor.Extract(ctx, root))
}

// marked is values with each that holds $( or ${ marked not literal.
func (computedReader) marked(values []check.Value, err error) ([]check.Value, error) {
	for i, v := range values {
		if strings.Contains(v.Text, "$(") || strings.Contains(v.Text, "${") {
			values[i].NotLiteral = true
		}
	}
	return values, err
}
