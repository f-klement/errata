package cli

import (
	"context"
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// The scripts in testdata/script are the API tests: each one runs the errata
// binary as a user or CI would and asserts its output and exit code. The
// exit codes and output formats are the contract, so these tests guard it.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"errata": func() { os.Exit(Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) },
	})
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{Dir: "testdata/script"})
}
