package main

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// fn is a function's score and where it is.
type fn struct {
	key            string // namespace, a space, name
	where          string // file:line, from the module root
	comp           int
	stmts, covered int
}

// cov is the share of the function's statements the tests ran; a function
// with none is covered whole.
func (f fn) cov() float64 {
	if f.stmts == 0 {
		return 1
	}
	return float64(f.covered) / float64(f.stmts)
}

// crap is the function's CRAP score.
func (f fn) crap() float64 {
	c, u := float64(f.comp), 1-f.cov()
	return c*c*u*u*u + c
}

// scoreTree scores every function of the root that the profile and the
// -require directories put in scope, or the -only directories alone, keyed by
// fn.key, and gives the import path of each package it scored.
func scoreTree(root, profileName string, require, only []string) (map[string]fn, []string, error) {
	module, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, nil, err
	}
	prof, err := readProfileFile(profileName, module)
	if err != nil {
		return nil, nil, err
	}
	if err := checkFiles(root, prof); err != nil {
		return nil, nil, err
	}
	dirs, err := pickDirs(root, prof, require, only)
	if err != nil {
		return nil, nil, err
	}
	fns := map[string]fn{}
	pkgs := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if err := scoreDir(root, module, d, prof, fns); err != nil {
			return nil, nil, err
		}
		pkgs = append(pkgs, importPath(module, d))
	}
	return fns, pkgs, nil
}

// pickDirs is the directories to score: the -only ones when there are any,
// and otherwise those of scoredDirs.
func pickDirs(root string, prof profile, require, only []string) ([]string, error) {
	if len(only) > 0 {
		return onlyDirs(root, only)
	}
	return scoredDirs(root, prof, require)
}

// onlyDirs is the -only directories, each cleaned and from the root, sorted.
// It refuses one that holds no Go file other than a test.
func onlyDirs(root string, only []string) ([]string, error) {
	set := map[string]bool{}
	for _, d := range only {
		d = path.Clean(filepath.ToSlash(d))
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return !e.IsDir() && sourceFile(e.Name()) }) {
			return nil, fmt.Errorf("-only %s holds no Go package", d)
		}
		set[d] = true
	}
	return sortedKeys(set), nil
}

// modulePath is the module line of a go.mod file.
func modulePath(gomod string) (string, error) {
	data, err := os.ReadFile(gomod)
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

// checkFiles refuses a profile that names a file the root lacks.
func checkFiles(root string, prof profile) error {
	for rel := range prof {
		local := filepath.FromSlash(rel)
		if !filepath.IsLocal(local) {
			return fmt.Errorf("the profile names %s, outside the module root", rel)
		}
		st, err := os.Stat(filepath.Join(root, local))
		if err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("the profile names %s, which the tree at %s lacks: the profile is of another tree", rel, root)
		}
	}
	return nil
}

// scoredDirs is each directory the profile names and each package directory
// under a -require directory, from the module root, sorted.
func scoredDirs(root string, prof profile, require []string) ([]string, error) {
	set := map[string]bool{}
	for rel := range prof {
		set[path.Dir(rel)] = true
	}
	for _, r := range require {
		found, err := packageDirs(root, r)
		if err != nil {
			return nil, err
		}
		for _, d := range found {
			set[d] = true
		}
	}
	return sortedKeys(set), nil
}

// sortedKeys is the members of a set, sorted.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// packageDirs is each directory under dir that holds a Go file other than a
// test, skipping what the go command skips: testdata and names that start
// with a dot or an underscore. It refuses a dir that holds none.
func packageDirs(root, dir string) ([]string, error) {
	set := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), collect(root, set))
	if err == nil && len(set) == 0 {
		err = fmt.Errorf("-require %s holds no Go package", dir)
	}
	return sortedKeys(set), err
}

// collect is the walk that puts in set, from the root, each directory that
// holds a Go file other than a test.
func collect(root string, set map[string]bool) fs.WalkDirFunc {
	return func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && skippedDir(d.Name()) {
			return filepath.SkipDir
		}
		if !d.IsDir() && sourceFile(d.Name()) {
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			set[filepath.ToSlash(rel)] = true
		}
		return nil
	}
}

// skippedDir is a directory name the go command does not look in.
func skippedDir(name string) bool {
	return name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// sourceFile is a Go file name other than a test.
func sourceFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// helperDir is a package that only tests import, named like httptest, which
// the total of `make cover` leaves out.
func helperDir(dir string) bool {
	return strings.HasSuffix(path.Base(dir), "test")
}

// scoreDir scores each function of the directory's files into fns.
func scoreDir(root, module, dir string, prof profile, fns map[string]fn) error {
	if helperDir(dir) {
		return nil
	}
	abs := filepath.Join(root, filepath.FromSlash(dir))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return err
	}
	pkg := importPath(module, dir)
	for _, e := range entries {
		if e.IsDir() || !sourceFile(e.Name()) {
			continue
		}
		if err := scoreFile(abs, pkg, path.Join(dir, e.Name()), prof[path.Join(dir, e.Name())], fns); err != nil {
			return err
		}
	}
	return nil
}

// importPath is the import path of a directory of the module.
func importPath(module, dir string) string {
	if dir == "." {
		return module
	}
	return module + "/" + dir
}

// scoreFile scores each function of one file into fns.
func scoreFile(abs, pkg, rel string, blocks map[span]block, fns map[string]fn) error {
	fset, file, err := readSource(abs, path.Base(rel))
	if err != nil || file == nil {
		return err
	}
	for _, fd := range funcs(file) {
		f, err := score(fset, fd, pkg, rel, blocks)
		if err != nil {
			return err
		}
		keep(fns, f)
	}
	return nil
}

// readSource parses a file of the directory abs, or gives no file when a plain
// build leaves it out or a generator wrote it.
func readSource(abs, name string) (*token.FileSet, *ast.File, error) {
	if ok, err := build.Default.MatchFile(abs, name); err != nil || !ok {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(abs, name), nil, parser.ParseComments)
	if err != nil || ast.IsGenerated(file) {
		return nil, nil, err
	}
	return fset, file, nil
}

// funcs is each function and method of a file that has a body.
func funcs(file *ast.File) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			out = append(out, fd)
		}
	}
	return out
}

// keep puts f in fns. Two functions with one key, such as two init
// functions of a package, keep the worse score.
func keep(fns map[string]fn, f fn) {
	if had, ok := fns[f.key]; !ok || f.crap() > had.crap() {
		fns[f.key] = f
	}
}

// score is one function's complexity and coverage. It refuses a function
// with statements and no block in the profile.
func score(fset *token.FileSet, fd *ast.FuncDecl, pkg, rel string, blocks map[span]block) (fn, error) {
	line := fset.PositionFor(fd.Pos(), false).Line
	f := fn{key: key(pkg, fd), where: fmt.Sprintf("%s:%d", rel, line), comp: complexity(fd)}
	body := span{at(fset, fd.Body.Lbrace), at(fset, fd.End())}
	found := false
	for s, b := range blocks {
		if s.start.before(body.start) || body.end.before(s.end) {
			continue
		}
		found = true
		f.stmts += b.stmts
		if b.covered {
			f.covered += b.stmts
		}
	}
	if !found && len(fd.Body.List) > 0 {
		return f, fmt.Errorf("%s at %s has statements and no block in the profile: the run did not instrument its package", f.key, f.where)
	}
	return f, nil
}

// at is a position as the profile counts it: the file's own lines, not those
// a //line directive gives.
func at(fset *token.FileSet, p token.Pos) pos {
	q := fset.PositionFor(p, false)
	return pos{q.Line, q.Column}
}

// key is how crapper keys a Go function: the package's import path and the
// name, or for a method the import path, a dot and the receiver's type name,
// then the name.
func key(pkg string, fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return pkg + " " + fd.Name.Name
	}
	return pkg + "." + receiver(fd.Recv.List[0].Type) + " " + fd.Name.Name
}

// receiver is the type name of a receiver: T for T, (T), *T, T[P] and
// *T[P, Q]. As crapper takes the first type identifier of the receiver, it
// takes the first identifier: the type's name comes before its parameters.
func receiver(t ast.Expr) string {
	name := ""
	ast.Inspect(t, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && name == "" {
			name = id.Name
		}
		return name == ""
	})
	return name
}

// complexity is 1 plus each decision in the function, its closures' included.
func complexity(fd *ast.FuncDecl) int {
	n := 1
	ast.Inspect(fd, func(x ast.Node) bool {
		if decision(x) {
			n++
		}
		return true
	})
	return n
}

// decision is a node crapper counts for Go: if, for, range, each case clause
// of a switch, a type switch or a select (default included), && and ||.
func decision(x ast.Node) bool {
	switch x := x.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CaseClause, *ast.CommClause:
		return true
	case *ast.BinaryExpr:
		return x.Op == token.LAND || x.Op == token.LOR
	}
	return false
}
