package rule

import (
	"strconv"

	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/operators"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

// sourceKind says what a source is.
type sourceKind uint8

const (
	// literalSource is a list or map literal, e.
	literalSource sourceKind = iota
	// listSource is the list an identifier no comprehension binds names:
	// an extractor's values. e is the identifier.
	listSource
	// memberSource is one member of the list a listSource names. e is the
	// identifier.
	memberSource
	// subsetSource is a list of some members of a comprehension's range,
	// the result of filter() or of map(x, x). e is the comprehension.
	subsetSource
	// opaqueSource is anything else, a value the tracer does not follow.
	// e is nil.
	opaqueSource
)

// source is one value an expression may stand for, as a tracer finds it.
type source struct {
	kind sourceKind
	e    celast.Expr
}

// opaque is the sources of an expression the tracer does not follow.
var opaque = []source{{kind: opaqueSource}}

// tracer follows a name in a checked expression back to the values it
// stands for: a cel.bind name to the value it binds, a comprehension
// variable to each member of its range, and a field or an index of a list
// or map literal to the member it selects. The sources of each access path,
// a name and the fields and literal indexes read from it, are traced once
// and kept, and so are the members of each comprehension's range. So a name
// read many times, x.a read under x many times, or a chain of names that
// each read the last several times, costs one trace per path and one
// expansion per comprehension.
type tracer struct {
	checked *celast.AST
	nodes   map[int64]celast.NavigableExpr
	// memo holds the sources of each access path, by its key.
	memo map[string][]source
	// keys holds each node's path key, by its ID.
	keys map[int64]string
	// members holds the sources of a member of each comprehension's range,
	// by the range's path key (rangeKey), so comprehensions over one path
	// share one expansion.
	members map[string][]source
	// visits is the number of nodes whose sources were traced.
	visits int
	// work is the number of sources the tracer and its callers handled: each
	// one added to a set and each one a caller looked at.
	work int
	// budget is the most work the tracer does; past it, it traces nothing
	// more, and the expression does not load (MaxTraceWork).
	budget int
}

// exceeded reports whether the tracer has done more work than its budget.
// Its results are then incomplete, and the caller refuses the expression.
func (t *tracer) exceeded() bool { return t.work > t.budget }

func newTracer(checked *celast.AST) *tracer {
	t := &tracer{checked: checked, nodes: map[int64]celast.NavigableExpr{}, memo: map[string][]source{}, keys: map[int64]string{}, members: map[string][]source{}, budget: MaxTraceWork}
	for _, n := range celast.MatchDescendants(celast.NavigateAST(checked), func(celast.NavigableExpr) bool { return true }) {
		t.nodes[n.ID()] = n
	}
	return t
}

// sources is every value e may stand for, each once. It holds an opaque
// source when some path through e reaches a value the tracer does not
// follow, so a caller that needs every value knows when it has them.
func (t *tracer) sources(e celast.Expr) []source {
	if t.exceeded() {
		return opaque
	}
	e = t.unbound(e)
	key := t.key(e)
	if s, ok := t.memo[key]; ok {
		return s
	}
	// A path reached again while its own sources are traced stands for
	// nothing the tracer can name. Scoping keeps the trace from coming
	// back to a path, so this only guards the recursion.
	t.memo[key] = opaque
	t.visits++
	s := t.trace(e)
	t.memo[key] = s
	return s
}

// unbound is e, or for a cel.bind name the value it binds, followed until
// it is no such name.
func (t *tracer) unbound(e celast.Expr) celast.Expr {
	for e.Kind() == celast.IdentKind {
		c, ok := binder(t.nodes[e.ID()], e.AsIdent())
		if !ok || c.AccuVar() != e.AsIdent() {
			break
		}
		e = c.AccuInit()
	}
	return e
}

// key names the access path e reads, so that two nodes reading one path
// share one trace: a comprehension variable by its range, a free name by
// itself, a field or a literal index by its operand's path and the key,
// and any other expression by its node. Callers pass e through unbound
// first, and so does key for an operand.
func (t *tracer) key(e celast.Expr) string {
	if k, ok := t.keys[e.ID()]; ok {
		return k
	}
	k := "#" + strconv.FormatInt(e.ID(), 10)
	switch e.Kind() {
	case celast.IdentKind:
		if c, ok := binder(t.nodes[e.ID()], e.AsIdent()); !ok {
			k = "$" + e.AsIdent()
		} else if c.IterVar() == e.AsIdent() {
			k = t.rangeKey(c)
		}
	case celast.SelectKind:
		if sel := e.AsSelect(); !sel.IsTestOnly() {
			k = t.key(t.unbound(sel.Operand())) + "." + sel.FieldName()
		}
	case celast.CallKind:
		call := e.AsCall()
		if call.FunctionName() == operators.Index && len(call.Args()) == 2 && call.Args()[1].Kind() == celast.LiteralKind {
			switch v := call.Args()[1].AsLiteral().(type) {
			case types.String:
				k = t.key(t.unbound(call.Args()[0])) + "[" + strconv.Quote(string(v)) + "]"
			case types.Int:
				k = t.key(t.unbound(call.Args()[0])) + "[" + strconv.FormatInt(int64(v), 10) + "]"
			}
		}
	default:
	}
	t.keys[e.ID()] = k
	return k
}

func (t *tracer) trace(e celast.Expr) []source {
	switch e.Kind() {
	case celast.ListKind, celast.MapKind:
		return []source{{kind: literalSource, e: e}}
	case celast.IdentKind:
		name := e.AsIdent()
		c, ok := binder(t.nodes[e.ID()], name)
		switch {
		case !ok && t.checked.GetType(e.ID()).Kind() == types.ListKind:
			return []source{{kind: listSource, e: e}}
		case ok && c.AccuVar() == name:
			return t.sources(c.AccuInit())
		case ok && c.IterVar() == name:
			return t.member(c)
		}
	case celast.SelectKind:
		if sel := e.AsSelect(); !sel.IsTestOnly() {
			return t.selected(sel.Operand(), types.String(sel.FieldName()), false)
		}
	case celast.CallKind:
		if call := e.AsCall(); call.FunctionName() == operators.Index && len(call.Args()) == 2 {
			var key ref.Val
			if k := call.Args()[1]; k.Kind() == celast.LiteralKind {
				key = k.AsLiteral()
			}
			return t.selected(call.Args()[0], key, true)
		}
	case celast.ComprehensionKind:
		if keeps(e.AsComprehension()) {
			return []source{{kind: subsetSource, e: e}}
		}
	default:
		// A literal or a message stands for no value the tracer follows.
	}
	return opaque
}

// member is the sources of one member of c's range, expanded once for each
// comprehension however often its variable is read: without that, each read
// walked the range again, and the work grew as the cube of the expression,
// 4.8 s to load 4 KiB.
func (t *tracer) member(c celast.ComprehensionExpr) []source {
	if t.exceeded() {
		return opaque
	}
	id := t.rangeKey(c)
	if s, ok := t.members[id]; ok {
		return s
	}
	t.members[id] = opaque
	out := sourceSet{t: t}
	for _, s := range t.sources(c.IterRange()) {
		switch {
		case s.kind == literalSource && s.e.Kind() == celast.ListKind:
			for _, el := range s.e.AsList().Elements() {
				out.add(t.sources(el)...)
			}
		case s.kind == listSource:
			out.add(source{kind: memberSource, e: s.e})
		case s.kind == subsetSource:
			out.add(t.kept(s.e.AsComprehension())...)
		default:
			out.add(opaque...)
		}
	}
	t.members[id] = out.list
	return out.list
}

// rangeKey is the key of a member of c's range: @ and the range's path, so
// two comprehensions over one bound name, or one field of it, share one.
func (t *tracer) rangeKey(c celast.ComprehensionExpr) string {
	return "@" + t.key(t.unbound(c.IterRange()))
}

// kept is the sources of one member of the list c keeps of its range: a
// member of an extractor, which stands for every member since all of them
// have the same fields, or an extractor's whole list, which stands for every
// such list; and opaque for any other, since the comprehension may drop it.
func (t *tracer) kept(c celast.ComprehensionExpr) []source {
	out := sourceSet{t: t}
	for _, s := range t.member(c) {
		if s.kind == memberSource || s.kind == listSource {
			out.add(s)
		} else {
			out.add(opaque...)
		}
	}
	return out.list
}

// keeps reports whether c's result is a list of some members of its range,
// each whole: c starts from an empty list, returns it, and each step adds
// the variable itself, perhaps under a condition. That is filter(x, p),
// map(x, x) and map(x, p, x) as cel-go writes them, matched by shape rather
// than by the macro's name.
func keeps(c celast.ComprehensionExpr) bool {
	init, result := c.AccuInit(), c.Result()
	if c.IterVar2() != "" || init.Kind() != celast.ListKind || len(init.AsList().Elements()) != 0 ||
		result.Kind() != celast.IdentKind || result.AsIdent() != c.AccuVar() {
		return false
	}
	accu := func(e celast.Expr) bool { return e.Kind() == celast.IdentKind && e.AsIdent() == c.AccuVar() }
	step := c.LoopStep()
	if step.Kind() == celast.CallKind && step.AsCall().FunctionName() == operators.Conditional && len(step.AsCall().Args()) == 3 {
		if !accu(step.AsCall().Args()[2]) {
			return false
		}
		step = step.AsCall().Args()[1]
	}
	if step.Kind() != celast.CallKind || step.AsCall().FunctionName() != operators.Add || len(step.AsCall().Args()) != 2 {
		return false
	}
	added := step.AsCall().Args()[1]
	if !accu(step.AsCall().Args()[0]) || added.Kind() != celast.ListKind || len(added.AsList().Elements()) != 1 {
		return false
	}
	el := added.AsList().Elements()[0]
	return el.Kind() == celast.IdentKind && el.AsIdent() == c.IterVar()
}

// selected is the sources of the member key selects from operand, a field
// when index is false and an index when it is true. key is nil when the
// index is not a literal. A literal map without the key and a list literal
// shorter than the index add nothing, since reading them is an error at
// run time. Any index into an extractor's list is one of its members.
func (t *tracer) selected(operand celast.Expr, key ref.Val, index bool) []source {
	out := sourceSet{t: t}
	for _, s := range t.sources(operand) {
		switch {
		case s.kind == literalSource:
			member, found, known := lookup(s.e, key)
			switch {
			case !known:
				out.add(opaque...)
			case found:
				out.add(t.sources(member)...)
			}
		case s.kind == listSource && index:
			out.add(source{kind: memberSource, e: s.e})
		case s.kind == subsetSource && index:
			out.add(t.kept(s.e.AsComprehension())...)
		default:
			out.add(opaque...)
		}
	}
	return out.list
}

// lookup is the member key selects from lit, a list or map literal. known
// is false when the literal cannot say: the key is not a literal, or the
// map has a key that is not one. found is false when the literal has no
// such member.
func lookup(lit celast.Expr, key ref.Val) (member celast.Expr, found, known bool) {
	if key == nil {
		return nil, false, false
	}
	if lit.Kind() == celast.ListKind {
		i, ok := key.(types.Int)
		if !ok {
			return nil, false, false
		}
		els := lit.AsList().Elements()
		if i < 0 || int64(i) >= int64(len(els)) {
			return nil, false, true
		}
		return els[i], true, true
	}
	known = true
	for _, entry := range lit.AsMap().Entries() {
		k := entry.AsMapEntry().Key()
		if k.Kind() != celast.LiteralKind {
			known = false
			continue
		}
		if k.AsLiteral().Equal(key) == types.True {
			return entry.AsMapEntry().Value(), true, true
		}
	}
	return nil, false, known
}

// targets is what a followed name, field or index e reaches one step on,
// for a pass that looks at each: the value a cel.bind name binds, the
// elements of the list literals a comprehension variable runs over, or the
// member a field or a literal index selects from a list or map literal. It
// is false when e is none of these, or when some value e may stand for is
// not such a literal.
func (t *tracer) targets(e celast.Expr) ([]celast.Expr, bool) {
	var (
		over []source
		key  ref.Val
		elem bool // over's list literals give their elements
	)
	switch e.Kind() {
	case celast.IdentKind:
		c, ok := binder(t.nodes[e.ID()], e.AsIdent())
		switch {
		case ok && c.AccuVar() == e.AsIdent():
			return []celast.Expr{c.AccuInit()}, true
		case ok && c.IterVar() == e.AsIdent():
			over, elem = t.sources(c.IterRange()), true
		default:
			return nil, false
		}
	case celast.SelectKind:
		sel := e.AsSelect()
		if sel.IsTestOnly() {
			return nil, false
		}
		over, key = t.sources(sel.Operand()), types.String(sel.FieldName())
	case celast.CallKind:
		call := e.AsCall()
		if call.FunctionName() != operators.Index || len(call.Args()) != 2 || call.Args()[1].Kind() != celast.LiteralKind {
			return nil, false
		}
		over, key = t.sources(call.Args()[0]), call.Args()[1].AsLiteral()
	default:
		return nil, false
	}
	var to []celast.Expr
	for _, s := range over {
		t.work++
		if t.exceeded() || s.kind != literalSource {
			return nil, false
		}
		if elem {
			if s.e.Kind() != celast.ListKind {
				return nil, false
			}
			to = append(to, s.e.AsList().Elements()...)
			continue
		}
		member, found, known := lookup(s.e, key)
		if !known {
			return nil, false
		}
		if found {
			to = append(to, member)
		}
	}
	return to, true
}

// sourceSet is a list of sources, each once, built for t, whose work it
// counts.
type sourceSet struct {
	t    *tracer
	list []source
	seen map[sourceKey]bool
}

// sourceKey names a source by its node's ID, since two celast.Expr values
// for one node need not compare equal.
type sourceKey struct {
	kind sourceKind
	id   int64
}

func (s *sourceSet) add(sources ...source) {
	if s.seen == nil {
		s.seen = map[sourceKey]bool{}
	}
	for _, src := range sources {
		s.t.work++
		if s.t.exceeded() {
			return
		}
		key := sourceKey{kind: src.kind}
		if src.e != nil {
			key.id = src.e.ID()
		}
		if !s.seen[key] {
			s.seen[key] = true
			s.list = append(s.list, src)
		}
	}
}
