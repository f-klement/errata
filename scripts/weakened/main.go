// Command weakened prints what a change did to the Go tests it touched that
// could weaken them, one finding a line, for a reviewer to weigh (#260).
//
// Usage: weakened <base> <head>, from the root of the repository, as
// ci/gate.sh runs it from its tree at the sha.
//
// It reads the _test.go files, outside testdata directories, of each package
// the diff from <base> to <head> touches a test file of, at both commits,
// through go/ast, and reports:
//
//   - a Test or Fuzz function go test runs (its name and its one *testing.T
//     or *testing.F parameter) at the base that is gone at the head, unless
//     a test of those packages at the head has the same body;
//   - a table row that is gone: a row named by its map key or by a name,
//     desc, description or title field that the table no longer holds, or
//     fewer unnamed rows than at the base, unless the row's text is in a
//     table at the head;
//   - a call of Skip, Skipf or SkipNow, or of testing.Short, or of a helper
//     of the package (a function that takes a *testing.T, B or F or a
//     testing.TB) that makes such a call itself, whose line, or a line of the
//     init or condition of an if around it, the change added; unless the
//     declaration had as many of the same call under the same if inits and
//     conditions at the base, so a reworded message, a new argument or a
//     condition only split over lines is no finding, and a dropped or
//     rewritten guard is one. A skip in an else is guarded by its if's
//     condition failing, printed `if !(<cond>)`;
//   - a test with fewer assertions (a call of Error, Errorf, Fatal, Fatalf,
//     Fail or FailNow not made through a package) than at the base, counting
//     those of the package's helpers it calls as the base has them, and a
//     helper with fewer assertions than at the base;
//   - a //go:build line added to, or changed in, a test file the base had;
//   - a file that does not parse, at either commit;
//   - when the change also edits a .go file that is not a test, a want value
//     that changed and each file under a testdata directory it edited or
//     deleted. A want value is a variable, or a row's field, keyed or at its
//     place in a struct type the file spells out or declares, whose name
//     starts with want, expect or golden; and a literal that an if compares
//     with, by == or !=, before its block fails the test.
//
// It does not follow a helper into another helper or another package, count
// assertions made through a package such as testify's, read rows that are not
// composite literals, nor see a want computed rather than written out. A
// skip's guard is only the if statements around it: a switch case around it,
// or a variable an if reads (`skip := <cond>; ... if skip`), is not part of
// it, so changing either to one that always holds is no finding. A guard
// rewritten only in its parentheses reads as rewritten.
//
// scripts/changedtests names the tests a change adds or edits, for the gate's
// atbase and shuffle checks; this command does not take that list. A test the
// head no longer has is not on it, nor is a test whose text is unchanged but
// whose helper lost an assertion, and this command needs each test's facts at
// both commits, not only its name.
//
// The spec may ask for any of these, so a finding is a question for the
// reviewer and never a failure: the command exits 0 whatever it finds, 1 when
// git fails, and 2 on a wrong call.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, execGit))
}

// run prints the findings of the change from args[0] to args[1] and returns
// the exit status.
func run(args []string, stdout, stderr io.Writer, git gitRunner) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: weakened <base> <head>")
		return 2
	}
	in, err := load(git, args[0], args[1])
	if err != nil {
		fmt.Fprintln(stderr, "weakened:", err)
		return 1
	}
	for _, f := range check(in) {
		fmt.Fprintln(stdout, f)
	}
	return 0
}
