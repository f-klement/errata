// Package yaml is the yaml extractor: it reads a path in a YAML file, from
// the tree of core.Node each of its documents parses into.
package yaml

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads a path in a YAML file. A file holding several documents needs
// Kind or Name, or both, to select one. Embedded reads a path in the
// document each string the path selects holds, and so on, step by step, as
// core.Embedded says.
type Spec struct {
	core.Where
	Path     string          `json:"path"`
	Kind     string          `json:"kind,omitempty"`
	Name     string          `json:"name,omitempty"`
	Embedded []core.Embedded `json:"embedded,omitempty"`
	// As is map to hand assert each mapping the path selects whole, or
	// record to read Fields from each node it selects into one, as
	// core.CheckAs says; "" for each scalar.
	As string `json:"as,omitempty"`
	// Fields names each path read from a selected node into its record,
	// for as: record, as core.CheckFields says.
	Fields map[string]string `json:"fields,omitempty"`
}

// Kind is the yaml kind, which a rule writes under the key yaml.
var Kind = core.Kind[Spec]{Key: "yaml", Place: Place, Resolve: Resolve, Describe: Describe}

// Place is y with only what decides where it reads: its where part as
// core.Where.Place writes it.
func Place(y Spec) Spec {
	y.Where = y.Place()
	return y
}

// Resolve is y with its max_bytes written out, and its files, embedded
// steps and fields copied, for errata list.
func Resolve(y Spec) Spec {
	y.Where = y.Resolved()
	y.Embedded, y.Fields = slices.Clone(y.Embedded), maps.Clone(y.Fields)
	return y
}

// Describe is what y's extractor returns: literal values, each flat, a
// mapping or a record as its as key says.
func Describe(y Spec) check.Description {
	shape, fields := core.TreeShape(y.As, y.Fields)
	return check.Description{Label: "yaml", Shape: shape, Fields: fields}
}

// yamlExtractor reads a path in the documents of a YAML file that its kind
// and name select, and its embedded steps in the strings the path selects.
// inSet marks a file of a set, where a file with no document they select
// adds no value rather than being refused.
type yamlExtractor struct {
	core.Source
	path       core.Path
	embedded   core.Embedding
	kind, name string
	inSet      bool
	maps       bool
	fields     []core.Field
}

// Build checks y and returns its extractor, which reads the documents
// embedded names with p: of File, or a core.FileSet of one for each file
// Files names.
func Build(y Spec, p core.Parsers) (check.Extractor, error) {
	return core.BuildOneOrSet(Kind.Key, y.Where, func(src core.Source, inSet bool) (core.FileReader, error) {
		path, err := core.CheckPath("yaml", y.Path)
		if err != nil {
			return nil, err
		}
		embedded, err := core.CheckEmbedded("yaml", y.Embedded, p)
		if err != nil {
			return nil, err
		}
		maps, err := core.CheckAs("yaml", y.As)
		if err != nil {
			return nil, err
		}
		fields, err := core.CheckFields("yaml", y.As, y.Fields)
		if err != nil {
			return nil, err
		}
		return yamlExtractor{Source: src, path: path, embedded: embedded, kind: y.Kind, name: y.Name, inSet: inSet, maps: maps, fields: fields}, nil
	})
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e yamlExtractor) ExtractWith(root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(root)
}

// Extract reads the file under root, selects its documents and returns every
// scalar the path selects in them.
func (e yamlExtractor) Extract(root string) ([]check.Value, error) {
	return core.ExtractOne(root, e.Source, e)
}

// FromData returns the values in data, the bytes of the file, as Extract
// does once it has read them.
func (e yamlExtractor) FromData(data []byte) ([]check.Value, error) {
	return e.FromShared(data, &core.Spent{})
}

// FromShared is FromData with the budgets the files of a set read before
// it have spent: the nodes aliases and merge keys added, and the filter
// work, which it adds to.
func (e yamlExtractor) FromShared(data []byte, spent *core.Spent) ([]check.Value, error) {
	if err := e.CheckNodes(data, core.YAMLFormat); err != nil {
		return nil, err
	}
	docs, expanded, err := ParseNoted(data, spent.Expanded, nil)
	spent.Expanded = expanded
	if err != nil && e.inSet && expanded > core.MaxExpanded {
		return nil, fmt.Errorf("%s: %w; the files of a set share the limits on aliases and merge keys", e.File, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.File, err)
	}
	docs, err = e.selectDocs(docs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", e.File, err)
	}
	vs, err := e.EmbeddedValues(e.path, e.embedded, core.Reading{Last: core.LastLine(data), Maps: e.maps, Fields: e.fields}, spent, docs...)
	return vs, spent.SharedAliases(err)
}

// selectDocs keeps the documents whose kind and metadata.name match. Without
// a selector, a file must hold at most one document: picking one of several
// for the user could compare the wrong object, so that is an error, and so is
// a selector that matches nothing, but in a file of a set, which then adds
// no value: a glob over every manifest selects the kind in some of them.
func (e yamlExtractor) selectDocs(docs []*core.Node) ([]*core.Node, error) {
	if e.kind == "" && e.name == "" {
		if len(docs) > 1 {
			return nil, fmt.Errorf("holds %d documents; set kind or name to select one", len(docs))
		}
		return docs, nil
	}
	var out []*core.Node
	for _, d := range docs {
		if (e.kind == "" || scalarAt(d, "kind") == e.kind) && (e.name == "" || scalarAt(d, "metadata", "name") == e.name) {
			out = append(out, d)
		}
	}
	if len(out) == 0 && !e.inSet {
		return nil, fmt.Errorf("no document has kind %s and name %s", core.Quote(e.kind), core.Quote(e.name))
	}
	return out, nil
}

// scalarAt returns the scalar at keys in n, or "" when there is none.
func scalarAt(n *core.Node, keys ...string) string {
	for _, k := range keys {
		if n.Shape != core.Mapping || n.Field[k] == nil {
			return ""
		}
		n = n.Field[k]
	}
	if n.Shape != core.Scalar {
		return ""
	}
	return n.Text
}

// Parse reads every document in data into a tree. yaml.v3 parses a
// document into its node tree in time linear in the file, and checks no key
// there. Decoding the document would check them, but its repeated key check
// compares every key of a mapping with every later key. So errata never
// decodes a document: one walk over the node tree does what the decode did,
// with a hash map per mapping. An empty document, such as the one after a
// trailing ---, is skipped.
func Parse(data []byte) ([]*core.Node, error) {
	docs, _, err := ParseNoted(data, 0, nil)
	return docs, err
}

// ParseNoted is Parse for a reader of a format built on YAML, such as GitLab
// CI's, that reads several files as one. It starts the count of nodes
// aliases and merge keys add at expanded, the count the files before used,
// so core.MaxExpanded bounds them all together, and returns the count it
// ends at. It calls note, when it is not nil, once for each node of the
// tree: with the node, the tag the file wrote on it, such as !reference or
// !!float, or "" for none, and whether it is a plain scalar with no tag,
// which a YAML 1.1 loader types by its text. The tree keeps neither. An
// alias is noted once, as its anchor.
func ParseNoted(data []byte, expanded int, note func(n *core.Node, tag string, plain bool)) ([]*core.Node, int, error) {
	w := yamlWalker{
		expanded: expanded,
		note:     note,
		last:     bytes.Count(data, []byte("\n")) + 1,
		blocks:   newBlocks(data),
		anchors:  map[*yaml.Node]converted{},
		merges:   map[*core.Node]mergeable{},
		ids:      map[*yaml.Node]any{},
		unmerged: map[*core.Node]error{},
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*core.Node
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return docs, w.expanded, nil
		}
		if err != nil {
			return nil, w.expanded, core.ClipError(err)
		}
		if len(doc.Content) == 0 {
			continue
		}
		// The content is converted even when it is empty, since a later
		// document may name an anchor on it.
		tree, _, err := w.convert(doc.Content[0], 0, false)
		if err != nil {
			return nil, w.expanded, err
		}
		if !isEmptyDoc(doc.Content[0]) {
			docs = append(docs, tree)
		}
	}
}

func isEmptyDoc(c *yaml.Node) bool {
	return c.Kind == yaml.ScalarNode && c.Tag == "!!null" && c.Value == "" && c.Style == 0
}

// yamlWalker turns yaml.v3 nodes into a tree, refusing what decoding the
// document would refuse: a repeated key, a key that is a mapping or a
// sequence, a scalar that its tag does not fit, a merge key that does not
// name mappings, an alias inside the anchor it names, and a merged key it
// cannot name apart from another key (see mergeKeys). It replaces the
// decode's ratio of aliased to decoded nodes with a count, against
// maxExpanded, of the nodes aliases and merge keys add.
//
// An alias becomes the node its anchor names, converted once, so its values
// keep the anchor's line and the tree does not grow; it counts the nodes it
// stands for, as a decode would make them. The anchors carry over from one
// document to the next, as in yaml.v3, and so does the count.
type yamlWalker struct {
	last     int                      // the file's last line
	anchors  map[*yaml.Node]converted // each anchored node, once converted
	expanded int                      // the nodes aliases and merge keys added so far
	merges   map[*core.Node]mergeable // each converted mapping, as a merge key adds it
	ids      map[*yaml.Node]any       // a key as yaml.v3 decodes it into any, once decoded
	unmerged map[*core.Node]error     // a merged mapping's refusal, made if an alias decodes it on its own
	note     func(n *core.Node, tag string, plain bool)
	blocks   *blocks // the lines of the | of literal block scalars
}

// mergeable is what a merge key adds of a mapping, as yaml.v3 decodes it:
// the mapping's own keys in the file's order, then what its own merge key
// names, each in turn. The keys are kept as the file has them, since which
// of them a merge adds depends on the mapping it merges into.
type mergeable struct {
	keys    []mergedKey
	sources []*core.Node // the mappings its merge key names
}

type mergedKey struct {
	key  *yaml.Node // the scalar, the anchor's for an alias key
	name string     // the key's text in the tree
	null bool       // whether it decodes to null
	val  *core.Node
}

// converted is a node of the tree, with its size: the nodes it stands for,
// with every alias and merge key under it expanded.
type converted struct {
	n    *core.Node
	size int
}

// line is where y starts. yaml.v3 puts an empty node at the end of the file
// one line past it (the value of a lone ? does), so a line is kept inside
// the file.
func (w *yamlWalker) line(y *yaml.Node) int {
	return max(1, min(y.Line, w.last))
}

// convert turns y into a node. merged is whether y is what a merge key
// names or an item of that, which yaml.v3 decodes into the map of the
// mapping that merges it rather than on its own.
func (w *yamlWalker) convert(y *yaml.Node, depth int, merged bool) (*core.Node, int, error) {
	if depth > core.MaxDepth {
		return nil, 0, core.TooDeep(y.Line)
	}
	var c converted
	var err error
	switch y.Kind {
	case yaml.AliasNode:
		if c, err = w.alias(y, merged); err == nil {
			err = w.expand(c.size, y.Line)
		}
	case yaml.SequenceNode:
		c, err = w.sequence(y, depth, merged)
	case yaml.MappingNode:
		c, err = w.mapping(y, depth, merged)
	default:
		// A scalar keeps the text the file wrote: 1.0 stays 1.0, and a null
		// is null, ~ or the empty string, as written.
		c, err = converted{&core.Node{Type: yamlType(y), Block: w.blocks.lineOf(y), Text: y.Value, Line: w.line(y)}, 1}, checkTag(y)
	}
	if err != nil {
		return nil, 0, err
	}
	if w.note != nil && y.Kind != yaml.AliasNode {
		w.note(c.n, writtenTag(y), y.Kind == yaml.ScalarNode && y.Style == 0)
	}
	if y.Anchor != "" {
		w.anchors[y] = c
	}
	return c.n, c.size, nil
}

// writtenTag is the tag the file wrote on y, such as !reference or !!str,
// and "" when it wrote none: the tag yaml.v3 resolves for an untagged node
// is not the file's.
func writtenTag(y *yaml.Node) string {
	if y.Style&yaml.TaggedStyle != 0 || (strings.HasPrefix(y.Tag, "!") && !strings.HasPrefix(y.Tag, "!!")) {
		return y.Tag
	}
	return ""
}

// alias returns what an alias names. Its anchor comes before it in the file,
// so the anchor is converted unless the alias is inside it. Unless merged,
// the alias decodes the anchor on its own, so a refusal that waited for that
// (see mapping) is made here.
func (w *yamlWalker) alias(y *yaml.Node, merged bool) (converted, error) {
	c, ok := w.anchors[y.Alias]
	if !ok {
		return c, fmt.Errorf("line %d: alias *%s is inside the anchor it names", y.Line, core.Clip(y.Value))
	}
	if merged {
		return c, nil
	}
	if err := w.unmerged[c.n]; err != nil {
		return c, err
	}
	for _, item := range c.n.Items {
		if err := w.unmerged[item]; err != nil {
			return c, err
		}
	}
	return c, nil
}

// expand counts n nodes an alias adds, at line, and refuses them past
// maxExpanded, before anything is built from them.
func (w *yamlWalker) expand(n, line int) error {
	w.expanded += n
	if w.expanded > core.MaxExpanded {
		return fmt.Errorf("line %d: aliases and merge keys expand to more than %d nodes, the limit; write the repeated part out, or %s",
			line, core.MaxExpanded, core.OtherWay)
	}
	return nil
}

func (w *yamlWalker) sequence(y *yaml.Node, depth int, merged bool) (converted, error) {
	c := converted{&core.Node{Shape: core.Sequence, Line: w.line(y)}, 1}
	for _, item := range y.Content {
		v, size, err := w.convert(item, depth+1, merged)
		if err != nil {
			return c, err
		}
		c.n.Items = append(c.n.Items, v)
		c.size += size
	}
	return c, nil
}

// mapping converts a mapping, refusing a key it repeats. A key repeats
// another when they have one text, which is how yaml.v3 refuses a repeated
// key too; keys of one value written differently, such as 1 and 0x1, are two
// keys, each named by its text. A merge key (<<) adds the keys of the
// mappings it names that the mapping does not have (see mergeKeys). The
// merge key's value is converted where the file has it, so a later key may
// alias an anchor inside it, and its keys are added once the mapping's own
// are known.
//
// A mapping a merge key names (merged) is decoded into the map that merges
// it, not on its own, so a refusal its own merge makes waits until an alias
// decodes it on its own, and is not made if none does.
func (w *yamlWalker) mapping(y *yaml.Node, depth int, merged bool) (converted, error) {
	c := converted{core.NewMapping(w.line(y)), 1}
	var own mergeable
	var merge *yaml.Node
	var mergeVal *core.Node
	aliasKeys := map[string]bool{}
	for i := 0; i+1 < len(y.Content); i += 2 {
		k, v := y.Content[i], y.Content[i+1]
		key, err := w.key(k)
		if err != nil {
			return c, err
		}
		if _, repeated := c.n.Field[key]; repeated || (key == "<<" && merge != nil) {
			return c, fmt.Errorf("line %d: key %s is repeated in one mapping", k.Line, core.Quote(key))
		}
		// yaml.v3 compares an alias key by the alias's name, so two aliases
		// of one name repeat a key even when the anchor was defined again
		// between them and they name different text.
		if k.Kind == yaml.AliasNode {
			if aliasKeys[k.Value] {
				return c, fmt.Errorf("line %d: alias key *%s is repeated in one mapping", k.Line, core.Clip(k.Value))
			}
			aliasKeys[k.Value] = true
		}
		val, size, err := w.convert(v, depth+1, isMerge(k))
		if err != nil {
			return c, err
		}
		if isMerge(k) {
			merge, mergeVal = v, val
			c.size += size
			continue
		}
		c.n.Set(key, val)
		scalar := keyScalar(k)
		own.keys = append(own.keys, mergedKey{key: scalar, name: key, null: scalar.ShortTag() == "!!null", val: val})
		c.size += 1 + size
	}
	if merge != nil {
		own.sources = []*core.Node{mergeVal}
		if merge.Kind == yaml.SequenceNode {
			own.sources = mergeVal.Items
		}
		for _, f := range own.sources {
			if f.Shape != core.Mapping {
				return c, fmt.Errorf("line %d: a merge key (<<) takes a mapping, an alias of one, or a sequence of those", merge.Line)
			}
		}
		var clash *mergeClash
		if err := w.mergeKeys(c.n, y, own.sources, merge.Line); errors.As(err, &clash) && merged {
			w.unmerged[c.n] = err
		} else if err != nil {
			return c, err
		}
	}
	w.merges[c.n] = own
	return c, nil
}

// keyScalar is the scalar a key names: the anchor's for an alias key.
func keyScalar(k *yaml.Node) *yaml.Node {
	if k.Kind == yaml.AliasNode {
		return k.Alias
	}
	return k
}

// mergeKeyBytes is how many bytes of a key a merge key copies, or an alias
// key stands for, count as one node against maxExpanded (see mergeKeys and
// key).
const mergeKeyBytes = 64

// expandKey counts n nodes for the bytes of a key that an alias or a merge
// makes a mapping hash, at line, and refuses them past maxExpanded before
// the key is hashed.
func (w *yamlWalker) expandKey(n, line int) error {
	if w.expanded += n; w.expanded > core.MaxExpanded {
		return fmt.Errorf("line %d: aliases and merge keys expand to more than %d nodes, the limit, counting an alias key or a merged key as a node for every %d bytes; write the repeated part out, or %s",
			line, core.MaxExpanded, mergeKeyBytes, core.OtherWay)
	}
	return nil
}

// mergeKeys adds to n, the mapping y, the keys of sources, the mappings its
// merge key names, that n does not have, at line. It adds them as yaml.v3
// decodes a merge: each source's own keys in order, then what the source's
// own merge key names, at any depth, an earlier key winning over a later
// one. The keys of each source are its own, as the file has them, not those
// its merges gave it, since which keys a merge adds depends on the map it
// decodes into.
//
// yaml.v3 decodes y into a map of string keys when every key of y is a
// string, and into a map of any keys otherwise, and a merge adds a key the
// map does not have yet. In a map of string keys a key is its text, and a
// null key decodes to no string, so the merge drops it. In a map of any keys
// a key is its value: 0x1 is the key 1, ~ is null, and '~' is a string. The
// tree names a key by its text, so it cannot hold two keys of one text that
// yaml.v3 tells apart this way, and that is refused.
//
// The keys it copies are no more than the nodes of the sources, which the
// file holds or its aliases counted, so the copies stay inside the file's
// size and maxExpanded too. Each key it looks at is hashed, twice when it is
// copied, so a merge costs the bytes of its keys as well as their number. A
// key counts one node against maxExpanded for every mergeKeyBytes bytes it
// holds, before it is hashed, so merging is linear in the bytes merged, and
// a file that merges long keys many times is refused rather than slow: 50000
// merges of nine keys of 2 MB took 212 s. A key shorter than mergeKeyBytes
// counts nothing more, since the alias or the file that holds it counted it
// already.
func (w *yamlWalker) mergeKeys(n *core.Node, y *yaml.Node, sources []*core.Node, line int) error {
	stringKeys := yamlStringKeys(y)
	seen := map[any]bool{}
	for i := 0; i+1 < len(y.Content); i += 2 {
		k := keyScalar(y.Content[i])
		seen[w.keyID(k, keyName(k), stringKeys)] = true
	}
	stack := slices.Clone(sources)
	slices.Reverse(stack)
	for len(stack) > 0 {
		f := w.merges[stack[len(stack)-1]]
		stack = stack[:len(stack)-1]
		for _, k := range f.keys {
			if err := w.expandKey(len(k.name)/mergeKeyBytes, line); err != nil {
				return err
			}
			if stringKeys && k.null {
				continue
			}
			id := w.keyID(k.key, k.name, stringKeys)
			if seen[id] {
				continue
			}
			seen[id] = true
			if _, set := n.Field[k.name]; set {
				return &mergeClash{line, k.name}
			}
			n.Set(k.name, k.val)
		}
		for i := len(f.sources) - 1; i >= 0; i-- {
			stack = append(stack, f.sources[i])
		}
	}
	return nil
}

// mergeClash is a merged key that yaml.v3 tells apart from the key of the
// same text the mapping has.
type mergeClash struct {
	line int
	key  string
}

func (e *mergeClash) Error() string {
	return fmt.Sprintf("line %d: the merge key (<<) adds key %s, which YAML tells apart from the key of that text the mapping has; errata names a key by its text",
		e.line, core.Quote(e.key))
}

// yamlStringKeys is yaml.v3's test for decoding a mapping into a map of
// string keys: every key's tag is !!str or !!merge.
func yamlStringKeys(y *yaml.Node) bool {
	for i := 0; i < len(y.Content); i += 2 {
		if tag := y.Content[i].ShortTag(); tag != "!!str" && tag != "!!merge" {
			return false
		}
	}
	return true
}

// keyID is the key k, of text name, as a merge into a map of string keys
// or of any keys tells it apart from another: its text, or its value as
// yaml.v3 decodes it into any. A NaN equals no other key, as in yaml.v3's
// map.
func (w *yamlWalker) keyID(k *yaml.Node, name string, stringKeys bool) any {
	if stringKeys {
		return name
	}
	id, ok := w.ids[k]
	if !ok {
		// The key's tag was checked when it was read, so it decodes.
		_ = k.Decode(&id)
		w.ids[k] = id
	}
	return id
}

// keyName is a key's text in the tree: what the file wrote, or for a
// !!binary key the text it decodes to, which is the key yaml.v3 decodes.
// The key's tag was checked when it was read, so it decodes.
func keyName(k *yaml.Node) string {
	if k.ShortTag() != "!!binary" {
		return k.Value
	}
	var s string
	_ = k.Decode(&s)
	return s
}

// isMerge is yaml.v3's test for a merge key: << with no tag but the one it
// resolves to. A quoted "<<" is an ordinary key.
func isMerge(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || k.Tag == "!!merge")
}

// key is the text of a mapping key: a scalar, or an alias of one. A
// !!binary key is the text it decodes to, as yaml.v3 decodes it. A mapping
// or a sequence as a key is refused, as decoding refuses it, and no path
// could name it. An anchor on a key is kept, since a value may name it.
//
// An alias key stands for its anchor's text, which the mapping that uses it
// hashes, so it counts one node against maxExpanded, and one more for every
// mergeKeyBytes bytes of that text.
func (w *yamlWalker) key(k *yaml.Node) (string, error) {
	named := k
	if k.Kind == yaml.AliasNode {
		named = k.Alias
		if err := w.expandKey(1+len(named.Value)/mergeKeyBytes, k.Line); err != nil {
			return "", err
		}
	}
	if named.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("line %d: a key is a mapping or a sequence; errata reads only scalar keys", k.Line)
	}
	if k.Anchor != "" {
		w.anchors[k] = converted{&core.Node{Type: yamlType(k), Text: k.Value, Line: w.line(k)}, 1}
	}
	if err := checkTag(named); err != nil {
		return "", err
	}
	return keyName(named), nil
}

// yamlType is the type a filter compares a scalar as, from the tag yaml.v3
// resolves for it: a quoted 8080 is !!str and a plain one !!int.
func yamlType(y *yaml.Node) core.ScalarType {
	switch y.ShortTag() {
	case "!!int", "!!float":
		return core.TypeNumber
	case "!!bool":
		return core.TypeBool
	case "!!null":
		return core.TypeNull
	default:
		return core.TypeString
	}
}

// checkTag refuses a scalar whose explicit tag does not fit its text, such
// as !!int x, as decoding it would. Decoding one scalar is linear in it, and
// a scalar with no tag of its own always resolves. The message is errata's,
// since yaml.v3's quotes the whole text.
func checkTag(y *yaml.Node) error {
	if y.Style&yaml.TaggedStyle == 0 {
		return nil
	}
	var v any
	if err := y.Decode(&v); err != nil {
		return fmt.Errorf("line %d: %s is not a valid %s", y.Line, core.Quote(y.Value), core.Clip(y.Tag))
	}
	return nil
}
