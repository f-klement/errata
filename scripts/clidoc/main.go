// Command clidoc writes docs/cli.md, the command line reference, from the
// help errata prints (cli.Reference), or checks the file against it.
//
// Usage: clidoc FILE          write the reference to FILE
//
//	clidoc --check FILE  exit 1 when FILE differs from the reference
//
// make cli-doc writes the file in the commit a release tags, and make
// cli-doc-check is the tag pipeline's check:cli-doc job, which refuses a
// release whose committed file differs from what its binary prints. A push
// does not check it: between releases the file describes the last release.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/f-klement/errata/internal/cli"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, cli.Reference))
}

// run is clidoc on args, with the reference as an argument so the tests
// give it one of their own.
func run(args []string, stdout, stderr io.Writer, reference func() ([]byte, error)) int {
	check := len(args) == 2 && args[0] == "--check"
	if !check && (len(args) != 1 || strings.HasPrefix(args[0], "-")) {
		fmt.Fprintln(stderr, "usage: clidoc [--check] FILE")
		return 2
	}
	file := args[len(args)-1]
	want, err := reference()
	if err != nil {
		fmt.Fprintln(stderr, "clidoc:", err)
		return 1
	}
	if !check {
		if err := os.WriteFile(file, want, 0o644); err != nil { //nolint:gosec // the file make cli-doc names, docs/cli.md, readable by all as every file in the tree
			fmt.Fprintln(stderr, "clidoc:", err)
			return 1
		}
		fmt.Fprintln(stdout, "clidoc: wrote", file)
		return 0
	}
	got, err := os.ReadFile(file) //nolint:gosec // the file make cli-doc-check names
	if err != nil || !bytes.Equal(got, want) {
		reason := "differs from the help this commit's errata prints"
		if err != nil {
			reason = "cannot be read (" + err.Error() + ")"
		}
		fmt.Fprintf(stderr, "clidoc: REFUSED: %s %s. Run make cli-doc and commit %s. "+
			"On a tag pipeline, delete the tag and set it again on the commit that holds the new %s.\n",
			file, reason, file, file)
		return 1
	}
	fmt.Fprintln(stdout, "clidoc:", file, "is the help this commit's errata prints")
	return 0
}
