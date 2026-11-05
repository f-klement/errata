package literal

import "slices"

// observer watches every token of the file for a name the file changes
// after it is assigned: a name followed, through members and indices, by an
// assignment, such as X += 1, X["k"] = 1 or X.y = 1; a name whose member is
// called, such as X.append(1); a name a Rebind word binds anew, such as
// def X or for X in; and every name an import binds. The parser marks each
// such name of the root computed, unless the statement is one that sets it.
type observer struct {
	taints []taint
	cur    *chain  // the chain being read, nil for none
	saved  []chain // the chains an index [...] interrupts, innermost last
	rebind bool    // the next name is bound anew
	all    bool    // every name to the end of the statement is bound anew
	text   string  // the dotted name the chain has read, such as table.insert
	arg    bool    // a Mutators call just opened: the name after it changes
}

// taint is a name a statement changes.
type taint struct {
	name string
	stmt int
}

// chain is a name and the members and indices after it so far.
type chain struct {
	name  string
	state uint8
	depth int // for a saved chain, the brackets open before its index
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
			if p.syn.Command {
				// A row that calls without parentheses may call the member:
				// LIST.push 'b' changes LIST as LIST.push('b') does.
				o.taint(o.cur.name, p.stmt)
			}
			o.text += "." + name
			return
		}
		o.cur = nil
		if !p.member(p.prev) && !p.keyword(t) {
			o.cur = &chain{name: name}
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
			o.taint(o.cur.name, p.stmt)
		}
		o.cur = nil
	case tOpen:
		switch {
		case o.cur != nil && o.cur.state != sDot && p.src[t.start] == '[':
			c := *o.cur
			c.depth = len(p.open)
			o.saved = append(o.saved, c)
		case o.cur != nil && o.cur.state == sMember && p.src[t.start] == '(':
			o.taint(o.cur.name, p.stmt)
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
	o.taints = append(o.taints, taint{name, stmt})
}
