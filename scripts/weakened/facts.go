package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// position is a line of a file, at the base or the head.
type position struct {
	file string
	line int
}

// test is a Test or Fuzz function go test runs: where it starts, its body as
// go/printer prints it (without comments), how many assertions its body makes
// itself, and the plain function names it calls, once a call, so the
// assertions of a helper it calls can be added.
type test struct {
	pos     position
	body    string
	asserts int
	calls   []string
}

// helper is a function of a test file, not a test, that takes a *testing.T,
// B or F or a testing.TB: its assertions, and whether it can skip the test
// that calls it.
type helper struct {
	pos     position
	asserts int
	skips   bool
}

// call is a call of a plain function name, in decl, under guard (the
// conditions of the if statements around it), and the lines of the call and
// of those conditions.
type call struct {
	pos   position
	decl  string
	name  string
	guard string
	lines []int
}

// want is a want value: a variable or a row's field named want, expect or
// golden, or a literal an if compares with before it fails the test. what
// says which, and text is the value's text.
type want struct {
	pos  position
	what string
	decl string
	text string
}

// skip is a call that can skip a test, in decl, under guard, and the lines
// of the call and of the conditions in guard.
type skip struct {
	pos   position
	decl  string
	call  string
	guard string
	lines []int
}

// build is a file's //go:build line.
type build struct {
	pos  position
	text string
}

// facts is what the test files of one side hold. A test, a helper, a table
// and a want value are keyed by their package directory and declaration, not
// their file, so one moved between files of a package is the same one.
type facts struct {
	files   map[string]bool
	tests   map[string]test
	helpers map[string]helper
	bodies  map[string]bool
	decls   map[string]bool
	tables  map[string]*table
	bySeq   map[string]*table
	rows    map[string]bool
	wants   map[string]want
	skips   []skip
	calls   []call
	builds  map[string]build
	broken  []finding
}

// collect parses each file of a side, in the order of their names. A file
// that does not parse is a finding of its own, at side, and the others are
// read.
func collect(files map[string][]byte, side string) *facts {
	f := &facts{files: map[string]bool{}, tests: map[string]test{}, helpers: map[string]helper{}, bodies: map[string]bool{},
		decls: map[string]bool{}, tables: map[string]*table{}, bySeq: map[string]*table{}, rows: map[string]bool{},
		wants: map[string]want{}, builds: map[string]build{}}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		f.files[name] = true
		if err := f.add(name, files[name]); err != nil {
			f.broken = append(f.broken, finding{pos: position{file: name}, base: side == "base",
				text: fmt.Sprintf("does not parse at the %s, so the check read nothing in it: %s", side, clip(err.Error()))})
		}
	}
	return f
}

// reader parses one file into its side's facts.
type reader struct {
	f       *facts
	fset    *token.FileSet
	file    string
	decl    string
	count   map[string]int
	imports map[string]bool
	structs map[string][]string
}

func (f *facts) add(file string, src []byte) error {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return err
	}
	r := &reader{f: f, fset: fset, file: file, imports: imports(parsed), structs: structs(parsed)}
	r.build(parsed)
	for _, d := range parsed.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			r.funcDecl(d)
		case *ast.GenDecl:
			r.genDecl(d)
		}
	}
	return nil
}

// imports is the names a file's imports go by.
func imports(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, spec := range file.Imports {
		p, _ := strconv.Unquote(spec.Path.Value)
		name := path.Base(p)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = true
	}
	return names
}

// structs is the field names, in order, of each struct type a file declares,
// at the top or in a function.
func structs(file *ast.File) map[string][]string {
	types := map[string][]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		if spec, ok := n.(*ast.TypeSpec); ok {
			if st, ok := spec.Type.(*ast.StructType); ok {
				types[spec.Name.Name] = fieldNames(st)
			}
		}
		return true
	})
	return types
}

// fieldNames is a struct's fields in order; an embedded one goes by its type.
func fieldNames(st *ast.StructType) []string {
	var names []string
	for _, field := range st.Fields.List {
		for _, n := range field.Names {
			names = append(names, n.Name)
		}
		if len(field.Names) == 0 {
			names = append(names, embedded(field.Type))
		}
	}
	return names
}

func embedded(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return embedded(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// build records the file's //go:build line, which comes before its package
// clause.
func (r *reader) build(file *ast.File) {
	for _, g := range file.Comments {
		for _, c := range g.List {
			if c.Pos() < file.Package && constraint.IsGoBuild(c.Text) {
				r.f.builds[r.file] = build{pos: r.pos(c.Pos()), text: c.Text}
			}
		}
	}
}

// key is name's key in the facts: the file's package directory and name.
func (r *reader) key(name string) string {
	return path.Dir(r.file) + " " + name
}

func (r *reader) pos(p token.Pos) position {
	return position{file: r.file, line: r.fset.Position(p).Line}
}

// print is a node's text, without its comments.
func (r *reader) print(n ast.Node) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, r.fset, n)
	return b.String()
}

func (r *reader) funcDecl(d *ast.FuncDecl) {
	name := d.Name.Name
	if d.Recv != nil {
		name = r.print(d.Recv.List[0].Type) + "." + name
	}
	r.enter(name)
	if d.Body == nil {
		return
	}
	skips := len(r.f.skips)
	r.walk(d)
	asserts, calls := r.asserts(d.Body)
	switch {
	case d.Recv == nil && r.isTest(d):
		body := r.print(d.Body)
		r.f.tests[r.key(name)] = test{pos: r.pos(d.Pos()), body: body, asserts: asserts, calls: calls}
		r.f.bodies[body] = true
	case d.Recv == nil && r.takesTB(d.Type):
		r.f.helpers[r.key(name)] = helper{pos: r.pos(d.Pos()), asserts: asserts, skips: len(r.f.skips) > skips}
	}
}

// isTest is go test's rule: Test or Fuzz, then nothing or a rune that is not
// lower case, and one parameter, a *testing.T or a *testing.F.
func (r *reader) isTest(d *ast.FuncDecl) bool {
	want := ""
	for prefix, param := range map[string]string{"Test": "*testing.T", "Fuzz": "*testing.F"} {
		rest, ok := strings.CutPrefix(d.Name.Name, prefix)
		if first, _ := utf8.DecodeRuneInString(rest); ok && !unicode.IsLower(first) {
			want = param
		}
	}
	params := d.Type.Params.List
	return want != "" && len(params) == 1 && len(params[0].Names) <= 1 && r.print(params[0].Type) == want
}

// takesTB: a parameter through which a function can fail or skip a test.
func (r *reader) takesTB(ft *ast.FuncType) bool {
	for _, p := range ft.Params.List {
		switch r.print(p.Type) {
		case "*testing.T", "*testing.B", "*testing.F", "testing.TB":
			return true
		}
	}
	return false
}

// genDecl walks each var or const of a declaration under its own names.
func (r *reader) genDecl(d *ast.GenDecl) {
	for _, s := range d.Specs {
		if v, ok := s.(*ast.ValueSpec); ok {
			names := make([]string, len(v.Names))
			for i, n := range v.Names {
				names[i] = n.Name
			}
			r.enter(strings.Join(names, ","))
			r.walk(v)
		}
	}
}

// enter starts the declaration whose tables and wants come next.
func (r *reader) enter(decl string) {
	r.decl = decl
	r.count = map[string]int{}
	r.f.decls[r.key(decl)] = true
}

// next numbers the things of one kind in the declaration, from 1.
func (r *reader) next(kind string) int {
	r.count[kind]++
	return r.count[kind]
}

// walk records the tables, want values, skips and calls under n.
func (r *reader) walk(n ast.Node) {
	var stack []ast.Node
	ast.Inspect(n, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch n := n.(type) {
		case *ast.CompositeLit:
			r.table(n)
		case *ast.AssignStmt:
			r.assigned(n.Lhs, n.Rhs)
		case *ast.ValueSpec:
			r.assigned(identExprs(n.Names), n.Values)
		case *ast.IfStmt:
			r.inline(n)
		case *ast.CallExpr:
			r.call(n, stack)
		}
		return true
	})
}

// guard is the conditions of the if statements whose block or else holds the
// innermost node of stack, outermost first, as printed: what must hold for a
// call there to run. lines is every line those conditions span.
func (r *reader) guard(stack []ast.Node) (conds string, lines []int) {
	var all []string
	for i, n := range stack[:len(stack)-1] {
		s, ok := n.(*ast.IfStmt)
		if !ok || (stack[i+1] != s.Body && stack[i+1] != s.Else) {
			continue
		}
		text, spans := r.condition(s, stack[i+1] == s.Else)
		all = append(all, text)
		lines = append(lines, spans...)
	}
	return strings.Join(all, "; "), lines
}

// condition is an if's init and condition, `if <init>; <cond>`, or, for a
// call in its else, `if <init>; !(<cond>)`, the guard that holds there. They
// are printed with no positions, so the printer puts each on one line in one
// form however the source breaks them, and their lines are every line from
// the init's start to the condition's end.
func (r *reader) condition(s *ast.IfStmt, inElse bool) (string, []int) {
	cond := flat(s.Cond)
	if inElse {
		cond = "!(" + cond + ")"
	}
	start, text := s.Cond.Pos(), "if "+cond
	if s.Init != nil {
		start, text = s.Init.Pos(), "if "+flat(s.Init)+"; "+cond
	}
	var lines []int
	for l := r.pos(start).line; l <= r.pos(s.Cond.End()).line; l++ {
		lines = append(lines, l)
	}
	return text, lines
}

// flat is a node printed with an empty file set: without the source's
// positions, the printer lays it out in its own form.
func flat(n ast.Node) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, token.NewFileSet(), n)
	return b.String()
}

func identExprs(ids []*ast.Ident) []ast.Expr {
	out := make([]ast.Expr, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// assigned records each want variable given a value, numbered within its
// declaration, so a second `want = ...` is a value of its own.
func (r *reader) assigned(lhs, rhs []ast.Expr) {
	if len(lhs) != len(rhs) {
		return
	}
	for i, l := range lhs {
		if id, ok := l.(*ast.Ident); ok && isWant(id.Name) {
			r.want(fmt.Sprintf("%s #%d", id.Name, r.next("var "+id.Name)), id.Name, rhs[i])
		}
	}
}

func (r *reader) want(key, what string, value ast.Expr) {
	r.f.wants[r.key(r.decl+" "+key)] = want{pos: r.pos(value.Pos()), what: what, decl: r.decl, text: r.print(value)}
}

// isWant: a name that holds what a test expects.
func isWant(name string) bool {
	l := strings.ToLower(name)
	return strings.HasPrefix(l, "want") || strings.HasPrefix(l, "expect") || strings.HasPrefix(l, "golden")
}
