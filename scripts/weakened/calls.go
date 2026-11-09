package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"slices"
)

// isPackage: e names an import of the file, so a call through it, such as
// fmt.Errorf or log.Fatal, is no method of a test.
func (r *reader) isPackage(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && r.imports[id.Name]
}

// call records a call that can skip a test, and a call of a plain function
// name, which may be a helper, each with the conditions it runs under; stack
// is the nodes from the walk's root down to c.
func (r *reader) call(c *ast.CallExpr, stack []ast.Node) {
	at := r.pos(c.Pos())
	guard, lines := r.guard(stack)
	lines = append(lines, at.line)
	if id, ok := c.Fun.(*ast.Ident); ok {
		r.f.calls = append(r.f.calls, call{pos: at, decl: r.decl, name: id.Name, guard: guard, lines: lines})
		return
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg, _ := sel.X.(*ast.Ident)
	short := pkg != nil && pkg.Name == "testing" && sel.Sel.Name == "Short"
	method := !r.isPackage(sel.X) && slices.Contains([]string{"Skip", "Skipf", "SkipNow"}, sel.Sel.Name)
	if short || method {
		r.f.skips = append(r.f.skips, skip{pos: at, decl: r.decl, call: r.print(c.Fun), guard: guard, lines: lines})
	}
}

// asserts counts the calls in body that fail a test, and lists the plain
// function names it calls. Error and Fatal take an argument, so err.Error()
// is not one, nor is a call through a package, such as fmt.Errorf.
func (r *reader) asserts(body ast.Node) (int, []string) {
	n := 0
	var calls []string
	ast.Inspect(body, func(node ast.Node) bool {
		c, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := c.Fun.(*ast.Ident); ok {
			calls = append(calls, id.Name)
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); ok && failing(sel.Sel.Name, len(c.Args)) && !r.isPackage(sel.X) {
			n++
		}
		return true
	})
	return n, calls
}

func failing(method string, args int) bool {
	switch method {
	case "Error", "Errorf", "Fatal", "Fatalf":
		return args > 0
	case "Fail", "FailNow":
		return args == 0
	}
	return false
}

// inline records the literals an if compares with, by == or !=, before its
// block fails the test: `if got != 3 { t.Errorf(...) }` expects 3. Each is
// keyed by the other side of its comparison and its place among those, so
// the value compared with got is the same one at both sides.
func (r *reader) inline(s *ast.IfStmt) {
	if n, _ := r.asserts(s.Body); n == 0 {
		return
	}
	ast.Inspect(s.Cond, func(n ast.Node) bool {
		b, ok := n.(*ast.BinaryExpr)
		if !ok || (b.Op != token.EQL && b.Op != token.NEQ) {
			return !isFuncLit(n)
		}
		lit, other := literal(b.X), b.Y
		if lit == nil {
			lit, other = literal(b.Y), b.X
		}
		if lit != nil {
			side := r.print(other)
			r.want(fmt.Sprintf("inline %s #%d", side, r.next("inline "+side)), "the value compared with "+clip(side), lit)
		}
		return true
	})
}

func isFuncLit(n ast.Node) bool {
	_, ok := n.(*ast.FuncLit)
	return ok
}

// literal is e when it is a literal, or a negated one, or nil.
func literal(e ast.Expr) ast.Expr {
	inner := e
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.SUB {
		inner = u.X
	}
	if _, ok := inner.(*ast.BasicLit); ok {
		return e
	}
	return nil
}
