package yaml

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// decodeYAML decodes every document in data as errata did before it walked
// the node tree itself. The fuzz targets use it as the oracle; on a fuzzed
// input its quadratic key check costs nothing.
func decodeYAML(data []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// The YAML walk reads what yaml.v3's decode reads, the same values, and
// refuses what it refuses, with these differences. The walk checks every
// node of the file, and yaml.v3 only the nodes it decodes: a merge skips a
// key its map has and the key's value, and a map of string keys drops a
// merged null key and its value. excusedYAML replays the decode to tell
// which nodes those are. The differences no decode order changes: yaml.v3
// refuses a small file that is almost all aliases, by a ratio, where errata
// counts nodes against maxExpanded; errata refuses nesting past maxDepth;
// alias keys and merged keys that, a node for every mergeKeyBytes bytes,
// pass maxExpanded; a key that repeats another key's text but is written
// otherwise, an alias or a !!binary key, where yaml.v3 compares keys as
// written and keeps the later one; a merged key that yaml.v3 tells apart
// from a key of the same text, which the tree cannot hold both of; and an
// alias inside the anchor it names, which yaml.v3 reads when its decode
// never meets the alias inside itself. A refusal is excused only at its
// line and for its check.
func FuzzYAML(f *testing.F) {
	f.Add([]byte(extracttest.Manifests))
	f.Add([]byte("a: &x {b: 1}\nc:\n  <<: *x\n  d: [1, *x]\n"))
	f.Add([]byte("--- |\n  text\n...\n"))
	f.Add([]byte("a: &k x\nb: {*k: 1, x: 2}\n"))
	f.Add([]byte("a: {<<: [{x: 1}, {y: 2}], '<<': 3}\n"))
	f.Add([]byte("? [a]\n: 1\nb: !!int x\nc: !!binary aGk=\n"))
	f.Add([]byte("--- &a {x: 1}\n--- *a\n"))
	f.Add([]byte("x:\n  <<: &m {a: 1}\n  b: *m\n"))
	f.Add([]byte("m: &a x\nn: {*a: 1, k: &a y, *a: 2}\n"))
	f.Add([]byte("kc: {<<: {b: vb, <<: {b: {b: &n0 vb, b: &n0 va}}}}\n"))
	f.Add([]byte("a: &x 1\nhosts: {'*.example.com': *x}\n"))
	f.Add([]byte("ka: {a: 1, <<: {<<: {a: {a: 1, a: 2}}}}\n"))
	f.Add([]byte("ka:\n  a: 1\n  <<:\n    <<:\n      a: {a: 1, a: 2}\n"))
	f.Add([]byte("ka:\n  a: 1\n  <<:\n    <<:\n      a: {<<: 1}\n"))
	f.Add([]byte("kc: &n {b: 1, a: [{a: 1, <<: {<<: *n}}]}\n"))
	f.Add([]byte("x:\n  <<: {~: {a: 1, a: 2}}\ny: {1: z, <<: [{~: w}]}\n"))
	f.Add([]byte("kc: &n {b: 1, ~: [{A: {null: x, <<: *n}}]}\n"))
	f.Add([]byte("x: {~: 1, <<: {null: !!int y}}\n"))
	f.Add([]byte("x: {1: a, <<: {0x1: !!int y}}\n"))
	f.Add([]byte("x: {<<: [{~: 1}, {'~': 2}]}\ny: {1: q, <<: [{'1': b}]}\n"))
	f.Add([]byte("s: &s {<<: {'1': b}}\nx: {1: q, <<: *s}\n"))
	f.Add([]byte("x:\n  1: q\n  <<:\n    <<:\n      '1': b\n"))
	f.Add([]byte("x:\n  1: q\n  <<:\n    - a: 1\n    - <<:\n        '1': b\n"))
	f.Add([]byte("x: {a: q, <<: &m {a: {'b': 1, b: 2}}}\n"))
	f.Add([]byte("x:\n  <<:\n    1: q\n    <<: {'2': BUG, '1': c}\n"))
	f.Add([]byte("x: {1: q, <<: [&m {1: {'b': 1, b: 2}}, *m]}\n"))
	f.Add([]byte("x: {a: q, <<: &m {a: {'b': 1, b: 2}}}\ny: {a: r, <<: *m}\n"))
	f.Add([]byte("x:\n  a: q\n  <<: &m\n    a:\n      'b': 1\n      b: 2\ny: {a: r, <<: *m}\n"))
	f.Add([]byte("-\u0085-\u0085"))
	f.Add([]byte("x\u2028---\u2028y"))
	f.Add([]byte(extracttest.UTF16LE("- - x\n- {a: ~, b: [1, '2']}\n")))
	f.Fuzz(func(t *testing.T, data []byte) {
		docs, err := Parse(data)
		derr := decodeYAML(data)
		switch {
		case err == nil && derr != nil && !strings.Contains(derr.Error(), "excessive aliasing"):
			t.Fatalf("yaml.v3 refuses what the walk reads: %v", derr)
		case err != nil && derr == nil && !strings.Contains(err.Error(), "levels deep") &&
			!strings.Contains(err.Error(), "counting an alias key or a merged key") && !excusedYAML(data, err):
			t.Fatalf("the walk refuses what yaml.v3 reads: %v", err)
		case err == nil && derr == nil:
			if diff := compareYAML(data, docs); diff != "" {
				t.Fatalf("the walk reads values yaml.v3 does not: %s", diff)
			}
		}
		for _, d := range docs {
			coretest.WalkLines(t, d, data)
		}
		// The node limit's count bounds what yaml.v3 builds.
		if n, bound := extracttest.YAMLNodes(data), core.YAMLFormat.CountNodes(data); n > bound {
			t.Fatalf("yaml.v3 builds %d nodes, over the bound of %d", n, bound)
		}
	})
}

// yamlDocs is yaml.v3's node tree of every document in data.
func yamlDocs(data []byte) []*yaml.Node {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			return docs
		}
		docs = append(docs, &doc)
	}
}

// excusedYAML reports whether the walk's refusal err, of a file yaml.v3
// reads, is one of FuzzYAML's differences. The walk checks every node of
// the file, and yaml.v3 only the nodes it decodes, so a refusal is excused
// when the node it names is one yaml.v3 never decodes: replayYAML marks the
// nodes it does. errata's errors name a node by its line and by the check
// it failed, with no column, so the node is a node at that line that fails
// that check, as yaml.v3 judges it: a scalar whose tag its text does not
// fit, or a mapping that repeats a key as yaml.v3 compares keys, has a key
// that is a mapping or a sequence, or merges what is not a mapping. yaml.v3
// makes each of these checks whenever it decodes the node, so in a file it
// reads, every such node is one it never decodes; the replay confirms it.
// Two nodes on one line that fail the same check, one of them errata's
// mistake and the other one yaml.v3 skips, would excuse the mistake.
//
// The other refusals are the walk's own limits, which no decode order
// changes, each excused by its shape at the line: a key that repeats
// another key's text but is written otherwise; a merged key that yaml.v3
// tells apart from a key of the same text; and an alias inside the anchor
// it names, which yaml.v3 reads when its decode never meets the alias
// inside itself, as the replay tells.
//
// The replay costs what yaml.v3's decode of the file costs, so it is only
// for a file yaml.v3 reads (derr == nil in FuzzYAML), whose decode yaml.v3's
// alias ratio has bounded: on a file yaml.v3 refuses as almost all aliases
// it has no bound.
func excusedYAML(data []byte, err error) bool {
	var line int
	if _, scanErr := fmt.Sscanf(err.Error(), "line %d:", &line); scanErr != nil {
		return false
	}
	failed := func(check string) bool { return strings.Contains(err.Error(), check) }
	docs := yamlDocs(data)
	decoded, selfAlias := replayYAML(docs)
	excused := false
	within := map[*yaml.Node]bool{} // the nodes that hold the one walked
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.ScalarNode:
			if n.Line == line && failed(core.Quote(n.Value)+" is not a valid "+core.Clip(n.Tag)) && !decodes(n) && !decoded[n] {
				excused = true
			}
		case yaml.AliasNode:
			if n.Line == line && failed("is inside the anchor it names") && within[n.Alias] && !selfAlias {
				excused = true
			}
		case yaml.MappingNode:
			if !decoded[n] && failsWhenDecoded(n, line, err.Error()) {
				excused = true
			}
			if failed("is repeated") && repeatsByText(n, line) {
				excused = true
			}
			if failed("the merge key (<<) adds key") && clashLine(n) == line {
				excused = true
			}
		case yaml.DocumentNode, yaml.SequenceNode:
			// Neither fails a check of its own.
		}
		within[n] = true
		for _, c := range n.Content {
			walk(c)
		}
		delete(within, n)
	}
	for _, d := range docs {
		walk(d)
	}
	return excused
}

// decodes reports whether yaml.v3 decodes the scalar n, its tag and text.
func decodes(n *yaml.Node) bool {
	var v any
	return n.Decode(&v) == nil
}

// failsWhenDecoded reports whether the mapping m fails, at line, the check
// the walk's message msg names: a key at line that repeats an earlier key's
// text, as the walk names keys, a key at line that is a mapping or a
// sequence, or a merge key whose value, at line, is not a mapping, an alias
// of one, or a sequence of those. yaml.v3 refuses each when it decodes m,
// but for a repeat by text of a key written otherwise (see repeatsByText).
func failsWhenDecoded(m *yaml.Node, line int, msg string) bool {
	repeated := strings.Contains(msg, "is repeated")
	texts := map[string]bool{} // the text of each scalar key before the one looked at
	for j := 0; j+1 < len(m.Content); j += 2 {
		k, v := m.Content[j], m.Content[j+1]
		if repeated && resolved(k).Kind == yaml.ScalarNode {
			text := keyText(k)
			if k.Line == line && texts[text] {
				return true
			}
			texts[text] = true
		}
		switch {
		case k.Line == line && strings.Contains(msg, "a key is a mapping or a sequence"):
			if resolved(k).Kind != yaml.ScalarNode {
				return true
			}
		case v.Line == line && isMerge(k) && strings.Contains(msg, "a merge key (<<) takes"):
			if !mergesMappings(v) {
				return true
			}
		}
	}
	return false
}

// mergesMappings is yaml.v3's test of a merge key's value: a mapping, an
// alias of one, or a sequence of those.
func mergesMappings(v *yaml.Node) bool {
	isMapping := func(n *yaml.Node) bool {
		return n.Kind == yaml.MappingNode || n.Kind == yaml.AliasNode && n.Alias != nil && n.Alias.Kind == yaml.MappingNode
	}
	if v.Kind != yaml.SequenceNode {
		return isMapping(v)
	}
	for _, item := range v.Content {
		if !isMapping(item) {
			return false
		}
	}
	return true
}

// yamlReplay replays yaml.v3's decode of a file into any, in decode.go's
// order, and marks every node it decodes. An alias is followed into its
// anchor, in the alias's place. A mapping decodes into a map of string keys
// when every key is a string, and into a map of any keys otherwise, and a
// key decodes as that map takes it: a null key decodes to no string, so the
// map of string keys drops it and its value. A merge key's value is merged
// after the mapping's own keys, into the same map: the keys set so far, <<
// among them, are kept, each merged mapping then adds its own keys in
// order, skipping a key already set and its value, and then merges what its
// own merge key names, at any depth.
type yamlReplay struct {
	decoded   map[*yaml.Node]bool
	selfAlias bool                // whether it met an alias inside itself, which yaml.v3 refuses
	inAlias   map[*yaml.Node]bool // the aliases being followed
	set       map[any]bool        // while a merge runs, the keys its map has
}

// replayInto is what a node decodes into.
type replayInto int

const (
	intoAny       replayInto = iota
	intoString               // a key of a map of string keys
	intoStringMap            // the map of string keys a merge adds to
	intoAnyMap               // the map of any keys a merge adds to
)

// replayYAML marks the nodes yaml.v3 decodes, each document on its own, as
// decodeYAML decodes them, and reports whether it met an alias inside
// itself.
func replayYAML(docs []*yaml.Node) (decoded map[*yaml.Node]bool, selfAlias bool) {
	decoded = map[*yaml.Node]bool{}
	for _, d := range docs {
		r := yamlReplay{decoded: decoded, inAlias: map[*yaml.Node]bool{}}
		r.decode(d, intoAny)
		selfAlias = selfAlias || r.selfAlias
	}
	return decoded, selfAlias
}

// decode marks n and what yaml.v3 decodes under it, and reports whether
// yaml.v3 sets a value from it: a null does not set a string.
func (r *yamlReplay) decode(n *yaml.Node, into replayInto) bool {
	r.decoded[n] = true
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 1 {
			r.decode(n.Content[0], into)
		}
		return true
	case yaml.AliasNode:
		// yaml.v3 refuses an alias it meets inside itself, and the replay
		// stops there.
		if r.inAlias[n] || n.Alias == nil {
			r.selfAlias = true
			return false
		}
		r.inAlias[n] = true
		ok := r.decode(n.Alias, into)
		delete(r.inAlias, n)
		return ok
	case yaml.ScalarNode:
		return into != intoString || n.ShortTag() != "!!null"
	case yaml.SequenceNode:
		for _, item := range n.Content {
			r.decode(item, intoAny)
		}
		return true
	case yaml.MappingNode:
		switch into {
		case intoString:
			return false
		case intoAny:
			into = map[bool]replayInto{true: intoStringMap, false: intoAnyMap}[yamlStringKeys(n)]
		case intoStringMap, intoAnyMap:
			// A merge decodes the mapping into the map it adds to.
		}
		r.mapping(n, into == intoStringMap)
		return true
	}
	return false
}

// mapping decodes the mapping m into a map of string keys or of any keys.
func (r *yamlReplay) mapping(m *yaml.Node, stringKeys bool) {
	set := r.set
	r.set = nil // what m's values hold decodes into maps of their own
	keyInto := map[bool]replayInto{true: intoString, false: intoAny}[stringKeys]
	var merge *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if isMerge(k) {
			merge = v
			continue
		}
		if !r.decode(k, keyInto) || resolved(k).Kind != yaml.ScalarNode {
			continue
		}
		if set != nil {
			key, _ := decodedKey(k, stringKeys)
			if set[key] {
				continue
			}
			set[key] = true
		}
		r.decode(v, intoAny)
	}
	r.set = set
	if merge != nil {
		r.merge(m, merge, stringKeys)
	}
}

// merge decodes what the merge key of m names into m's map. The first merge
// of a map starts its set from m's keys, decoded into any; a merge inside a
// merged mapping goes on with the set it has.
func (r *yamlReplay) merge(m, merge *yaml.Node, stringKeys bool) {
	set := r.set
	if set == nil {
		r.set = map[any]bool{}
		for i := 0; i < len(m.Content); i += 2 {
			k := m.Content[i]
			r.decode(k, intoAny)
			if resolved(k).Kind == yaml.ScalarNode {
				key, _ := decodedKey(k, false)
				r.set[key] = true
			}
		}
	}
	into := map[bool]replayInto{true: intoStringMap, false: intoAnyMap}[stringKeys]
	r.decoded[merge] = true
	sources := []*yaml.Node{merge}
	if merge.Kind == yaml.SequenceNode {
		sources = merge.Content
	}
	for _, src := range sources {
		r.decode(src, into)
	}
	r.set = set
}

// The excuse holds exactly where yaml.v3 reads what the walk refuses: where
// the node the refusal names is one yaml.v3's decode never reaches, or the
// refusal is one of the walk's own limits. A value under a null key is
// skipped in a merge into a map of string keys; into a map of any keys,
// which a mapping with a key of another tag gets, yaml.v3 decodes it, and
// tells keys apart by value, so 0x1 shadows 1 there and not in a map of
// string keys. The tag that decides is on the mapping decoded on its own,
// not on one it merges. An anchor is decoded where an alias names it, in the
// alias's place. yaml.v3 refuses a repeated key only when it is written the
// same way. The last rows are the files a random run of nested merges found
// past the excuse's earlier forms.
func TestExcusedYAMLHoldsWhereYAMLReads(t *testing.T) {
	for _, data := range []string{
		"kc: &n {b: 1,&0: [{A0000000: {<<: *n}}]} ",
		"kc: &n\n  b: 1\n  ~:\n    - A:\n        <<: [{c: 2}, *n]\n",
		"z: &z ~\nkc: &n {*z: [{A: {<<: *n}}]}\n",
		"kc: &n {b: 1, ~: [{A: {1: x, <<: *n}}]}\n",
		"x: {<<: {~: !!int x}}\n",
		"x: {1: y, <<: {~: !!int x}}\n",
		"p: {<<: {1: y, <<: {~: !!int x}}}\n",
		"p: {1: z, <<: {<<: {~: !!int x}}}\n",
		"p: {1: z, <<: [{<<: {~: !!int x}}]}\n",
		"kc: &n {b: 1, ~: [{A: {null: x, <<: *n}}]}\n",
		"x: {~: 1, <<: {null: !!int y}}\n",
		"x: {1: a, <<: {0x1: !!int y}}\n",
		"x: {true: a, <<: {True: !!int y}}\n",
		"x: {<<: {!!binary aGk=: 1, hi: !!int y}}\n",
		"p: {<<: {1: a, <<: {0x1: !!int y}}}\n",
		"x: {1: q, <<: {'1': b}}\n",
		"x: {1: q, <<: {<<: {'1': b}}}\n",
		"p: {<<: {1: q, <<: {'1': b}}}\nr: {1: q, <<: [{x: 1}, {'1': b}]}\n",
		"{!!binary aGk=: 1, hi: 2}\n",
		"a: &k x\nb: {*k: 1, x: 2}\n",
		"a: {x: 1, 'x': 2}\n",
		"a: {!!binary aGk=: 1, !!binary aGk=: 2}\n",
		"s: &s {<<: {'1': b}}\nx: {1: q, <<: *s}\n",
		"x:\n  1: q\n  <<:\n    <<:\n      '1': b\n",
		"x:\n  1: q\n  <<:\n    - a: 1\n    - <<:\n        '1': b\n",
		"x: {a: q, <<: &m {a: {'b': 1, b: 2}}}\n",
		"x:\n  a: q\n  <<: &m\n    a:\n      'b': 1\n      b: 2\n",
		"x: {a: q, <<: &m {a: {'b': 1, b: 2}}}\ny: *m\n",
		"x: {1: q, <<: [&m {1: {'b': 1, b: 2}}, *m]}\n",
		"x: {a: q, <<: &m {a: {'b': 1, b: 2}}}\ny: {a: r, <<: *m}\n",
		"x:\n  a: q\n  <<: &m\n    a:\n      'b': 1\n      b: 2\ny: {a: r, <<: *m}\n",
		"x: {a: q, <<: &m {a: {'b': 1, b: 2}}}\ny: {1: r, <<: *m}\n",
		"x: {a: 1, <<: {b: {a: !!int q}}}\n",
		"{x: {a: 1, <<: {a: {<<: {}}}}, y: {<<: 1}}\n",
		"k0: {'a': v1, <<: [&m3 {NULL: &m2 {<<: [{!!binary aGk=: v9, 0x1: v10}, &m1 {<<: &m0 {True: v11}}, {}], a: {1.0: v4}, 2: {1: v6, <<: {!!binary aGk=: v8}, '<<': v7}}, 1: v12}, {<<: *m3}, {2: v13}]}\n",
		"k0: {!!str 1: v1, true: {2: v3, <<: [&m0 {}], b: v4}, <<: [{<<: [*m0, {<<: {b: v5}}]}, &m4 {'<<': v13, True: {!!str 1: v11, 1: &m2 {!!str 1: v8, 1e0: v9, <<: [{a: v10}, *m0, &m1 {}]}, <<: &m3 {true: v12}}}, {<<: [*m4]}]}\nk1: &m5 {0o1: v14}\nk2: {! 1: v15}\nz: *m0\n",
		"k0: {hi: v1, <<: {<<: {01: {}, '': v3}}}\nk1: {+1: v4, <<: [&m0 {1: {<<: {Null: v8, 'true': v9}, '1': v7, ! 1: v6}}, {}, *m0]}\n",
		"k0: {Null: v1, null: v2, <<: [&m1 {<<: {1.0: {hi: v8, 1: v9}, <<: {<<: [{}, &m0 {hi: v10, true: v11}, *m0]}, null: {<<: [{}, {0x1: v6}], '<<': v5, b: v4}}}, {<<: [&m2 {0x1: v13, +1: v12}, *m1]}]}\nz: *m2\n",
		"k0: &m0 {Null: v1}\nk1: {!foo 1: {<<: [&m1 {null: {!!str 1: v6, !foo 1: v5}}, *m1, *m0]}, 'true': v2}\n",
		"k0: {'true': {<<: [{<<: {1e0: v4, hi: v5}}, {b: v7, '': v6, <<: {True: v8}}], 'a': v2, True: v3}}\nk1: &m5 {<<: [{<<: &m0 {<<: {NULL: v10, <<: {}, '': v11}, Null: v9}}, &m4 {~: {'1': v15, 1: &m3 {'': v17, <<: &m2 {}, ~: v18}}, <<: *m3, 1.0: &m1 {<<: {}, !foo 1: v13}}, *m3]}\nk2: {true: {1e0: v20, <<: [{<<: *m5, +1: v22, !!binary aGk=: v21}, {<<: [*m4]}]}, <<: [{<<: &m6 {<<: *m0, 0o1: v23}}]}\n",
		"k0: {<<: [&m1 {True: v6, NULL: {<<: &m0 {0x1: v5}, '<<': {}, b: {null: v3}}}]}\nk1: {<<: {<<: [&m3 {null: &m2 {2: v10}, <<: *m1}, *m0], 1.0: v8}, hi: v7}\nz: *m0\n",
		"k0: {!foo 1: {<<: [&m0 {0o1: {'true': v6, true: v5}}, *m0], 0x1: v3, 1e0: v2}}\nk1: &m1 {NULL: v7}\nk2: {<<: *m1, '': {'a': {}}}\n",
		"k0: &m1 {<<: [{true: v5, <<: {'~': v6}}, &m0 {+1: v7}, *m0], hi: {~: v3, <<: [{b: v4}]}, '': v1}\nk1: &m3 {<<: [{+1: v8}, &m2 {+1: {NULL: {!!str 1: v11, ! 1: v12}, 2: {b: v14, <<: [*m0, {True: v16}, {!!str 1: v17}], 1: v15}}, ! 1: v18}, *m2]}\nk2: &m4 {}\n",
		"k0: {<<: &m3 {!!str 1: v3, 2: v4, <<: [&m0 {}, &m2 {<<: [{<<: [{}, *m0, &m1 {}]}], hi: v5}]}, +1: v2, 01: v1}\nk1: {1.0: {}, 'a': {}}\nk2: {1e0: v17, True: {'': v9, <<: [&m4 {Null: {<<: [{2: v13}], true: v11, 'true': v12}}, *m4, {0x1: v16, 1: &m5 {'1': v15, <<: *m2}}]}}\n",
		"k0: {1: v1, <<: &m0 {'<<': {<<: {a: v5, <<: [{2: v6}]}, ~: v3, '<<': v4}}}\nk1: {<<: *m0, 1e0: v7}\nk2: {<<: &m1 {<<: {'': v8}}}\n",
		"k0: &m3 {<<: &m2 {2: {1e0: {hi: v3, <<: [{1: v4}]}, <<: &m1 {1.0: v9}, '~': {'~': v6, 1: v7, <<: &m0 {1: v8}}}, <<: [*m0, *m1, {!!str 1: {'<<': v13, <<: [{True: v14}, *m0], a: v12}, 'a': {}, <<: *m0}]}}\nk1: {<<: [*m3, &m4 {<<: *m2, 1: v16, True: {}}, {'<<': v17, Null: v18}]}\nk2: {}\nz: *m0\n",
		"k0: {}\nk1: {1: v2, <<: [&m2 {<<: [{}, {<<: [{<<: [{True: v8}], !foo 1: v7}, &m1 {<<: [{'~': v11, b: v10}, {b: v12}, &m0 {True: v13}], !!str 1: v9}, *m1], true: {!foo 1: v5, !!str 1: v6}}], ~: v3}, *m2], true: v1}\nz: *m0\n",
		"k0: {<<: [{<<: [&m4 {'a': &m2 {'1': v10, !!binary aGk=: v9}, 0x1: {'<<': v12, 1: v13, <<: &m3 {'~': v14}}}, {}]}, *m2, *m4], 'a': v1, 01: {'true': &m1 {'true': {2: v5, <<: [&m0 {!!binary aGk=: v6}]}, ~: v7}}}\n",
		"k0: &m1 {<<: &m0 {<<: [{NULL: v2}, {<<: {a: v3}}, {null: {~: v6, '~': v5, <<: [{'a': v7}, {}, {0o1: v8, b: v9}]}}], b: v1}}\nk1: {<<: *m1, 1: {}, true: v11}\nz: *m0\n",
		"k0: {'a': {<<: [&m0 {!!binary aGk=: v3}, *m0, *m0], 1: v2}, <<: [&m1 {+1: v8, null: {<<: [{}, {NULL: v7}], +1: v5, '<<': v6}}, *m1]}\nk1: {}\n",
	} {
		_, err := Parse([]byte(data))
		if err == nil {
			t.Fatalf("%q: the walk reads it", data)
		}
		reads := decodeYAML([]byte(data)) == nil
		if got := excusedYAML([]byte(data), err); got != reads {
			t.Errorf("%q: excused %v for %v, though yaml.v3 reading it is %v", data, got, err, reads)
		}
	}
}

// The tag excuse holds for the scalar errata's message names, by its text
// and its tag: a refusal of BUG that copies the tag message is not excused
// by the bad tag that yaml.v3 skips on its line.
func TestExcusedYAMLTagNamesTheScalar(t *testing.T) {
	data := []byte("x: {<<: {~: !!int q}, b: BUG}\n")
	if excusedYAML(data, errors.New(`line 1: "BUG" is not a valid !!int`)) {
		t.Error("a refusal of BUG is excused by the skipped !!int q")
	}
	if !excusedYAML(data, errors.New(`line 1: "q" is not a valid !!int`)) {
		t.Error("the refusal of the skipped !!int q is not excused")
	}
}

// The excuse costs time linear in the file, as yaml.v3's decode does: a
// mapping of 20000 keys that a merge shadows, which yaml.v3 never decodes,
// once took the repeat check 12.8 s, 14.8 s here. Slowest of three on a
// loaded machine 0.14 s, 1.08 s under -race at a load of 12 on four CPUs;
// bound 1 s, seven times that. -race slows it past make test's time, so
// make slow runs it, and TestExcusedYAMLShadowedMapping excuses the same
// shape at 2000 keys in make test.
func TestExcusedYAMLIsLinear(t *testing.T) {
	slowOnly(t)
	if took := excuseShadowed(t, 20000); took > excuseBound {
		t.Errorf("took %v, over %v", took, excuseBound)
	}
}

// The shape of TestExcusedYAMLIsLinear at 2000 keys, without its bound.
func TestExcusedYAMLShadowedMapping(t *testing.T) {
	excuseShadowed(t, 2000)
}

// excuseShadowed excuses the refusal of a mapping of n keys that a merge
// shadows, and returns how long the excuse took.
func excuseShadowed(t *testing.T, n int) time.Duration {
	t.Helper()
	data := []byte("x: {a: 1, <<: {a: {" + strings.TrimSuffix(strings.ReplaceAll(extracttest.KeyLines(n, ": "), "\n", ", "), ", ") + ", k0: 2}}}\n")
	if err := decodeYAML(data); err != nil {
		t.Fatalf("yaml.v3 refuses it: %v", err)
	}
	_, err := Parse(data)
	if err == nil {
		t.Fatal("the walk reads it")
	}
	start := time.Now()
	excused := excusedYAML(data, err)
	took := time.Since(start)
	if !excused {
		t.Errorf("%v is not excused", err)
	}
	return took
}

// excuseBound bounds TestExcusedYAMLIsLinear, which runs without -race.
const excuseBound = time.Second

// The excuse for a merged key of another key's text holds only for that
// refusal. In a mapping a merge key names, the walk does not make it, and
// yaml.v3 decodes the mapping's other merged keys, such as '2' here.
func TestExcusedYAMLClashOnlyForTheClash(t *testing.T) {
	data := []byte("x:\n  <<:\n    1: q\n    <<:\n      '2': BUG\n      '1': c\n")
	if excusedYAML(data, errors.New("line 5: planted")) {
		t.Error("a planted refusal at line 5 is excused")
	}
	clash := []byte("x:\n  1: q\n  <<:\n    <<: {'2': b, '1': c}\n")
	_, err := Parse(clash)
	if err == nil || !excusedYAML(clash, err) {
		t.Errorf("the clash refusal %v is not excused", err)
	}
}

// A merge key adds the keys the mapping does not have yet, and yaml.v3
// tells keys apart as it decodes them. Into a map of string keys, which a
// mapping gets when all its keys are strings, a key is its text, a !!binary
// key what it decodes to, and a null key decodes to no string and is
// dropped. Into a map of any keys, a key is its value, so 0x1 is the key 1,
// ~ and null are one key, and the string "~" is another. Which map it is
// depends on the mapping decoded on its own, at any merge depth.
func TestYAMLMergeTellsKeysApartAsYAMLDoes(t *testing.T) {
	for data, want := range map[string]string{
		"x: {<<: [{~: 1}, {'~': 2}]}\n":                                `{"x": {"~": 2}}`,
		"x: {<<: {~: 1, <<: {'~': 2}}}\n":                              `{"x": {"~": 2}}`,
		"x: {<<: [{null: 2}, {'null': 1}]}\n":                          `{"x": {"null": 1}}`,
		"x: {<<: {'<<': 1, a: 2}}\n":                                   `{"x": {"a": 2}}`,
		"{!!binary aGk=: 1, x: {<<: [{!!binary aGk=: 2}, {hi: 3}]}}\n": `{"hi": 1, "x": {"hi": 2}}`,
		"x: {1: a, <<: {0x1: b, 2: c}}\n":                              `{"x": {"1": a, "2": c}}`,
		"x: {1: a, <<: {~: b, null: c}}\n":                             `{"x": {"1": a, "~": b}}`,
		"p: {<<: {1: a, <<: {0x1: b}}}\n":                              `{"p": {"1": a, "0x1": b}}`,
		"n: &n {<<: [{~: 1}, {null: 2}]}\nx: {1: q, <<: *n}\n":         `{"n": {}, "x": {"1": q, "~": 1}}`,
		"x: {<<: &s {~: 1, <<: {'~': 2}}}\ny: {<<: *s}\n":              `{"x": {"~": 2}, "y": {"~": 2}}`,
	} {
		if err := decodeYAML([]byte(data)); err != nil {
			t.Fatalf("%q: yaml.v3 refuses it: %v", data, err)
		}
		docs, err := Parse([]byte(data))
		if err != nil {
			t.Errorf("%q: %v", data, err)
			continue
		}
		if got := treeText(docs[0]); got != want {
			t.Errorf("%q: got %s, want %s", data, got, want)
		}
	}
}

// Where yaml.v3 keeps two keys of one text, a merged key that is not the
// key of that text already in the mapping, errata refuses: it names a key
// by its text and cannot hold both. In a mapping a merge key names, the
// refusal waits for an alias that decodes the mapping on its own.
func TestYAMLMergedKeyOfAnotherKeysTextIsRefused(t *testing.T) {
	for _, c := range []struct {
		data string
		line int
		key  string
	}{
		{"x: {1: q, <<: {'1': b}}\n", 1, "1"},
		{"x: {~: q, <<: [{'~': b}]}\n", 1, "~"},
		{"x: {1: q,\n  <<: {'1': b}}\n", 2, "1"},
		{"x: {<<: &s {~: 1, <<: {'~': 2}}}\ny: *s\n", 1, "~"},
		{"x: {<<: &s [{a: 1}, {~: 1, <<: {'~': 2}}]}\ny: *s\n", 1, "~"},
	} {
		_, err := Parse([]byte(c.data))
		extracttest.WantError(t, err, fmt.Sprintf("line %d: the merge key (<<) adds key %q, which YAML tells apart from the key of that text the mapping has; errata names a key by its text", c.line, c.key))
	}
}

// treeText writes a tree's keys in their order, a scalar as its text.
func treeText(n *core.Node) string {
	switch n.Shape {
	case core.Mapping:
		parts := make([]string, len(n.Keys))
		for i, k := range n.Keys {
			parts[i] = strconv.Quote(k) + ": " + treeText(n.Vals[i])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case core.Sequence:
		parts := make([]string, len(n.Items))
		for i, item := range n.Items {
			parts[i] = treeText(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return n.Text
	}
}

// compareYAML compares a merge into a map of string keys, which decodes as
// yamlText decodes, so it sees which key a merge keeps.
func TestCompareYAMLSeesWhichKeyAMergeKeeps(t *testing.T) {
	wrong, err := Parse([]byte("x: {'~': 1}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := compareYAML([]byte("x: {<<: [{~: 1}, {'~': 2}]}\n"), wrong); diff != `$["x"]["~"]: "1", yaml.v3 "2"` {
		t.Errorf("diff %q", diff)
	}
}

// The planted defect of the reviews: a scalar BUG that the walk refuses
// though nothing is wrong with it. Such a refusal names no check, so it is
// never excused, on a line yaml.v3 decodes or not. Written as !!int BUG,
// which fails the tag check, the same files show that the excuse depends on
// the decode: it holds exactly where yaml.v3, which checks the tag when it
// decodes the scalar, reads the file.
func TestExcusedYAMLPlantedBug(t *testing.T) {
	for _, data := range []string{
		"kc: &n\n  b: 1\n  ~:\n    - BUG\nz:\n  <<: *n\n",
		"kc: &n {b: 1, ~: [BUG]}\nz: {<<: *n}\n",
		"x: {<<: &m {~: [BUG]}}\ny: *m\n",
		"x:\n  <<: &m\n    ~:\n      - BUG\ny: {1: q, <<: *m}\n",
		"x: {<<: {~: 1}, b: BUG}\n",
		"x: {b: BUG, <<: {~: 1}}\n",
		"[{<<: {~: 1}}, BUG]\n",
		"x: {<<: {~: [1,\n  2]}, b: BUG}\n",
		"x: {<<: {~: 1}}\ny: BUG\n",
		"x: {<<: {~: 1}}\ny: {<<: {b: BUG}}\n",
		"x:\n  <<:\n    1: q\n    <<: {'2': BUG, '1': c}\n",
		"x:\n  <<:\n    1: q\n    <<:\n      '2': BUG\n      '1': c\n",
		"x:\n  a: q\n  <<: &m\n    a: [BUG]\n",
		"x:\n  a: q\n  <<: &m\n    a: [BUG]\ny: *m\n",
		"x: {<<: {~: [BUG]}}\n",
		"x: {a: 1, <<: {a: [BUG]}}\n",
	} {
		before, _, _ := strings.Cut(data, "BUG")
		line := 1 + strings.Count(before, "\n")
		if excusedYAML([]byte(data), fmt.Errorf("line %d: planted", line)) {
			t.Errorf("%q: the planted refusal at line %d is excused", data, line)
		}
		tagged := []byte(strings.Replace(data, "BUG", "!!int BUG", 1))
		_, err := Parse(tagged)
		if err == nil {
			t.Fatalf("%q: the walk reads it", tagged)
		}
		reads := decodeYAML(tagged) == nil
		if got := excusedYAML(tagged, err); got != reads {
			t.Errorf("%q: excused %v for %v, though yaml.v3 reading it is %v", tagged, got, err, reads)
		}
	}
}

// repeatsByText reports whether the key of m at line has the text of an
// earlier key of m, as the walk names keys, though yaml.v3, which compares
// keys by their kind and their text as written, does not refuse it.
func repeatsByText(m *yaml.Node, line int) bool {
	// The walk refuses the first key that repeats a text, so one key of
	// that text comes before it: the first, kept here as it is written.
	type written struct {
		kind  yaml.Kind
		value string
	}
	first := map[string]written{}
	for j := 0; j < len(m.Content); j += 2 {
		k := m.Content[j]
		if resolved(k).Kind != yaml.ScalarNode {
			continue
		}
		text, form := keyText(k), written{k.Kind, k.Value}
		was, seen := first[text]
		if k.Line == line && seen && was != form {
			return true
		}
		if !seen {
			first[text] = form
		}
	}
	return false
}

// keyText is a key's text as the walk names it: an alias key's anchor's
// text, and a !!binary key's decoded text, as yaml.v3 decodes it.
func keyText(k *yaml.Node) string {
	k = resolved(k)
	if k.ShortTag() != "!!binary" {
		return k.Value
	}
	var s string
	_ = k.Decode(&s)
	return s
}

// decodedKey is a scalar key as yaml.v3 tells it apart in a merge: decoded
// into a string, for a map of string keys, or into any. A null key decodes
// to no string, which the merge drops.
func decodedKey(k *yaml.Node, stringKeys bool) (key any, null bool) {
	k = resolved(k)
	if stringKeys {
		var s string
		_ = k.Decode(&s)
		return s, k.ShortTag() == "!!null"
	}
	var v any
	_ = k.Decode(&v)
	return v, v == nil
}

// clashLine is the line of the value of m's merge key when the merge adds a
// key that yaml.v3 tells apart from a key of the same text, at any merge
// depth, which the walk refuses at that line; or 0. It merges as yamlReplay
// does, keys decoded as m's map decodes them, and names keys as the walk
// does.
func clashLine(m *yaml.Node) int {
	merge := mergeValue(m)
	if merge == nil {
		return 0
	}
	stringKeys := yamlStringKeys(m)
	seen := map[any]bool{}
	names := map[string]bool{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if resolved(k).Kind != yaml.ScalarNode {
			continue
		}
		key, _ := decodedKey(k, false)
		seen[key] = true
		if !isMerge(k) {
			names[keyText(k)] = true
		}
	}
	top, clash := merge.Line, 0
	var into func(merge *yaml.Node, depth int)
	into = func(merge *yaml.Node, depth int) {
		sources := []*yaml.Node{merge}
		if merge.Kind == yaml.SequenceNode {
			sources = merge.Content
		}
		for _, src := range sources {
			x := resolved(src)
			if depth > 100 || x.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(x.Content); i += 2 {
				k := x.Content[i]
				if isMerge(k) || resolved(k).Kind != yaml.ScalarNode {
					continue
				}
				key, null := decodedKey(k, stringKeys)
				if stringKeys && null || seen[key] {
					continue
				}
				seen[key] = true
				if name := keyText(k); names[name] && clash == 0 {
					clash = top
				} else {
					names[name] = true
				}
			}
			if inner := mergeValue(x); inner != nil {
				into(inner, depth+1)
			}
		}
	}
	into(merge, 0)
	return clash
}

// mergeValue is the value of m's merge key, or nil.
func mergeValue(m *yaml.Node) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isMerge(m.Content[i]) {
			return m.Content[i+1]
		}
	}
	return nil
}

func resolved(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

// yamlText is a value as yaml.v3's decode builds it, with each scalar's text
// as the file wrote it, which is what the walk keeps. yaml.v3 decodes a null
// without calling UnmarshalYAML, to a nil *yamlText. A key decodes into a
// string, which yaml.v3 sets to the text the file wrote, so keys are text as
// in the walk. A null key decodes into no string, and yaml.v3 drops it.
type yamlText struct {
	shape core.Shape
	text  string
	keys  map[string]*yamlText
	items []*yamlText
}

func (v *yamlText) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		v.shape = core.Mapping
		return n.Decode(&v.keys)
	case yaml.SequenceNode:
		v.shape = core.Sequence
		return n.Decode(&v.items)
	default:
		v.text = n.Value
		return nil
	}
}

// mergeIntoAnyKeys reports whether a mapping under n with a merge key has a
// key that is not a string, so that yaml.v3 decodes it into a map of any
// keys, which tells keys apart by value where yamlText's map of string keys
// tells them apart by text.
func mergeIntoAnyKeys(n *yaml.Node) bool {
	if n.Kind == yaml.MappingNode && mergeValue(n) != nil && !yamlStringKeys(n) {
		return true
	}
	for _, c := range n.Content {
		if mergeIntoAnyKeys(c) {
			return true
		}
	}
	return false
}

// isNullText reports whether a plain scalar of text s is null.
func isNullText(s string) bool {
	switch s {
	case "", "~", "null", "Null", "NULL":
		return true
	default:
		return false
	}
}

// compareYAML describes where the walk's documents differ from yaml.v3's
// decode, or returns "" when they agree. A merge decides which keys it
// adds by the keys as yaml.v3 decodes them into any. Into a map of string
// keys that is as yamlText decodes them, but a merge into a map of any keys
// is not, so a file with one is not compared.
func compareYAML(data []byte, docs []*core.Node) string {
	yamlDocs := yamlDocs(data)
	for _, d := range yamlDocs {
		if mergeIntoAnyKeys(d) {
			return ""
		}
	}
	var want []*yamlText
	for _, d := range yamlDocs {
		if len(d.Content) == 0 || isEmptyDoc(d.Content[0]) {
			continue
		}
		var v *yamlText
		if err := d.Content[0].Decode(&v); err != nil {
			return "yaml.v3 decodes a document alone with " + err.Error()
		}
		want = append(want, v)
	}
	if len(want) != len(docs) {
		return fmt.Sprintf("%d documents, yaml.v3 %d", len(docs), len(want))
	}
	for i := range want {
		if diff := compareYAMLNode("$", docs[i], want[i]); diff != "" {
			return diff
		}
	}
	return ""
}

func compareYAMLNode(at string, n *core.Node, v *yamlText) string {
	switch {
	case v == nil:
		if n.Shape != core.Scalar || !isNullText(n.Text) {
			return fmt.Sprintf("%s: %s %q, yaml.v3 null", at, n.Shape, n.Text)
		}
		return ""
	case n.Shape != v.shape:
		return fmt.Sprintf("%s: shape %d, yaml.v3 %d", at, n.Shape, v.shape)
	case n.Shape == core.Scalar && n.Text != v.text:
		return fmt.Sprintf("%s: %q, yaml.v3 %q", at, n.Text, v.text)
	case n.Shape == core.Sequence && len(n.Items) != len(v.items):
		return fmt.Sprintf("%s: %d items, yaml.v3 %d", at, len(n.Items), len(v.items))
	}
	for i, item := range n.Items {
		if diff := compareYAMLNode(fmt.Sprintf("%s[%d]", at, i), item, v.items[i]); diff != "" {
			return diff
		}
	}
	if n.Shape != core.Mapping {
		return ""
	}
	// yaml.v3 drops a null key, so the walk's null keys are not compared.
	matched := 0
	for _, k := range n.Keys {
		want, ok := v.keys[k]
		switch {
		case !ok && isNullText(k):
			continue
		case !ok:
			return fmt.Sprintf("%s: key %q, which yaml.v3 does not have", at, k)
		}
		matched++
		if diff := compareYAMLNode(at+"["+strconv.Quote(k)+"]", n.Field[k], want); diff != "" {
			return diff
		}
	}
	if matched != len(v.keys) {
		return fmt.Sprintf("%s: keys %q, yaml.v3 %d keys", at, n.Keys, len(v.keys))
	}
	return ""
}
