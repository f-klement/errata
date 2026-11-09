package main

// drop removes from a package's _test.go files each top-level declaration
// that holds a build error, so the rest of its tests can build.
//
//	changedtests drop <dir>    reads the errors of `go test -c -gcflags=-e
//	                           <dir>`, run from the root, on stdin
//
// ci/changed-tests.sh's atbase check builds a package's tests at the base,
// where a test that calls API the change adds does not compile. #261 leaves
// out that test, not its package, so the check builds, drops what the errors
// name, and builds again, up to three times: a dropped helper's callers and an
// import left unused are errors of the next build.
//
// An error inside a function, method, var, const or type declaration drops
// that whole declaration, its doc comment with it, and an error in an import
// spec drops the spec. In the same round drop also drops everything of dir's
// _test.go files that names a dropped name, transitively (spread, in
// reach.go): a dropped helper's callers, a dropped type's methods and users.
// A dropped method drops its receiver type with it, since a type that lost a
// method can still compile and act otherwise: another method promoted in its
// place, an interface no longer met at run time. Only the imports left unused
// wait for the next build.
//
// drop prints the name of each declaration it dropped, a method's as
// ".<type>.<name>", and rewrites the files in place. It exits 3, dropping
// nothing, when an error lies anywhere else: outside dir's _test.go files, in
// a package clause, or in a line it cannot read as an error; and when it
// would drop TestMain or init, since the tests would then run at the base
// without the setup the change gave them. Such a package cannot be built by
// dropping tests, and the check leaves it out.

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func dropRun(dir string, stdin io.Reader, stdout, stderr io.Writer) int {
	names, err := drop(dir, stdin)
	if errors.Is(err, errCannotDrop) {
		fmt.Fprintln(stderr, "changedtests:", err)
		return 3
	}
	if err != nil {
		fmt.Fprintln(stderr, "changedtests:", err)
		return 1
	}
	for _, name := range names {
		fmt.Fprintln(stdout, name)
	}
	return 0
}

var errCannotDrop = errors.New("cannot drop")

// errorLine is a compiler error: the file, its line and column (0 when it
// names none), and the message.
var errorLine = regexp.MustCompile(`^(\S+\.go):(\d+)(?::(\d+))?: `)

// drop reads the build errors and removes from dir's _test.go files what
// they name and what names that, returning the names removed in the order of
// the files and the declarations.
func drop(dir string, errs io.Reader) ([]string, error) {
	at, err := errorsByFile(dir, errs)
	if err != nil {
		return nil, err
	}
	files, err := parseDir(dir)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, tf := range files {
		if err := tf.seed(at[tf.path], names); err != nil {
			return nil, err
		}
		delete(at, tf.path)
	}
	for path := range at {
		return nil, fmt.Errorf("%w: %s is no _test.go file of %s", errCannotDrop, path, dir)
	}
	return cutAll(files, names)
}

// errorsByFile is each error's place, by file, all of them in dir's
// _test.go files, or errCannotDrop.
func errorsByFile(dir string, errs io.Reader) (map[string][][2]int, error) {
	at := map[string][][2]int{}
	sc := bufio.NewScanner(errs)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "# ") || line == "" {
			continue
		}
		m := errorLine.FindStringSubmatch(line)
		if m == nil || filepath.Dir(filepath.Clean(m[1])) != filepath.Clean(dir) || !strings.HasSuffix(m[1], "_test.go") {
			return nil, fmt.Errorf("%w: %s is no error in a _test.go file of %s", errCannotDrop, line, dir)
		}
		ln, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		f := filepath.Clean(m[1])
		at[f] = append(at[f], [2]int{ln, col})
	}
	if len(at) == 0 {
		return nil, fmt.Errorf("%w: no error in the build's output", errCannotDrop)
	}
	return at, sc.Err()
}

// testFile is a _test.go file of the directory being dropped from: its
// source, its syntax, its declarations, and the import specs to cut.
type testFile struct {
	path    string
	src     []byte
	file    *ast.File
	tok     *token.File
	decls   []decl
	imports [][2]int
}

// parseDir reads every _test.go file of dir, sorted by name.
func parseDir(dir string) ([]*testFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	var files []*testFile
	for _, p := range paths {
		src, err := os.ReadFile(p) //nolint:gosec // a test file of the directory the caller names
		if err != nil {
			return nil, err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, &testFile{path: filepath.Clean(p), src: src, file: f, tok: fset.File(f.Pos()), decls: topLevel(f, src)})
	}
	return files, nil
}

// seed adds to names the key of each declaration holding one of the places,
// and to tf.imports each import spec holding one.
func (tf *testFile) seed(places [][2]int, names map[string]bool) error {
	for _, p := range places {
		off := offset(tf.tok, p)
		if spec, ok := importAt(tf.file, off); ok {
			tf.imports = append(tf.imports, spec)
			continue
		}
		i := slices.IndexFunc(tf.decls, func(d decl) bool { return off >= d.from && off < d.to })
		if i < 0 {
			return fmt.Errorf("%w: %s:%d:%d is in no declaration", errCannotDrop, tf.path, p[0], p[1])
		}
		names[tf.decls[i].key()] = true
	}
	return nil
}

// importAt is the range to cut for an error at off in an import declaration:
// the spec holding it, or the whole declaration when it has one spec.
func importAt(f *ast.File, off int) ([2]int, bool) {
	pos := func(p token.Pos) int { return int(p - f.FileStart) }
	g := importDecl(f, off, pos)
	if g == nil {
		return [2]int{}, false
	}
	if len(g.Specs) == 1 {
		return [2]int{docStart(g.Doc, pos(g.Pos()), pos), pos(g.End())}, true
	}
	i := slices.IndexFunc(g.Specs, func(s ast.Spec) bool { return off >= pos(s.Pos()) && off < pos(s.End()) })
	if i < 0 {
		return [2]int{}, false
	}
	return [2]int{pos(g.Specs[i].Pos()), pos(g.Specs[i].End())}, true
}

// importDecl is the import declaration holding off, or nil.
func importDecl(f *ast.File, off int, pos func(token.Pos) int) *ast.GenDecl {
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT && off >= pos(g.Pos()) && off < pos(g.End()) {
			return g
		}
	}
	return nil
}

// cutAll spreads names over the declarations of every file, widening each to
// the whole block it shares a range with, refuses to drop TestMain or init,
// and cuts the rest from the files.
func cutAll(files []*testFile, names map[string]bool) ([]string, error) {
	all, owner, blocks := flatten(files)
	marked := spread(all, names, false)
	for widen(all, blocks, marked, names) {
		marked = spread(all, names, false)
	}
	var dropped []string
	cuts := map[*testFile][][2]int{}
	for i, d := range all {
		if !marked[i] {
			continue
		}
		if d.setup {
			return nil, fmt.Errorf("%w: it would drop %s, and the tests would run at the base without their setup", errCannotDrop, d.name)
		}
		dropped = append(dropped, d.name)
		cuts[owner[i]] = append(cuts[owner[i]], [2]int{d.from, d.to})
	}
	for _, tf := range files {
		if err := tf.cut(append(cuts[tf], tf.imports...)); err != nil {
			return nil, err
		}
	}
	return dropped, nil
}

// flatten is the declarations of every file in one list, the file of each,
// and for each the indexes of the declarations of its block: the names of
// one var, const or type block share its range and are cut together.
func flatten(files []*testFile) ([]decl, []*testFile, [][]int) {
	var all []decl
	var owner []*testFile
	byRange := map[[2]int][]int{}
	var ranges [][2]int
	for n, tf := range files {
		for _, d := range tf.decls {
			r := [2]int{n, d.from}
			byRange[r] = append(byRange[r], len(all))
			ranges = append(ranges, r)
			all = append(all, d)
			owner = append(owner, tf)
		}
	}
	blocks := make([][]int, len(all))
	for i, r := range ranges {
		blocks[i] = byRange[r]
	}
	return all, owner, blocks
}

// widen adds to names the key of each declaration in a marked declaration's
// block, and reports whether it added one.
func widen(all []decl, blocks [][]int, marked []bool, names map[string]bool) bool {
	grew := false
	for i, m := range marked {
		if !m {
			continue
		}
		for _, j := range blocks[i] {
			if !names[all[j].key()] {
				names[all[j].key()], grew = true, true
			}
		}
	}
	return grew
}

// cut removes the ranges from the file, each once, and writes it back when
// there are any.
func (tf *testFile) cut(ranges [][2]int) error {
	if len(ranges) == 0 {
		return nil
	}
	slices.SortFunc(ranges, func(a, b [2]int) int { return cmp.Compare(b[0], a[0]) })
	src := tf.src
	for _, r := range slices.Compact(ranges) {
		src = append(src[:r[0]:r[0]], src[r[1]:]...)
	}
	return os.WriteFile(tf.path, src, 0o644) //nolint:gosec // a test file of the base's tree, rewritten as it was
}

// offset is the byte offset of a line and column, the line's start when the
// column is 0 or past the line.
func offset(tf *token.File, p [2]int) int {
	if p[0] < 1 || p[0] > tf.LineCount() {
		return -1
	}
	start := tf.Offset(tf.LineStart(p[0]))
	end := tf.Size()
	if p[0] < tf.LineCount() {
		end = tf.Offset(tf.LineStart(p[0] + 1))
	}
	if at := start + p[1] - 1; p[1] > 0 && at < end-1 {
		return at
	}
	return start
}
