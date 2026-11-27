package k8s

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/core/workload"
)

// extractor reads one manifest file. inSet marks a file of a set, where a
// file with no object the selector matches adds no value rather than being
// refused: a glob over every manifest holds the kind in some of them.
type extractor struct {
	core.Source
	spec    Spec
	path    core.Path
	parsers core.Parsers
	inSet   bool
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract reads the file under root and returns the values the path
// selects in the objects, containers, ports or volumes the spec selects.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, e.Source, e)
}

// FromShared returns the values in data, the bytes of the file, with the
// budgets the files of a set read before it have spent.
func (e extractor) FromShared(data []byte, spent *core.Spent) ([]check.Value, error) {
	docs, err := e.parse(data, spent)
	if err != nil {
		return nil, err
	}
	objs := e.selected(markExpanded(docs))
	if len(objs) == 0 && !e.inSet {
		return nil, fmt.Errorf("%s: no object is %s", e.File, e.spec.Object)
	}
	var roots []*core.Node
	for _, obj := range objs {
		r, err := e.roots(obj)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.File, err)
		}
		roots = append(roots, r...)
	}
	vs, err := e.EmbeddedValues(e.path, core.Embedding{}, core.Reading{Last: core.LastLine(data)}, spent, roots...)
	if err != nil {
		return nil, spent.SharedAliases(err)
	}
	return e.markTemplated(data, vs)
}

// parse is the documents of data: the one a JSON file (workload.JSON)
// holds, or every document of any other file, parsed as YAML, starting the
// count of the nodes aliases add at what the files before spent. A file
// that does not parse is an error, so its rule is unknown.
func (e extractor) parse(data []byte, spent *core.Spent) ([]*core.Node, error) {
	if workload.JSON(e.File) {
		if err := e.CheckNodes(data, core.JSONFormat); err != nil {
			return nil, err
		}
		doc, err := e.parsers.JSON(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.File, err)
		}
		return []*core.Node{doc}, nil
	}
	if err := e.CheckNodes(data, core.YAMLFormat); err != nil {
		return nil, err
	}
	docs, expanded, err := e.parsers.YAML(data, spent.Expanded)
	spent.Expanded = expanded
	if err != nil {
		return nil, spent.SharedAliases(fmt.Errorf("%s: %w", e.File, err))
	}
	return docs, nil
}

// selected is every object of objs the spec's Object matches, in the
// file's order.
func (e extractor) selected(objs []*core.Node) []*core.Node {
	var out []*core.Node
	for _, d := range objs {
		if e.spec.Object.matches(d) {
			out = append(out, d)
		}
	}
	return out
}

// matches reports whether obj is an object o selects.
func (o Object) matches(obj *core.Node) bool {
	meta := obj.Field["metadata"]
	return scalar(obj, "kind") == o.Kind &&
		(o.APIVersion == "" || scalar(obj, "apiVersion") == o.APIVersion) &&
		(o.Name == "" || scalar(meta, "name") == o.Name) &&
		(o.Namespace == "" || namespace(meta) == o.Namespace)
}

// namespace is the namespace of an object whose metadata is meta: the one
// it writes, or default.
func namespace(meta *core.Node) string {
	if ns := scalar(meta, "namespace"); ns != "" {
		return ns
	}
	return "default"
}

// String is the selector as a message names it: its kind, and each other
// field it sets.
func (o Object) String() string {
	var b strings.Builder
	b.WriteString("of kind " + core.Quote(o.Kind))
	for _, f := range []struct{ what, v string }{{"apiVersion", o.APIVersion}, {"namespace", o.Namespace}, {"name", o.Name}} {
		if f.v != "" {
			b.WriteString(", " + f.what + " " + core.Quote(f.v))
		}
	}
	return b.String()
}

// roots are the nodes of obj the path is read in: obj itself, the container
// the spec names, its port or its mount of the volume, or the pod's
// volume. A container, port or volume obj does not hold is an error,
// naming what it holds, since the object the rule meant is there.
func (e extractor) roots(obj *core.Node) ([]*core.Node, error) {
	if e.spec.Container == "" && e.spec.Volume == "" {
		return []*core.Node{obj}, nil
	}
	at := fmt.Sprintf("the object %s at line %d", e.spec.Object, obj.Line)
	if e.spec.Container == "" {
		return named(workload.PodSpec(obj), "volumes", e.spec.Volume, at, "volume")
	}
	var found []*core.Node
	var names []string
	for _, c := range workload.Of(obj) {
		names = append(names, c.Name)
		if c.Name == e.spec.Container {
			found = append(found, c.Node)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("%s has no container %s; its containers are %s", at, core.Quote(e.spec.Container), quoted(names))
	}
	at = fmt.Sprintf("the container %s of %s", core.Quote(e.spec.Container), at)
	switch {
	case e.spec.Port != "":
		return many(found, "ports", e.spec.Port, at, "port")
	case e.spec.Volume != "":
		return many(found, "volumeMounts", e.spec.Volume, at, "volume mount")
	}
	return found, nil
}

// many is the items named finds in each of parents, the containers of one
// name, in turn. A container that holds none adds none, so the rule reads
// the port or mount of the one that does; when none does, it is the error
// named gives for the last.
func many(parents []*core.Node, list, name, at, what string) ([]*core.Node, error) {
	var out []*core.Node
	var err error
	for _, p := range parents {
		got, e := named(p, list, name, at, what)
		if e != nil {
			err = e
			continue
		}
		out = append(out, got...)
	}
	if len(out) == 0 {
		return nil, err
	}
	return out, nil
}

// named is every item of the sequence under list in parent whose name is
// name, or an error naming the items' names when none is: a pod's
// volumes, a container's ports or its volume mounts. at names parent, and
// what an item, for the message.
func named(parent *core.Node, list, name, at, what string) ([]*core.Node, error) {
	var out []*core.Node
	var names []string
	if l := field(parent, list); l != nil && l.Shape == core.Sequence {
		for _, item := range l.Items {
			n := scalar(item, "name")
			names = append(names, n)
			if n == name {
				out = append(out, item)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no %s %s under %s; its names there are %s", at, what, core.Quote(name), list, quoted(names))
	}
	return out, nil
}

// quoted is names quoted and joined for a message, or none.
func quoted(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = core.Quote(n)
	}
	return strings.Join(q, ", ")
}

// field is the node under key in the mapping n, or nil.
func field(n *core.Node, key string) *core.Node {
	if n == nil || n.Shape != core.Mapping {
		return nil
	}
	return n.Field[key]
}

// scalar is the text of the scalar under key in the mapping n, or "".
func scalar(n *core.Node, key string) string {
	v := field(n, key)
	if v == nil || v.Shape != core.Scalar {
		return ""
	}
	return v.Text
}

// markExpanded is the objects of docs (workload.Objects), a List's items
// among them, with each scalar of their containers that the kubelet changes
// when it expands it (workload.Expands) marked not literal. Every object is
// marked, not only those a spec selects, so a path read through a List's
// items sees the marks its items' containers hold.
//
// The marks go on copies. The yaml reader hands an anchor and each of its
// aliases one node, so a mark on that node would also mark a use of it
// outside any container, which the kubelet does not expand. Each container
// that holds such a scalar is replaced in its pod spec by a copy, whose
// lists, items and scalars on the way to each mark are copies too, so a
// container list an alias shares with a place outside a pod spec stays
// unmarked there. The pod spec itself is changed in place: a pod spec, or
// a mapping above it such as a template, that an alias shares with a
// place outside any pod spec is the one case still marked there.
func markExpanded(docs []*core.Node) []*core.Node {
	objs := workload.Objects(docs)
	for _, obj := range objs {
		spec := workload.PodSpec(obj)
		if spec == nil {
			continue
		}
		for g := workload.Regular; g <= workload.Ephemeral; g++ {
			if l := replaceItems(spec.Field[g.Key()], markedContainer); l != nil {
				spec.Set(g.Key(), l)
			}
		}
	}
	return objs
}

// expandedItems are the keys of a container's lists whose every item the
// kubelet expands $(NAME) in.
var expandedItems = []string{"command", "args"}

// expandedFields are the keys of a container's lists of mappings, and the
// key in each item whose value the kubelet expands: an env entry's value,
// and a volume mount's subPathExpr.
var expandedFields = []struct{ list, key string }{{"env", "value"}, {"volumeMounts", "subPathExpr"}}

// markedContainer is a copy of the container c with every scalar the
// kubelet expands marked (markedItems, markedFields), or nil when c holds
// none, as an item that is no mapping does.
func markedContainer(c *core.Node) *core.Node {
	var out *core.Node
	set := func(key string, l *core.Node) {
		if l == nil {
			return
		}
		if out == nil {
			out = copyMapping(c)
		}
		out.Set(key, l)
	}
	for _, k := range expandedItems {
		set(k, markedItems(field(c, k)))
	}
	for _, f := range expandedFields {
		set(f.list, markedFields(field(c, f.list), f.key))
	}
	return out
}

// markedItems is a copy of the list l with each item the kubelet expands
// marked, or nil when it holds none.
func markedItems(l *core.Node) *core.Node {
	return replaceItems(l, func(item *core.Node) *core.Node {
		if !expands(item) {
			return nil
		}
		return marked(item)
	})
}

// markedFields is a copy of the list l with each mapping whose value at key
// the kubelet expands replaced by a copy with that value marked, or nil
// when it holds none.
func markedFields(l *core.Node, key string) *core.Node {
	return replaceItems(l, func(item *core.Node) *core.Node {
		v := field(item, key)
		if !expands(v) {
			return nil
		}
		m := copyMapping(item)
		m.Set(key, marked(v))
		return m
	})
}

// replaceItems is a copy of the sequence l in which each item that f
// returns a node for is that node, or nil when l is no sequence or f
// returns nil for every item.
func replaceItems(l *core.Node, f func(*core.Node) *core.Node) *core.Node {
	if l == nil || l.Shape != core.Sequence {
		return nil
	}
	var out *core.Node
	for i, item := range l.Items {
		r := f(item)
		if r == nil {
			continue
		}
		if out == nil {
			c := *l
			c.Items = slices.Clone(l.Items)
			out = &c
		}
		out.Items[i] = r
	}
	return out
}

// expands reports whether n is a scalar the kubelet changes when it expands
// it, and not marked yet: a pod spec two objects share through an alias is
// met twice, and the second time holds the marked copies.
func expands(n *core.Node) bool {
	return n != nil && n.Shape == core.Scalar && !n.NotLiteral && workload.Expands(n.Text)
}

// marked is a copy of the scalar n, marked not literal.
func marked(n *core.Node) *core.Node {
	m := *n
	m.NotLiteral = true
	return &m
}

// copyMapping is a copy of the mapping n whose values Set may replace
// without changing n.
func copyMapping(n *core.Node) *core.Node {
	m := *n
	m.Field = maps.Clone(n.Field)
	m.Vals = slices.Clone(n.Vals)
	return &m
}

// markTemplated marks not literal each of vs that a template file computes:
// one holding its engine's tags, or inside one of its blocks, such as
// {% if %}, which the rendered file may not hold. Any other file's values
// are vs.
func (e extractor) markTemplated(data []byte, vs []check.Value) ([]check.Value, error) {
	test := core.TemplateNotLiteral(e.File, nil)
	if test == nil {
		return vs, nil
	}
	blocks, err := core.TemplateBlocks(e.File, data, core.NewMeter(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.File, err)
	}
	for i, v := range vs {
		in, err := blocks.Holds(v.At.Line)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.File, err)
		}
		vs[i].NotLiteral = v.NotLiteral || in || test(&core.Entry{Value: []byte(v.Text)})
	}
	return vs, nil
}
