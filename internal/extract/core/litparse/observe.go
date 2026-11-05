package litparse

import "slices"

// observer watches every token of the file for a name the file changes
// after it is assigned: a name followed, through members and indices, by an
// assignment, such as X += 1, X["k"] = 1 or X.y = 1; a name whose member is
// called, such as X.append(1); a name a Rebind word binds anew, such as
// def X or for X in; and every name an import binds. The parser marks each
// such name of the root computed, unless the statement is one that sets it.
type observer struct {
	// taints are the statements that change each name, in order, each once.
	taints map[string][]int
	cur    *chain  // the chain being read, nil for none
	saved  []chain // the chains an index [...] interrupts, innermost last
	rebind bool    // the next name is bound anew
	all    bool    // every name to the end of the statement is bound anew
	text   string  // the dotted name the chain has read, such as table.insert
	arg    bool    // a Mutators call just opened: the name after it changes
}

// chain is a name and the members and indices after it so far. Self is
// whether the name is a word of the row's Selves, as this or self, whose
// first member, first, a change to the chain changes too.
type chain struct {
	name  string
	state uint8
	depth int // for a saved chain, the brackets open before its index
	self  bool
	first string
	// members counts the members read after the name, and last is the
	// latest of them.
	members int
	last    string
}

// The states of a chain.
const (
	sHead    uint8 = iota // the name
	sDot                  // a . after it, before a member
	sMember               // a member
	sIndexed              // an index's ]
)

// end ends the statement: nothing carries into the next.
func (o *observer) end() {
	o.cur, o.saved, o.rebind, o.all = nil, o.saved[:0], false, false
}

// assigns are the operators that change the name before them.
var assigns = map[string]bool{
	"=": true, ":=": true, "+=": true, "-=": true, "*=": true, "/=": true, "%=": true, ".=": true, "|=": true,
	"&=": true, "^=": true, "<<=": true, ">>=": true, "**=": true, "??=": true, "||=": true, "&&=": true,
	"//=": true, "++": true, "--": true, "<<": true,
}

// observe reads t, the token the parser moves past.
func (p *parser) observe(t token) {
	o := &p.obs
	switch t.kind {
	case tIdent:
		name := p.key(t)
		switch w := p.t.text(t); {
		case o.all || o.rebind:
			o.taint(name, p.stmt)
			o.rebind = false
		case p.t.oneOf(t, p.syn.Rebind):
			o.rebind = true
			o.all = w == "import" || w == "from"
			o.cur = nil
			return
		}
		if o.arg {
			o.taint(name, p.stmt) // table.insert(deps, 'b') changes deps
		}
		o.arg = false
		if o.cur != nil && o.cur.state == sDot {
			o.cur.state = sMember
			if o.cur.first == "" {
				o.cur.first = name
			}
			o.cur.members++
			o.cur.last = name
			if p.syn.Command {
				// A row that calls without parentheses may call the member:
				// LIST.push 'b' changes LIST as LIST.push('b') does.
				o.call(p, o.cur)
			}
			o.text += "." + name
			return
		}
		o.cur = nil
		self := p.members && slices.Contains(p.syn.Selves, name)
		if !p.member(p.prev) && (!p.keyword(t) || self) {
			o.cur = &chain{name: name, self: self}
			o.text = name
		}
	case tPunct:
		o.arg = false
		w := p.t.text(t)
		switch {
		case o.all && w == "*":
			p.star = p.stmt
		case o.cur != nil && o.cur.state != sDot && p.member(t):
			o.cur.state = sDot
			return
		case o.cur != nil && o.cur.state != sDot && assigns[w]:
			o.change(o.cur, p.stmt)
		}
		o.cur = nil
	case tOpen:
		switch {
		case o.cur != nil && o.cur.state != sDot && p.src[t.start] == '[':
			c := *o.cur
			c.depth = len(p.open)
			o.saved = append(o.saved, c)
		case o.cur != nil && o.cur.state == sMember && p.src[t.start] == '(':
			o.call(p, o.cur)
			o.arg = slices.Contains(p.syn.Mutators, o.text)
			o.cur = nil
			return
		}
		o.arg = false
		o.cur = nil
	case tClose:
		o.cur, o.arg = nil, false
		if n := len(o.saved); n > 0 && o.saved[n-1].depth == len(p.open)+1 && p.src[t.start] == ']' {
			c := o.saved[n-1]
			o.saved = o.saved[:n-1]
			c.state = sIndexed
			o.cur = &c
		}
	default:
		o.cur, o.arg = nil, false
	}
}

// taint notes that statement stmt changes name.
func (o *observer) taint(name string, stmt int) {
	if o.taints == nil {
		o.taints = map[string][]int{}
	}
	if l := o.taints[name]; len(l) == 0 || l[len(l)-1] != stmt {
		o.taints[name] = append(l, stmt)
	}
}

// call notes the change a call of c's last member makes: any call changes
// the name it is called on, NAME.upper() as NAME.append(1), since the
// reader cannot tell them apart; but a call through this or self, as
// self.HOST.rstrip('/'), changes its member only when the method is one of
// the row's Methods, and self.f() changes nothing.
func (o *observer) call(p *parser, c *chain) {
	if !c.self {
		o.change(c, p.stmt)
		return
	}
	if c.members >= 2 && slices.Contains(p.syn.Methods, c.last) {
		o.taint(c.first, p.stmt)
	}
}

// change notes that statement stmt changes c's name, and for a chain from
// this or self in a class, the member of the class it names first: this.x
// = 1 in a method changes the class's x.
func (o *observer) change(c *chain, stmt int) {
	o.taint(c.name, stmt)
	if c.self && c.first != "" {
		o.taint(c.first, stmt)
	}
}
