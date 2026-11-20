package hcl

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	hclv2 "github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/f-klement/errata/internal/extract/core/lineend"
	"github.com/f-klement/errata/internal/plugin/wire"
)

// read is the values s's path selects in f, in the file's order, each at
// the line its expression starts on. A file that does not parse is an
// error naming the file and the line, and so is a path that selects a
// block, an object or a list, which hold no value to compare. So is a
// value past room, the values the request's earlier files left of
// wire.MaxValues: read stops there, before it holds more. A file that may
// make more tokens than maxBytes allows, one for every bytesPerToken, or
// that is nested more than maxDepth levels deep, that holds a number
// longer than maxNumber bytes, or a template that costs more than
// max_bytes << joinShift to join, is an error before it is parsed.
func (s spec) read(f wire.File, room int, maxBytes int64) ([]wire.Value, error) {
	w := &walk{s: s, f: f, room: room, values: []wire.Value{}}
	if err := w.bounded(maxBytes); err != nil {
		return nil, err
	}
	parsed, diags := hclsyntax.ParseConfig(f.Data, f.Path, hclv2.InitialPos)
	if diags.HasErrors() {
		return nil, w.parseError(diags)
	}
	w.body(parsed.Body.(*hclsyntax.Body), s.steps, nil)
	return w.values, w.err
}

// bounded is an error naming the first bound w's file passes that the
// parser would not hold it to: the tokens it may make, counted before it
// is lexed, then the length of each number, the depth it nests and what
// each template costs to join.
func (w *walk) bounded(maxBytes int64) error {
	if n, limit := tokens(w.f.Data), maxBytes/bytesPerToken; int64(n) > limit {
		return fmt.Errorf("%s: may make %d tokens, past the limit of %d, one for every %d bytes of max_bytes; raise max_bytes to read a file this dense", w.f.Path, n, limit, bytesPerToken)
	}
	lexed, _ := hclsyntax.LexConfig(w.f.Data, w.f.Path, hclv2.InitialPos)
	if off, long := longAt(lexed); long {
		return fmt.Errorf("%s:%d: a number longer than %d bytes, the limit, since parsing one takes time that grows with the square of its length", w.f.Path, w.line(off), maxNumber)
	}
	if off, deep := deepAt(lexed); deep {
		return fmt.Errorf("%s:%d: nested more than %d levels deep, the limit, counting each bracket, quote, template sequence, directive, minus sign, not, condition and full splat", w.f.Path, w.line(off), maxDepth)
	}
	if off, long := joinAt(lexed, maxBytes<<joinShift); long {
		return fmt.Errorf("%s:%d: a template that costs more than %d to join, %d for every byte of max_bytes, counting its joins times its parts and each run of literals' parts times bytes over %d; each heredoc line, each ${ and %%{, and the text up to each $${ or %%%%{ is a part; raise max_bytes to read it", w.f.Path, w.line(off), maxBytes<<joinShift, 1<<joinShift, bytesPerMove)
	}
	return nil
}

// walk is one file's read: the values found so far and the first error,
// after which the walk stops.
type walk struct {
	s      spec
	f      wire.File
	room   int
	starts []int // where each line of f starts, as errata counts lines; made when first needed
	values []wire.Value
	err    error
}

// along is where a walk stands on its way down the path: the name the
// next value is set under, whether the file computes that name, and the
// steps taken, each [*] written as the name or index it matched.
type along struct {
	key      string
	computed bool
	taken    []step
}

// then is a, one step further on.
func (a along) then(st step) along {
	a.taken = append(slices.Clip(a.taken), st)
	return a
}

// body walks steps from b, a file's body or a block's: a name selects the
// attribute and the blocks of that name, [*] every attribute, each in the
// file's order.
func (w *walk) body(b *hclsyntax.Body, steps []step, a []step) {
	st := steps[0]
	for _, child := range children(b) {
		switch c := child.(type) {
		case *hclsyntax.Attribute:
			if st.kind == stepAll || st.named(c.Name) {
				w.expr(c.Expr, steps[1:], along{key: c.Name, taken: a}.then(step{kind: stepName, name: c.Name}))
			}
		case *hclsyntax.Block:
			if st.named(c.Type) {
				w.block(c, steps[1:], append(slices.Clip(a), st))
			}
		}
		if w.err != nil {
			return
		}
	}
}

// children are b's attributes and blocks in the file's order.
func children(b *hclsyntax.Body) []hclsyntax.Node {
	out := make([]hclsyntax.Node, 0, len(b.Attributes)+len(b.Blocks))
	for _, attr := range b.Attributes {
		out = append(out, attr)
	}
	for _, block := range b.Blocks {
		out = append(out, block)
	}
	slices.SortFunc(out, func(x, y hclsyntax.Node) int { return x.Range().Start.Byte - y.Range().Start.Byte })
	return out
}

// block walks steps from b, whose type the step before matched: one step
// for each of its labels, which names the label or is [*], and the rest in
// its body. A path that ends at the block, or among its labels, selects
// the block, which holds no value.
func (w *walk) block(b *hclsyntax.Block, steps []step, a []step) {
	for i, label := range b.Labels {
		if i == len(steps) {
			w.fail(b.TypeRange.Start.Byte, "selects the block %s, which holds no value; name an attribute in it", blockText(b))
			return
		}
		if st := steps[i]; st.kind != stepAll && !st.named(label) {
			return
		}
		a = append(slices.Clip(a), step{kind: stepName, name: label})
	}
	if len(steps) == len(b.Labels) {
		w.fail(b.TypeRange.Start.Byte, "selects the block %s, which holds no value; name an attribute in it", blockText(b))
		return
	}
	w.body(b.Body, steps[len(b.Labels):], a)
}

// blockText is b's type and labels as HCL writes them.
func blockText(b *hclsyntax.Block) string {
	parts := []string{b.Type}
	for _, l := range b.Labels {
		parts = append(parts, strconv.Quote(l))
	}
	return strings.Join(parts, " ")
}

// expr walks steps from e, an attribute's value or a part of one: a name
// or [*] into an object's items, an index or [*] into a list's elements.
// When steps are left at a value the file computes, that value is the one
// the path means, and it is returned marked not literal; a literal holds
// nothing further.
func (w *walk) expr(e hclsyntax.Expression, steps []step, a along) {
	if len(steps) == 0 {
		w.value(e, a)
		return
	}
	switch e := e.(type) {
	case *hclsyntax.ObjectConsExpr:
		w.object(e, steps, a)
	case *hclsyntax.TupleConsExpr:
		w.list(e, steps, a)
	default:
		if _, ok := w.literal(e); !ok {
			w.add(e, w.text(e), false, a)
		}
	}
}

// object walks steps into o's items whose key the first step names, or
// every item for [*]. A key the file computes matches only [*].
func (w *walk) object(o *hclsyntax.ObjectConsExpr, steps []step, a along) {
	for _, item := range o.Items {
		key, ok := w.key(item.KeyExpr)
		if steps[0].kind == stepAll || ok && steps[0].named(key) {
			w.expr(item.ValueExpr, steps[1:], along{key: key, computed: a.computed || !ok, taken: a.taken}.then(step{kind: stepName, name: key}))
		}
	}
}

// key is the text of an object item's key, and whether it is literal: a
// bare name or a string the file wrote. A key in parentheses, or one the
// file computes, is its text as written.
func (w *walk) key(e hclsyntax.Expression) (string, bool) {
	k, ok := e.(*hclsyntax.ObjectConsKeyExpr)
	if !ok || k.ForceNonLiteral {
		return w.text(e), false
	}
	if kw := hclv2.ExprAsKeyword(k.Wrapped); kw != "" {
		return kw, true
	}
	return w.literal(k.Wrapped)
}

// list walks steps into l's element the first step indexes, or every
// element for [*]; the elements keep the key of their list.
func (w *walk) list(l *hclsyntax.TupleConsExpr, steps []step, a along) {
	for i, e := range l.Exprs {
		if steps[0].kind == stepAll || steps[0].kind == stepIndex && steps[0].index == i {
			w.expr(e, steps[1:], a.then(step{kind: stepIndex, index: i}))
		}
	}
}

// value adds e, where the path ends, as a value: its text, literal or as
// the file wrote it. An object or a list holds no value to compare.
func (w *walk) value(e hclsyntax.Expression, a along) {
	switch e.(type) {
	case *hclsyntax.ObjectConsExpr:
		w.fail(e.Range().Start.Byte, "selects an object, which is no value to compare; name one of its keys, or add [*] for every value")
		return
	case *hclsyntax.TupleConsExpr:
		w.fail(e.Range().Start.Byte, "selects a list, which is no value to compare; add [*] for every element, or an index such as [0]")
		return
	}
	text, ok := w.literal(e)
	w.add(e, text, ok && !a.computed, a)
}

// literal is the text of e and whether it is literal: a string with no
// interpolation or directive, its escapes read as HCL reads them; a
// number, true, false or null as the file wrote it, a minus sign
// included. Anything else is its text as the file wrote it, not literal.
func (w *walk) literal(e hclsyntax.Expression) (string, bool) {
	switch e := e.(type) {
	case *hclsyntax.LiteralValueExpr:
		if e.Val.Type() == cty.String {
			return e.Val.AsString(), true
		}
		return w.text(e), true
	case *hclsyntax.TemplateExpr:
		if text, ok := templateText(e); ok {
			return text, true
		}
	case *hclsyntax.UnaryOpExpr:
		if negative(e) {
			return "-" + w.text(e.Val), true
		}
	}
	return w.text(e), false
}

// negative reports whether u is a minus sign before a number the file
// wrote, as -1 is.
func negative(u *hclsyntax.UnaryOpExpr) bool {
	n, ok := u.Val.(*hclsyntax.LiteralValueExpr)
	return ok && u.Op == hclsyntax.OpNegate && n.Val.Type() == cty.Number
}

// templateText is the text of t when every part of it is a string the file
// wrote, as a quoted string or a heredoc with no interpolation or
// directive is.
func templateText(t *hclsyntax.TemplateExpr) (string, bool) {
	var b strings.Builder
	for _, p := range t.Parts {
		lit, ok := p.(*hclsyntax.LiteralValueExpr)
		if !ok || lit.Val.Type() != cty.String {
			return "", false
		}
		b.WriteString(lit.Val.AsString())
	}
	return b.String(), true
}

// text is e as the file wrote it.
func (w *walk) text(e hclsyntax.Expression) string {
	r := e.Range()
	return string(w.f.Data[r.Start.Byte:r.End.Byte])
}

// add appends the value of e with text, at the line e starts on, unless the
// file already gave room values.
func (w *walk) add(e hclsyntax.Expression, text string, literal bool, a along) {
	line := w.line(e.Range().Start.Byte)
	if len(w.values) == w.room {
		w.err = fmt.Errorf("%s:%d: the files hold more than %d values, the limit; narrow the path or the files", w.f.Path, line, wire.MaxValues)
		return
	}
	v := wire.Value{Text: &text, File: w.f.Path, Line: line, NotLiteral: !literal, Key: &a.key}
	if w.s.fans() {
		cond := pathText(a.taken)
		v.Condition = &cond
	}
	w.values = append(w.values, v)
}

// fail stops the walk with what the path selects at byte off.
func (w *walk) fail(off int, format string, args ...any) {
	w.err = fmt.Errorf("%s:%d: the path %s %s", w.f.Path, w.line(off), pathText(w.s.steps), fmt.Sprintf(format, args...))
}

// parseError is the first error of diags, at the line errata counts for
// its place, or the file's first line when it names none.
func (w *walk) parseError(diags hclv2.Diagnostics) error {
	d := diags[slices.IndexFunc(diags, func(d *hclv2.Diagnostic) bool { return d.Severity == hclv2.DiagError })]
	line := 1
	if d.Subject != nil {
		line = w.line(d.Subject.Start.Byte)
	}
	msg := d.Summary
	if d.Detail != "" {
		msg += ": " + d.Detail
	}
	return fmt.Errorf("%s:%d: %s", w.f.Path, line, msg)
}

// line is the line, counted from 1 as errata counts lines, that holds byte
// off of the file.
func (w *walk) line(off int) int {
	if w.starts == nil {
		w.starts = lineend.Starts(w.f.Data)
	}
	n, _ := slices.BinarySearch(w.starts, min(off, len(w.f.Data))+1)
	return n
}
