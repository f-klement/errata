package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	refxpath "github.com/antchfx/xpath"
	"pgregory.net/rapid"
)

// The property test and the differential below draw an XML document as a
// model, with the value and the line of every node known as it is written,
// and write it with the shapes that move lines and names: attributes on
// lines of their own, white space around =, a namespace by a prefix or by
// the default namespace, xmlns="" to leave one, entities, CDATA and
// comments. The model is the reference, not errata's parse of the file.

// The namespaces a model uses, the prefixes its file writes for them, and
// the prefixes its paths bind them to, which differ from the file's.
var (
	modelSpaces = []string{"", "urn:a", "urn:b"}
	filePrefix  = map[string]string{"urn:a": "p", "urn:b": "q"}
	pathNS      = map[string]string{"m": "urn:a", "n": "urn:b"}
	pathPrefix  = map[string]string{"urn:a": "m", "urn:b": "n"}
)

// mnode is a node of the model: the root, an element, an attribute or a
// text node, with what errata must return for it.
type mnode struct {
	typ          refxpath.NodeType
	space, local string
	text         string // a text node's text or an attribute's value
	line         int
	order        int // document order, among all nodes
	parent       *mnode
	kids         []*mnode // elements and text nodes
	attrs        []*mnode
}

// value is the node's value as errata gives it: an element's text content
// trimmed, as XPath compares it here too.
func (n *mnode) value() string {
	if n.typ == refxpath.AttributeNode || n.typ == refxpath.TextNode {
		return n.text
	}
	var b strings.Builder
	var walk func(*mnode)
	walk = func(m *mnode) {
		for _, k := range m.kids {
			if k.typ == refxpath.TextNode {
				b.WriteString(k.text)
			} else {
				walk(k)
			}
		}
	}
	walk(n)
	return strings.Trim(b.String(), " \t\r\n")
}

// modelWriter writes a model as XML, counting the lines it writes.
type modelWriter struct {
	t     *rapid.T
	b     strings.Builder
	line  int
	order int
}

// write writes s, counting its line ends as XML does: a CRLF, a lone CR or
// a lone LF. No write that follows one ending in CR starts with LF.
func (w *modelWriter) write(s string) {
	w.b.WriteString(s)
	w.line += strings.Count(strings.ReplaceAll(s, "\r\n", "\n"), "\n") + strings.Count(strings.ReplaceAll(s, "\r\n", ""), "\r")
}

// blank draws white space, with a newline at times, and from more shapes
// when wide is set.
func (w *modelWriter) blank(label string, wide bool) string {
	shapes := []string{" ", "\n", "  ", " \n\t", "\r\n", "\r", "\r\r\n"}
	if !wide {
		shapes = shapes[:2]
	}
	return rapid.SampledFrom(shapes).Draw(w.t, label)
}

var textRunes = []rune("ab <&>'\"\n\té]")

func drawText(t *rapid.T, label string, newlines bool) string {
	runes := textRunes
	if !newlines {
		runes = []rune("ab <&>'\"é]\t\n\r")
	}
	return rapid.StringOfN(rapid.SampledFrom(runes), 0, 6, -1).Draw(t, label)
}

// escape writes s as character data or as an attribute value in quote q.
func escape(s string, q rune) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&#62;")
		case q:
			b.WriteString("&#" + strconv.Itoa(int(r)) + ";")
		case '\r':
			b.WriteString("&#13;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// element draws an element and writes it, under the default namespace
// def, with budget elements at most left to draw.
func (w *modelWriter) element(parent *mnode, def string, depth int, budget *int) *mnode {
	t := w.t
	*budget--
	el := &mnode{
		typ: refxpath.ElementNode, parent: parent,
		space: rapid.SampledFrom(modelSpaces).Draw(t, "space"),
		local: rapid.SampledFrom([]string{"a", "b", "c"}).Draw(t, "local"),
		order: w.order,
	}
	w.order++
	w.write("<")
	el.line = w.line + 1
	var decls []string
	prefix := ""
	switch {
	case el.space == def:
	case el.space == "":
		decls, def = append(decls, `xmlns=""`), ""
	case rapid.Bool().Draw(t, "default"):
		decls, def = append(decls, `xmlns="`+el.space+`"`), el.space
	default:
		prefix = filePrefix[el.space] + ":"
	}
	if parent.typ == refxpath.RootNode {
		decls = append(decls, `xmlns:p="urn:a"`, `xmlns:q="urn:b"`)
	}
	w.write(prefix + el.local)
	for _, d := range decls {
		w.write(w.blank("declblank", false) + d)
	}
	seen := map[string]bool{}
	for range rapid.IntRange(0, 3).Draw(t, "attrs") {
		a := &mnode{
			typ: refxpath.AttributeNode, parent: el,
			space: rapid.SampledFrom(modelSpaces).Draw(t, "aspace"),
			local: rapid.SampledFrom([]string{"x", "y"}).Draw(t, "alocal"),
			text:  drawText(t, "avalue", false),
		}
		if seen[a.space+" "+a.local] {
			continue
		}
		seen[a.space+" "+a.local] = true
		a.order = w.order
		w.order++
		w.write(w.blank("ablank", true))
		a.line = w.line + 1
		name := a.local
		if a.space != "" {
			name = filePrefix[a.space] + ":" + a.local
		}
		q := rapid.SampledFrom([]rune{'"', '\''}).Draw(t, "quote")
		w.write(name + rapid.SampledFrom([]string{"=", " = ", "\n=\n"}).Draw(t, "eq") + string(q) + escape(a.text, q) + string(q))
		// XML 1.0 section 3.3.3: a tab or a newline written in a value reads
		// as a space; escape writes a CR as a reference, which keeps it.
		a.text = strings.NewReplacer("\t", " ", "\n", " ").Replace(a.text)
		el.attrs = append(el.attrs, a)
	}
	if rapid.Bool().Draw(t, "tagblank") {
		w.write(w.blank("endblank", true))
	}
	n := 0
	if depth < 4 && *budget > 0 {
		n = rapid.IntRange(0, 3).Draw(t, "kids")
	}
	if n == 0 && rapid.Bool().Draw(t, "empty") {
		w.write("/>")
		return el
	}
	w.write(">")
	lastText := false
	for range n {
		if rapid.IntRange(0, 2).Draw(t, "kind") == 0 {
			text := drawText(t, "text", true)
			if text == "" {
				continue
			}
			if lastText {
				w.write("<!-- -->") // a comment keeps two text nodes apart
			}
			tx := &mnode{typ: refxpath.TextNode, parent: el, text: text, line: w.line + 1, order: w.order}
			w.order++
			if rapid.Bool().Draw(t, "cdata") && !strings.Contains(text, "]]>") && !strings.Contains(text, "\r") {
				w.write("<![CDATA[" + text + "]]>")
			} else {
				w.write(escape(text, 0))
			}
			el.kids = append(el.kids, tx)
			lastText = true
			continue
		}
		el.kids = append(el.kids, w.element(el, def, depth+1, budget))
		lastText = false
	}
	w.write("</" + prefix + el.local)
	if rapid.Bool().Draw(t, "closeblank") {
		w.write(w.blank("closeblank", false))
	}
	w.write(">")
	return el
}

// drawModel draws a document and the file that writes it.
func drawModel(t *rapid.T) (*mnode, string) {
	w := &modelWriter{t: t}
	if rapid.Bool().Draw(t, "decl") {
		w.write("<?xml version=\"1.0\"?>" + w.blank("declend", true))
	}
	if rapid.Bool().Draw(t, "comment") {
		w.write("<!-- before -->\n")
	}
	root := &mnode{typ: refxpath.RootNode, order: -1}
	w.order = 0
	budget := 12
	root.kids = []*mnode{w.element(root, "", 0, &budget)}
	w.write(rapid.SampledFrom([]string{"", "\n", "\n<!-- after -->\n"}).Draw(t, "tail"))
	return root, w.b.String()
}

// nodes lists every node of the model but the root, in document order.
func (n *mnode) nodes(out []*mnode) []*mnode {
	if n.typ != refxpath.RootNode {
		out = append(out, n)
	}
	out = append(out, n.attrs...)
	for _, k := range n.kids {
		out = k.nodes(out)
	}
	return out
}

// pathTo is a path that selects n alone: each step names the element as the
// paths bind its namespace, and its position among its siblings of that
// name.
func pathTo(n *mnode) string {
	name := func(m *mnode) string {
		if m.space == "" {
			return m.local
		}
		return pathPrefix[m.space] + ":" + m.local
	}
	var steps []string
	cur := n
	switch n.typ {
	case refxpath.AttributeNode:
		steps = append(steps, "@"+name(n))
		cur = n.parent
	case refxpath.TextNode:
		k := 0
		for _, s := range n.parent.kids {
			if s.typ == refxpath.TextNode {
				k++
			}
			if s == n {
				break
			}
		}
		steps = append(steps, "text()["+strconv.Itoa(k)+"]")
		cur = n.parent
	default: // an element, whose steps the loop below writes
	}
	for ; cur.typ != refxpath.RootNode; cur = cur.parent {
		k := 0
		for _, s := range cur.parent.kids {
			if s.typ == refxpath.ElementNode && s.space == cur.space && s.local == cur.local {
				k++
			}
			if s == cur {
				break
			}
		}
		steps = append(steps, name(cur)+"["+strconv.Itoa(k)+"]")
	}
	slices.Reverse(steps)
	return "/" + strings.Join(steps, "/")
}

// Every element, attribute and text node of a drawn file comes back once,
// by a path that names it, with its value, at the line where it starts.
func TestPropertyXMLValuesSitAtTheirLine(t *testing.T) {
	dir := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		model, body := drawModel(t)
		if err := os.WriteFile(filepath.Join(dir, "f.xml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, n := range model.nodes(nil) {
			path := pathTo(n)
			got := mustExtract(t, Spec{XML: &XML{File: "f.xml", Path: path, Namespaces: pathNS}}, dir)
			if len(got) != 1 || got[0].Text != n.value() || got[0].At.Line != n.line {
				t.Fatalf("%s: got %q, want %q at line %d\n%s", path, texts(got), n.value(), n.line, body)
			}
		}
	})
}

// drawPath draws a path in the subset over the names a model uses, with
// literals taken from its values, so that predicates hold at times.
func drawPath(t *rapid.T, values []string) string {
	name := func(label string, attr bool) string {
		locals := []string{"a", "b", "c", "*"}
		if attr {
			locals = []string{"x", "y", "*"}
		}
		local := rapid.SampledFrom(locals).Draw(t, label+"local")
		if local == "*" {
			return local
		}
		return rapid.SampledFrom([]string{"", "m:", "n:"}).Draw(t, label+"prefix") + local
	}
	literal := func() string {
		v := rapid.SampledFrom(append(values, "zz")).Draw(t, "literal")
		if strings.Contains(v, "'") {
			if strings.Contains(v, `"`) {
				return "'zz'"
			}
			return `"` + v + `"`
		}
		return "'" + v + "'"
	}
	var b strings.Builder
	steps := rapid.IntRange(1, 4).Draw(t, "steps")
	for i := range steps {
		b.WriteString(rapid.SampledFrom([]string{"/", "//"}).Draw(t, "sep"))
		last := i == steps-1
		kind := 0
		if last {
			kind = rapid.IntRange(0, 2).Draw(t, "lastkind")
		}
		switch kind {
		case 1:
			b.WriteString("@" + name("step", true))
		case 2:
			b.WriteString("text()")
		default:
			b.WriteString(name("step", false))
		}
		preds := rapid.IntRange(0, 2).Draw(t, "preds")
		if kind == 1 {
			// The reference counts every attribute as position 1 (its
			// getNodePosition knows no attribute axis), so @*[1] selects
			// every attribute there. TestXMLSelectsWhatXPathSelects holds
			// errata's answer.
			preds = 0
		}
		if kind == 2 {
			preds = min(preds, 1) // text() takes a position only
		}
		for i := range preds {
			// The reference counts a position after another predicate per
			// depth, not per parent, so //*[1][1] and //*[@x='a'][1]
			// select one node there for each depth. A position is drawn
			// only first; errata's answer for the others is in
			// TestXMLSelectsWhatXPathSelects.
			if i == 0 && (kind != 0 || rapid.Bool().Draw(t, "position")) {
				b.WriteString("[" + strconv.Itoa(rapid.IntRange(1, 3).Draw(t, "n")) + "]")
				continue
			}
			var cs []string
			for range rapid.IntRange(1, 2).Draw(t, "compares") {
				operand := name("operand", false)
				if rapid.Bool().Draw(t, "attr") {
					operand = "@" + name("operand", true)
				}
				cs = append(cs, operand+"="+literal())
			}
			b.WriteString("[" + strings.Join(cs, " and ") + "]")
		}
	}
	return b.String()
}

// A differential against github.com/antchfx/xpath, an XPath 1.0
// implementation under the MIT licence, run over the model through a
// navigator. Wherever both read the path, errata selects the nodes the
// reference selects, each once and in document order, with their values and
// lines. An
// element compares and returns its text trimmed in both, which is errata's
// rule and not XPath's; the navigator gives a node in no namespace the
// prefix "" and any other node one, so an unprefixed step selects only nodes
// in no namespace in both, as XPath 1.0 has it.
func TestXPathAgreesWithReference(t *testing.T) {
	dir := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		model, body := drawModel(t)
		if err := os.WriteFile(filepath.Join(dir, "f.xml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var values []string
		for _, n := range model.nodes(nil) {
			values = append(values, n.value())
		}
		path := drawPath(t, values)
		expr, err := refxpath.CompileWithNS(path, pathNS)
		if err != nil {
			t.Fatalf("the reference refuses %s: %v", path, err)
		}
		var want []*mnode
		for it := expr.Select(&mnav{root: model, cur: model, attr: -1}); it.MoveNext(); {
			nav := it.Current().(*mnav)
			n := nav.cur
			if nav.attr >= 0 {
				n = n.attrs[nav.attr]
			}
			want = append(want, n)
		}
		// A node set holds each node once and has no order; the reference
		// can return one node twice, through two contexts of a // step.
		slices.SortFunc(want, func(a, b *mnode) int { return a.order - b.order })
		want = slices.Compact(want)
		got := mustExtract(t, Spec{XML: &XML{File: "f.xml", Path: path, Namespaces: pathNS}}, dir)
		if len(got) != len(want) {
			t.Fatalf("%s: errata %s, the reference %s\n%s", path, texts(got), modelTexts(want), body)
		}
		for i, n := range want {
			if got[i].Text != n.value() || got[i].At.Line != n.line {
				t.Fatalf("%s: errata %s, the reference %s\n%s", path, texts(got), modelTexts(want), body)
			}
		}
	})
}

func modelTexts(ns []*mnode) string {
	var parts []string
	for _, n := range ns {
		parts = append(parts, fmt.Sprintf("%s@%d", n.value(), n.line))
	}
	return strings.Join(parts, " ")
}

// mnav is an refxpath.NodeNavigator over a model: at an attribute when attr is
// its index, at cur otherwise.
type mnav struct {
	root, cur *mnode
	attr      int
}

func (n *mnav) node() *mnode {
	if n.attr >= 0 {
		return n.cur.attrs[n.attr]
	}
	return n.cur
}

func (n *mnav) NodeType() refxpath.NodeType { return n.node().typ }
func (n *mnav) LocalName() string           { return n.node().local }
func (n *mnav) NamespaceURL() string        { return n.node().space }
func (n *mnav) Value() string               { return n.node().value() }
func (n *mnav) Copy() refxpath.NodeNavigator {
	c := *n
	return &c
}
func (n *mnav) MoveToRoot() { n.cur, n.attr = n.root, -1 }

func (n *mnav) Prefix() string {
	if n.node().space == "" {
		return ""
	}
	return "ns"
}

func (n *mnav) MoveToParent() bool {
	switch {
	case n.attr >= 0:
		n.attr = -1
	case n.cur.parent != nil:
		n.cur = n.cur.parent
	default:
		return false
	}
	return true
}

func (n *mnav) MoveToNextAttribute() bool {
	if n.attr+1 >= len(n.cur.attrs) {
		return false
	}
	n.attr++
	return true
}

func (n *mnav) MoveToChild() bool {
	if n.attr >= 0 || len(n.cur.kids) == 0 {
		return false
	}
	n.cur = n.cur.kids[0]
	return true
}

func (n *mnav) sibling(delta int) bool {
	if n.attr >= 0 || n.cur.parent == nil {
		return false
	}
	kids := n.cur.parent.kids
	i := slices.Index(kids, n.cur) + delta
	if i < 0 || i >= len(kids) {
		return false
	}
	n.cur = kids[i]
	return true
}

func (n *mnav) MoveToFirst() bool {
	if n.attr >= 0 || n.cur.parent == nil {
		return false
	}
	n.cur = n.cur.parent.kids[0]
	return true
}

func (n *mnav) MoveToNext() bool     { return n.sibling(1) }
func (n *mnav) MoveToPrevious() bool { return n.sibling(-1) }

func (n *mnav) MoveTo(other refxpath.NodeNavigator) bool {
	o, ok := other.(*mnav)
	if !ok || o.root != n.root {
		return false
	}
	*n = *o
	return true
}
