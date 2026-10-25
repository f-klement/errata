package shell

import (
	"bytes"
	"cmp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/f-klement/errata/internal/extract/core"
)

// maxTreeDepth is the deepest the walk descends into a statement's syntax
// tree. The parser builds a pipeline, a list of && and ||, and arithmetic
// such as 1+1+1 in a loop, but as a tree one level deeper for each command
// or term, and syntax.Walk recurses on it: without the limit, 32 MiB of +1
// passed Go's 1 GB stack limit. What the parser nests itself the guard
// bounds, to a tree at most 12800 deep (an array of $( ),
// TestGuardFramesPerLevel), so the limit refuses only those loops.
var maxTreeDepth = 20000

// found is one value of the spec's variable, before it is visited. from
// and to are where its text stands as written, which is the value's once an
// attribute after it makes the shell compute it (emit).
type found struct {
	text     string
	off      int
	from, to int
	computed bool
}

// varState is what the walk knows of a variable at the point it has
// reached: its value, when the script gave it a literal one (known), or
// nothing.
type varState struct {
	value string
	known bool
}

// assigned is a value a declaration gives a variable, kept until the
// declaration's arguments are all read, since the shell expands them all
// before it assigns any. readonly marks the variable read only once it is
// assigned.
type assigned struct {
	name     string
	st       varState
	readonly bool
}

// pendingDoc is a here-document whose body the parser had not read when it
// gave the walk its statement: a statement before ; or & on its line comes
// before the line's here-documents. off is its statement's offset, for a
// message.
type pendingDoc struct {
	r   *syntax.Redirect
	off int
}

// walker finds the values of one variable in the statements of a script.
// It keeps them until the script is read, since an attribute that makes the
// shell compute the variable, such as declare -i, may come after a value.
type walker struct {
	name string
	lang syntax.LangVariant
	data []byte
	m    *core.Meter
	// limit is the extractor's max_bytes: no value longer can be returned,
	// so a reference that would make one is not resolved.
	limit int
	// stack holds the nodes from the statement down to the one visited, and
	// quotes the quoting each of them stands in, the last the quoting of
	// the node being visited.
	stack  []syntax.Node
	quotes []quoting
	// base is the offset of the statement the walk reads, for a message.
	base int
	// declArgs marks the arguments of a declaration that come after an
	// option that changes the value, or a word errata cannot read as an
	// option or a name. Each argument is visited once, so the marks of a
	// declaration nested in another's value leave the outer ones as they
	// are; they are cleared for each statement.
	declArgs map[*syntax.Assign]bool
	// transformed is set once the script gives the variable an attribute
	// that makes the shell compute its value: every value is then not
	// literal.
	transformed bool
	found       []found
	bytes       int64
	err         error

	// vars is what the walk knows of each variable at the point it has
	// reached, for a reference $Y or ${Y} to resolve; varBytes is the
	// bytes of the known values. opaque is set once the script has run a
	// statement that is not quiet or does not keep the variables (allow.go):
	// no reference resolves after it. readonly holds the variables made read
	// only, whose value a later assignment does not change.
	vars     map[string]varState
	varBytes int
	opaque   bool
	readonly map[string]bool
	// deferred are the values of the declaration being read.
	deferred []assigned
	pending  []pendingDoc
	// prevEnd is the offset where the statement walked last ends, before
	// any here-document body of its own.
	prevEnd int
}

func newWalker(name string, lang syntax.LangVariant, data []byte, m *core.Meter, limit int) *walker {
	return &walker{name: name, lang: lang, data: data, m: m, limit: limit, declArgs: map[*syntax.Assign]bool{},
		vars: map[string]varState{}, readonly: map[string]bool{}}
}

// walk visits every node of one statement, after the here-documents of the
// statements before it, which the parser has read by now. A statement that
// is not quiet ends resolution before it is read, and one that does not
// keep the variables after.
func (w *walker) walk(stmt *syntax.Stmt) {
	w.flushDocs(offset(stmt.Position))
	if w.err != nil {
		return
	}
	clear(w.declArgs)
	w.stack, w.quotes, w.base = w.stack[:0], w.quotes[:0], offset(stmt.Position)
	isQuiet := quiet(stmt)
	if !isQuiet {
		w.opaque = true
	}
	syntax.Walk(stmt, w.visit)
	if !isQuiet || !keeps(stmt) {
		w.opaque = true
	}
	w.prevEnd = offset(stmt.End())
	w.release(stmt)
}

// flushDocs walks the pending here-documents once the parser has read them,
// before the statement at offset next. The parser reads all the bodies of a
// line at once, at the newline that ends it, and leaves an empty body nil.
// So the pending documents are read when the last of them has a body, or
// when the text before next holds the newline that ends their line; then a
// document with no body is empty. Each document is released once walked.
func (w *walker) flushDocs(next int) {
	if len(w.pending) == 0 || w.pending[len(w.pending)-1].r.Hdoc == nil && !w.lineEnds(next) {
		return
	}
	docs := w.pending
	w.pending = nil
	for _, d := range docs {
		if d.r.Hdoc != nil && w.err == nil {
			w.stack, w.quotes, w.base = append(w.stack[:0], d.r), append(w.quotes[:0], unquoted), d.off
			syntax.Walk(d.r.Hdoc, w.visit)
		}
		if w.err == nil {
			w.stack = w.stack[:0]
			syntax.Walk(d.r.Word, w.clearNode)
			if d.r.Hdoc != nil {
				syntax.Walk(d.r.Hdoc, w.clearNode)
			}
		}
	}
	clear(docs)
}

// lineEnds reports whether the text between the statement walked last and
// offset next ends a line as the parser reads it: blanks or a comment, then
// a newline. Anything else, such as a backslash that continues the line,
// keeps the documents pending, which is safe: they are read with the next
// document that has a body.
func (w *walker) lineEnds(next int) bool {
	gap := w.data[min(w.prevEnd, len(w.data)):min(max(next, w.prevEnd), len(w.data))]
	i := bytes.IndexByte(gap, '\n')
	if i < 0 {
		return false
	}
	before := bytes.TrimLeft(gap[:i], " \t")
	return len(before) == 0 || before[0] == '#'
}

// release empties the syntax of a statement once it is walked. The parser
// hands out words and literals from batches of 32 and keeps the rest of a
// batch, so the words of a statement read keep its whole tree reachable
// while the next ones are parsed: 8 statements of $a$a at the statement
// limit held 3.7 GB. With each word's parts and each literal's text
// cleared, a batch holds only empty structs. The parser also holds the
// statement it gave until the walk returns, so each statement's command is
// cut loose too: a group of a;a;a at the limit stayed live at 474 MiB
// through the next statement's parse. A here-document still pending keeps
// its redirect, whose word the parser reads the body by, so it holds only
// itself and not its line. A read that failed releases nothing: syntax.Walk
// recurses as deep as the tree, and the walk's refusal of a deep tree is
// what bounds that.
func (w *walker) release(stmt *syntax.Stmt) {
	if w.err != nil {
		return
	}
	w.stack = w.stack[:0]
	syntax.Walk(stmt, w.clearNode)
}

// clearNode is release's callback: it clears a node once its children are
// walked, when syntax.Walk calls it with nil.
func (w *walker) clearNode(n syntax.Node) bool {
	if r, ok := n.(*syntax.Redirect); ok && r.Hdoc == nil && (r.Op == syntax.Hdoc || r.Op == syntax.DashHdoc) {
		return false
	}
	if n != nil {
		w.stack = append(w.stack, n)
		return true
	}
	switch n := w.stack[len(w.stack)-1].(type) {
	case *syntax.Stmt:
		n.Cmd, n.Redirs = nil, nil
	case *syntax.Word:
		clear(n.Parts)
		n.Parts = nil
	case *syntax.Lit:
		n.Value = ""
	case *syntax.DblQuoted:
		clear(n.Parts)
		n.Parts = nil
	case *syntax.SglQuoted:
		n.Value = ""
	}
	w.stack = w.stack[:len(w.stack)-1]
	return true
}

// visit is syntax.Walk's callback: a node, or nil when the children of the
// last node it descended into are done. A declaration, and a posix export
// or readonly, assign once their arguments are all read.
func (w *walker) visit(n syntax.Node) bool {
	if n == nil {
		w.closeNode(w.parent())
		w.stack, w.quotes = w.stack[:len(w.stack)-1], w.quotes[:len(w.quotes)-1]
		return true
	}
	if w.err != nil {
		return false
	}
	if w.err = w.m.Charge(1); w.err != nil {
		return false
	}
	if len(w.stack) == maxTreeDepth {
		// The line is the statement's: a node's own Pos may walk down the
		// whole tree below it, one call a level, as BinaryArithm's does.
		w.err = core.LineError(lineAt(w.data, w.stmtOffset()), "the script's syntax is more than %d levels deep, the limit, as a pipeline, a list of && and || or a sum of about %d commands or terms makes it; a regex extractor can still read the file", maxTreeDepth, maxTreeDepth/2)
		return false
	}
	switch n := n.(type) {
	case *syntax.DeclClause:
		w.decl(n)
	case *syntax.CallExpr:
		w.call(n)
	case *syntax.Assign:
		w.assign(n)
	case *syntax.ParamExp:
		w.param(n)
	case *syntax.Redirect:
		if n.Hdoc == nil && (n.Op == syntax.Hdoc || n.Op == syntax.DashHdoc) {
			w.pending = append(w.pending, pendingDoc{r: n, off: w.base})
		}
	}
	w.quotes = append(w.quotes, w.quotingOf(n))
	w.stack = append(w.stack, n)
	return true
}

// closeNode applies the values a declaration or a posix export, readonly
// or local deferred, once its arguments are read.
func (w *walker) closeNode(n syntax.Node) {
	_, decl := n.(*syntax.DeclClause)
	_, call := n.(*syntax.CallExpr)
	if decl || call {
		w.applyDeferred()
	}
}

// stmtOffset is the offset of the nearest statement on the stack, whose
// position is a field, or else of the statement read.
func (w *walker) stmtOffset() int {
	for i := len(w.stack) - 1; i >= 0; i-- {
		if s, ok := w.stack[i].(*syntax.Stmt); ok {
			return offset(s.Position)
		}
	}
	return w.base
}

// keep records a value at offset off, written from one offset to another,
// while the values found stay within what any extractor may return, so that
// the visits after the read meet the extractor's limits without the walker
// holding more than they allow.
func (w *walker) keep(text string, off, from, to int, computed bool) {
	if w.full() {
		return
	}
	if w.err = w.m.Charge(len(text)); w.err != nil {
		return
	}
	w.bytes += int64(len(text))
	w.found = append(w.found, found{text: text, off: off, from: from, to: to, computed: computed})
}

// full reports whether the values kept are past every extractor's limits,
// so the walker need not read another: nested defaults return each one
// whole, so a script of 32 MiB can hold gigabytes of them.
func (w *walker) full() bool {
	return len(w.found) > core.MaxValues || w.bytes > core.MaxMaxBytes
}

// emit visits the values found in file order, which the walk's is not: it
// reads a command before its redirects, and a here-document after the
// statements on its line. The lines come from one pass of a cursor over
// the sorted offsets, which are distinct: cmp.Compare leaves gremlins no
// boundary to flip, where < against <= could not differ. When an attribute makes the shell compute every value,
// a literal one is returned as written, not as its quotes, escapes and
// references read: $'\x01' and not the byte it stands for, which the script
// does not hold (FuzzShell found it). A literal value holds no expansion, so
// no other value, and the texts it adds come to at most the script's size.
func (w *walker) emit(visit func(*core.Entry) error) error {
	slices.SortStableFunc(w.found, func(a, b found) int { return cmp.Compare(a.off, b.off) })
	lines := core.NewLineCursor(w.data)
	e := core.Entry{Key: []byte(w.name)}
	for _, f := range w.found {
		text := f.text
		if w.transformed && !f.computed {
			text = w.source(f.from, f.to)
		}
		e.Value, e.Line, e.Computed = []byte(text), lines.At(f.off), f.computed || w.transformed
		if err := visit(&e); err != nil {
			return err
		}
	}
	return nil
}

// source is the script's text from one offset to another.
func (w *walker) source(from, to int) string {
	a := min(from, len(w.data))
	b := min(max(to, a), len(w.data))
	return string(w.data[a:b])
}

// offset is a position's offset in the script.
func offset(p syntax.Pos) int { return int(p.Offset()) }

// assign reads an assignment: the spec's variable's value is kept, and
// every variable's is recorded for the references after it. An array, an
// element (X[i]=v), an append (X+=v) and a value after an option that
// changes it are not literal; so is a value the shell expands. An
// assignment before a command sets the variable for that command alone, so
// the walk no longer knows it.
func (w *walker) assign(a *syntax.Assign) {
	if a.Name == nil {
		return
	}
	name := a.Name.Value
	d, inDecl := w.parent().(*syntax.DeclClause)
	if a.Naked {
		if inDecl && d.Variant.Value == "readonly" {
			w.readonly[name] = true
		}
		return
	}
	notLiteral := a.Append || a.Index != nil || w.declArgs[a]
	var text string
	var from, to int
	ok := true
	if a.Array != nil {
		from, to = offset(a.Array.Lparen), offset(a.Array.End())
		text, ok = w.source(from, to), false
	} else if a.Value != nil {
		from, to = offset(a.Value.Pos()), offset(a.Value.End())
		text, ok = w.literal(a.Value, unquoted, inDecl)
		if !ok || notLiteral {
			text = w.source(from, to)
		}
	}
	literal := ok && !notLiteral
	if name == w.name && !w.full() {
		w.keep(text, offset(a.Pos()), from, to, !literal)
	}
	st := varState{value: text, known: literal}
	if inDecl {
		w.deferred = append(w.deferred, assigned{name, st, d.Variant.Value == "readonly"})
		return
	}
	if c, ok := w.parent().(*syntax.CallExpr); ok && len(c.Args) != 0 {
		st = varState{}
	}
	w.set(name, st)
}

// set records what the walk knows of a variable, once nothing has ended
// resolution. A read only variable, or a value past the bytes the walk keeps
// of values, is unknown from then on.
func (w *walker) set(name string, st varState) {
	if w.opaque {
		return
	}
	old := w.vars[name]
	w.varBytes -= len(old.value)
	if w.readonly[name] || w.varBytes+len(st.value) > w.limit {
		st = varState{}
	}
	if !st.known {
		st.value = ""
	}
	w.varBytes += len(st.value)
	w.vars[name] = st
}

// applyDeferred records the values of the declaration just read.
func (w *walker) applyDeferred() {
	for _, a := range w.deferred {
		w.set(a.name, a.st)
		if a.readonly {
			w.readonly[a.name] = true
		}
	}
	w.deferred = w.deferred[:0]
}

// resolve is the value of a reference $Y or ${Y} where it stands, and
// whether the walk knows it: every statement before the reference, and its
// own, kept the variables as the walk records them (allow.go), the last of
// them that assigned Y gave it a literal value, and the shell does not own
// Y.
func (w *walker) resolve(name string) (string, bool) {
	if w.opaque || owned(name) {
		return "", false
	}
	st := w.vars[name]
	if !st.known {
		return "", false
	}
	if w.err = w.m.Charge(len(st.value)); w.err != nil {
		return "", false
	}
	return st.value, true
}

// param keeps the default a parameter expansion of the variable gives:
// ${X:-v}, ${X-v}, ${X:=v} and ${X=v}, read in the quoting the expansion is
// in.
func (w *walker) param(p *syntax.ParamExp) {
	if p.Param == nil || p.Exp == nil || p.Param.Value != w.name || w.full() {
		return
	}
	if p.Index != nil || p.Excl || p.Length || p.Width || !defaultOp(p.Exp.Op) {
		return
	}
	if p.Exp.Word == nil {
		w.keep("", offset(p.Pos()), 0, 0, false)
		return
	}
	from, to := offset(p.Exp.Word.Pos()), offset(p.Exp.Word.End())
	text, ok := w.literal(p.Exp.Word, w.quoting(), false)
	if !ok {
		text = w.source(from, to)
	}
	w.keep(text, offset(p.Pos()), from, to, !ok)
}

// defaultOp reports whether a parameter expansion's operator gives a
// default: :-, -, := or =.
func defaultOp(op syntax.ParExpOperator) bool {
	return op == syntax.DefaultUnset || op == syntax.DefaultUnsetOrNull || op == syntax.AssignUnset || op == syntax.AssignUnsetOrNull
}

// parent is the node the one being visited is a child of.
func (w *walker) parent() syntax.Node {
	if len(w.stack) == 0 {
		return nil
	}
	return w.stack[len(w.stack)-1]
}

// quoting is the quoting of the node being visited (quotingOf).
func (w *walker) quoting() quoting {
	if len(w.quotes) == 0 {
		return unquoted
	}
	return w.quotes[len(w.quotes)-1]
}

// quotingOf is the quoting the children of n stand in: in double quotes, in
// the body of a here-document whose delimiter is not quoted, in arithmetic,
// or none, the nearest of them on the stack. A statement, inside $( ) too,
// starts over with none. Inside the pattern of ${Y#p}, ${Y%p} or ${Y/p/r},
// or its replacement, bash and dash read quotes afresh, which errata does
// not model, so only plain text is literal there. The walk keeps each
// node's quoting as it descends, so finding it costs nothing per value.
func (w *walker) quotingOf(n syntax.Node) quoting {
	switch n := n.(type) {
	case *syntax.Stmt:
		return unquoted
	case *syntax.DblQuoted:
		return double
	case *syntax.ArithmExp, *syntax.ArithmCmd, *syntax.LetClause:
		return arithmetic
	case *syntax.ParamExp:
		if n.Exp == nil || !wordOp(n.Exp.Op) {
			return pattern
		}
	case *syntax.Word:
		if r, ok := w.parent().(*syntax.Redirect); ok && r.Hdoc == n {
			return heredoc
		}
	}
	return w.quoting()
}

// wordOp reports whether a parameter expansion's operator takes a word,
// read as the expansion's own quoting reads it, and not a pattern.
func wordOp(op syntax.ParExpOperator) bool {
	return defaultOp(op) || op == syntax.AlternateUnset || op == syntax.AlternateUnsetOrNull ||
		op == syntax.ErrorUnset || op == syntax.ErrorUnsetOrNull
}

// decl reads the options of a declaration, export, readonly, declare,
// typeset, local or nameref. An option that changes how the shell stores a
// value, such as -i, -l, -u, -a or -n, makes the values after it not literal,
// and so does an argument errata cannot read as an option or a name, such as
// "$opts". If such an option names the variable, every value of it is not
// literal. A quoted argument "Y=v" assigns, as the shell splits it after
// quote removal.
func (w *walker) decl(d *syntax.DeclClause) {
	s := declState{variant: d.Variant.Value, changes: d.Variant.Value == "nameref"}
	for _, a := range d.Args {
		if a.Name != nil {
			if s.changes || s.unreadable {
				w.declArgs[a] = true
			}
			if s.changes && a.Name.Value == w.name {
				w.transformed = true
			}
			continue
		}
		if a.Value != nil {
			w.declWord(&s, a.Value)
		}
	}
}

// declState is what the arguments of one declaration read so far say.
type declState struct {
	variant             string
	changes, unreadable bool
}

// declWord reads one argument of a declaration that the parser left as a
// word: NAME=v, quoted or not, a name alone, or an option. In posix every
// argument is one, since the parser reads export, readonly and local as
// commands.
func (w *walker) declWord(s *declState, word *syntax.Word) {
	readonly := s.variant == "readonly"
	name, value, from, literal, ok := w.splitAssign(word)
	if ok {
		text := value
		notLiteral := !literal || s.changes || s.unreadable
		if notLiteral {
			text = w.source(from, offset(word.End()))
		}
		if name == w.name {
			if !w.full() {
				w.keep(text, offset(word.Pos()), from, offset(word.End()), notLiteral)
			}
			w.transformed = w.transformed || s.changes
		}
		w.deferred = append(w.deferred, assigned{name, varState{value: text, known: !notLiteral}, readonly})
		return
	}
	text, isLit := w.literal(word, unquoted, false)
	if isLit && isName(text) {
		if readonly {
			w.readonly[text] = true
		}
		w.transformed = w.transformed || s.changes && text == w.name
		return
	}
	if isLit && (strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+")) {
		s.changes = s.changes || changesValues(s.variant, text)
		return
	}
	s.unreadable = true
}

// splitAssign reads a word that starts, once its quotes are removed, with
// NAME=: the name, the value's text when every part of the word is
// literal, and the offset the value starts at as written: after the = when
// the name and the = are plain text at the word's start, else the word's
// start. A tilde after the =, which the shell expands, is not literal.
func (w *walker) splitAssign(word *syntax.Word) (name, value string, from int, literal, ok bool) {
	var b strings.Builder
	literal = w.parts(&b, word.Parts, unquoted)
	if !literal {
		// The name may still be literal: the text before the first part
		// that is not, inside double quotes too.
		b.Reset()
		w.literalPrefix(&b, word.Parts, unquoted)
		if first, isLit := word.Parts[0].(*syntax.Lit); isLit && !strings.Contains(b.String(), "=") {
			// The name and its = share the first part, which is not
			// literal as a whole, as in V=a:~/b or V=a\ at the end of
			// the file: the name is still plain text before the =.
			b.Reset()
			b.WriteString(first.Value)
		}
	}
	name, rest, found := strings.Cut(b.String(), "=")
	if !found || !isName(name) {
		return "", "", 0, false, false
	}
	from = offset(word.Pos())
	if first, isLit := word.Parts[0].(*syntax.Lit); isLit && strings.HasPrefix(first.Value, name+"=") {
		from += len(name) + 1
	}
	for _, p := range word.Parts {
		if l, isLit := p.(*syntax.Lit); isLit && strings.Contains(l.Value, "=~") {
			literal = false
		}
	}
	if literal && w.lang != syntax.LangPOSIX && bracesExpand(word) {
		literal = false
	}
	return name, rest, from, literal, true
}

// literalPrefix writes the text of the parts before the first that is not
// literal, inside double quotes too, and reports whether every part was.
func (w *walker) literalPrefix(b *strings.Builder, parts []syntax.WordPart, q quoting) bool {
	for i, p := range parts {
		if dq, ok := p.(*syntax.DblQuoted); ok && !dq.Dollar && q == unquoted {
			if !w.literalPrefix(b, dq.Parts, double) {
				return false
			}
			continue
		}
		var part strings.Builder
		if !w.parts(&part, parts[i:i+1], q) {
			return false
		}
		b.WriteString(part.String())
	}
	return true
}

// call reads posix's export, readonly and local, which the parser reads as
// commands and whose arguments assign once they are all read.
func (w *walker) call(c *syntax.CallExpr) {
	if w.lang != syntax.LangPOSIX || !posixDecl(c) {
		return
	}
	s := declState{variant: c.Args[0].Lit()}
	for _, word := range c.Args[1:] {
		w.declWord(&s, word)
	}
}

// changesValues reports whether an option of a declaration changes the
// values after it.
func changesValues(variant, opt string) bool {
	return strings.HasPrefix(opt, "-") && opt != "--" && strings.Trim(opt[1:], keptOptions(variant)) != ""
}

// keptOptions are the options of a declaration that leave its values as
// written: global, readonly, trace and export for declare, typeset and
// local, and export's -n, which only stops the export. readonly's -a, -A
// and -f, and every other option, change what the shell stores.
func keptOptions(variant string) string {
	switch variant {
	case "declare", "typeset", "local":
		return "grtxI"
	case "export":
		return "n"
	}
	return ""
}
