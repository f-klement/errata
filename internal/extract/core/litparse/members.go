package litparse

import (
	"fmt"

	"github.com/f-klement/errata/internal/extract/core"
)

// annotations moves past each @Name, dotted or not, and its arguments in
// parentheses, at tok in a row that writes them (Syntax.Annotations), as
// Java's @Deprecated or Python's @dataclass before a class.
func (p *parser) annotations() {
	for p.syn.Annotations && p.t.is(p.tok, "@") && p.peek().kind == tIdent && p.peek().glued {
		p.advance()
		if p.callee() == "" {
			return // a word of the language, as Java's @interface
		}
		if p.tok.kind == tOpen && p.src[p.tok.start] == '(' && p.tok.glued {
			depth := len(p.open)
			p.advance()
			for len(p.open) > depth && p.tok.kind != tEOF {
				p.advance()
			}
		}
	}
}

// class reads the class, object or module whose word is at tok, in the
// statement that starts at first: its name, its header, and the members of
// its body, which its indentation, its End word or its } ends, into a
// mapping under its name in the scope around it. A class with no name in a
// body of braces, as Kotlin's companion object, gives its members to the
// scope around it; one with no body, or no name elsewhere, as Ruby's class
// << self, is read as any statement the reader does not know.
func (p *parser) class(first token) {
	stmt, outer := p.stmt, p.sc
	constant := p.t.oneOf(p.tok, p.syn.ConstantBodies)
	p.advance() // the word, which in Ruby opens a block End closes
	name := ""
	if p.tok.kind == tIdent && !p.keyword(p.tok) {
		name = p.key(p.tok)
		p.advance()
	}
	inner := scope{open: len(p.open), blocks: len(p.blocks), col: outer.col, depth: outer.depth + 1, constant: constant}
	switch {
	case len(p.blocks) > outer.blocks:
		// The header is the rest of the line, inside the block.
		inner.body = bodyEnd
		p.sc = inner
		p.endStatement()
		p.sc = outer
		if name == "" {
			return
		}
	case p.syn.Indent:
		inner.body = bodyIndent
		p.endStatement()
		if name == "" || !p.tok.first || p.tok.col <= outer.col {
			return
		}
		inner.col = p.tok.col
	default:
		inner.body = bodyBrace
		for p.tok.kind != tOpen || p.src[p.tok.start] != '{' || !p.top() {
			if p.atEnd() {
				p.endStatement()
				return
			}
			p.advance()
		}
		p.advance()
		inner.open = len(p.open)
	}
	if inner.depth > core.MaxDepth {
		// Each class nests a mapping one level deeper.
		p.fail(fmt.Errorf("%s: %w", p.t.file, core.TooDeep(p.t.line(first.start))))
		return
	}
	root, defs, own, consts := p.root, p.defs, p.own, p.consts
	p.root, p.defs, p.own, p.consts = core.NewMapping(p.t.line(first.start)), map[string]def{}, map[owned]bool{}, map[string]bool{}
	p.sc = inner
	from := p.stmt + 1
	for p.err == nil && p.tok.kind != tEOF && !p.bodyEnds() {
		p.statement() // which reads at least one token
	}
	p.tainted(from)
	m, members, owns, inDefs := p.root, p.consts, p.own, p.defs
	p.root, p.defs, p.own, p.consts, p.sc = root, defs, own, consts, outer
	if inner.body != bodyIndent {
		p.advance() // the } or the End
	}
	p.obs.end()
	if name == "" {
		for i, k := range m.Keys {
			p.own[owned{stmt, k}] = true
			p.defs[k] = inDefs[k]
			p.set(p.root, k, m.Vals[i])
		}
		for k, c := range members {
			p.consts[k] = c
		}
		for o := range owns {
			p.own[o] = true // the statements that set them, as in their own body
		}
		return
	}
	p.own[owned{stmt, name}] = true
	p.defs[name] = def{stmt, first.start, p.prev.end}
	p.set(p.root, name, m)
	p.consts[name] = false
	p.constsOf[m] = members
}

// bodyEnds reports whether tok, where a statement would start, ends the
// class body the scope is: a line that starts further out than the body's
// column, or the body's End word or }.
func (p *parser) bodyEnds() bool {
	t := p.tok
	if p.sc.body == bodyIndent {
		return t.first && t.col < p.sc.col
	}
	return p.top() && p.closes(t)
}

// typed reads a member whose type stands before its name, in the statement
// that starts at first, as Java's static final int X = 1 or PHP's public
// ?int $x = 1: the name is the word straight before the =. Constant is
// whether a Constants word stood before the type. A statement with no = at
// its top, such as a method, assigns nothing.
func (p *parser) typed(first token, constant bool) {
	var name token
	for !p.atEnd() && (!p.assign(p.tok) || !p.top()) {
		if p.tok.kind == tIdent && p.top() {
			name = p.tok
		}
		p.advance()
	}
	if !p.assign(p.tok) || name.end == 0 || p.prev.start != name.start {
		p.endStatement()
		return
	}
	p.advance()
	k := p.key(name)
	p.define([]string{k}, first, p.statementValue())
	p.consts[k] = constant || p.sc.constant
	p.endStatement()
}
