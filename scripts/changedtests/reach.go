package main

import (
	"go/ast"
	"go/token"
	"strings"
)

// decl is one top-level name of a _test.go file: a function, a method, or
// one name of a var, const or type spec. list compares its text between the
// base and HEAD and follows what it names; drop cuts its range.
type decl struct {
	// name is its name; a method's is ".<receiver type>.<method>", so a
	// method named like a test is never one.
	name string
	// recv is a method's receiver type, "" for anything else.
	recv string
	// text is what list compares, the doc comment left out.
	text string
	// uses is every identifier it names, its own name too.
	uses map[string]bool
	// from and to are the byte range of the whole declaration, doc comment
	// included; the names of one var, const or type block share it.
	from, to int
	// setup marks TestMain and init, which run around a package's tests.
	setup bool
}

// key is the name by which other declarations reach d: a method's receiver
// type, since a test names the type to get at the method (a literal, a
// conversion, a constructor), and the name of anything else. Keyed by its
// own name, a method of a fake would reach every test that calls a method of
// that name on anything, t.Name() for a fake's Name.
func (d decl) key() string {
	if d.recv != "" {
		return d.recv
	}
	return d.name
}

// keyOf is the key of a declaration list has only the name of.
func keyOf(name string) string {
	if rest, ok := strings.CutPrefix(name, "."); ok {
		recv, _, _ := strings.Cut(rest, ".")
		return recv
	}
	return name
}

// spread marks each declaration of all whose key is in names or that names
// one of names, and adds its key to names, until no more is marked; with
// every, it marks them all. It reports which it marked. list spreads from the
// changed declarations to the tests that reach them, drop from the
// declarations a build error names to everything that names them.
func spread(all []decl, names map[string]bool, every bool) []bool {
	marked := make([]bool, len(all))
	for grew := true; grew; {
		grew = false
		for i, d := range all {
			if !marked[i] && (every || names[d.key()] || namesAny(d.uses, names)) {
				marked[i], names[d.key()], grew = true, true, true
			}
		}
	}
	return marked
}

func namesAny(uses, names map[string]bool) bool {
	for name := range uses {
		if names[name] {
			return true
		}
	}
	return false
}

// topLevel is every top-level name f declares, imports left out.
func topLevel(f *ast.File, src []byte) []decl {
	pos := func(p token.Pos) int { return int(p - f.FileStart) }
	text := func(n ast.Node) string { return string(src[pos(n.Pos()):pos(n.End())]) }
	var found []decl
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			found = append(found, funcDecl(d, text, docStart(d.Doc, pos(d.Pos()), pos), pos(d.End())))
		case *ast.GenDecl:
			found = append(found, specDecls(d, text, docStart(d.Doc, pos(d.Pos()), pos), pos(d.End()))...)
		}
	}
	return found
}

func funcDecl(d *ast.FuncDecl, text func(ast.Node) string, from, to int) decl {
	name := d.Name.Name
	return decl{
		name: funcName(d), recv: recvName(d), text: funcText(d, text), uses: idents(d),
		from: from, to: to, setup: d.Recv == nil && (name == "TestMain" || name == "init"),
	}
}

// specDecls is a decl for each name a var, const or type declaration
// declares, with its spec's text; an import declaration's none.
func specDecls(d *ast.GenDecl, text func(ast.Node) string, from, to int) []decl {
	var found []decl
	for _, spec := range d.Specs {
		for _, name := range specNames(spec) {
			found = append(found, decl{name: name, text: text(spec), uses: idents(spec), from: from, to: to})
		}
	}
	return found
}

// funcName is a function's name, and a method's as ".<receiver type>.<name>".
func funcName(d *ast.FuncDecl) string {
	if d.Recv != nil {
		return "." + recvName(d) + "." + d.Name.Name
	}
	return d.Name.Name
}

// recvName is the name of a method's receiver type, pointer and type
// arguments taken off, or "" for a function.
func recvName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	t := d.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		t = x.X
	case *ast.IndexListExpr:
		t = x.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// funcText is a function's text from its func keyword, its doc comment left
// out.
func funcText(d *ast.FuncDecl, text func(ast.Node) string) string {
	if d.Body == nil {
		return text(d.Type)
	}
	return text(d.Type) + text(d.Body)
}

// specNames is the names a type or value spec declares; an import's none.
func specNames(spec ast.Spec) []string {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return []string{s.Name.Name}
	case *ast.ValueSpec:
		var names []string
		for _, n := range s.Names {
			names = append(names, n.Name)
		}
		return names
	}
	return nil
}

func idents(n ast.Node) map[string]bool {
	uses := map[string]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			uses[id.Name] = true
		}
		return true
	})
	return uses
}

func docStart(doc *ast.CommentGroup, from int, pos func(token.Pos) int) int {
	if doc == nil {
		return from
	}
	return pos(doc.Pos())
}
