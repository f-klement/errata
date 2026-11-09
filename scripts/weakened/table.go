package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// row is one element of a table: its printed text and where it starts.
type row struct {
	pos  position
	text string
}

// table is a slice or map literal whose elements are composite literals:
// its rows with a name, in order, and its rows without one. seq is its key
// by its place among the tables of its declaration.
type table struct {
	pos     position
	decl    string
	seq     string
	names   []string
	named   map[string]row
	unnamed []row
}

// element is one value of a row and the field it sets: the key of a keyed
// element, the field at its place for a positional one when the row's
// struct type is in the file, or "".
type element struct {
	field string
	value ast.Expr
}

// tableRow is a row as table reads it: its name, its inputs (the text of its
// elements but its wants), its elements and the row itself.
type tableRow struct {
	row
	name     string
	inputs   string
	elements []element
}

// table records lit when it is a table: a slice, array or map literal with at
// least one element that is a composite literal. Its key is its declaration,
// its type and its place among the tables of that type there; it is also
// found by its place among all the tables there, for a table whose type the
// change edited, such as a field added to its rows.
func (r *reader) table(lit *ast.CompositeLit) {
	elt := elementType(lit.Type)
	if elt == nil {
		return
	}
	typ := r.print(lit.Type)
	key := fmt.Sprintf("table %s #%d", typ, r.next("table "+typ))
	rows := r.rows(lit, r.fields(elt))
	if len(rows) == 0 {
		return
	}
	t := &table{pos: r.pos(lit.Pos()), decl: r.decl, named: map[string]row{}}
	inputs := map[string]int{}
	for _, rw := range rows {
		inputs[rw.inputs]++
		r.addRow(t, key, rw, inputs[rw.inputs])
	}
	t.seq = fmt.Sprintf("%s table #%d", r.decl, r.next("table"))
	r.f.tables[r.key(r.decl+" "+key)] = t
	r.f.bySeq[r.key(t.seq)] = t
}

// elementType is the type of a slice's, array's or map's elements, or nil.
func elementType(typ ast.Expr) ast.Expr {
	switch t := typ.(type) {
	case *ast.ArrayType:
		return t.Elt
	case *ast.MapType:
		return t.Value
	}
	return nil
}

// fields is the field names, in order, of a struct type literal or of a
// struct type the file declares, through a pointer; nil for any other type.
func (r *reader) fields(typ ast.Expr) []string {
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	switch t := typ.(type) {
	case *ast.StructType:
		return fieldNames(t)
	case *ast.Ident:
		return r.structs[t.Name]
	}
	return nil
}

// rows reads the elements of a table literal that are rows.
func (r *reader) rows(lit *ast.CompositeLit, fields []string) []tableRow {
	var rows []tableRow
	for _, e := range lit.Elts {
		name, at := "", r.pos(e.Pos())
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			name, e = clip(r.print(kv.Key)), kv.Value
		}
		rl := rowLit(e)
		if rl == nil {
			continue
		}
		f := fields
		if own := r.fields(rl.Type); own != nil {
			f = own
		}
		elements := elementsOf(rl, f)
		if name == "" {
			name = rowName(elements)
		}
		rows = append(rows, tableRow{row: row{pos: at, text: r.print(e)}, name: name,
			inputs: r.inputs(elements), elements: elements})
	}
	return rows
}

// elementsOf pairs each element of a row with its field.
func elementsOf(lit *ast.CompositeLit, fields []string) []element {
	out := make([]element, len(lit.Elts))
	for i, e := range lit.Elts {
		out[i] = element{value: e}
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			out[i] = element{field: keyName(kv.Key), value: kv.Value}
		} else if i < len(fields) {
			out[i].field = fields[i]
		}
	}
	return out
}

// keyName is a key's name when it is an identifier.
func keyName(key ast.Expr) string {
	if id, ok := key.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// inputs is the text of a row's elements but its wants.
func (r *reader) inputs(elements []element) string {
	var b strings.Builder
	for _, e := range elements {
		if !isWant(e.field) {
			b.WriteString(e.field + ":" + r.print(e.value) + ", ")
		}
	}
	return b.String()
}

// addRow adds a row to t, with its want fields. A row with no name is keyed
// by its inputs, so a row inserted before it does not move its wants, and,
// when other rows of the table have the same inputs, by its place among them.
func (r *reader) addRow(t *table, key string, rw tableRow, occurrence int) {
	r.f.rows[rw.text] = true
	id := rw.name
	switch {
	case rw.name == "":
		id = "in " + rw.inputs + " #" + strconv.Itoa(occurrence)
		t.unnamed = append(t.unnamed, rw.row)
	case t.named[rw.name] == row{}:
		t.names = append(t.names, rw.name)
		t.named[rw.name] = rw.row
	}
	for _, e := range rw.elements {
		if isWant(e.field) {
			r.want(key+" row "+id+" "+e.field, e.field, e.value)
		}
	}
}

// rowLit is e as a composite literal, or the one e takes the address of.
func rowLit(e ast.Expr) *ast.CompositeLit {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
		e = u.X
	}
	lit, _ := e.(*ast.CompositeLit)
	return lit
}

// rowName is the string a row's name, desc or title field holds, keyed or at
// its place, or "".
func rowName(elements []element) string {
	for _, e := range elements {
		if value, ok := e.value.(*ast.BasicLit); ok && value.Kind == token.STRING && isNameField(e.field) {
			return clip(value.Value)
		}
	}
	return ""
}

func isNameField(name string) bool {
	switch strings.ToLower(name) {
	case "name", "desc", "description", "title":
		return true
	}
	return false
}
