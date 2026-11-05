package code

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/scanner"
	"go/token"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// goDecl is a name a Go file declares at its top with const or var: the
// expression its value is, the type it declares, the iota of its spec, and
// whether the file declares the name more than once or gives it no value
// of its own, as var a, b = f() does.
type goDecl struct {
	isConst bool
	spec    *ast.ValueSpec
	name    *ast.Ident
	typ     ast.Expr
	value   ast.Expr
	iota    int
	broken  bool
	// repeated is a const whose spec repeats the values of the spec before
	// it, so its line is its name's.
	repeated bool
}

// goFile is what the Go reader knows of a file: its bytes, its top level
// consts, vars and types, the vars a statement assigns or takes the address
// of, and the state of each const it evaluates.
type goFile struct {
	file     string
	data     []byte
	fset     *token.FileSet
	decls    map[string]*goDecl
	types    map[string]ast.Expr
	aliases  map[string]bool
	assigned map[string]bool
	done     map[string]goVal
	busy     map[string]bool
	err      error
	// evals counts the expressions of constants evaluated, each once for
	// every iota it reads (constant).
	evals int
	// shared is the value of each constant's expression, by the expression
	// and the iota it was read with, -1 for one that reads no iota, so the
	// specs that repeat an expression evaluate it once (constant).
	shared map[sharedKey]goVal
	// readIota is whether the expression being evaluated read iota.
	readIota bool
	// work is what the evaluation has spent of budget (spend).
	work, budget int
}

// sharedKey is an expression and the iota it is read with.
type sharedKey struct {
	e    ast.Expr
	iota int
}

// The work an evaluation of a Go file may do: workPerToken units for each
// token of the file, and workBase more, so a comment, which is no token,
// buys none. Each expression evaluated costs a unit, a number literal a
// unit for each 8 bytes, an operation on numbers a unit for each 64 bits
// of its operands (size), and a comparison of two strings the bytes of
// both, which go/constant joins to compare.
const (
	workPerToken = 8
	workBase     = 1 << 16
)

// size is the bits go/constant holds v in: an integer's, and a rational's
// numerator and denominator together; a float past a rational's range is
// held to 512 bits. A string or a bool counts none, since go/constant joins
// strings only to compare them, which spend charges by their bytes.
func size(v constant.Value) int {
	switch v.Kind() {
	case constant.Int:
		return constant.BitLen(v)
	case constant.Float:
		if n := constant.Num(v); n.Kind() == constant.Int {
			return constant.BitLen(n) + constant.BitLen(constant.Denom(v))
		}
		return maxIntBits
	default:
		return 0
	}
}

// chargeProduct spends, for an operation whose cost grows with the product
// of its operands' sizes, a unit for each pair of their 64 bit words: a
// multiplication, a division, and an addition, a subtraction or a
// comparison of rationals, which go/constant cross-multiplies and reduces.
func (g *goFile) chargeProduct(op token.Token, x, y goVal) bool {
	if !x.ok || !y.ok {
		return false
	}
	rational := x.v.Kind() == constant.Float || y.v.Kind() == constant.Float
	switch op {
	case token.MUL, token.QUO, token.REM:
	case token.ADD, token.SUB, token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		if !rational {
			return false
		}
	default:
		return false
	}
	return g.spend((size(x.v)/64 + 1) * (size(y.v)/64 + 1))
}

// charge spends a unit for each 64 bits of the operands of an operation on
// numbers, and reports whether the budget refused it.
func (g *goFile) charge(vs ...goVal) bool {
	bits := 0
	for _, v := range vs {
		if v.ok {
			bits += size(v.v)
		}
	}
	return g.spend(bits / 64)
}

// goVal is a constant's value and type, the text the file wrote for it when
// it is one literal number, as 0x1F, and the length of a string, which a
// long string is held to before go/constant builds it. Ok is false for a
// value that is no constant errata evaluates: it is not literal.
type goVal struct {
	v      constant.Value
	t      goType
	lit    string
	strLen int
	ok     bool
	// char is a rune written as one literal whose lit is its character: it
	// stays one while its type is untyped, rune or int32 (convert).
	char bool
}

// goType is a constant's type: its class, its size in bits for a number,
// whether it is signed, and its name, "" for an untyped constant. Two typed
// constants combine only under one name.
type goType struct {
	class  uint8
	bits   int
	signed bool
	name   string
}

// The classes of a Go constant's type. An untyped rune is cRune; typed, it
// is int32.
const (
	cBool uint8 = iota + 1
	cString
	cInt
	cRune
	cFloat
	cComplex
)

// predeclared are Go's types of constants, by name. int, uint and uintptr
// are read with 64 bits, as on the platforms errata is built for.
var predeclared = map[string]goType{
	"bool": {class: cBool}, "string": {class: cString},
	"int": {class: cInt, bits: 64, signed: true}, "int8": {class: cInt, bits: 8, signed: true},
	"int16": {class: cInt, bits: 16, signed: true}, "int32": {class: cInt, bits: 32, signed: true},
	"rune": {class: cInt, bits: 32, signed: true}, "int64": {class: cInt, bits: 64, signed: true},
	"uint": {class: cInt, bits: 64}, "uint8": {class: cInt, bits: 8}, "byte": {class: cInt, bits: 8},
	"uint16": {class: cInt, bits: 16}, "uint32": {class: cInt, bits: 32}, "uint64": {class: cInt, bits: 64},
	"uintptr": {class: cInt, bits: 64},
	"float32": {class: cFloat, bits: 32}, "float64": {class: cFloat, bits: 64},
	"complex64": {class: cComplex}, "complex128": {class: cComplex},
}

// maxIntBits is the most bits an integer constant holds, as go/types
// allows; a larger one is not literal, and a shift by more is refused before
// go/constant builds it.
const maxIntBits = 512

// maxNumberLit is the longest number literal read: 512 bits in binary with
// a _ between each digit, and its prefix. A longer one overflows anyway.
const maxNumberLit = 2*maxIntBits + 2

// bytesPerToken is how many bytes of max_bytes allow a Go file one token:
// go/parser builds the syntax of the whole file, and the reader its
// constants, at up to about 175 bytes of memory for each token of the
// densest shape measured, so the token limit holds a read at the default
// max_bytes to about 750 MB.
const bytesPerToken = 8

// readGo parses data, a Go file read under a max_bytes of limit, and
// gathers its top level consts, vars and types. It refuses a file with more
// tokens than limit allows, counted before the parse, and a file go/parser
// refuses. Nothing is compiled or run.
func readGo(file string, data []byte, limit int64) (*goFile, error) {
	most := limit / bytesPerToken
	n := tokens(data, most)
	if n > most {
		return nil, fmt.Errorf("%s: the file holds more than %d tokens, the limit for a max_bytes of %d (one for each %d bytes); set max_bytes: higher to read it", file, most, limit, bytesPerToken)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, parseError(file, data, err)
	}
	g := &goFile{file: file, data: data, fset: fset, decls: map[string]*goDecl{}, types: map[string]ast.Expr{}, aliases: map[string]bool{},
		assigned: map[string]bool{}, done: map[string]goVal{}, busy: map[string]bool{}, shared: map[sharedKey]goVal{},
		budget: workPerToken*int(n) + workBase}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		g.gather(gd)
	}
	ast.Inspect(f, g.watch)
	return g, nil
}

// tokens counts the tokens of data, a Go file, with go/scanner, which builds
// nothing, and stops past most.
func tokens(data []byte, most int64) int64 {
	var s scanner.Scanner
	s.Init(token.NewFileSet().AddFile("", -1, len(data)), data, nil, 0)
	var n int64
	for ; n <= most; n++ {
		if _, tok, _ := s.Scan(); tok == token.EOF {
			break
		}
	}
	return n
}

// parseError is the refusal of a file go/parser refuses, at the line of
// its first error, counted as errata counts lines.
func parseError(file string, data []byte, err error) error {
	var list scanner.ErrorList
	if errors.As(err, &list) && len(list) > 0 {
		e := list[0]
		return fmt.Errorf("%s: line %d: %s; errata reads a Go file the compiler parses", file, core.LineOf(data, e.Pos.Offset), e.Msg)
	}
	return fmt.Errorf("%s: %w; errata reads a Go file the compiler parses", file, err)
}

// gather notes each name a const, var or type declaration declares. A const
// spec with no value repeats the type and the values of the spec before it,
// with its own iota.
func (g *goFile) gather(gd *ast.GenDecl) {
	var typ ast.Expr
	var values []ast.Expr
	for i, s := range gd.Specs {
		switch s := s.(type) {
		case *ast.TypeSpec:
			if s.TypeParams == nil {
				g.types[s.Name.Name] = s.Type
				g.aliases[s.Name.Name] = s.Assign.IsValid()
			}
		case *ast.ValueSpec:
			if gd.Tok == token.VAR || s.Type != nil || len(s.Values) > 0 {
				typ, values = s.Type, s.Values
			}
			for j, name := range s.Names {
				if name.Name == "_" {
					continue
				}
				d := &goDecl{isConst: gd.Tok == token.CONST, spec: s, name: name, typ: typ, iota: i, repeated: len(s.Values) == 0}
				switch {
				case len(values) == len(s.Names):
					d.value = values[j]
				case len(values) > 0 || d.isConst:
					d.broken = true // var a, b = f(), or a const with too few values
				}
				if _, twice := g.decls[name.Name]; twice {
					d.broken = true
				}
				g.decls[name.Name] = d
			}
		}
	}
}

// watch notes each name a statement assigns, increments or takes the
// address of, so a var the program changes is not literal. A local that
// shadows the name counts too.
func (g *goFile) watch(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.AssignStmt:
		if n.Tok != token.DEFINE {
			for _, l := range n.Lhs {
				g.note(l)
			}
		}
	case *ast.IncDecStmt:
		g.note(n.X)
	case *ast.UnaryExpr:
		if n.Op == token.AND {
			g.note(n.X)
		}
	case *ast.RangeStmt:
		if n.Tok == token.ASSIGN {
			g.note(n.Key)
			g.note(n.Value)
		}
	}
	return true
}

// note marks the name at the root of e, such as X in X.f[1], assigned.
func (g *goFile) note(e ast.Expr) {
	for e != nil {
		switch x := e.(type) {
		case *ast.Ident:
			g.assigned[x.Name] = true
			return
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return
		}
	}
}

// value is the value of the name the file declares, with its line and
// whether it is a const; ok is false when the file declares no such name.
func (g *goFile) value(name string) (n *core.Node, isConst, ok bool, err error) {
	d, found := g.decls[name]
	if !found {
		return nil, false, false, nil
	}
	line := g.line(d.name.Pos())
	if d.value != nil && !d.repeated {
		line = g.line(d.value.Pos())
	}
	var v goVal
	switch {
	case d.broken || d.value == nil && !d.isConst:
		v = goVal{}
	case d.isConst:
		v = g.constant(name, 0)
	default:
		v = g.typed(g.eval(d.value, -1, 0), d.typ)
		v = defaulted(v)
		if g.assigned[name] {
			v.ok = false
		}
	}
	if g.err != nil {
		return nil, false, false, g.err
	}
	if !v.ok {
		return &core.Node{Shape: core.Scalar, Type: core.TypeString, NotLiteral: true, Text: g.source(d), Line: line}, d.isConst, true, nil
	}
	return g.scalar(v, line), d.isConst, true, nil
}

// source is the text the file wrote for d's value, or for its spec when it
// has none of its own.
func (g *goFile) source(d *goDecl) string {
	var n ast.Node = d.spec
	if d.value != nil {
		n = d.value
	}
	return strings.TrimSpace(string(g.data[g.offset(n.Pos()):g.offset(n.End())]))
}

// scalar is the node of v, a literal constant, at line: a number as the file
// wrote it when it wrote one literal, and otherwise as Go prints it.
func (g *goFile) scalar(v goVal, line int) *core.Node {
	n := &core.Node{Shape: core.Scalar, Line: line}
	switch k := v.v.Kind(); {
	case k == constant.Bool:
		n.Type, n.Text = core.TypeBool, strconv.FormatBool(constant.BoolVal(v.v))
	case k == constant.String:
		n.Type, n.Text = core.TypeString, constant.StringVal(v.v)
	case v.char && v.lit != "":
		n.Type, n.Text = core.TypeString, v.lit // a rune literal's character
	default:
		n.Type, n.Text = core.TypeNumber, v.lit
		if n.Text == "" {
			n.Text = number(v)
		}
	}
	return n
}

// number is the text of v, a number: an integer in decimal, and a float
// that holds an integer under 1e21 in decimal too, as 1e6 is 1000000;
// any other float in Go's shortest form for its size.
func number(v goVal) string {
	if i := constant.ToInt(v.v); i.Kind() == constant.Int {
		if f, _ := constant.Float64Val(i); v.v.Kind() == constant.Int || math.Abs(f) < 1e21 {
			return i.ExactString()
		}
	}
	f, _ := constant.Float64Val(v.v)
	if math.IsInf(f, 0) {
		return v.v.String() // an untyped float past float64, as 1e+600
	}
	bits := 64
	if v.t.class == cFloat && v.t.bits == 32 {
		bits = 32
	}
	return strconv.FormatFloat(f, 'g', -1, bits)
}

// line is the line of pos, counted from 1.
func (g *goFile) line(pos token.Pos) int { return core.LineOf(g.data, g.offset(pos)) }

// offset is the byte offset of pos in the file.
func (g *goFile) offset(pos token.Pos) int { return g.fset.Position(pos).Offset }

// fail stops the evaluation with err, the first error.
func (g *goFile) fail(err error) {
	if g.err == nil {
		g.err = err
	}
}

// constant is the value of the const name, evaluated once; one whose value
// refers back to itself is not literal, as the compiler refuses it.
func (g *goFile) constant(name string, depth int) goVal {
	if v, ok := g.done[name]; ok {
		return v
	}
	d := g.decls[name]
	if g.busy[name] || d.broken || d.value == nil {
		return goVal{}
	}
	g.busy[name] = true
	v := g.typed(g.sharedEval(d.value, d.iota, depth+1), d.typ)
	g.busy[name] = false
	g.done[name] = v
	return v
}

// sharedEval is eval of e, the expression of a constant's spec, at iota io,
// read once for every spec that repeats e if e reads no iota, and once for
// each iota if it does.
func (g *goFile) sharedEval(e ast.Expr, io, depth int) goVal {
	if v, ok := g.shared[sharedKey{e, -1}]; ok {
		return v
	}
	if v, ok := g.shared[sharedKey{e, io}]; ok {
		return v
	}
	outer := g.readIota
	g.readIota = false
	g.evals++
	v := g.eval(e, io, depth)
	key := sharedKey{e, -1}
	if g.readIota {
		key.iota = io
	}
	g.shared[key] = v
	g.readIota = outer
	return v
}

// spend charges n units of work, and refuses the file, as unknown, once the
// evaluation has spent more than its budget.
func (g *goFile) spend(n int) bool {
	g.work += n
	if g.work > g.budget && g.err == nil {
		g.fail(fmt.Errorf("%s: evaluating its constants took more than %d units of work, the limit for a file of %d tokens; a regex extractor can still read the file", g.file, g.budget, (g.budget-workBase)/workPerToken))
	}
	return g.err != nil
}

// typed is v given the type typ declares, nil for none: an untyped v is
// converted to it, and a typed one must have it.
func (g *goFile) typed(v goVal, typ ast.Expr) goVal {
	if typ == nil || !v.ok {
		return v
	}
	t, ok := g.resolve(typ)
	if !ok || v.t.name != "" && v.t.name != t.name {
		return goVal{}
	}
	return convert(v, t)
}

// defaulted is v, the value of a var, with an untyped constant given its
// default type, int, int32, float64, string or bool, which must hold it.
func defaulted(v goVal) goVal {
	if !v.ok || v.t.name != "" {
		return v
	}
	name := map[uint8]string{cBool: "bool", cString: "string", cInt: "int", cRune: "rune", cFloat: "float64", cComplex: "complex128"}[v.t.class]
	t := predeclared[name]
	t.name = canonical(name)
	return convert(v, t)
}

// canonical is the name of Go's own type name, with byte read as uint8 and
// rune as int32, which they are aliases of.
func canonical(name string) string {
	switch name {
	case "byte":
		return "uint8"
	case "rune":
		return "int32"
	}
	return name
}

// resolve is the type the expression typ names: one of Go's own, or one
// the file declares whose underlying type is, by any number of steps.
func (g *goFile) resolve(typ ast.Expr) (goType, bool) {
	id, ok := typ.(*ast.Ident)
	if !ok {
		return goType{}, false // a type of another package, or no type of a constant
	}
	name := "" // the type's identity: the first name that is no alias
	for range core.MaxDepth {
		if under, declared := g.types[id.Name]; declared {
			if name == "" && !g.aliases[id.Name] {
				name = id.Name
			}
			if id, ok = under.(*ast.Ident); !ok {
				return goType{}, false
			}
			continue
		}
		t, known := predeclared[id.Name]
		if name == "" {
			name = canonical(id.Name)
		}
		t.name = name
		return t, known && t.class != cComplex
	}
	return goType{}, false // a type that names itself
}

// eval is the value of the constant expression e, in a const spec of iota
// io (-1 outside one), depth steps into the evaluation. Anything but a
// literal, a const of the file, iota, true, false, an operator on those and
// a conversion to a type of the file's or Go's own is not literal.
func (g *goFile) eval(e ast.Expr, io, depth int) goVal {
	if depth > core.MaxDepth {
		g.fail(fmt.Errorf("%s: %w", g.file, core.TooDeep(g.line(e.Pos()))))
		return goVal{}
	}
	if g.spend(1) {
		return goVal{}
	}
	switch e := e.(type) {
	case *ast.BasicLit:
		if g.spend(len(e.Value) / 8) {
			return goVal{}
		}
		return literal(e)
	case *ast.ParenExpr:
		v := g.eval(e.X, io, depth+1)
		v.lit, v.char = "", false
		return v
	case *ast.Ident:
		return g.ident(e.Name, io, depth)
	case *ast.UnaryExpr:
		x := g.eval(e.X, io, depth+1)
		if g.charge(x) {
			return goVal{}
		}
		return unary(e.Op, x)
	case *ast.BinaryExpr:
		x, y := g.eval(e.X, io, depth+1), g.eval(e.Y, io, depth+1)
		if g.charge(x, y) || g.chargeProduct(e.Op, x, y) {
			return goVal{}
		}
		if e.Op == token.SHL || e.Op == token.SHR {
			return shift(e.Op, x, y)
		}
		return g.binary(e.Op, x, y)
	case *ast.CallExpr:
		if len(e.Args) != 1 || e.Ellipsis.IsValid() {
			return goVal{}
		}
		t, ok := g.resolve(e.Fun)
		if !ok {
			return goVal{} // a call, which is no constant
		}
		v := g.eval(e.Args[0], io, depth+1)
		if !v.ok || v.t.class == cInt && t.class == cString || v.t.class == cRune && t.class == cString {
			return goVal{} // string(65), which is A, and not literal here
		}
		if g.charge(v) {
			return goVal{}
		}
		v.lit, v.char = "", false
		return convert(v, t)
	}
	return goVal{}
}

// ident is the value of a name in a constant expression.
func (g *goFile) ident(name string, io, depth int) goVal {
	if d, ok := g.decls[name]; ok {
		if !d.isConst {
			return goVal{} // a var, which is no constant
		}
		v := g.constant(name, depth)
		v.lit, v.char = "", false
		return v
	}
	switch name {
	case "true", "false":
		return goVal{v: constant.MakeBool(name == "true"), t: goType{class: cBool}, ok: true}
	case "iota":
		if io >= 0 {
			g.readIota = true
			return goVal{v: constant.MakeInt64(int64(io)), t: goType{class: cInt}, ok: true}
		}
	}
	return goVal{}
}

// literal is the value of a literal: a number keeps the text the file
// wrote, a string is decoded, and a string that is no valid UTF-8, an
// imaginary number and a number too long to hold are not literal.
func literal(e *ast.BasicLit) goVal {
	switch e.Kind {
	case token.INT, token.FLOAT:
		if len(e.Value) > maxNumberLit {
			return goVal{}
		}
		v := constant.MakeFromLiteral(e.Value, e.Kind, 0)
		class := cInt
		if e.Kind == token.FLOAT {
			class = cFloat
		}
		return checked(goVal{v: v, t: goType{class: class}, lit: e.Value, ok: v.Kind() != constant.Unknown})
	case token.CHAR:
		// A rune written as one literal is its character, as the file
		// means it; a computed one is its number.
		v := constant.MakeFromLiteral(e.Value, e.Kind, 0)
		n, _ := constant.Int64Val(v)
		if v.Kind() != constant.Int || n < 0 || n > utf8.MaxRune {
			return goVal{}
		}
		r := rune(n)
		if !utf8.ValidRune(r) {
			return goVal{}
		}
		return goVal{v: v, t: goType{class: cRune}, lit: string(r), char: true, ok: true}
	case token.STRING:
		v := constant.MakeFromLiteral(e.Value, e.Kind, 0)
		if v.Kind() != constant.String || !utf8.ValidString(constant.StringVal(v)) {
			return goVal{}
		}
		return goVal{v: v, t: goType{class: cString}, strLen: len(constant.StringVal(v)), ok: true}
	default:
		return goVal{} // an imaginary number
	}
}

// unary is op applied to x: +, - and ^ on a number, ! on a bool.
func unary(op token.Token, x goVal) goVal {
	if !x.ok {
		return goVal{}
	}
	numeric := x.t.class == cInt || x.t.class == cRune || x.t.class == cFloat
	switch {
	case (op == token.ADD || op == token.SUB) && numeric,
		op == token.NOT && x.t.class == cBool,
		op == token.XOR && (x.t.class == cInt || x.t.class == cRune):
	default:
		return goVal{}
	}
	prec := uint(0)
	if op == token.XOR && x.t.name != "" && !x.t.signed {
		prec = uint(x.t.bits) // ^ of a typed unsigned flips its bits only
	}
	return checked(goVal{v: constant.UnaryOp(op, x.v, prec), t: x.t, ok: true})
}

// shift is x shifted by y, a count of at most maxIntBits. An untyped x
// becomes an integer, as Go makes it.
func shift(op token.Token, x, y goVal) goVal {
	if !x.ok || !y.ok {
		return goVal{}
	}
	s := constant.ToInt(y.v)
	n, exact := constant.Uint64Val(s)
	if s.Kind() != constant.Int || !exact || n > maxIntBits {
		return goVal{}
	}
	if x.t.name == "" {
		x.v = constant.ToInt(x.v)
		if x.t.class != cRune {
			x.t = goType{class: cInt}
		}
	}
	if x.v.Kind() != constant.Int || x.t.class != cInt && x.t.class != cRune {
		return goVal{}
	}
	return checked(goVal{v: constant.Shift(x.v, op, uint(n)), t: x.t, ok: true})
}

// binary is x op y. An untyped operand takes the other's type; two untyped
// ones take the later of int, rune, float. Integers divide as integers. A
// comparison is an untyped bool. A string longer than the file is refused.
func (g *goFile) binary(op token.Token, x, y goVal) goVal {
	if !x.ok || !y.ok {
		return goVal{}
	}
	switch {
	case x.t.name != "" && y.t.name != "" && x.t.name != y.t.name:
		return goVal{}
	case x.t.name != "" && y.t.name == "":
		y = convert(y, x.t)
	case x.t.name == "" && y.t.name != "":
		x = convert(x, y.t)
	case x.t.class < y.t.class && x.t.class >= cInt && y.t.class <= cFloat:
		x.t = y.t
	case y.t.class < x.t.class && y.t.class >= cInt && x.t.class <= cFloat:
		y.t = x.t
	}
	if !x.ok || !y.ok || x.t.class != y.t.class {
		return goVal{}
	}
	t := x.t
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		if t.class == cBool && op != token.EQL && op != token.NEQ {
			return goVal{}
		}
		if t.class == cString && g.spend(x.strLen+y.strLen) {
			return goVal{} // go/constant joins both strings to compare them
		}
		return goVal{v: constant.MakeBool(constant.Compare(x.v, op, y.v)), t: goType{class: cBool}, ok: true}
	case token.LAND, token.LOR:
		if t.class != cBool {
			return goVal{}
		}
	case token.ADD:
		if t.class == cString {
			if x.strLen+y.strLen > len(g.data) {
				g.fail(fmt.Errorf("%s: a string constant is longer than the file, %d bytes; errata reads no constant built that long", g.file, len(g.data)))
				return goVal{}
			}
			return goVal{v: constant.BinaryOp(x.v, op, y.v), t: t, strLen: x.strLen + y.strLen, ok: true}
		}
		if t.class == cBool {
			return goVal{}
		}
	case token.SUB, token.MUL, token.QUO:
		if t.class == cBool || t.class == cString {
			return goVal{}
		}
	case token.REM, token.AND, token.OR, token.XOR, token.AND_NOT:
		if t.class != cInt && t.class != cRune {
			return goVal{}
		}
	default:
		return goVal{}
	}
	if (op == token.QUO || op == token.REM) && constant.Sign(y.v) == 0 {
		return goVal{} // a division by zero, which the compiler refuses
	}
	if op == token.QUO && (t.class == cInt || t.class == cRune) {
		op = token.QUO_ASSIGN // go/constant's integer division
	}
	return checked(goVal{v: constant.BinaryOp(x.v, op, y.v), t: t, ok: true})
}

// convert is v given the type t: a number must be one t holds, rounded to
// a float type's precision, and a bool or a string stays one.
func convert(v goVal, t goType) goVal {
	if !v.ok {
		return goVal{}
	}
	numeric := func(c uint8) bool { return c == cInt || c == cRune || c == cFloat }
	switch {
	case t.class == cBool && v.t.class == cBool, t.class == cString && v.t.class == cString:
		v.t = t
		return v
	case t.class == cInt && numeric(v.t.class):
		if t.bits != 32 || !t.signed {
			// const I int = '0' is the number 48; a type over int32 or
			// rune, as type Delim rune, keeps the character.
			v.lit, v.char = "", false
		}
		v.v = constant.ToInt(v.v)
	case t.class == cFloat && numeric(v.t.class):
		if v.char {
			v.lit, v.char = "", false
		}
		v.v = constant.ToFloat(v.v)
	default:
		return goVal{}
	}
	v.t = t
	return checked(v)
}

// checked is v, or not literal when its type does not hold it: an integer
// past its type's range, or for an untyped one past maxIntBits, and a float
// that overflows its type, which v is rounded to.
func checked(v goVal) goVal {
	switch {
	case !v.ok || v.v.Kind() == constant.Unknown:
		return goVal{}
	case v.t.class == cInt || v.t.class == cRune:
		if v.v.Kind() != constant.Int {
			return goVal{}
		}
		if v.t.name == "" {
			v.ok = constant.BitLen(v.v) <= maxIntBits
			return v
		}
		lo, hi := constant.MakeInt64(0), constant.Shift(constant.MakeInt64(1), token.SHL, uint(v.t.bits))
		if v.t.signed {
			hi = constant.Shift(constant.MakeInt64(1), token.SHL, uint(v.t.bits-1))
			lo = constant.UnaryOp(token.SUB, hi, 0)
		}
		v.ok = constant.Compare(v.v, token.GEQ, lo) && constant.Compare(v.v, token.LSS, hi)
	case v.t.class == cFloat && v.t.name != "":
		f, _ := constant.Float64Val(v.v)
		if v.t.bits == 32 {
			f32, _ := constant.Float32Val(v.v)
			f = float64(f32)
		}
		if math.IsInf(f, 0) {
			return goVal{}
		}
		v.v = constant.MakeFloat64(f) // a literal keeps its text: 0.10 is 0.10
	case v.t.class == cFloat:
		v.ok = v.v.Kind() == constant.Int || v.v.Kind() == constant.Float
	}
	return v
}
