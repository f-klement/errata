package literal

import (
	"fmt"
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
	lines := core.LineIndex(data)
	p := &parser{
		t:     newTokenizer(file, lang, data, lines),
		syn:   lang.Syntax,
		lang:  lang,
		src:   string(data),
		root:  core.NewMapping(1),
		defs:  map[string]def{},
		own:   map[owned]bool{},
		made:  map[*core.Node]bool{},
		pos:   map[*core.Node]map[string]int{},
		fuel:  2*len(data) + 4*core.MaxDepth + 64,
		calls: map[*core.Node]bool{},
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
		return nil, p.err
	}
	p.computed()
	return p.root, nil
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
}

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
	switch t.kind {
	case tOpen:
		p.open = append(p.open, opened{close: closer(p.src[t.start]), at: t.start})
	case tClose:
		if n := len(p.open); n == 0 || p.open[n-1].close != p.src[t.start] {
			p.fail(p.t.lost(t.start, "a "+p.src[t.start:t.end]+" closes no bracket open"))
		} else {
			p.open = p.open[:n-1]
		}
	case tIdent:
		p.block(t)
	default: // a value or a punctuation mark opens and closes nothing
	}
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
	if s.End == "" {
		return // Until closes a block End closes too
	}
	if p.member(p.prev) {
		return // a method named end or do
	}
	if n := p.peek(); n.glued && p.t.is(n, ":") {
		return // a key or a keyword argument named do or if
	}
	switch {
	case p.t.is(t, s.End) || s.Until != "" && p.t.is(t, s.Until):
		if len(p.blocks) == 0 {
			p.fail(p.t.lost(t.start, p.t.text(t)+" ends no block open"))
			return
		}
		p.blocks = p.blocks[:len(p.blocks)-1]
	case s.Do != "" && p.pendingDo && p.t.is(t, s.Do):
		p.pendingDo = false
	case p.t.oneOf(t, s.Blocks) || s.Repeat != "" && p.t.is(t, s.Repeat) || p.t.oneOf(t, s.Lead) && p.lead(t):
		p.blocks = append(p.blocks, t.start)
		p.pendingDo = p.t.oneOf(t, s.Loops)
	}
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

// top reports whether no bracket or block is open.
func (p *parser) top() bool { return len(p.open) == 0 && len(p.blocks) == 0 }

// atEnd reports whether tok ends the statement: the end of the file, a ;, or
// a line end with no bracket or block open, unless the language ends its
// statements only at ; or the next line goes on with an operator.
func (p *parser) atEnd() bool {
	t := p.tok
	if t.kind == tEOF {
		return true
	}
	if !p.top() || t.start == p.first {
		return false // a statement's first token does not end it
	}
	if p.t.is(t, ";") {
		return true
	}
	if p.syn.Semicolons || !t.line {
		return false
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
	s := p.syn
	first := p.tok
	p.first = first.start
	if p.t.is(first, ";") {
		p.advance()
		return
	}
	if s.Indent && first.first && first.col > 0 || !p.top() {
		p.endStatement()
		return
	}
	if s.Return != "" && p.t.is(first, s.Return) && !p.name() {
		p.advance()
		p.define([]string{s.Return}, first, p.statementValue())
		p.endStatement()
		return
	}
	declared := false
	for p.t.oneOf(p.tok, s.Declare) && !p.name() {
		p.advance()
		declared = true
	}
	if declared && s.Default != "" && p.t.is(p.tok, s.Default) {
		p.advance()
		p.define([]string{s.Default}, first, p.statementValue())
		p.endStatement()
		return
	}
	head := p.tok
	if head.kind != tIdent || p.keyword(head) && !p.name() {
		p.endStatement()
		return
	}
	names := []string{p.key(head)}
	p.advance()
	for p.t.is(p.tok, ".") && p.peek().kind == tIdent && !p.keyword(p.peek()) {
		p.advance()
		if len(names) == core.MaxDepth {
			// Each part nests a mapping one level deeper.
			p.fail(fmt.Errorf("%s: %w", p.t.file, core.TooDeep(p.t.line(p.tok.start))))
			return
		}
		names = append(names, p.key(p.tok))
		p.advance()
	}
	if s.Annotate && p.t.is(p.tok, ":") {
		for !p.atEnd() && !p.assign(p.tok) {
			p.advance()
		}
	}
	switch {
	case p.assign(p.tok):
		p.advance()
		p.define(names, first, p.statementValue())
	case declared || len(names) > 1:
	case p.tok.kind == tOpen && p.src[p.tok.start] == '(' && !p.tok.line:
		v := p.finish(head, p.call(head, 1), ctx{top: true}, 0, 0)
		p.called(names[0], head, v)
	case s.Command && !p.tok.line && !p.tok.glued && p.valueStart(p.tok):
		p.called(names[0], head, p.command(head))
	}
	p.endStatement()
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
	for _, t := range p.obs.taints {
		d, ok := p.defs[t.name]
		if !ok || p.own[owned{t.stmt, t.name}] {
			continue
		}
		p.set(p.root, t.name, p.notLiteral(d.start, d.end))
	}
	for i, k := range p.root.Keys {
		if d := p.defs[k]; d.stmt < p.star {
			p.root.Vals[i] = p.notLiteral(d.start, d.end)
			p.root.Field[k] = p.root.Vals[i]
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
	cast := n != nil && !n.NotLiteral && p.t.oneOf(p.tok, p.syn.Casts)
	for !p.stop(c, depth, blocks) {
		p.advance()
	}
	if cast {
		return n
	}
	return p.notLiteral(start.start, p.prev.end)
}

// literal reads the literal that starts at tok, or nil when none does; it
// does not read past the literal.
func (p *parser) literal(c ctx) *core.Node {
	s := p.syn
	t := p.tok
	inner := ctx{level: c.level + 1}
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
			return p.list(b, t, inner)
		}
	case tPunct:
		n := p.peek()
		switch {
		case (p.t.is(t, "-") || p.t.is(t, "+")) && n.kind == tNumber && n.glued:
			p.advance()
			p.advance()
			return p.scalar(core.TypeNumber, p.src[t.start:n.end], t.start)
		case s.Members && p.t.is(t, ".") && n.kind == tIdent && n.glued:
			p.advance()
			p.advance()
			if p.tok.kind == tOpen && p.src[p.tok.start] == '(' && p.tok.glued {
				return p.call(t, c.level+1)
			}
			return p.scalar(core.TypeString, p.src[t.start:n.end], t.start)
		case n.kind == tOpen && n.glued:
			if b, ok := p.bracket(p.src[n.start], p.src[t.start]); ok && b.Pre != 0 && t.end-t.start == 1 {
				p.advance()
				return p.list(b, t, inner)
			}
		}
	case tIdent:
		switch {
		case p.t.fold(t, s.True, s.FoldWords) || p.t.fold(t, s.False, s.FoldWords):
			p.advance()
			return p.scalar(core.TypeBool, p.t.text(t), t.start)
		case p.t.fold(t, s.Null, s.FoldWords):
			p.advance()
			return p.scalar(core.TypeNull, p.t.text(t), t.start)
		case p.peek().kind == tString && p.peek().glued:
			pre := p.prefix(t)
			if pre == nil {
				return nil // a tagged template or an interpolator errata does not know
			}
			p.advance()
			return p.strings(pre)
		case s.ArrayCall != "" && strings.EqualFold(p.t.text(t), s.ArrayCall) && p.peek().kind == tOpen && p.src[p.peek().start] == '(':
			p.advance()
			b := s.Brackets[0]
			b.Open, b.Close = '(', ')'
			return p.list(b, t, inner)
		}
		chain := p.callee()
		if chain == "" {
			return nil
		}
		if p.tok.kind == tOpen && p.src[p.tok.start] == '(' && !p.tok.line {
			return p.call(t, c.level+1)
		}
		if s.Aliases && alias(chain) {
			return p.scalar(core.TypeString, chain, t.start)
		}
	default: // the end of the file, or a closer where a value should be
	}
	return nil
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
	var (
		items         []*core.Node
		m             = core.NewMapping(line)
		comma         bool // a comma stands between two values
		bad           bool
		entries, vals int
	)
	if p.t.is(p.tok, ":") && p.peek().kind == tClose {
		p.advance() // [:], an empty mapping
		entries = -1
	}
	for p.err == nil && (p.tok.kind != tClose || len(p.open) != depth) && p.tok.kind != tEOF {
		key, isEntry, ok := p.entryKey()
		v := p.value(ctx{level: c.level})
		switch {
		case isEntry && !ok:
			bad = true
		case isEntry:
			entries++
			if _, dup := m.Field[key]; dup {
				bad = true // and setting it again would search the keys
			} else {
				m.Set(key, v)
			}
		default:
			vals++
			items = append(items, v)
		}
		if p.t.is(p.tok, ",") && len(p.open) == depth {
			comma = true
			p.advance()
		}
	}
	p.advance() // the closer
	parens := b.Open == '('
	switch {
	case bad || entries > 0 && vals > 0 || entries > 0 && !b.Entries || vals > 0 && !b.Items && !parens:
		return p.notLiteral(start.start, p.prev.end)
	case entries != 0:
		return m
	case parens && vals == 1 && !comma:
		return items[0] // parentheses around one value
	case vals == 0 && b.Map:
		return m
	}
	return &core.Node{Shape: core.Sequence, Line: line, Items: items}
}

// entryKey reads the key and the separator of a mapping entry at tok, if
// one starts there: a string, a number, a symbol, or a bare name before a
// separator of NameKeys. isEntry is whether one does, and ok whether its key
// is literal.
func (p *parser) entryKey() (key string, isEntry, ok bool) {
	s := p.syn
	t, n := p.tok, p.peek()
	sep := n.kind == tPunct && (p.t.oneOf(n, s.Entry) || p.t.oneOf(n, s.NameKeys))
	if !sep {
		return "", false, false
	}
	switch t.kind {
	case tString:
		text, lit := p.str(t, nil)
		if !lit {
			return "", true, false
		}
		key = text
	case tNumber:
		key = p.t.text(t)
	case tSymbol:
		key = p.src[t.start+1 : t.end]
	case tIdent:
		if !p.t.oneOf(n, s.NameKeys) {
			return "", true, false // a variable as the key
		}
		key = p.t.text(t)
	default:
		return "", false, false
	}
	p.advance()
	p.advance()
	return key, true, true
}

// call reads the arguments of the call whose callee starts at start; tok
// is its (. The call is a mapping from each keyword argument's name, and
// from each positional argument's place counted from 0, to the argument. A
// call with an argument that is not literal, or a keyword twice, is
// computed: it is the call's data, never what the call returns.
func (p *parser) call(start token, level int) *core.Node {
	m := core.NewMapping(p.t.line(start.start))
	p.advance() // the (
	depth := len(p.open)
	bad, place := false, 0
	for p.err == nil && (p.tok.kind != tClose || len(p.open) != depth) && p.tok.kind != tEOF {
		name, ok := p.keywordArg()
		v := p.value(ctx{level: level})
		if v.NotLiteral {
			bad = true
		}
		if ok {
			if _, dup := m.Field[name]; dup {
				bad = true // and setting it again would search the keys
			} else {
				m.Set(name, v)
			}
		} else {
			m.Set(strconv.Itoa(place), v)
			place++
		}
		if p.t.is(p.tok, ",") && len(p.open) == depth {
			p.advance()
		}
	}
	p.advance()
	if bad {
		return p.notLiteral(start.start, p.prev.end)
	}
	return m
}

// command reads the arguments of a call written without parentheses, to
// the end of its statement: Ruby's gem "rails", "~> 7.1", require: false.
func (p *parser) command(head token) *core.Node {
	m := core.NewMapping(p.t.line(head.start))
	bad, place := false, 0
	for p.err == nil && !p.atEnd() {
		name, ok := p.keywordArg()
		v := p.value(ctx{top: true, comma: true, level: 1})
		if v.NotLiteral {
			bad = true
		}
		if ok {
			if _, dup := m.Field[name]; dup {
				bad = true // and setting it again would search the keys
			} else {
				m.Set(name, v)
			}
		} else {
			m.Set(strconv.Itoa(place), v)
			place++
		}
		if p.t.is(p.tok, ",") {
			p.advance()
		}
	}
	if bad {
		return p.notLiteral(head.start, p.prev.end)
	}
	return m
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
