// Command casecover adds to a Go coverage profile a block for the expression
// list of each case clause, and the statement of each select arm, whose body
// the tests ran, and for the value of each package level const and var of a
// package the tests ran.
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
// select, which the profile treats alike.
//
// The profile has no block for a package level declaration either, so
// gremlins read a mutant in a const or var's value as not covered, however
// the tests pinned it. A test binary that ran any block of a package ran that
// package's var initializers before its first test, and has its constants
// compiled in, so casecover adds a covered block for the values of every
// package level const and var in each file of such a package. A func literal
// in a value is cut out of the block, so its body keeps the blocks go test
// gives it, covered only where it ran. Test files are never named by the
// profile, nor read. A file that holds no function has no block, so the
// profile does not name it; casecover reads such a file from the package's
// directory when the tests' build holds it for certain: its name matches this
// system and architecture, it has no build constraint, since the tags the
// tests were built with are not known here, and it does not import "C", since
// whether cgo was on is not known either. Generated files are read like any
// other, since their values are compiled in as well. Code after a statement's
// first func literal is still outside every block, and stays so.
//
// Positions are the file's own lines, not those a //line directive gives,
// because the profile counts lines that way too (go test -cover, Go 1.27.2). A
// directive that names another file makes the profile name that file. When no
// file under the root has the name, casecover skips it, since gremlins mutates
// only files that exist; when one does, it is the second shape below.
//
// It fails rather than add nothing when the root is not the tree the tests ran
// in: when the profile names files and none is under the root's module, and
// when no in-module name that a test ran has a file under the root.
//
// Three shapes it does not judge. No file that gremlins mutates uses the
// first two, and the third shows only where a package's tests leave code out:
//
//   - A clause entered only through the fallthrough at the end of the clause
//     before it. Its body runs without its case expression being evaluated,
//     so casecover credits the expression, and a mutant in it reads as lived
//     where no test reached it.
//   - A //line directive that names a file which does exist under the module.
//     The profile takes the file name from the directive and keeps the
//     original line numbers, so casecover reads the named file at those lines
//     and may credit a case clause in it that no test ran. Gremlins already
//     misplaces its mutants after such a directive.
//   - A const whose uses no test ran, in a package some test ran. A const
//     never runs, since go build folds it into each use, so casecover covers
//     it with the rest of the package. Its mutant then reads as lived, not
//     as not covered: the record says a test reaches it and asserts nothing,
//     where the truth is that no test reaches it. The fix is a test that runs
//     a use of the const, not another assertion.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() { os.Exit(cli(os.Args[1:], os.Stderr)) }

// cli runs the command line args, which follow the program's name, and
// returns the status to exit with: 2 on a misuse, 1 on an error.
func cli(args []string, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: casecover <module root> <profile>")
		return 2
	}
	if err := run(args[0], args[1]); err != nil {
		fmt.Fprintf(stderr, "casecover: %v\n", err)
		return 1
	}
	return 0
}

// run appends the blocks for the profile's files to the profile.
func run(root, profile string) error {
	data, err := os.ReadFile(profile) //nolint:gosec // the caller names the profile, as go test's -coverprofile does
	if err != nil {
		return err
	}
	mod, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	lines, err := addedBlocks(data, mod, os.DirFS(root))
	var wrong notThisTree
	if errors.As(err, &wrong) {
		return fmt.Errorf("%s is not the tree the tests ran in: %w", root, err)
	}
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

// notThisTree is addedBlocks' refusal of a profile that shows the module root
// is not the tree the tests ran in. run adds the root to it.
type notThisTree string

func (e notThisTree) Error() string { return string(e) }

// addedBlocks is the profile lines to add, read from the module's source in
// fsys, rooted at the module root: a covered block from the case keyword to
// the colon of each case clause whose body block is covered, and one for the
// value expressions of each package level const and var of every package a
// test ran a block of. A clause's body block starts after its colon and no
// later than its first statement: Go 1.27 starts it at the first statement,
// and an empty body's at the column after the colon. Files outside the module,
// and names in it that no file has, are left alone. It fails with notThisTree
// when the profile names files and none is under mod, and when it read names
// and found a file for none. It reads only the files of packages a test ran a
// block of.
func addedBlocks(profile []byte, mod string, fsys fs.FS) ([]string, error) {
	starts, err := coveredStarts(profile)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(starts))
	for name := range starts {
		names = append(names, name)
	}
	sort.Strings(names)
	ran := ranDirs(starts, mod)
	var out []string
	var n fileCount
	for _, name := range names {
		blocks, err := n.file(name, mod, starts[name], ran, fsys)
		if err != nil {
			return nil, err
		}
		out = append(out, blocks...)
	}
	if err := n.refusal(len(names), mod); err != nil {
		return nil, err
	}
	for _, dir := range sortedKeys(ran) {
		blocks, err := unlistedBlocks(fsys, dir, mod, starts)
		if err != nil {
			return nil, err
		}
		out = append(out, blocks...)
	}
	return out, nil
}

// ranDirs is the directories, relative to the module root, of the packages
// the profile shows a test ran a block of. A test binary that ran any block
// of a package ran that package's var initializers too, and holds its
// constants.
func ranDirs(starts map[string][]pos, mod string) map[string]bool {
	ran := map[string]bool{}
	for name, covered := range starts {
		if rel, ok := strings.CutPrefix(name, mod+"/"); ok && len(covered) > 0 {
			ran[path.Dir(rel)] = true
		}
	}
	return ran
}

// sortedKeys is the keys of set in order.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fileCount counts the profile's names addedBlocks went through: those in the
// module, those of them a test ran a block of, and those of these that no file
// has.
type fileCount struct{ inModule, named, skipped int }

// file is the blocks to add for the profile's file name, whose covered blocks
// start at covered, in a package whose directory ran holds when a test ran a
// block of it: none for a file outside mod, one of a package no test ran a
// block of, or one fsys has no file for.
func (n *fileCount) file(name, mod string, covered []pos, ran map[string]bool, fsys fs.FS) ([]string, error) {
	rel, ok := strings.CutPrefix(name, mod+"/")
	if !ok {
		return nil, nil
	}
	n.inModule++
	if !ran[path.Dir(rel)] {
		return nil, nil
	}
	if len(covered) > 0 {
		n.named++
	}
	if !fs.ValidPath(rel) {
		return nil, fmt.Errorf("the profile names %s, outside the module root", rel)
	}
	src, err := fs.ReadFile(fsys, rel)
	if errors.Is(err, fs.ErrNotExist) {
		n.skipped += min(len(covered), 1)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file, fset, err := parse(rel, src, 0)
	if err != nil {
		return nil, err
	}
	return append(clauseBlocks(name, fset, file, covered), declBlocks(name, fset, file)...), nil
}

// parse parses src, the file at rel, with mode added to the parse's own.
func parse(rel string, src []byte, mode parser.Mode) (*ast.File, *token.FileSet, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, mode|parser.SkipObjectResolution)
	return file, fset, err
}

// at is where p is in the file as written, a //line directive notwithstanding.
func at(fset *token.FileSet, p token.Pos) pos {
	q := fset.PositionFor(p, false)
	return pos{q.Line, q.Column}
}

// clauseBlocks is the profile lines for file, the source of the profile's
// file name: one per case clause whose body block is covered.
func clauseBlocks(name string, fset *token.FileSet, file *ast.File, covered []pos) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		kw, colonPos, body, ok := clause(n)
		if !ok {
			return true
		}
		colon := at(fset, colonPos)
		lo := pos{colon.line, colon.col + 1}
		hi := lo
		if len(body) > 0 {
			hi = at(fset, body[0].Pos())
		}
		if ranAny(covered, lo, hi) {
			c := at(fset, kw)
			out = append(out, fmt.Sprintf("%s:%d.%d,%d.%d 0 1", name, c.line, c.col, colon.line, colon.col))
		}
		return true
	})
	return out
}

// declBlocks is the profile lines for the value expressions of file's package
// level consts and vars, file the source of the profile's file name: one from
// the expression's start to its end, cut around the body of each func literal
// in it, which has blocks of its own that say whether it ran.
func declBlocks(name string, fset *token.FileSet, file *ast.File) []string {
	var out []string
	for _, d := range file.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST && g.Tok != token.VAR {
			continue
		}
		for _, s := range g.Specs {
			for _, v := range s.(*ast.ValueSpec).Values {
				out = append(out, valueBlocks(name, fset, v)...)
			}
		}
	}
	return out
}

// valueBlocks is the blocks of the value expression e: e from its start to
// its end, less the body of each func literal it holds, braces included.
func valueBlocks(name string, fset *token.FileSet, e ast.Expr) []string {
	var out []string
	from := e.Pos()
	ast.Inspect(e, func(n ast.Node) bool {
		f, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		out = appendBlock(out, name, fset, from, f.Body.Lbrace)
		from = f.Body.Rbrace + 1
		return false
	})
	return appendBlock(out, name, fset, from, e.End())
}

// appendBlock appends to out a covered block from lo up to hi, which it ends
// at as go test ends a block, at the column after the last; none when the span
// is empty.
func appendBlock(out []string, name string, fset *token.FileSet, lo, hi token.Pos) []string {
	if lo >= hi {
		return out
	}
	a, b := at(fset, lo), at(fset, hi)
	return append(out, fmt.Sprintf("%s:%d.%d,%d.%d 0 1", name, a.line, a.col, b.line, b.col))
}

// unlistedBlocks is the declaration blocks of the Go files in dir, a package a
// test ran a block of, that the profile does not name: those that hold no
// function, of which go test writes no block. A file is read when the build
// of the tests holds it for certain: not a test, its name matching this
// system and architecture, and no build constraint in it, since the tags the
// tests were built with are not known here. A dir fsys does not have, as a
// //line directive naming another file gives, has none.
func unlistedBlocks(fsys fs.FS, dir, mod string, starts map[string][]pos) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ctx := build.Default
	ctx.JoinPath = path.Join
	ctx.OpenFile = func(p string) (io.ReadCloser, error) { return fsys.Open(p) }
	var out []string
	for _, e := range entries {
		rel := path.Join(dir, e.Name())
		if _, listed := starts[mod+"/"+rel]; listed || !goSource(ctx, dir, e) {
			continue
		}
		blocks, err := unconstrainedBlocks(fsys, rel, mod+"/"+rel)
		if err != nil {
			return nil, err
		}
		out = append(out, blocks...)
	}
	return out, nil
}

// goSource reports whether e, an entry of dir, is a Go file that is not a
// test and whose name ctx's system and architecture build.
func goSource(ctx build.Context, dir string, e fs.DirEntry) bool {
	n := e.Name()
	if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
		return false
	}
	ok, err := ctx.MatchFile(dir, n)
	return ok && err == nil
}

// unconstrainedBlocks is the declaration blocks of the file at rel, the
// profile's name for it name, or none when a build constraint holds it or it
// imports "C". go/build matches a cgo file's name even with cgo off, and
// whether the tests' build had cgo is not known here, so such a file stays
// not covered.
func unconstrainedBlocks(fsys fs.FS, rel, name string) ([]string, error) {
	src, err := fs.ReadFile(fsys, rel)
	if err != nil {
		return nil, err
	}
	head, _, err := parse(rel, src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if constrained(head) || importsC(head) {
		return nil, nil
	}
	file, fset, err := parse(rel, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	return declBlocks(name, fset, file), nil
}

// constrained reports whether file has a //go:build or // +build line above
// its package clause.
func constrained(file *ast.File) bool {
	for _, g := range file.Comments {
		if g.Pos() > file.Package {
			break
		}
		for _, c := range g.List {
			if constraint.IsGoBuild(c.Text) || constraint.IsPlusBuild(c.Text) {
				return true
			}
		}
	}
	return false
}

// importsC reports whether file imports "C", which makes it a cgo file.
func importsC(file *ast.File) bool {
	for _, i := range file.Imports {
		if i.Path.Value == `"C"` {
			return true
		}
	}
	return false
}

// refusal is notThisTree when the counts show the root is not the tree the
// tests ran in, of a profile that names nNames files; nil otherwise.
func (n fileCount) refusal(nNames int, mod string) error {
	if nNames > 0 && n.inModule == 0 {
		return notThisTree(fmt.Sprintf("the profile names %s and none is under %s, the root's module", files(nNames), mod))
	}
	if n.named > 0 && n.skipped == n.named {
		return notThisTree(fmt.Sprintf("the profile names %s under %s that the tests ran, and none is under the root", files(n.named), mod))
	}
	return nil
}

// files counts n files in words, so a count of one reads "1 file".
func files(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
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
