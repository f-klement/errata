// Command casecover adds to a Go coverage profile a block for the expression
// list of each case clause, and the statement of each select arm, whose body
// the tests ran.
//
// Usage: casecover <module root> <profile>
//
// Go's coverage profile has a block for a case clause's body and none for its
// expressions, the text from the case keyword to the colon. Gremlins counts a
// mutant as covered only inside a block a test ran, so it read every mutant in
// a case expression as not covered and never ran it, however the tests ran the
// arm. scripts/mutation.sh runs this on the profile of gremlins' coverage pass,
// before gremlins reads it: a case expression whose clause body is covered
// gets a covered block of its own, and gremlins then runs its mutants and
// judges them killed or lived like any other. A clause whose body no test ran
// gets nothing, so its mutants stay not covered, even when an earlier clause's
// test evaluated its expression on the way past.
//
// It reads the case clauses of expression and type switches and the arms of a
// select, which the profile treats alike. Package level declarations and code
// after a statement's first func literal are outside every block for other
// reasons, and stay so.
//
// Positions are the file's own lines, not those a //line directive gives,
// because the profile counts lines that way too (go test -cover, Go 1.27.2). A
// directive that names another file makes the profile name a file that does
// not exist; that name is skipped, since gremlins mutates only files that do.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: casecover <module root> <profile>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintf(os.Stderr, "casecover: %v\n", err)
		os.Exit(1)
	}
}

// run appends the case blocks for the profile's files to the profile.
func run(root, profile string) error {
	data, err := os.ReadFile(profile) //nolint:gosec // the caller names the profile, as go test's -coverprofile does
	if err != nil {
		return err
	}
	mod, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	lines, err := caseBlocks(data, mod, func(rel string) ([]byte, error) {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			return nil, fmt.Errorf("the profile names %s, outside the module root", rel)
		}
		return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) //nolint:gosec // a local path under the root, checked above
	})
	if err != nil || len(lines) == 0 {
		return err
	}
	f, err := os.OpenFile(profile, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // the profile read above
	if err != nil {
		return err
	}
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// modulePath is the module line of a go.mod file.
func modulePath(gomod string) (string, error) {
	data, err := os.ReadFile(gomod) //nolint:gosec // go.mod of the root the caller names
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	return "", fmt.Errorf("%s has no module line", gomod)
}

// pos is a line and a byte column, both from 1, as a profile gives them.
type pos struct{ line, col int }

func (p pos) before(q pos) bool {
	return p.line < q.line || p.line == q.line && p.col < q.col
}

// blockLine is a profile line: file:startLine.startCol,endLine.endCol statements count.
var blockLine = regexp.MustCompile(`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+) ([0-9]+) ([0-9]+)$`)

// coveredStarts is, for each file the profile names, where each block a test
// ran starts.
func coveredStarts(profile []byte) (map[string][]pos, error) {
	starts := map[string][]pos{}
	sc := bufio.NewScanner(bytes.NewReader(profile))
	sc.Buffer(nil, 1<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if strings.HasPrefix(line, "mode: ") {
				continue
			}
			return nil, errors.New("the profile does not start with a mode line")
		}
		if line == "" {
			continue
		}
		m := blockLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("not a profile line: %q", line)
		}
		if _, ok := starts[m[1]]; !ok {
			starts[m[1]] = nil
		}
		if m[7] == "0" {
			continue
		}
		l, _ := strconv.Atoi(m[2])
		c, _ := strconv.Atoi(m[3])
		starts[m[1]] = append(starts[m[1]], pos{l, c})
	}
	return starts, sc.Err()
}

// caseBlocks is the profile lines to add: one covered block from the case
// keyword to the colon of each case clause whose body block is covered. A
// clause's body block starts after its colon and no later than its first
// statement: Go 1.27 starts it at the first statement, and an empty body's at
// the column after the colon. read gives a file's source by its path relative
// to the module root. Files outside the module, and names in it that no file
// has, are left alone.
func caseBlocks(profile []byte, mod string, read func(rel string) ([]byte, error)) ([]string, error) {
	starts, err := coveredStarts(profile)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(starts))
	for name := range starts {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []string
	for _, name := range names {
		covered := starts[name]
		if len(covered) == 0 {
			continue
		}
		rel, ok := strings.CutPrefix(name, mod+"/")
		if !ok {
			continue
		}
		src, err := read(rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		at := func(p token.Pos) pos {
			q := fset.PositionFor(p, false)
			return pos{q.Line, q.Column}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			kw, colonPos, body, ok := clause(n)
			if !ok {
				return true
			}
			colon := at(colonPos)
			lo := pos{colon.line, colon.col + 1}
			hi := lo
			if len(body) > 0 {
				hi = at(body[0].Pos())
			}
			if ranAny(covered, lo, hi) {
				c := at(kw)
				out = append(out, fmt.Sprintf("%s:%d.%d,%d.%d 0 1", name, c.line, c.col, colon.line, colon.col))
			}
			return true
		})
	}
	return out, nil
}

// clause gives a case clause's or a select arm's case keyword, colon and body;
// false for any other node, and for a default clause, which has no expression.
func clause(n ast.Node) (kw, colon token.Pos, body []ast.Stmt, ok bool) {
	switch c := n.(type) {
	case *ast.CaseClause:
		return c.Case, c.Colon, c.Body, len(c.List) > 0
	case *ast.CommClause:
		return c.Case, c.Colon, c.Body, c.Comm != nil
	}
	return 0, 0, nil, false
}

// ranAny reports whether a covered block starts from lo to hi, both included.
func ranAny(covered []pos, lo, hi pos) bool {
	for _, s := range covered {
		if !s.before(lo) && !hi.before(s) {
			return true
		}
	}
	return false
}
