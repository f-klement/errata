package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/plugin"
)

// pluginSpecs are the entries of plugins' kinds in a config, by the index
// of their check under checks and then by extractor name.
type pluginSpecs map[int]entryPlugins

// entryPlugins are the entries of plugins' kinds of one check, by
// extractor name.
type entryPlugins map[string][]pluginSpec

// pluginSpec is one entry's spec under one plugin's key, decoded by the
// plugin's kind, or why it does not decode.
type pluginSpec struct {
	kind *check.ExtractorKind
	spec any
	err  error
}

// of is the entries of plugins' kinds of check i.
func (p pluginSpecs) of(i int) entryPlugins { return p[i] }

// readPlugins finds every entry of a plugin's kind under the checks of a
// config, decodes each with the kind its plugin describes, and sends each
// plugin's entries in one build request. first is the config's document,
// which holds each entry as it was written: a field of a plugin's kind is
// read from it as the fields of a built in kind are read from the file,
// with the case of its keys, the YAML 1.1 reading of its strings and the
// order of a map's keys checked or kept (pluginJSON).
func readPlugins(ctx context.Context, set *plugin.Set, checks []json.RawMessage, first *yaml.Node) pluginSpecs {
	out := pluginSpecs{}
	var built []any
	for i, raw := range checks {
		var e struct {
			ID      string                                `json:"id"`
			Extract map[string]map[string]json.RawMessage `json:"extract"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue // the check's own decode refuses it
		}
		for _, name := range slices.Sorted(maps.Keys(e.Extract)) {
			entry := e.Extract[name]
			for _, key := range slices.Sorted(maps.Keys(entry)) {
				value := entry[key]
				if !set.Has(key) {
					continue
				}
				kind := set.Kind(ctx, key)
				path := "extract." + name + "." + key
				spec, err := decodePlugin(kind, value, entryNode(first, i, name, key), path)
				if err == nil {
					spec = kind.ForRule(spec, e.ID)
					built = append(built, spec)
				}
				if out[i] == nil {
					out[i] = entryPlugins{}
				}
				out[i][name] = append(out[i][name], pluginSpec{kind, spec, err})
			}
		}
	}
	plugin.Build(ctx, built)
	return out
}

// decodePlugin decodes the entry under a plugin's kind, raw as the file's
// JSON holds it and n as the document wrote it, at path from the check.
func decodePlugin(kind *check.ExtractorKind, raw json.RawMessage, n *yaml.Node, path string) (any, error) {
	if kind.Spec.Kind() == reflect.Struct && n != nil {
		var err error
		if raw, err = pluginJSON(n, kind.Spec, path); err != nil {
			return nil, err
		}
	}
	return kind.Decode(raw)
}

// pluginJSON is the entry n of a plugin's kind as JSON, read against t, the
// struct of the plugin's fields: a key that differs from one of t's only in
// case, and a string YAML 1.1 reads as another type, are refused as they
// are in a built in kind's entry, and each mapping keeps the order of its
// keys, since a field of type map reaches the plugin in the config's order.
func pluginJSON(n *yaml.Node, t reflect.Type, path string) (json.RawMessage, error) {
	y, err := yaml.Marshal(expand(n))
	if err != nil {
		return nil, err
	}
	raw, err := toJSONAs(y, t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	errs := exactKeys(raw, t, path)
	w := &stringWalk{}
	w.walk(n, t, path)
	if errs = append(errs, w.errs...); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return inNodeOrder(raw, n), nil
}

// expand is a copy of n with each alias replaced by a copy of the node it
// names, so the copy marshals alone. checkExpansion has bounded what the
// aliases of the document add.
func expand(n *yaml.Node) *yaml.Node {
	n = resolve(n)
	c := *n
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		c.Content[i] = expand(child)
	}
	return &c
}

// inNodeOrder is raw, a JSON value, with the keys of each object in the
// order the mapping n writes them, as merge keys bring them in (pairs).
// The YAML library writes an object's keys sorted.
func inNodeOrder(raw json.RawMessage, n *yaml.Node) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw
	}
	var b bytes.Buffer
	b.WriteByte('{')
	written := 0
	for _, p := range pairs(n, nil) {
		v, ok := obj[p.key.Value]
		if !ok {
			continue
		}
		if written > 0 {
			b.WriteByte(',')
		}
		written++
		k, _ := json.Marshal(p.key.Value)
		b.Write(k)
		b.WriteByte(':')
		b.Write(inNodeOrder(v, p.value))
	}
	if written != len(obj) {
		return raw // a key the document does not write as written: keep the JSON's order
	}
	b.WriteByte('}')
	return b.Bytes()
}

// entryNode is the node of the key key of extractor name of check i in
// first, the config's document, or nil when the document does not hold it
// where it can be placed.
func entryNode(first *yaml.Node, i int, name, key string) *yaml.Node {
	seq := topLevel(first, "checks")
	if seq == nil {
		return nil
	}
	if seq = resolve(seq); seq.Kind != yaml.SequenceNode || i >= len(seq.Content) {
		return nil
	}
	n := seq.Content[i]
	for _, k := range []string{"extract", name, key} {
		if n = valueOf(n, k); n == nil {
			return nil
		}
	}
	return n
}

// valueOf is the value of key in the mapping n, or nil.
func valueOf(n *yaml.Node, key string) *yaml.Node {
	for _, p := range pairs(n, nil) {
		if p.key.Value == key {
			return p.value
		}
	}
	return nil
}

// strip is raw, a check, with every key of a plugin's kind taken out of its
// extract: entries, which the check's decode does not know.
func (e entryPlugins) strip(raw json.RawMessage) json.RawMessage {
	if len(e) == 0 {
		return raw
	}
	var c map[string]json.RawMessage
	var x map[string]map[string]json.RawMessage
	if json.Unmarshal(raw, &c) != nil || json.Unmarshal(c["extract"], &x) != nil {
		return raw
	}
	for name, specs := range e {
		for _, s := range specs {
			delete(x[name], s.kind.Key)
		}
	}
	c["extract"], _ = json.Marshal(x)
	out, err := json.Marshal(c)
	if err != nil {
		return raw
	}
	return out
}

// addTo sets each entry of a plugin's kind in c's extractors, or returns
// the first that does not decode, in the order of the extractors' names.
func (e entryPlugins) addTo(c *checkEntry) error {
	for _, name := range slices.Sorted(maps.Keys(e)) {
		for _, s := range e[name] {
			if s.err != nil {
				return s.err
			}
			c.Extract[name] = c.Extract[name].With(s.kind, s.spec)
		}
	}
	return nil
}
