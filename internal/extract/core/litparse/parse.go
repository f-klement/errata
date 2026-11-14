// Package litparse is the parse of a file's literal data that the literal
// and code readers share: one grammar of literal data for every language,
// spelt by the lexer table's row (core.Syntax), into core's tree, with
// whatever the file computes marked NotLiteral. Nothing is run, imported or
// evaluated (ADR 0002).
package litparse

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/f-klement/errata/internal/extract/core"
)

// Parse reads data, a file of lang, into a tree of the literal data its
// statements at the top of the file assign: a mapping from each name to the
// value assigned to it last, a list of the calls of each name a statement
// calls, and the value of a statement such as PHP's return [...] under the
// word. What is not literal data is a scalar marked NotLiteral, with the
// text the file wrote. Nothing is run, imported or evaluated.
func Parse(file string, lang core.Language, data []byte) (*core.Node, error) {
	t, err := parse(file, lang, data, false)
	return t.Root, err
}

// Tree is what ParseMembers reads: the tree, and for the root and each
// class's mapping in it, whether a word of the row's Constants declared each
// of its keys where it was assigned last (Const).
type Tree struct {
	Root   *core.Node
	consts map[*core.Node]map[string]bool
}

// Const reports whether a word of the row's Constants declared key, a key of
// m, the root or a class's mapping, where it was assigned last.
func (t Tree) Const(m *core.Node, key string) bool { return t.consts[m][key] }

// ParseMembers is Parse that also reads the members of each class, object
// or module the row's Classes open, at the top or in another class, into a
// mapping under the class's name, and the members of a class with no name,
// as Kotlin's companion object, into the class around it. A member is an
// assignment in the class's body, after any of the row's Modifiers, and in
// a Typed row after its type. The code reader reads through it.
func ParseMembers(file string, lang core.Language, data []byte) (Tree, error) {
	return parse(file, lang, data, true)
}

// parse is Parse, reading members too when members is set.
func parse(file string, lang core.Language, data []byte, members bool) (Tree, error) {
	lines := core.LineIndex(data)
	p := &parser{
		members:  members,
		consts:   map[string]bool{},
		constsOf: map[*core.Node]map[string]bool{},
		t:        newTokenizer(file, lang, data, lines),
		syn:      lang.Syntax,
		lang:     lang,
		src:      string(data),
		root:     core.NewMapping(1),
		defs:     map[string]def{},
		own:      map[owned]bool{},
		made:     map[*core.Node]bool{},
		pos:      map[*core.Node]map[string]int{},
		fuel:     2*len(data) + 4*core.MaxDepth + 64,
		calls:    map[*core.Node]bool{},
	}
	p.tok = p.t.token()
	defer p.t.stop()
	for p.tok.kind != tEOF && p.err == nil {
		p.statement() // which reads at least one token
	}
	if p.err == nil {
		p.err = p.t.err
	}
	if p.err == nil {
		p.closed()
	}
	if p.err != nil {
		return Tree{}, p.err
	}
	p.computed()
	p.constsOf[p.root] = p.consts
	return Tree{Root: p.root, consts: p.constsOf}, nil
}

// parser reads the statements of a file from its tokens.
type parser struct {
	t    *tokenizer
	syn  *core.Syntax
	lang core.Language
	src  string // the file, which every value's text is a part of
	tok  token  // the token being read
	prev token  // the token before it
	next []token
	err  error

	open      []opened // the brackets open, innermost last
	blocks    []int    // where each keyword block open starts
	pendingDo bool     // a loop opened on this line, which takes the next do

	stmt  int // the statement being read, counted from 1
	first int // where it starts
	root  *core.Node
	defs  map[string]def
	own   map[owned]bool
	star  int // the last statement that imports every name of a module, 0 for none
	// made are the mappings a dotted name made, and calls the lists of a
	// name's calls.
	made, calls map[*core.Node]bool
	// pos is where each key of the root and of each made mapping stands.
	pos map[*core.Node]map[string]int

	obs observer
	// fuel is how many more times advance may be called.
	fuel int

	// members reads the members of classes (ParseMembers); sc is the scope
	// being read, the top of the file or a class's body; consts is whether
	// a Constants word declared each key of the scope's root, and constsOf
	// that of each class's mapping read so far.
	members  bool
	sc       scope
	consts   map[string]bool
	constsOf map[*core.Node]map[string]bool
}

// scope is where the statements being read stand: the brackets and blocks
// open around them, the column a body's statements start at, which body
// they are in and how deeply classes nest there.
type scope struct {
	open, blocks, col int
	body              uint8
	depth             int
	// constant is whether every member of the body is a constant
	// (Syntax.ConstantBodies).
	constant bool
}

// The bodies a scope is: the top of the file, or a class's body that its
// indentation, an End word or a } ends.
const (
	bodyTop uint8 = iota
	bodyIndent
	bodyEnd
	bodyBrace
)

// opened is a bracket open: the byte that closes it and where it opened.
type opened struct {
	close byte
	at    int
}

// def is the statement that set a key of the root last, and the bytes of
// the value it set there.
type def struct {
	stmt       int
	start, end int
}

// owned is a statement that assigns a key of the root, which the key's
// own observation does not make computed.
type owned struct {
	stmt int
	name string
}

// fail stops the parse with err, the first error.
func (p *parser) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

// peek is the token after tok.
func (p *parser) peek() token {
	if len(p.next) == 0 {
		p.next = append(p.next, p.t.token())
	}
	return p.next[0]
}

// advance moves past tok, keeping the brackets and blocks it opens or
// closes, and lets the observer see it.
func (p *parser) advance() {
	if p.fuel--; p.fuel < 0 {
		// A file of n bytes has at most n tokens, and the parser moves past
		// the end a bounded number of times; more is a defect that would
		// loop, so the file's rule is unknown instead.
		p.fail(p.t.lost(p.tok.start, "the parser made no progress"))
	}
	t := p.tok
	if t.kind == tEOF {
		return
	}
	if t.line {
		p.pendingDo = false
	}
	p.brackets(t)
	p.observe(t)
	p.prev = t
	if len(p.next) > 0 {
		p.tok, p.next = p.next[0], p.next[1:]
	} else {
		p.tok = p.t.token()
	}
	if p.err != nil {
		p.tok = token{kind: tEOF, start: len(p.src), end: len(p.src)}
	}
}

// brackets keeps the brackets and keyword blocks open as t opens or closes
// one.
func (p *parser) brackets(t token) {
	switch t.kind {
	case tOpen:
		p.open = append(p.open, opened{close: closer(p.src[t.start]), at: t.start})
	case tClose:
		p.closeBracket(t)
	case tIdent:
		p.block(t)
	default: // a value or a punctuation mark opens and closes nothing
	}
}

// closeBracket closes the bracket t closes, the innermost open; a closer of
// another bracket, or of none, fails the parse.
func (p *parser) closeBracket(t token) {
	if n := len(p.open); n == 0 || p.open[n-1].close != p.src[t.start] {
		p.fail(p.t.lost(t.start, "a "+p.src[t.start:t.end]+" closes no bracket open"))
	} else {
		p.open = p.open[:n-1]
	}
}

// closed refuses a file that ends inside a bracket or a block.
func (p *parser) closed() {
	if n := len(p.open); n > 0 {
		p.fail(p.t.lost(p.open[n-1].at, "a bracket opens here and never closes"))
	} else if n := len(p.blocks); n > 0 {
		p.fail(p.t.lost(p.blocks[n-1], "a block opens here and never ends"))
	}
}

// closer is the byte that closes the bracket b.
func closer(b byte) byte {
	switch b {
	case '(':
		return ')'
	case '[':
		return ']'
	}
	return '}'
}

// block keeps the keyword blocks open as t, a word, opens or ends one.
func (p *parser) block(t token) {
	s := p.syn
	if !p.blockWord() {
		return
	}
	switch {
	case p.endsBlock(t):
		p.endBlock(t)
	case s.Do != "" && p.pendingDo && p.t.is(t, s.Do):
		p.pendingDo = false
	case p.opensBlock(t):
		p.blocks = append(p.blocks, t.start)
		p.pendingDo = p.t.oneOf(t, s.Loops)
	}
}

// blockWord reports whether tok may open or end a block: the row has an
// End, which an Until's block ends too, and the word is no method's name,
// as a method named end or do, nor a key or a keyword argument, as one
// named do or if.
func (p *parser) blockWord() bool {
	if p.syn.End == "" || p.member(p.prev) {
		return false
	}
	n := p.peek()
	return !n.glued || !p.t.is(n, ":")
}

// endsBlock reports whether t is the row's End or Until.
func (p *parser) endsBlock(t token) bool {
	return p.t.is(t, p.syn.End) || p.syn.Until != "" && p.t.is(t, p.syn.Until)
}

// endBlock ends the innermost block open; with none open it fails the
// parse.
func (p *parser) endBlock(t token) {
	if len(p.blocks) == 0 {
		p.fail(p.t.lost(t.start, p.t.text(t)+" ends no block open"))
		return
	}
	p.blocks = p.blocks[:len(p.blocks)-1]
}

// opensBlock reports whether t opens a block: one of Blocks, Repeat, or a
// word of Lead where a statement or an expression starts.
func (p *parser) opensBlock(t token) bool {
	s := p.syn
	return p.t.oneOf(t, s.Blocks) || s.Repeat != "" && p.t.is(t, s.Repeat) || p.t.oneOf(t, s.Lead) && p.lead(t)
}

// member reports whether t reaches a member of what is before it, so the
// name after it is no word of the language.
func (p *parser) member(t token) bool {
	return t.kind == tPunct && (p.t.is(t, ".") || p.t.is(t, "?.") || p.t.is(t, "::") || p.t.is(t, "->") || p.t.is(t, "&."))
}

// lead reports whether t stands where a statement or an expression starts:
// first on its line, or after anything that ends no value. After a value,
// Ruby's if is a modifier and opens no block.
func (p *parser) lead(t token) bool {
	return t.first || p.prev.end == 0 || !p.valueEnd(p.prev)
}

// valueEnd reports whether t may end a value.
func (p *parser) valueEnd(t token) bool {
	switch t.kind {
	case tNumber, tString, tSymbol, tClose:
		return true
	case tIdent:
		s := p.syn
		return !p.t.oneOf(t, s.Blocks) && !p.t.oneOf(t, s.Lead) && !p.t.is(t, s.Do) &&
			!p.t.oneOf(t, []string{"then", "else", "elsif", "elseif", "and", "or", "not", "when", "in", "repeat"})
	default:
		return false
	}
}

// top reports whether no bracket or block is open in the scope.
func (p *parser) top() bool { return len(p.open) == p.sc.open && len(p.blocks) == p.sc.blocks }

// atEnd reports whether tok ends the statement: the end of the file, a ;, or
// a line end with no bracket or block open, unless the next line goes on
// with an operator. A language that ends its statements at ; ends one at a
// line end only after a } , as a function's body ends.
func (p *parser) atEnd() bool {
	t := p.tok
	if t.kind == tEOF {
		return true
	}
	if !p.top() || t.start == p.first {
		return false // a statement's first token does not end it
	}
	if p.t.is(t, ";") || p.closes(t) {
		return true
	}
	return t.line && p.lineEnds(t)
}

// lineEnds reports whether t, first on its line, ends the statement before
// it, with no bracket or block open.
func (p *parser) lineEnds(t token) bool {
	if p.syn.Semicolons {
		// A function's or a class's body ends its statement with no ;.
		return p.prev.kind == tClose && p.src[p.prev.start] == '}'
	}
	return p.syn.Indent || !p.continues(t)
}

// operators are the punctuation that joins two values, so a line that
// starts with one, or one before it ends with one, goes on.
var operators = map[string]bool{
	".": true, "?.": true, "?": true, ":": true, "??": true, "&&": true, "||": true, "+": true, "-": true,
	"*": true, "/": true, "%": true, "|>": true, "..": true, "==": true, "!=": true, "<": true, ">": true,
	"<=": true, ">=": true, "=": true, "=>": true, "->": true, "|": true, "&": true, "^": true, "<<": true,
	">>": true, "**": true, "::": true, ",": true, "&.": true, "<>": true, "//": true, "!": true,
}

// closes reports whether t, with no bracket or block open in the scope,
// ends the class body the scope is: its } or its End word.
func (p *parser) closes(t token) bool {
	switch p.sc.body {
	case bodyBrace:
		return t.kind == tClose
	case bodyEnd:
		return p.t.is(t, p.syn.End) && !p.member(p.prev)
	}
	return false
}

// continues reports whether t, first on its line, goes on with the
// statement before it.
func (p *parser) continues(t token) bool {
	if p.prev.kind == tPunct && operators[p.t.text(p.prev)] {
		return true
	}
	if t.kind == tPunct && operators[p.t.text(t)] {
		return true
	}
	return p.t.oneOf(t, []string{"and", "or", "then", "else", "elsif", "elseif"}) && !p.syn.Indent
}

// endStatement moves past what is left of the statement, and its ;.
func (p *parser) endStatement() {
	for !p.atEnd() {
		p.advance()
	}
	if p.t.is(p.tok, ";") && p.top() {
		p.advance()
	}
	p.obs.end()
}

// statement reads one statement: an assignment, a call, or the value a
// file's Return word makes, into the root, and skips any other, letting the
// observer see it.
func (p *parser) statement() {
	p.stmt++
	first := p.tok
	p.first = first.start
	if p.skipped(first) {
		return
	}
	if p.members {
		p.annotations()
	}
	if p.returns(first) {
		return
	}
	declared, constant := p.declares()
	if p.defaults(first, declared) {
		return
	}
	if p.classHead() {
		p.class(first)
		return
	}
	if p.typedHead(p.tok) {
		p.typed(first, constant)
		return
	}
	p.assignment(first, declared, constant)
}

// skipped moves past a statement that is a lone ;, or one inside a bracket
// or block, or indented further than the body's statements, and reports
// whether it did.
func (p *parser) skipped(first token) bool {
	if p.t.is(first, ";") {
		p.advance()
		return true
	}
	if p.syn.Indent && first.first && first.col > p.sc.col || !p.top() {
		p.endStatement()
		return true
	}
	return false
}

// returns reads a statement of the row's Return word, as PHP's return
// [...], into the root under the word, and reports whether it did.
func (p *parser) returns(first token) bool {
	s := p.syn
	if s.Return == "" || !p.t.is(first, s.Return) || p.name() {
		return false
	}
	p.advance()
	p.define([]string{s.Return}, first, p.statementValue())
	p.endStatement()
	return true
}

// declares moves past the words of Declare, and in a class's members those
// of Modifiers, that start the statement; declared reports one, and
// constant one of Constants.
func (p *parser) declares() (declared, constant bool) {
	s := p.syn
	for (p.t.oneOf(p.tok, s.Declare) || p.members && p.t.oneOf(p.tok, s.Modifiers)) && !p.name() {
		constant = constant || p.t.oneOf(p.tok, s.Constants)
		p.advance()
		declared = true
	}
	return declared, constant
}

// defaults reads the value of a declared statement of the row's Default
// word into the root under the word, as JavaScript's export default {...},
// and reports whether it did. export default class C reads as export class
// C.
func (p *parser) defaults(first token, declared bool) bool {
	s := p.syn
	if !declared || s.Default == "" {
		return false
	}
	if p.t.is(p.tok, s.Default) && p.members && p.t.oneOf(p.peek(), s.Classes) {
		p.advance()
	}
	if !p.t.is(p.tok, s.Default) {
		return false
	}
	p.advance()
	p.define([]string{s.Default}, first, p.statementValue())
	p.endStatement()
	return true
}

// classHead reports whether tok opens a class whose members are read.
func (p *parser) classHead() bool {
	return p.members && p.t.oneOf(p.tok, p.syn.Classes) && !p.name()
}

// typedHead reports whether head starts a member of a Typed row's class
// with its type: no keyword, and no name an assignment follows.
func (p *parser) typedHead(head token) bool {
	return p.members && p.syn.Typed && p.sc.depth > 0 && !p.keyword(head) && (head.kind != tIdent || !p.assign(p.peek()))
}

// assignment reads a statement that starts with a name, dotted or not: an
// assignment of it, or a call of it at the top, and skips any other.
func (p *parser) assignment(first token, declared, constant bool) {
	head := p.tok
	if head.kind != tIdent || p.keyword(head) && !p.name() {
		p.endStatement()
		return
	}
	names, ok := p.dotted(head)
	if !ok {
		return
	}
	p.annotation()
	switch {
	case p.assign(p.tok):
		p.assigned(first, names, constant)
	case p.method(declared, names):
		// A call in a class's body is a method's head, not a call.
	default:
		p.callStatement(names[0], head)
	}
	p.endStatement()
}

// assigned reads the value assigned to names, tok being the assignment,
// into the root, and keeps whether the statement declared it constant.
func (p *parser) assigned(first token, names []string, constant bool) {
	p.advance()
	p.define(names, first, p.statementValue())
	p.consts[strings.Join(names, ".")] = constant || p.sc.constant
}

// dotted moves past the name head starts, with each part after a dot,
// and returns its parts; false when it nests past core.MaxDepth, which
// fails the parse.
func (p *parser) dotted(head token) ([]string, bool) {
	names := []string{p.key(head)}
	p.advance()
	for p.t.is(p.tok, ".") && p.peek().kind == tIdent && !p.keyword(p.peek()) {
		p.advance()
		if len(names) == core.MaxDepth {
			// Each part nests a mapping one level deeper.
			p.fail(fmt.Errorf("%s: %w", p.t.file, core.TooDeep(p.t.line(p.tok.start))))
			return nil, false
		}
		names = append(names, p.key(p.tok))
		p.advance()
	}
	return names, true
}

// annotation moves past a type annotation after a name, in a row that
// writes one after a :, up to its assignment or the statement's end.
func (p *parser) annotation() {
	if !p.syn.Annotate || !p.t.is(p.tok, ":") {
		return
	}
	for !p.atEnd() && !p.assign(p.tok) {
		p.advance()
	}
}

// method reports whether a name that no assignment follows is a method's
// head rather than a call: it was declared, is dotted, or stands in a
// class's body.
func (p *parser) method(declared bool, names []string) bool {
	return declared || len(names) > 1 || p.sc.depth > 0
}

// callStatement reads a statement that calls name, head, at the top: with
// its arguments in brackets, or as a command with them after a blank.
func (p *parser) callStatement(name string, head token) {
	s := p.syn
	switch {
	case p.callOpens():
		v := p.finish(head, p.call(head, 1), ctx{top: true}, 0, 0)
		p.called(name, head, v)
	case s.Command && !p.tok.line && !p.tok.glued && p.valueStart(p.tok):
		p.called(name, head, p.command(head))
	}
}

// name reports whether tok, a word of the language, is used as a name
// here: an assignment or a member follows it, as in Python's type = "x" or
// JavaScript's module.exports.
func (p *parser) name() bool {
	n := p.peek()
	return p.assign(n) || p.t.is(n, ".") && n.glued
}

// keyword reports whether t is a word that starts no assignment or call.
func (p *parser) keyword(t token) bool {
	s := p.syn
	return p.t.oneOf(t, s.Keywords) || p.t.oneOf(t, s.Declare) || p.t.oneOf(t, s.Blocks) || p.t.oneOf(t, s.Lead)
}

// key is the name t writes, without the language's sigil.
func (p *parser) key(t token) string {
	k := p.src[t.start:t.end]
	if s := p.syn.Sigil; s != 0 && k[0] == s {
		return k[1:]
	}
	return k
}

// assign reports whether t assigns a value to a name.
func (p *parser) assign(t token) bool {
	if t.kind != tPunct {
		return false
	}
	if len(p.syn.Assign) == 0 {
		return p.t.is(t, "=")
	}
	return p.t.oneOf(t, p.syn.Assign)
}

// valueStart reports whether t may start a command's first argument.
func (p *parser) valueStart(t token) bool {
	switch t.kind {
	case tString, tNumber, tSymbol:
		return true
	case tOpen:
		return p.src[t.start] == '['
	case tIdent:
		return !p.keyword(t)
	case tPunct:
		return p.t.is(t, "-") && p.peek().kind == tNumber && p.peek().glued
	default:
		return false
	}
}

// statementValue reads the value an assignment or a Return word sets, to
// the end of the statement.
func (p *parser) statementValue() valued {
	start := p.tok
	n := p.value(ctx{top: true})
	return valued{n, start.start, p.prev.end}
}

// valued is a value and the bytes it was read from.
type valued struct {
	n          *core.Node
	start, end int
}

// define sets the key names, a dotted name, of the root to v, the value of
// the statement that starts at first. A name with a dot in it is a member
// of the mapping its first part names, which the statement makes when no
// statement has assigned that part. A part another statement assigned a
// value that is no such mapping makes the first part computed: the file
// changes a value it wrote.
func (p *parser) define(names []string, first token, v valued) {
	p.own[owned{p.stmt, names[0]}] = true
	if len(names) == 1 {
		p.set(p.root, names[0], v.n)
		p.defs[names[0]] = def{p.stmt, v.start, v.end}
		return
	}
	p.defs[names[0]] = def{p.stmt, first.start, v.end}
	at := p.root
	for _, name := range names[:len(names)-1] {
		n, ok := at.Field[name]
		if !ok {
			n = core.NewMapping(p.t.line(first.start))
			p.made[n] = true
			p.set(at, name, n)
		} else if !p.made[n] {
			p.set(p.root, names[0], p.notLiteral(first.start, v.end))
			return
		}
		at = n
	}
	p.set(at, names[len(names)-1], v.n)
}

// set sets key of m, the root or a mapping a dotted name made, to v, in
// constant time: core.Node.Set searches the keys for one set before, and a
// file may set one name again as often as it likes (ADR 0003).
func (p *parser) set(m *core.Node, key string, v *core.Node) {
	at := p.pos[m]
	if at == nil {
		at = map[string]int{}
		p.pos[m] = at
	}
	if i, ok := at[key]; ok {
		m.Vals[i] = v
		m.Field[key] = v
		return
	}
	at[key] = len(m.Keys)
	m.Set(key, v)
}

// called adds v, a call of name the statement at head makes, to the list
// of name's calls in the root. A name another statement assigned is
// computed.
func (p *parser) called(name string, head token, v *core.Node) {
	p.own[owned{p.stmt, name}] = true
	p.defs[name] = def{p.stmt, head.start, p.prev.end}
	n, ok := p.root.Field[name]
	switch {
	case ok && p.calls[n]:
		n.Items = append(n.Items, v)
	case ok:
		p.set(p.root, name, p.notLiteral(head.start, p.prev.end))
	default:
		n = &core.Node{Shape: core.Sequence, Line: p.t.line(head.start), Items: []*core.Node{v}}
		p.calls[n] = true
		p.set(p.root, name, n)
	}
}

// computed marks each key of the root the observer saw the file change,
// in a statement other than one that sets the key, or that a statement
// after it imports every name of a module over, as computed: the value the
// file wrote for it is not the value it ends with.
func (p *parser) computed() {
	p.tainted(0)
	for i, k := range p.root.Keys {
		if d := p.defs[k]; d.stmt < p.star {
			p.root.Vals[i] = p.notLiteral(d.start, d.end)
			p.root.Field[k] = p.root.Vals[i]
		}
	}
}

// tainted marks each key of the scope's root that the observer saw a
// statement from first on change, other than one that sets the key, as
// computed, and no constant. It looks up each key's changes from first on,
// and stops at the first that is no statement setting the key, so each
// change is read once for each scope that sets its name in it (ADR 0003).
func (p *parser) tainted(first int) {
	for k, d := range p.defs {
		stmts := p.obs.taints[k]
		for i := sort.SearchInts(stmts, first); i < len(stmts); i++ {
			if !p.own[owned{stmts[i], k}] {
				p.set(p.root, k, p.notLiteral(d.start, d.end))
				p.consts[k] = false
				break
			}
		}
	}
}

// notLiteral is the scalar a computed value is: the text the file wrote,
// from start to end, at the line of start.
func (p *parser) notLiteral(start, end int) *core.Node {
	end = max(end, start) // a value cut short by a refusal
	return &core.Node{Shape: core.Scalar, Type: core.TypeString, NotLiteral: true,
		Text: strings.TrimSpace(p.src[start:end]), Line: p.t.line(start)}
}

// scalar is the literal scalar text of type typ at off.
func (p *parser) scalar(typ core.ScalarType, text string, off int) *core.Node {
	return &core.Node{Shape: core.Scalar, Type: typ, Text: text, Line: p.t.line(off)}
}

// ctx is where a value is read: at the top of a statement, where its end
// ends the value, and whether a comma ends it; inside brackets a value ends
// at a comma or at the bracket's close. Level is how deeply it nests.
type ctx struct {
	top, comma bool
	level      int
}

// stop reports whether tok ends a value read in c, whose brackets and
// blocks were open to depth and blocks.
func (p *parser) stop(c ctx, depth, blocks int) bool {
	if p.tok.kind == tEOF {
		return true
	}
	if len(p.open) != depth || len(p.blocks) != blocks {
		return false
	}
	if c.top {
		return p.atEnd() || c.comma && p.t.is(p.tok, ",")
	}
	return p.t.is(p.tok, ",") || p.tok.kind == tClose
}

// value reads one value in c: a literal, a list, a mapping or a call, or,
// for anything else, a computed scalar of the text up to where the value
// ends.
func (p *parser) value(c ctx) *core.Node {
	depth, blocks := len(p.open), len(p.blocks)
	start := p.tok
	if c.level > core.MaxDepth {
		p.fail(fmt.Errorf("%s: %w", p.t.file, core.TooDeep(p.t.line(start.start))))
		return p.scalar(core.TypeNull, "", start.start)
	}
	return p.finish(start, p.literal(c), c, depth, blocks)
}

// finish is n, the value read from start, if the value ends after it, and
// otherwise the computed scalar of the text up to where it ends, in c with
// depth brackets and blocks open: "a" + b is no literal. A cast after a
// literal, as TypeScript's as const, is skipped.
func (p *parser) finish(start token, n *core.Node, c ctx, depth, blocks int) *core.Node {
	if n != nil && p.stop(c, depth, blocks) {
		return n
	}
	if n != nil && p.frozen(start, n, c, depth, blocks) {
		return n
	}
	cast := p.cast(n)
	for !p.stop(c, depth, blocks) {
		p.advance()
	}
	if cast {
		return n
	}
	return p.notLiteral(start.start, p.prev.end)
}

// frozen moves past the row's Frozen call after n, a string literal read
// from start, as Ruby's "1.2".freeze, which is the string 1.2, and reports
// whether the value ends after it.
func (p *parser) frozen(start token, n *core.Node, c ctx, depth, blocks int) bool {
	if start.kind != tString || n.NotLiteral || p.syn.Frozen == "" || !p.t.is(p.tok, ".") ||
		p.peek().kind != tIdent || !p.t.is(p.peek(), p.syn.Frozen) {
		return false
	}
	p.advance()
	p.advance()
	return p.stop(c, depth, blocks)
}

// cast reports whether tok is a cast, one of the row's Casts, after n, a
// literal.
func (p *parser) cast(n *core.Node) bool {
	return n != nil && !n.NotLiteral && p.t.oneOf(p.tok, p.syn.Casts)
}

// literal reads the literal that starts at tok, or nil when none does; it
// does not read past the literal.
func (p *parser) literal(c ctx) *core.Node {
	t := p.tok
	switch t.kind {
	case tString:
		return p.strings(nil)
	case tNumber:
		p.advance()
		return p.scalar(core.TypeNumber, p.t.text(t), t.start)
	case tSymbol:
		p.advance()
		return p.scalar(core.TypeString, p.src[t.start+1:t.end], t.start)
	case tOpen:
		if b, ok := p.bracket(p.src[t.start], 0); ok {
			return p.list(b, t, ctx{level: c.level + 1})
		}
	case tPunct:
		return p.punctLiteral(t, c)
	case tIdent:
		return p.wordLiteral(t, c)
	default: // the end of the file, or a closer where a value should be
	}
	return nil
}

// punctLiteral reads the literal that starts at t, a punctuation mark: a
// signed number, a member's name such as Swift's .red, or a list with a
// prefix, as PHP's or Groovy's; nil for none.
func (p *parser) punctLiteral(t token, c ctx) *core.Node {
	n := p.peek()
	switch {
	case p.signed(t, n):
		p.advance()
		p.advance()
		return p.scalar(core.TypeNumber, p.src[t.start:n.end], t.start)
	case p.memberAt(t, n):
		return p.memberLiteral(t, n, c)
	case n.kind == tOpen && n.glued:
		return p.prefixedList(t, n, c)
	}
	return nil
}

// signed reports whether t is a sign that n, a number, follows glued.
func (p *parser) signed(t, n token) bool {
	return (p.t.is(t, "-") || p.t.is(t, "+")) && n.kind == tNumber && n.glued
}

// memberAt reports whether t is the . of a member's name n, glued to it,
// in a row with Members.
func (p *parser) memberAt(t, n token) bool {
	return p.syn.Members && p.t.is(t, ".") && n.kind == tIdent && n.glued
}

// memberLiteral reads the member .n that t starts: a call when a ( follows
// it glued, else the string of its text.
func (p *parser) memberLiteral(t, n token, c ctx) *core.Node {
	p.advance()
	p.advance()
	if p.tok.kind == tOpen && p.src[p.tok.start] == '(' && p.tok.glued {
		return p.call(t, c.level+1)
	}
	return p.scalar(core.TypeString, p.src[t.start:n.end], t.start)
}

// prefixedList reads the list that n, a bracket, opens after t, a prefix
// of one byte the row lists with that bracket; nil when it lists none.
func (p *parser) prefixedList(t, n token, c ctx) *core.Node {
	if b, ok := p.bracket(p.src[n.start], p.src[t.start]); ok && b.Pre != 0 && t.end-t.start == 1 {
		p.advance()
		return p.list(b, t, ctx{level: c.level + 1})
	}
	return nil
}

// wordLiteral reads the literal that starts at t, a word: a boolean, a
// null, a string with a prefix, the row's ArrayCall, or what named reads.
func (p *parser) wordLiteral(t token, c ctx) *core.Node {
	s := p.syn
	switch {
	case p.t.fold(t, s.True, s.FoldWords) || p.t.fold(t, s.False, s.FoldWords):
		p.advance()
		return p.scalar(core.TypeBool, p.t.text(t), t.start)
	case p.t.fold(t, s.Null, s.FoldWords):
		p.advance()
		return p.scalar(core.TypeNull, p.t.text(t), t.start)
	case p.peek().kind == tString && p.peek().glued:
		return p.prefixed(t)
	case p.arrayCall(t):
		p.advance()
		// The ( reads as the row's first pair, so one value in it is a
		// list of one, never a value in parentheses.
		return p.list(s.Brackets[0], t, ctx{level: c.level + 1})
	}
	return p.named(t, c)
}

// prefixed reads the string that t, a word of the row's Prefixes, stands
// glued before; nil for a tagged template or an interpolator errata does
// not know.
func (p *parser) prefixed(t token) *core.Node {
	pre := p.prefix(t)
	if pre == nil {
		return nil
	}
	p.advance()
	return p.strings(pre)
}

// arrayCall reports whether t is the row's ArrayCall, in any case, before
// its (, as PHP's array(...).
func (p *parser) arrayCall(t token) bool {
	s := p.syn
	return s.ArrayCall != "" && strings.EqualFold(p.t.text(t), s.ArrayCall) && p.peek().kind == tOpen && p.src[p.peek().start] == '('
}

// named reads the dotted name at t: a call when its ( follows on its line,
// or in a row with Aliases an Elixir alias, the string of its text; nil
// for any other name.
func (p *parser) named(t token, c ctx) *core.Node {
	chain := p.callee()
	if chain == "" {
		return nil
	}
	if p.callOpens() {
		return p.call(t, c.level+1)
	}
	if p.syn.Aliases && alias(chain) {
		return p.scalar(core.TypeString, chain, t.start)
	}
	return nil
}

// callOpens reports whether tok is the ( of a call, on the line of the name
// before it.
func (p *parser) callOpens() bool {
	return p.tok.kind == tOpen && p.src[p.tok.start] == '(' && !p.tok.line
}

// alias reports whether chain is an Elixir alias: dotted names that each
// start with an upper case letter.
func alias(chain string) bool {
	for part := range strings.SplitSeq(chain, ".") {
		r, _ := utf8.DecodeRuneInString(part)
		if !unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

// callee reads the dotted name at tok, which a call may follow, and moves
// past it; "" when tok starts none, or a word of the language does.
func (p *parser) callee() string {
	t := p.tok
	if t.kind != tIdent || p.keyword(t) {
		return ""
	}
	p.advance()
	end := t.end
	for (p.t.is(p.tok, ".") || p.t.is(p.tok, "::")) && p.tok.glued && p.peek().kind == tIdent && p.peek().glued {
		p.advance()
		end = p.tok.end
		p.advance()
	}
	return p.src[t.start:end]
}

// prefix is the word of the row's Prefixes that t, written straight before
// a string, is, or nil.
func (p *parser) prefix(t token) *core.Prefix {
	w := p.t.text(t)
	for i, pre := range p.syn.Prefixes {
		if w == pre.Word || pre.Fold && strings.EqualFold(w, pre.Word) {
			return &p.syn.Prefixes[i]
		}
	}
	return nil
}

// bracket is the row's pair that opens with b, written after pre (0 for
// none).
func (p *parser) bracket(b, pre byte) (core.Bracket, bool) {
	for _, br := range p.syn.Brackets {
		if br.Open == b && br.Pre == pre {
			return br, true
		}
	}
	return core.Bracket{}, false
}

// list reads the list or mapping b opens at tok, which start began: its
// items, or its entries, each a key, a separator and a value. A list with
// both, a mapping that repeats a key, a key that is no literal, or a pair
// that holds what b does not, is computed. A row's parentheses, where it
// lists them as a pair, make a list of values with a comma between them,
// and one value in them is that value.
func (p *parser) list(b core.Bracket, start token, c ctx) *core.Node {
	line := p.t.line(start.start)
	p.advance() // the opener
	depth := len(p.open)
	l := listed{m: core.NewMapping(line)}
	if p.t.is(p.tok, ":") && p.peek().kind == tClose {
		p.advance() // [:], an empty mapping
		l.entries = -1
	}
	for p.inside(depth) {
		p.item(&l, c, depth)
	}
	p.advance() // the closer
	return p.listNode(&l, b, start, line)
}

// listNode is the value of l, the list or mapping b opened at start, on
// line: computed, a mapping, the one value in parentheses, or a list.
func (p *parser) listNode(l *listed, b core.Bracket, start token, line int) *core.Node {
	parens := b.Open == '('
	switch {
	case l.refused(b, parens):
		return p.notLiteral(start.start, p.prev.end)
	case l.entries != 0:
		return l.m
	case parens && l.vals == 1 && !l.comma:
		return l.items[0] // parentheses around one value
	case l.vals == 0 && b.Map:
		return l.m
	}
	return &core.Node{Shape: core.Sequence, Line: line, Items: l.items}
}

// listed is what list has read of a list or a mapping: its items, its
// entries, whether a comma stands between two values, and whether it is
// computed.
type listed struct {
	items         []*core.Node
	m             *core.Node
	comma, bad    bool
	entries, vals int
}

// refused reports whether the list l read, which b opened, is computed: it
// is bad, holds both entries and items, or holds what b does not.
func (l *listed) refused(b core.Bracket, parens bool) bool {
	return l.bad || l.entries > 0 && l.vals > 0 || l.entries > 0 && !b.Entries || l.vals > 0 && !b.Items && !parens
}

// inside reports whether tok is still inside the bracket opened at depth,
// with no error.
func (p *parser) inside(depth int) bool {
	return p.err == nil && (p.tok.kind != tClose || len(p.open) != depth) && p.tok.kind != tEOF
}

// item reads one item or entry of the list l, opened at depth, and the
// comma after it.
func (p *parser) item(l *listed, c ctx, depth int) {
	key, isEntry, ok := p.entryKey()
	v := p.value(ctx{level: c.level})
	switch {
	case isEntry && !ok:
		l.bad = true
	case isEntry:
		l.entries++
		if !setOnce(l.m, key, v) {
			l.bad = true
		}
	default:
		l.vals++
		l.items = append(l.items, v)
	}
	if p.t.is(p.tok, ",") && len(p.open) == depth {
		l.comma = true
		p.advance()
	}
}

// setOnce sets key of m to v and reports whether no value was set for it
// before; setting one again would search the keys.
func setOnce(m *core.Node, key string, v *core.Node) bool {
	if _, dup := m.Field[key]; dup {
		return false
	}
	m.Set(key, v)
	return true
}

// entryKey reads the key and the separator of a mapping entry at tok, if
// one starts there: a string, a number, a symbol, or a bare name before a
// separator of NameKeys. isEntry is whether one does, and ok whether its key
// is literal.
func (p *parser) entryKey() (key string, isEntry, ok bool) {
	s := p.syn
	n := p.peek()
	if n.kind != tPunct || !p.t.oneOf(n, s.Entry) && !p.t.oneOf(n, s.NameKeys) {
		return "", false, false
	}
	if key, isEntry, ok = p.keyOf(p.tok, n); ok {
		p.advance()
		p.advance()
	}
	return key, isEntry, ok
}

// keyOf is the key t writes before n, a separator: isEntry when t may be a
// key, and ok when it is literal. A bare name is a key only before a
// separator of NameKeys; elsewhere it is a variable.
func (p *parser) keyOf(t, n token) (key string, isEntry, ok bool) {
	switch t.kind {
	case tString:
		if text, lit := p.str(t, nil); lit {
			return text, true, true
		}
		return "", true, false
	case tNumber:
		return p.t.text(t), true, true
	case tSymbol:
		return p.src[t.start+1 : t.end], true, true
	case tIdent:
		if !p.t.oneOf(n, p.syn.NameKeys) {
			return "", true, false // a variable as the key
		}
		return p.t.text(t), true, true
	default:
		return "", false, false
	}
}

// call reads the arguments of the call whose callee starts at start; tok
// is its (. The call is a mapping from each keyword argument's name, and
// from each positional argument's place counted from 0, to the argument. A
// call with an argument that is not literal, or a keyword twice, is
// computed: it is the call's data, never what the call returns.
func (p *parser) call(start token, level int) *core.Node {
	a := args{m: core.NewMapping(p.t.line(start.start))}
	p.advance() // the (
	depth := len(p.open)
	for p.inside(depth) {
		p.argument(&a, ctx{level: level})
		if p.t.is(p.tok, ",") && len(p.open) == depth {
			p.advance()
		}
	}
	p.advance()
	if a.bad {
		return p.notLiteral(start.start, p.prev.end)
	}
	return a.m
}

// command reads the arguments of a call written without parentheses, to
// the end of its statement: Ruby's gem "rails", "~> 7.1", require: false.
func (p *parser) command(head token) *core.Node {
	a := args{m: core.NewMapping(p.t.line(head.start))}
	for p.err == nil && !p.atEnd() {
		p.argument(&a, ctx{top: true, comma: true, level: 1})
		if p.t.is(p.tok, ",") {
			p.advance()
		}
	}
	if a.bad {
		return p.notLiteral(head.start, p.prev.end)
	}
	return a.m
}

// args are the arguments of a call read so far: the mapping, whether one
// is computed, and the place of the next positional one.
type args struct {
	m     *core.Node
	bad   bool
	place int
}

// argument reads one argument of a call into a, read in c: a keyword's
// under its name, any other under its place.
func (p *parser) argument(a *args, c ctx) {
	name, ok := p.keywordArg()
	v := p.value(c)
	if v.NotLiteral {
		a.bad = true
	}
	if !ok {
		a.m.Set(strconv.Itoa(a.place), v)
		a.place++
		return
	}
	if !setOnce(a.m, name, v) {
		a.bad = true // and setting it again would search the keys
	}
}

// keywordArg reads a keyword argument's name and separator at tok, if one
// starts there.
func (p *parser) keywordArg() (string, bool) {
	t, n := p.tok, p.peek()
	if t.kind != tIdent || n.kind != tPunct || !p.t.oneOf(n, p.syn.Keyword) {
		return "", false
	}
	p.advance()
	p.advance()
	return p.t.text(t), true
}
