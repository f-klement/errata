package compose

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// contributor is one service mapping of one file that compose-go merges
// into a service of the project: the file, the project that loads it, the
// service's name there and its YAML. A project's service is merged from
// its contributors in their order, each over the ones before it.
type contributor struct {
	f    *file
	p    *project
	name string
	raw  *core.Node
	// dir is the directory a path the contributor names is relative to:
	// p's for a service of a file p loads, and its file's own for a
	// service an extends chain reached in another file.
	dir string
}

// contributors are the contributors of the service name in p, in the order
// compose-go merges them: for each of p's files, the services the file's
// own one extends, deepest first, then its own; or, for a file that does
// not set the service, the contributors in the first project it includes
// that does.
func (l *loader) contributors(p *project, name string, depth int) []contributor {
	var out []contributor
	for _, f := range p.files {
		out = append(out, l.inFile(p, f, name, depth)...)
	}
	return out
}

func (l *loader) inFile(p *project, f *file, name string, depth int) []contributor {
	if svc := service(f, name); svc != nil {
		return l.chain(contributor{f, p, name, svc, p.dir}, 0)
	}
	if depth > maxFiles {
		return nil // compose-go refuses the cycle
	}
	for _, q := range p.includes[f] {
		if cs := l.contributors(q, name, depth+1); len(cs) > 0 {
			return cs
		}
	}
	return nil
}

// chain is the contributors c extends, deepest first, then c.
func (l *loader) chain(c contributor, depth int) []contributor {
	base, ok := l.extended(c)
	if !ok || depth > maxFiles {
		return []contributor{c}
	}
	return append(l.chain(base, depth+1), c)
}

// extended is the contributor c's extends: names, in its own file or in
// the file it names, which the walker read.
func (l *loader) extended(c contributor) (contributor, bool) {
	name, ref := extendsOf(c.raw.Field["extends"])
	if name == nil {
		return contributor{}, false
	}
	f, dir := c.f, c.dir
	if ref != nil {
		rel, err := inside(c.f.path, c.dir, ref.Text, c.p.env)
		if f = l.w.files[rel]; err != nil || f == nil {
			return contributor{}, false
		}
		dir = path.Dir(rel)
	}
	text, _ := interpolate(name.Text, c.p.env)
	svc := service(f, text)
	return contributor{f, c.p, text, svc, dir}, svc != nil
}

// service is f's own mapping of the service name, nil when it sets none.
func service(f *file, name string) *core.Node {
	if f.doc == nil {
		return nil
	}
	s := f.doc.Field["services"]
	if s == nil || s.Shape != core.Mapping {
		return nil
	}
	if svc := s.Field[name]; svc != nil && svc.Shape == core.Mapping {
		return svc
	}
	return nil
}

// leaf is one value of a service's configuration: the keys and the
// indexes from the service down to it, and the value, a string, a
// json.Number, a bool, or nil for a key compose leaves unset.
type leaf struct {
	steps []any
	value any
}

// selectKey is the part of t, a service's configuration, that key names,
// and the steps down to it. A part may hold dots, as the label
// com.example.name does: at a mapping, the longest run of parts that is
// one of its keys is taken first.
func selectKey(t any, key []string) (any, []any, bool) {
	var steps []any
	for len(key) > 0 {
		var step any
		var n int
		switch v := t.(type) {
		case map[string]any:
			step, n = mapKey(v, key)
		case []any:
			step, n = listIndex(v, key[0])
		}
		if n == 0 {
			return nil, nil, false
		}
		t = child(t, step)
		steps, key = append(steps, step), key[n:]
	}
	return t, steps, true
}

// mapKey is the key of m the first parts of key make, longest first, and
// how many parts it takes; 0 for none.
func mapKey(m map[string]any, key []string) (string, int) {
	for n := len(key); n > 0; n-- {
		k := strings.Join(key[:n], ".")
		if _, ok := m[k]; ok {
			return k, n
		}
	}
	return "", 0
}

// listIndex is part as an index of l, and 1; 0 when it is none.
func listIndex(l []any, part string) (int, int) {
	i, err := strconv.Atoi(part)
	if err != nil || i < 0 || i >= len(l) || strconv.Itoa(i) != part {
		return 0, 0
	}
	return i, 1
}

// child is the part of t under step, a key of a mapping or an index of a
// list.
func child(t any, step any) any {
	if i, ok := step.(int); ok {
		return t.([]any)[i]
	}
	return t.(map[string]any)[step.(string)]
}

// collect adds each value of t, below steps, to out, keys in their order
// and items in theirs, at most core.MaxValues.
func collect(t any, steps []any, out *[]leaf) error {
	switch v := t.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			if err := collect(v[k], append(slices.Clip(steps), k), out); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for i, it := range v {
			if err := collect(it, append(slices.Clip(steps), i), out); err != nil {
				return err
			}
		}
		return nil
	}
	return addLeaf(leaf{steps: steps, value: t}, out)
}

// addLeaf adds lf to out, refused past core.MaxValues.
func addLeaf(lf leaf, out *[]leaf) error {
	if len(*out) == core.MaxValues {
		return fmt.Errorf("compose: the key holds more than %d values, the limit", core.MaxValues)
	}
	*out = append(*out, lf)
	return nil
}

// winner is the last contributor whose own configuration holds the leaf's
// value at its place, and the steps to it there: a key the same, and an
// item of a list the item equal to the project's, wherever the
// contributor's list holds it, since compose-go appends one file's list to
// another's. nil when none holds it, as for a value compose-go derives.
func (l *loader) winner(cs []contributor, t any, steps []any) (*contributor, []any, error) {
	for i := len(cs) - 1; i >= 0; i-- {
		own, err := l.ownServices(cs[i].f, cs[i].p)
		if err != nil {
			return nil, nil, err
		}
		if at, ok := holds(own[cs[i].name], t, steps); ok {
			return &cs[i], at, nil
		}
	}
	return nil, nil, nil
}

// holds reports whether c, a contributor's own configuration, holds the
// value t holds at steps, and is the steps to it in c.
func holds(c, t any, steps []any) ([]any, bool) {
	var at []any
	for _, s := range steps {
		cs, ok := ownStep(c, child(t, s), s)
		if !ok {
			return nil, false
		}
		c, t = child(c, cs), child(t, s)
		at = append(at, cs)
	}
	return at, reflect.DeepEqual(c, t)
}

// ownStep is the step in c that matches step: the same key, or the index
// of the item of c's list equal to want.
func ownStep(c, want, step any) (any, bool) {
	if k, ok := step.(string); ok {
		m, ok := c.(map[string]any)
		if !ok {
			return nil, false
		}
		_, has := m[k]
		return k, has
	}
	list, _ := c.([]any)
	i := slices.IndexFunc(list, func(it any) bool { return reflect.DeepEqual(it, want) })
	return i, i >= 0
}

// rawAt is the deepest node of raw, a service's YAML, that the steps in
// its own configuration c reach: a key, an item of a list written as a
// list of NAME=value for a mapping, such as environment, the item at an
// index when the list has as many items as c's, the port item that wrote
// the port at an index of a ports: list, or the extra host that wrote the
// host at an index of an extra_hosts: list. Where the YAML writes a short
// form, such as a port "8080:80", the node is the scalar that holds every
// value below it. env is the variables the file interpolates with.
func rawAt(raw *core.Node, c any, steps []any, env map[string]string) *core.Node {
	n, _ := rawWalk(raw, c, steps, env)
	return n
}

// rawWalk is rawAt, and how many of the steps it took to reach the node.
func rawWalk(raw *core.Node, c any, steps []any, env map[string]string) (*core.Node, int) {
	n := raw
	var key any
	for i, s := range steps {
		next := rawStep(n, c, s, key, env)
		if next == nil {
			return n, i
		}
		n, c, key = next, child(c, s), s
	}
	return n, len(steps)
}

// rawStep is the node of n that step takes c to, as rawAt says, key being
// the step that reached n; nil where n writes none.
func rawStep(n *core.Node, c, step, key any, env map[string]string) *core.Node {
	switch {
	case n.Shape == core.Mapping:
		k, _ := step.(string)
		return n.Field[k]
	case n.Shape != core.Sequence:
		return nil
	}
	if k, ok := step.(string); ok {
		return itemNamed(n, k, env)
	}
	list, _ := c.([]any)
	switch {
	case key == "extra_hosts" && step.(int) < len(list):
		return hostItem(n, list[step.(int)], env)
	case len(list) == len(n.Items):
		return n.Items[step.(int)]
	case key == "ports":
		return portItem(n, step.(int), env)
	}
	return nil
}

// portItem is the item of n, a ports: list, that wrote the port at index i
// of the canonical list, where one item, such as the range
// "8000-8001:80-81", writes a port for each number of its range. It parses
// each item as compose-go does (types.ParsePortConfig); nil when one does
// not parse.
func portItem(n *core.Node, i int, env map[string]string) *core.Node {
	for _, it := range n.Items {
		k := 1
		if it.Shape == core.Scalar {
			text, err := interpolate(it.Text, env)
			ports, perr := types.ParsePortConfig(text)
			if err != nil || perr != nil {
				return nil
			}
			k = len(ports)
		}
		if i < k {
			return it
		}
		i -= k
	}
	return nil
}

// itemNamed is the item of the list n that sets name: name alone, or
// name=value, or name:value as extra_hosts writes it, once env fills in
// its variables, as compose-go fills them in before it splits off the
// name: - ${A} with A=FOO sets FOO.
func itemNamed(n *core.Node, name string, env map[string]string) *core.Node {
	for _, it := range n.Items {
		if it.Shape != core.Scalar {
			continue
		}
		text, err := interpolate(it.Text, env)
		if err != nil {
			text = it.Text
		}
		if text == name || strings.HasPrefix(text, name+"=") || strings.HasPrefix(text, name+":") {
			return it
		}
	}
	return nil
}

// lineOf is the line of n: a scalar's own, and a mapping's or a list's
// key's.
func lineOf(n *core.Node) int {
	if n.Shape == core.Scalar {
		return n.Line
	}
	return n.LineOfKey()
}

// value is the leaf as a value, at the line of the contributor that set
// it. A value no contributor holds, which compose-go derives, stands at
// the service in the last file that sets the service. It is literal only
// where errata can show that every source that could have given it or
// folded into it was: its own node, every file's YAML at the fold it lies
// below (folded), and, for a value a pre_start hook takes from its
// service, the service's (inherited).
func (l *loader) value(cs []contributor, t any, lf leaf) (check.Value, error) {
	if v, ok, err := l.inherited(cs, t, lf); ok || err != nil {
		return v, err
	}
	v, err := l.produced(cs, t, lf)
	if err == nil && !v.NotLiteral {
		v.NotLiteral = folded(cs, lf.steps)
	}
	return v, err
}

// produced is the leaf as a value at the line of the contributor that
// set it, not literal when its node there looks up a variable no file
// sets, its text then what that node wrote.
func (l *loader) produced(cs []contributor, t any, lf leaf) (check.Value, error) {
	c, at, err := l.winner(cs, t, lf.steps)
	if err != nil {
		return check.Value{}, err
	}
	n := cs[len(cs)-1].raw
	if c != nil {
		own, _ := l.ownServices(c.f, c.p)
		n = rawAt(c.raw, own[c.name], at, c.p.env)
	} else {
		c = &cs[len(cs)-1]
	}
	v := check.Value{Text: l.text(lf.value), At: check.Location{File: c.f.path, Line: lineOf(n)}, Key: keyOf(lf.steps), HasKey: true}
	if lf.value == nil || unset(n, c.p.env) {
		v.NotLiteral = true
		if n.Shape == core.Scalar {
			v.Text = n.Text
		}
	}
	return v, nil
}

// text is a value of the configuration as a value's text: a path
// compose-go made absolute on the stage as a path from the root.
func (l *loader) text(v any) string {
	switch v := v.(type) {
	case string:
		return l.relative(v)
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// relative is s, when it is the stage's directory or a path below it, as
// a path from the root.
func (l *loader) relative(s string) string {
	if s == l.w.stage.dir {
		return "."
	}
	if rest, ok := strings.CutPrefix(s, l.w.stage.dir+"/"); ok {
		return rest
	}
	return s
}

// keyOf is the key a value stands under, from its service down, such as
// environment.API_PORT or ports.0.target.
func keyOf(steps []any) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = fmt.Sprint(s)
	}
	return strings.Join(parts, ".")
}

// unset reports whether a scalar of n names a variable env does not set:
// its value is then one only the environment docker compose runs in
// decides, which errata does not read.
func unset(n *core.Node, env map[string]string) bool {
	if n.Shape == core.Scalar {
		return names(n.Text, env)
	}
	for _, c := range append(slices.Clone(n.Vals), n.Items...) {
		if unset(c, env) {
			return true
		}
	}
	return false
}

// names reports whether s names a variable env does not set, whatever
// default it gives.
func names(s string, env map[string]string) bool {
	k, err := shellVar(s, env)
	return k != "" || err != nil
}

// shellVar is the first variable s looks up, as compose-go interpolates
// it, that env does not set, through a default or an alternative of any
// form and nesting: docker compose takes that one from its shell, which
// errata does not read. "" when s looks up none such; err is the
// interpolation's.
func shellVar(s string, env map[string]string) (string, error) {
	if !strings.Contains(s, "$") {
		return "", nil
	}
	var missing string
	_, err := interpolateWith(s, func(k string) (string, bool) {
		v, ok := env[k]
		if !ok && missing == "" {
			missing = k
		}
		return v, ok
	})
	return missing, err
}

// shellRefused refuses n, a text the file at writes where it names what,
// when it looks up a variable env does not set: docker compose would take
// it from its shell, and with it which file, service or name it means.
func shellRefused(at string, n *core.Node, what string, env map[string]string) error {
	if k, _ := shellVar(n.Text, env); k != "" {
		return core.LineError(n.Line, "%s: the %s %s looks up %s, which no file errata read sets; docker compose would take it from its shell, which errata does not read", at, what, core.Quote(n.Text), k)
	}
	return nil
}

// pick is the service name of proj, refused when no profile given makes it
// active, or when the project has none of that name.
func pick(proj *types.Project, name string) (types.ServiceConfig, error) {
	if svc, ok := proj.Services[name]; ok {
		return svc, nil
	}
	if svc, ok := proj.DisabledServices[name]; ok {
		return types.ServiceConfig{}, fmt.Errorf("compose: the service %s is not active under the profiles given; its profiles are %s, so name one in profiles", core.Quote(name), strings.Join(svc.Profiles, ", "))
	}
	return types.ServiceConfig{}, fmt.Errorf("compose: the project has no service %s", core.Quote(name))
}
