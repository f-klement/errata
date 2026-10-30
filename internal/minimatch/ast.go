package minimatch

import (
	"errors"
	"slices"
	"strconv"
)

// A port of minimatch's ast.js: one segment of a glob parsed into its
// extglobs, +(a|b), *(a|b), ?(a|b), @(a|b) and !(a|b), and written out as
// the source of a JavaScript regex. It keeps ast.js's steps, quirks
// included, for minimatch's options as Renovate sets them: dot and nocase,
// and every other option at its default. With dot set, every place ast.js
// adds (?!\.) for a leading dot is left out.

// The regex sources ast.js builds with.
var (
	startNoTraversal = units(`(?!(?:^|/)\.\.?(?:$|/))`)
	qmark            = units(`[^/]`)
	star             = units(`[^/]*?`)
	starNoEmpty      = units(`[^/]+?`)
)

// maxExtglobRecursion is ast.js's default maxExtglobRecursion.
const maxExtglobRecursion = 2

// part is a string or a node of the tree.
type part struct {
	s units
	a *node
}

// node is ast.js's AST. typ is 0 for a plain sequence, or the extglob's
// character.
type node struct {
	typ         rune
	root        *node
	hasMagic    int8 // -1 for undefined, 0 or 1
	uflag       bool
	parts       []part
	parent      *node
	parentIndex int
	negs        *[]*node
	filledNegs  bool
	str         units
	strSet      bool
	emptyExt    bool
	// count is the nodes the segment's tree has made, shared by every
	// node of it, which maxNodes bounds.
	count *int
	// ends is how many times isEnd ran on the tree, kept on the root for
	// the test that holds compiling to polynomial time.
	ends int
}

// maxNodes is the most nodes one segment's tree may make. A negation takes
// a copy of everything after it in its segment, copies of later negations
// included, so each negation doubles the tree: minimatch itself took 2.5 s
// to parse x followed by 18 of !(a).
const maxNodes = 1 << 12

// errNodes is the error of a segment whose tree passes maxNodes.
var errNodes = errors.New("a segment holds so many negations that its tree passes " + strconv.Itoa(maxNodes) + " nodes")

func newNode(typ rune, parent *node) *node {
	n := &node{typ: typ, parent: parent, hasMagic: -1}
	if typ != 0 {
		n.hasMagic = 1
	}
	if parent != nil {
		n.root = parent.root
		n.negs = parent.root.negs
		n.parentIndex = len(parent.parts)
		n.count = parent.root.count
	} else {
		n.root = n
		n.negs = &[]*node{}
		n.count = new(int)
	}
	*n.count++
	if typ == '!' && !n.root.filledNegs {
		*n.negs = append(*n.negs, n)
	}
	return n
}

func isExtglobType(c rune) bool { return c == '!' || c == '?' || c == '+' || c == '*' || c == '@' }

func (p part) String() units {
	if p.a == nil {
		return p.s
	}
	return p.a.toString()
}

// toString writes the glob the node was read from.
func (n *node) toString() units {
	if n.strSet {
		return n.str
	}
	var out units
	if n.typ == 0 {
		for _, p := range n.parts {
			out = append(out, p.String()...)
		}
	} else {
		out = units{n.typ, '('}
		for i, p := range n.parts {
			if i > 0 {
				out = append(out, '|')
			}
			out = append(out, p.String()...)
		}
		out = append(out, ')')
	}
	n.str, n.strSet = out, true
	return out
}

// fillNegs copies into each negation everything that follows it in the
// segment, so that !(x)y is read as not x followed by y.
func (n *node) fillNegs() {
	if n.filledNegs {
		return
	}
	n.toString()
	n.filledNegs = true
	for len(*n.negs) > 0 {
		last := len(*n.negs) - 1
		neg := (*n.negs)[last]
		*n.negs = (*n.negs)[:last]
		if neg.typ != '!' {
			continue
		}
		p, pp := neg, neg.parent
		for pp != nil {
			for i := p.parentIndex + 1; pp.typ == 0 && i < len(pp.parts); i++ {
				for _, part := range neg.parts {
					part.a.copyIn(pp.parts[i])
				}
			}
			p, pp = pp, pp.parent
		}
	}
}

// push appends parts, leaving out empty strings.
func (n *node) push(parts ...part) {
	for _, p := range parts {
		if p.a == nil && len(p.s) == 0 {
			continue
		}
		n.parts = append(n.parts, p)
	}
}

func (n *node) isStart() bool {
	if n.root == n {
		return true
	}
	if n.parent == nil || !n.parent.isStart() {
		return false
	}
	if n.parentIndex == 0 {
		return true
	}
	for i := range n.parentIndex {
		if i >= len(n.parent.parts) {
			return false
		}
		pp := n.parent.parts[i]
		if pp.a == nil || pp.a.typ != '!' {
			return false
		}
	}
	return true
}

// isEnd is ast.js's isEnd. ast.js asks n.parent.isEnd() a second time for
// a plain node, which doubles the work at each level of nesting, so that
// a@(a@(...)) takes 2^depth to compile in minimatch 10.2.6 too. The second
// call only runs once the first has answered true, and isEnd changes
// nothing, so the port answers true there in its place.
func (n *node) isEnd() bool {
	n.root.ends++
	if n.root == n {
		return true
	}
	if n.parent != nil && n.parent.typ == '!' {
		return true
	}
	if n.parent == nil || !n.parent.isEnd() {
		return false
	}
	if n.typ == 0 {
		return true
	}
	return n.parentIndex == len(n.parent.parts)-1
}

func (n *node) copyIn(p part) {
	if *n.count > maxNodes {
		return
	}
	if p.a == nil {
		n.push(p)
		return
	}
	n.push(part{a: p.a.clone(n)})
}

func (n *node) clone(parent *node) *node {
	c := newNode(n.typ, parent)
	for _, p := range n.parts {
		c.copyIn(p)
	}
	return c
}

// charAt is str.charAt(i): the empty string past the end.
func charAt(s units, i int) rune {
	if i >= 0 && i < len(s) {
		return s[i]
	}
	return -1
}

// parseAST is ast.js's #parseAST: it reads str from pos into the node and
// returns where it stopped.
func parseAST(str units, n *node, pos, extDepth int) int {
	escaping, inBrace, braceNeg := false, false, false
	braceStart := -1
	if n.typ == 0 {
		i := pos
		var acc units
		for i < len(str) {
			c := str[i]
			i++
			if escaping || c == '\\' {
				escaping = !escaping
				acc = append(acc, c)
				continue
			}
			if inBrace {
				if i == braceStart+1 {
					if c == '^' || c == '!' {
						braceNeg = true
					}
				} else if c == ']' && (i != braceStart+2 || !braceNeg) {
					inBrace = false
				}
				acc = append(acc, c)
				continue
			} else if c == '[' {
				inBrace, braceStart, braceNeg = true, i, false
				acc = append(acc, c)
				continue
			}
			if isExtglobType(c) && charAt(str, i) == '(' && extDepth <= maxExtglobRecursion {
				n.push(part{s: acc})
				acc = nil
				ext := newNode(c, n)
				i = parseAST(str, ext, i, extDepth+1)
				n.push(part{a: ext})
				continue
			}
			acc = append(acc, c)
		}
		n.push(part{s: acc})
		return i
	}
	i := pos + 1
	cur := newNode(0, n)
	var parts []part
	var acc units
	for i < len(str) {
		c := str[i]
		i++
		if escaping || c == '\\' {
			escaping = !escaping
			acc = append(acc, c)
			continue
		}
		if inBrace {
			if i == braceStart+1 {
				if c == '^' || c == '!' {
					braceNeg = true
				}
			} else if c == ']' && (i != braceStart+2 || !braceNeg) {
				inBrace = false
			}
			acc = append(acc, c)
			continue
		} else if c == '[' {
			inBrace, braceStart, braceNeg = true, i, false
			acc = append(acc, c)
			continue
		}
		if isExtglobType(c) && charAt(str, i) == '(' && (extDepth <= maxExtglobRecursion || n.canAdoptType(c, adoptionAnyMap)) {
			depthAdd := 1
			if n.canAdoptType(c, adoptionAnyMap) {
				depthAdd = 0
			}
			cur.push(part{s: acc})
			acc = nil
			ext := newNode(c, cur)
			cur.push(part{a: ext})
			i = parseAST(str, ext, i, extDepth+depthAdd)
			continue
		}
		if c == '|' {
			cur.push(part{s: acc})
			acc = nil
			parts = append(parts, part{a: cur})
			cur = newNode(0, n)
			continue
		}
		if c == ')' {
			if len(acc) == 0 && len(n.parts) == 0 {
				n.emptyExt = true
			}
			cur.push(part{s: acc})
			n.push(append(parts, part{a: cur})...)
			return i
		}
		acc = append(acc, c)
	}
	// An unfinished extglob is no extglob: the text from its type on.
	n.typ = 0
	n.hasMagic = -1
	n.parts = []part{{s: slices.Clone(str[pos-1:])}}
	return i
}

// The extglob adoption tables of ast.js.
var (
	adoptionMap          = map[rune]string{'!': "@", '?': "?@", '@': "@", '*': "*+?@", '+': "+@"}
	adoptionWithSpaceMap = map[rune]string{'!': "?", '@': "?", '+': "?*"}
	adoptionAnyMap       = map[rune]string{'!': "?@", '?': "?@", '@': "?@", '*': "*+?@", '+': "+@?*"}
	usurpMap             = map[rune]map[rune]rune{
		'!': {'!': '@'},
		'?': {'*': '*', '+': '*'},
		'@': {'!': '!', '?': '?', '@': '@', '*': '*', '+': '+'},
		'+': {'?': '*', '*': '*'},
	}
)

func (n *node) canAdoptType(c rune, m map[rune]string) bool {
	return slices.Contains([]rune(m[n.typ]), c)
}

func (n *node) canAdopt(child *node, m map[rune]string) bool {
	if child == nil || child.typ != 0 || len(child.parts) != 1 || n.typ == 0 {
		return false
	}
	gc := child.parts[0].a
	if gc == nil || gc.typ == 0 {
		return false
	}
	return n.canAdoptType(gc.typ, m)
}

func (n *node) adoptWithSpace(child *node, index int) {
	gc := child.parts[0].a
	blank := newNode(0, gc)
	blank.parts = append(blank.parts, part{s: units{}})
	gc.push(part{a: blank})
	n.adopt(child, index)
}

func (n *node) adopt(child *node, index int) {
	gc := child.parts[0].a
	n.parts = slices.Concat(n.parts[:index], gc.parts, n.parts[index+1:])
	for _, p := range gc.parts {
		if p.a != nil {
			p.a.parent = n
		}
	}
	n.strSet = false
}

func (n *node) canUsurp(child *node) bool {
	if child == nil || child.typ != 0 || len(child.parts) != 1 || n.typ == 0 || len(n.parts) != 1 {
		return false
	}
	gc := child.parts[0].a
	if gc == nil || gc.typ == 0 {
		return false
	}
	_, ok := usurpMap[n.typ][gc.typ]
	return ok
}

func (n *node) usurp(child *node) {
	gc := child.parts[0].a
	nt := usurpMap[n.typ][gc.typ]
	n.parts = gc.parts
	for _, p := range n.parts {
		if p.a != nil {
			p.a.parent = n
		}
	}
	n.typ = nt
	n.strSet = false
	n.emptyExt = false
}

func (n *node) flatten() {
	if !isExtglobType(n.typ) {
		for _, p := range n.parts {
			if p.a != nil {
				p.a.flatten()
			}
		}
	} else {
		for iterations := 0; ; {
			done := true
			//nolint:intrange // adopting changes the parts, and ast.js reads their length each turn
			for i := 0; i < len(n.parts); i++ {
				c := n.parts[i].a
				if c == nil {
					continue
				}
				c.flatten()
				switch {
				case n.canAdopt(c, adoptionMap):
					done = false
					n.adopt(c, i)
				case n.canAdopt(c, adoptionWithSpaceMap):
					done = false
					n.adoptWithSpace(c, i)
				case n.canUsurp(c):
					done = false
					n.usurp(c)
				}
			}
			iterations++
			if done || iterations >= 10 {
				break
			}
		}
	}
	n.strSet = false
}

// fromGlob parses one segment of a glob.
func fromGlob(pattern units) *node {
	n := newNode(0, nil)
	parseAST(pattern, n, 0, 0)
	return n
}

// mmPattern is what toMMPattern returns: a string to compare, or a regex.
type mmPattern struct {
	literal units
	re      *regex
}

// toMMPattern is the segment's regex if it has magic, or if nocase finds a
// letter in it, and otherwise its unescaped text.
func (n *node) toMMPattern(sb *sourceBudget) (mmPattern, error) {
	glob := n.toString()
	n.flatten()
	n.fillNegs()
	// fillNegs makes every node the tree will have, so the node bound
	// refuses before any source is written.
	if *n.count > maxNodes {
		return mmPattern{}, errNodes
	}
	re, body, hasMagic, uflag, err := n.rootSource(glob, sb)
	if err != nil {
		return mmPattern{}, err
	}
	anyMagic := hasMagic || n.hasMagic == 1 || !slices.Equal(jsUpper(glob), jsLower(glob))
	if !anyMagic {
		return mmPattern{literal: body}, nil
	}
	src := concat(units{'^'}, re, units{'$'})
	if sb.left -= len(src); sb.left < 0 {
		return mmPattern{}, errSource
	}
	r, err := compileJS(src, true, uflag)
	if err != nil {
		return mmPattern{}, err
	}
	return mmPattern{re: r}, nil
}

// source is the regex source of one segment, written once as the tree is
// walked. ast.js has each node return its source and copies it into its
// parent's, and unescapes each node's body, which only the root uses, so
// a negation whose tail fillNegs copied into 4096 nodes built gigabytes
// before the source bound could refuse it. Here every node appends to buf,
// and the build stops as soon as the source passes limit: what maxSource
// leaves for the glob, or two units for each character of the segment
// where that is more. A segment without magic, which toMMPattern does not
// hold to maxSource, has at most two units of source per character, so the
// build refuses only what toMMPattern's own check of maxSource would.
type source struct {
	limit int
	buf   units
	// noTrav are the offsets in buf where startNoTraversal goes. A plain
	// node knows whether it needs it only once its parts are written, and
	// the offsets keep it from moving them.
	noTrav  []int
	err     error
	written int
}

// size is the length the source will have.
func (b *source) size() int { return len(b.buf) + len(b.noTrav)*len(startNoTraversal) }

// write appends u, and sets err once the source passes limit.
func (b *source) write(u ...rune) {
	if b.err != nil {
		return
	}
	b.buf = append(b.buf, u...)
	b.written += len(u)
	if b.size() > b.limit {
		b.err = errSource
	}
}

// truncate drops what was written from mark on.
func (b *source) truncate(mark int) {
	b.buf = b.buf[:mark]
	b.noTrav = slices.DeleteFunc(b.noTrav, func(at int) bool { return at >= mark })
}

// from is the source from offset mark on, with startNoTraversal where it
// goes, except at mark itself.
func (b *source) from(mark int) units {
	slices.Sort(b.noTrav)
	out := make(units, 0, b.size()-mark)
	last := mark
	for _, at := range b.noTrav {
		if at <= mark {
			continue
		}
		out = append(out, b.buf[last:at]...)
		out = append(out, startNoTraversal...)
		last = at
	}
	return append(out, b.buf[last:]...)
}

// rootSource is ast.js's toRegExpSource on the root, with dot set: the
// regex source, the unescaped body, whether it has magic, and whether it
// needs the u flag. glob is the segment's text; sb.written gains the units
// the build appended.
func (n *node) rootSource(glob units, sb *sourceBudget) (units, units, bool, bool, error) {
	b := &source{limit: max(sb.left, 2*len(glob))}
	hasMagic, uflag := n.writeSource(b)
	sb.written += b.written
	if b.err != nil {
		return nil, nil, false, false, b.err
	}
	body := b.from(0)
	re := body
	if slices.Contains(b.noTrav, 0) {
		re = concat(startNoTraversal, body)
	}
	return re, unescape(body), hasMagic, uflag, nil
}

// writeSource is ast.js's toRegExpSource for every node: it writes the
// node's regex source to b, and returns whether it has magic and whether
// it needs the u flag.
func (n *node) writeSource(b *source) (bool, bool) {
	if !isExtglobType(n.typ) {
		noEmpty := n.isStart() && n.isEnd() && !slices.ContainsFunc(n.parts, func(p part) bool { return p.a != nil })
		mark := len(b.buf)
		for _, p := range n.parts {
			var magic, uflag bool
			if p.a == nil {
				var re units
				re, magic, uflag = parseGlob(p.s, n.hasMagic, noEmpty)
				b.write(re...)
			} else {
				magic, uflag = p.a.writeSource(b)
			}
			if b.err != nil {
				return false, false
			}
			if n.hasMagic != 1 {
				n.hasMagic = boolInt(magic)
			}
			n.uflag = n.uflag || uflag
		}
		if n.isStart() && len(n.parts) > 0 && n.parts[0].a == nil {
			first := n.parts[0].s
			dotTravAllowed := len(n.parts) == 1 && (slices.Equal(first, units{'.'}) || slices.Equal(first, units{'.', '.'}))
			if !dotTravAllowed {
				// The first part is a string, whose source is not empty,
				// and an extglob after it opens with at least three
				// units, so no offset in noTrav lies among the five
				// units read here.
				src := b.buf[mark:]
				aps := func(c rune) bool { return c == '[' || c == '.' }
				needNoTrav := aps(charAt(src, 0)) ||
					(hasPrefixAt(src, units{'\\', '.'}, 0) && aps(charAt(src, 2))) ||
					(hasPrefixAt(src, units{'\\', '.', '\\', '.'}, 0) && aps(charAt(src, 4)))
				if needNoTrav {
					b.noTrav = append(b.noTrav, mark)
					if b.size() > b.limit {
						b.err = errSource
						return false, false
					}
				}
			}
		}
		if n.isEnd() && n.root.filledNegs && n.parent != nil && n.parent.typ == '!' {
			b.write(units(`(?:$|\/)`)...)
		}
		if n.hasMagic < 0 {
			n.hasMagic = 0
		}
		return n.hasMagic == 1, n.uflag
	}
	mark := len(b.buf)
	if n.typ == '!' {
		b.write(units(`(?:(?!(?:`)...)
	} else {
		b.write(units(`(?:`)...)
	}
	bodyMark := len(b.buf)
	n.writeParts(b)
	if b.err != nil {
		return false, false
	}
	if n.isStart() && n.isEnd() && len(b.buf) == bodyMark && n.typ != '!' {
		// An extglob that is the whole segment and matches nothing is
		// read as its text.
		s := n.toString()
		n.parts = []part{{s: s}}
		n.typ = 0
		n.hasMagic = -1
		b.truncate(mark)
		b.write(s...)
		return false, false
	}
	if n.typ == '!' && n.emptyExt {
		b.truncate(mark)
		b.write(starNoEmpty...)
	} else {
		switch n.typ {
		case '!':
			b.write(units(`))`)...)
			b.write(star...)
			b.write(')')
		case '@':
			b.write(')')
		case '?':
			b.write(')', '?')
		default:
			b.write(')', n.typ)
		}
	}
	if n.hasMagic < 0 {
		n.hasMagic = 0
	}
	return n.hasMagic == 1, n.uflag
}

func boolInt(b bool) int8 {
	if b {
		return 1
	}
	return 0
}

// writeParts is ast.js's partsToRegExp: the alternatives, joined by |.
func (n *node) writeParts(b *source) {
	// Writing a part changes neither n's type nor its ancestors, so
	// whether n is the whole segment holds for every part.
	whole := n.isStart() && n.isEnd()
	first := true
	for _, p := range n.parts {
		sep := len(b.buf)
		if !first {
			b.write('|')
		}
		start := len(b.buf)
		_, uflag := p.a.writeSource(b)
		if b.err != nil {
			return
		}
		n.uflag = n.uflag || uflag
		if whole && len(b.buf) == start {
			b.truncate(sep)
			continue
		}
		first = false
	}
}

// reSpecials are the characters an escape in a glob keeps escaped.
const reSpecials = `().*{}+?[]^$\!`

// parseGlob is ast.js's #parseGlob: the regex source of a glob without
// extglobs.
func parseGlob(glob units, hasMagic int8, noEmpty bool) (units, bool, bool) {
	escaping, uflag, inStar := false, false, false
	magic := hasMagic == 1
	allStars := len(glob) > 0 && !slices.ContainsFunc(glob, func(c rune) bool { return c != '*' })
	var re units
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		if escaping {
			escaping = false
			if slices.Contains([]rune(reSpecials), c) {
				re = append(re, '\\')
			}
			re = append(re, c)
			continue
		}
		if c == '*' {
			if inStar {
				continue
			}
			inStar = true
			if noEmpty && allStars {
				re = append(re, starNoEmpty...)
			} else {
				re = append(re, star...)
			}
			magic = true
			continue
		}
		inStar = false
		if c == '\\' {
			if i == len(glob)-1 {
				re = append(re, '\\', '\\')
			} else {
				escaping = true
			}
			continue
		}
		if c == '[' {
			src, needU, consumed, isMagic := parseClass(glob, i)
			if consumed > 0 {
				re = append(re, src...)
				uflag = uflag || needU
				i += consumed - 1
				magic = magic || isMagic
				continue
			}
		}
		if c == '?' {
			re = append(re, qmark...)
			magic = true
			continue
		}
		re = append(re, regExpEscape(units{c})...)
	}
	return re, magic, uflag
}

// unescape is minimatch's unescape with its defaults: a class of one
// character becomes the character, and a backslash before anything but /
// goes.
func unescape(s units) units {
	var a units
	for i := 0; i < len(s); {
		// ((?!\\).|^)\[([^/\\])\]
		if i+3 < len(s) && s[i] != '\\' && !lineTerminator(s[i]) && s[i+1] == '[' && s[i+2] != '/' && s[i+2] != '\\' && s[i+3] == ']' {
			a = append(a, s[i], s[i+2])
			i += 4
			continue
		}
		if i == 0 && i+2 < len(s) && s[0] == '[' && s[1] != '/' && s[1] != '\\' && s[2] == ']' {
			a = append(a, s[1])
			i += 3
			continue
		}
		a = append(a, s[i])
		i++
	}
	var out units
	for i := 0; i < len(a); i++ {
		if a[i] == '\\' && i+1 < len(a) && a[i+1] != '/' {
			out = append(out, a[i+1])
			i++
			continue
		}
		out = append(out, a[i])
	}
	return out
}
