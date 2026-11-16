package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	yamlv2 "go.yaml.in/yaml/v2"
	"go.yaml.in/yaml/v3"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/f-klement/errata/internal/extract"
)

// typedFile is the target the YAML is converted against. sigs.k8s.io/yaml
// turns a YAML scalar into a string only where the target field is a string,
// so converting against the real types keeps run: [ls, .inf] a list of
// strings ("+Inf") rather than a float that JSON cannot hold.
type typedFile struct {
	Checks   []checkEntry `json:"checks"`
	Required []string     `json:"required"`
	Defaults defaults     `json:"defaults"`
}

// typedTarget is typedFile as it is written: each extract.Spec in it read
// as extract.Schema, the struct of every kind's spec under its key. A Spec
// finds its kind by key when it decodes, which sigs.k8s.io/yaml cannot see
// when it converts, so without this a kind's string field written 5 would
// reach the decode as a number.
var typedTarget = asWritten(reflect.TypeFor[typedFile]())

// asWritten is t with each extract.Spec in it, at any depth, replaced by
// extract.Schema. A type that holds no Spec is t itself, so its methods,
// such as its own UnmarshalJSON, stay. A struct that holds one is rebuilt
// from its exported fields, the only ones a decode reads.
func asWritten(t reflect.Type) reflect.Type {
	switch {
	case t == specType:
		return extract.Schema()
	case t.Kind() == reflect.Struct:
		return structAsWritten(t)
	}
	return elemAsWritten(t)
}

// elemAsWritten is asWritten of t, a pointer, a slice or a map, rebuilt
// around its element as written when that differs, or t when it is none of
// these.
func elemAsWritten(t reflect.Type) reflect.Type {
	var wrap func(reflect.Type) reflect.Type
	switch t.Kind() {
	case reflect.Pointer:
		wrap = reflect.PointerTo
	case reflect.Slice:
		wrap = reflect.SliceOf
	case reflect.Map:
		wrap = func(e reflect.Type) reflect.Type { return reflect.MapOf(t.Key(), e) }
	default:
		return t
	}
	if e := asWritten(t.Elem()); e != t.Elem() {
		return wrap(e)
	}
	return t
}

// structAsWritten is asWritten of the struct t.
func structAsWritten(t reflect.Type) reflect.Type {
	var fields []reflect.StructField
	changed := false
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		if w := asWritten(f.Type); w != f.Type {
			f.Type, changed = w, true
		}
		f.Index, f.Offset = nil, 0
		fields = append(fields, f)
	}
	if !changed {
		return t
	}
	return reflect.StructOf(fields)
}

// toJSON converts raw YAML to JSON with the coercions of the real types and
// the keys exactly as the author spelt them. The library decodes the JSON
// into the target with encoding/json, which matches keys without regard to
// case; capturing the JSON before that decode lets parse refuse a key in the
// wrong case instead of having it merged with the right one.
func toJSON(raw []byte) (json.RawMessage, error) {
	var (
		out        json.RawMessage
		captureErr error
	)
	capture := func(d *json.Decoder) *json.Decoder {
		captureErr = d.Decode(&out)
		// The library decodes this instead, so its own decode cannot fail
		// on what the per-check pass reports with the check's name.
		return json.NewDecoder(strings.NewReader("{}"))
	}
	if err := sigsyaml.UnmarshalStrict(raw, reflect.New(typedTarget).Interface(), capture); err != nil {
		return nil, err
	}
	if captureErr != nil {
		return nil, captureErr
	}
	return out, nil
}

// documents counts the YAML documents in raw with go.yaml.in/yaml/v3's
// decoder and returns the first. sigs.k8s.io/yaml reads only the first
// document, so a check after a second --- would be dropped without an error.
// The count comes from a decoder, not from a scan of the lines, because only
// a decoder knows every line break (CR, NEL, LS and PS as well as LF) and an
// encoding such as UTF-16. Empty documents at the end, such as the one a
// last --- makes, are not counted: they hold no check, so none is lost. A
// document after the first that does not decode, such as text after a ...
// marker, is an error, since it may hold checks; the first document is
// returned with it when it decoded.
func documents(raw []byte) (int, *yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var (
		first *yaml.Node
		n     int // documents decoded
		kept  int // documents up to the last one that is not empty
	)
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return kept, first, nil
		}
		if err != nil {
			// The first document stays with the error, so the alias bound
			// still counts it: the YAML library that converts it reads only
			// the first document and never sees this error.
			return 0, first, err
		}
		n++
		if first == nil {
			first = &doc
		}
		if !empty(&doc) {
			kept = n
		}
	}
}

// empty reports whether doc holds nothing at all: no node was written, so it
// decodes to a null with no text. A document that writes ~ or null is not
// empty, since its author wrote something there.
func empty(doc *yaml.Node) bool {
	if len(doc.Content) == 0 {
		return true
	}
	c := doc.Content[0]
	return c.Kind == yaml.ScalarNode && c.Tag == "!!null" && c.Value == ""
}

// lines returns the line each entry under the top-level checks key starts
// on, by its index, from the document the checks were decoded from. Checks it
// cannot place, such as those a merge key brings in, get no line, which
// SARIF reads as the file without a line. It follows no alias. When the
// whole value of checks is an alias, no check gets a line, as merged checks
// do not. One entry written as an alias, - *name, gets the line of the alias
// it is written as, not that of the anchor it names.
func lines(doc *yaml.Node) []int {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	// A mapping's Content holds each key and then its value.
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != "checks" || root.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		entries := root.Content[i+1].Content
		out := make([]int, len(entries))
		for j, entry := range entries {
			out[j] = entry.Line
		}
		return out
	}
	return nil
}

// decodeStrict decodes raw into v and refuses a key v does not have. A
// type error inside an extract.Spec comes from the Spec's own decode,
// which cannot see the path to it, and stops the decode where an error
// elsewhere would not. So when v holds a Spec and its decode fails, raw is
// decoded again as it is written (asWritten), and that decode's error,
// which names its path from v down, as it would anywhere else, is the one
// returned.
func decodeStrict(raw json.RawMessage, v any) error {
	err := strictly(raw, v)
	if err == nil {
		return nil
	}
	t := reflect.TypeOf(v).Elem()
	w := asWritten(t)
	if w == t {
		return err
	}
	werr := strictly(raw, reflect.New(w).Interface())
	if werr == nil {
		return err
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](werr); ok {
		// The struct asWritten built has no name, and the Schema stands
		// for a Spec.
		if te.Struct == "" {
			te.Struct = t.Name()
		}
		if te.Type == extract.Schema() {
			te.Type = specType
		}
	}
	return werr
}

func strictly(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

var (
	rawMessageType = reflect.TypeFor[json.RawMessage]()
	specType       = reflect.TypeFor[extract.Spec]()
)

// exactKeys returns an error for every key in raw that matches a key of t
// only when case is ignored, with the path to it. A key that matches nothing
// is left to decodeStrict, and a value of the wrong shape to the decode.
func exactKeys(raw json.RawMessage, t reflect.Type, path string) []error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == rawMessageType:
		return nil
	case t.Kind() == reflect.Struct:
		return structKeys(raw, t, path)
	case t.Kind() == reflect.Map:
		return mapKeys(raw, t, path)
	case t.Kind() == reflect.Slice:
		return sliceKeys(raw, t, path)
	}
	return nil // a scalar has no keys
}

// structKeys is exactKeys of an object raw read as the struct t.
func structKeys(raw json.RawMessage, t reflect.Type, path string) []error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	var errs []error
	fields := jsonFields(t)
	names := slices.Sorted(maps.Keys(fields))
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if ft, ok := fields[k]; ok {
			errs = append(errs, exactKeys(obj[k], ft, join(path, k))...)
		} else if name, ok := foldMatch(k, names); ok {
			errs = append(errs, fmt.Errorf("%skey %q must be spelled %q", prefix(path), k, name))
		}
	}
	return errs
}

// foldMatch is the first of names that k matches when case is ignored.
func foldMatch(k string, names []string) (string, bool) {
	for _, name := range names {
		if strings.EqualFold(k, name) {
			return name, true
		}
	}
	return "", false
}

// mapKeys is exactKeys of an object raw read as the map t.
func mapKeys(raw json.RawMessage, t reflect.Type, path string) []error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	var errs []error
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		errs = append(errs, exactKeys(obj[k], t.Elem(), join(path, k))...)
	}
	return errs
}

// sliceKeys is exactKeys of an array raw read as the slice t.
func sliceKeys(raw json.RawMessage, t reflect.Type, path string) []error {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	var errs []error
	for i, v := range arr {
		errs = append(errs, exactKeys(v, t.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
	}
	return errs
}

// jsonFields maps each JSON key of struct t to the field's type. An
// extract.Spec has the fields of extract.Schema: each kind's key, with the
// kind's spec type, so a key and a string inside an extractor are checked
// as any other key and string of a check. A struct embedded with no JSON
// name, such as the core.Where of a kind's spec, or a pointer to one, gives
// its keys as encoding/json promotes them, and a key of t's own wins over
// one of them.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	if t == specType {
		t = extract.Schema()
	}
	fields := make(map[string]reflect.Type, t.NumField())
	promoted := map[string]reflect.Type{}
	for f := range t.Fields() {
		if embedded, ok := promotes(f); ok {
			maps.Copy(promoted, jsonFields(embedded))
		} else if name, ok := jsonName(f); ok {
			fields[name] = f.Type
		}
	}
	for k, ft := range promoted {
		if _, own := fields[k]; !own {
			fields[k] = ft
		}
	}
	return fields
}

// jsonTag is the name f's json tag gives it, or "" for none.
func jsonTag(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return name
}

// promotes is the struct f embeds with no JSON name, directly or through a
// pointer, whose keys encoding/json promotes.
func promotes(f reflect.StructField) (reflect.Type, bool) {
	embedded := f.Type
	if embedded.Kind() == reflect.Pointer {
		embedded = embedded.Elem()
	}
	return embedded, f.Anonymous && jsonTag(f) == "" && embedded.Kind() == reflect.Struct
}

// jsonName is the JSON key of the field f, which embeds no struct
// encoding/json promotes, if a decode reads it.
func jsonName(f reflect.StructField) (string, bool) {
	name := jsonTag(f)
	if name == "-" || !f.IsExported() {
		return "", false
	}
	if name == "" {
		name = f.Name
	}
	return name, true
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func prefix(path string) string {
	if path == "" {
		return ""
	}
	return path + ": "
}

// written is what plainStrings finds in one entry under checks: its id as
// the author wrote it, which names the check in the error, and an error for
// each string field written as a scalar YAML 1.1 reads as no string.
type written struct {
	id   string
	errs []error
}

// plainStrings looks at every string field of every check in doc, the first
// document, and returns an error for each one written as a plain scalar that
// loads as a string other than the text written. sigs.k8s.io/yaml decodes
// with yaml/v2, which reads y, yes, on, no and off as bools, 1e3 and 007 as
// numbers and ~ as null, and then turns the value back into a string for
// the field, so id: y loads as the id "true" and 007 as "7". The fields come from the types the file
// decodes into, so a string field added later is looked at too.
func plainStrings(doc *yaml.Node) []written {
	seq := topLevel(doc, "checks")
	if seq == nil {
		return nil
	}
	seq = resolve(seq)
	if seq.Kind != yaml.SequenceNode {
		return nil
	}
	var out []written
	for _, entry := range seq.Content {
		var e written
		for _, f := range pairs(entry, nil) {
			if f.key.Value == "id" {
				e.id = resolve(f.value).Value
			}
		}
		w := &stringWalk{}
		w.walk(entry, reflect.TypeFor[checkEntry](), "")
		e.errs = w.errs
		out = append(out, e)
	}
	return out
}

// topLevel is the value of key at the top of doc, the first document, after
// any alias and as merge keys bring it in, or nil when the document has no
// such key.
func topLevel(doc *yaml.Node, key string) *yaml.Node {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	for _, p := range pairs(doc.Content[0], nil) {
		if p.key.Value == key {
			return p.value
		}
	}
	return nil
}

// hasKey reports whether the mapping n, after any alias and as merge keys
// bring keys in, has key. A nil n has none.
func hasKey(n *yaml.Node, key string) bool {
	if n == nil {
		return false
	}
	for _, p := range pairs(n, nil) {
		if p.key.Value == key {
			return true
		}
	}
	return false
}

// plainKey is plainStrings for the top-level key, read as a value of type t:
// an error for each string under it that YAML 1.1 loads as other text than
// was written, so required: [y] is refused as id: y is, rather than loading
// as the id "true", and so is a defaults.run argument of yes.
func plainKey(doc *yaml.Node, key string, t reflect.Type) []error {
	n := topLevel(doc, key)
	if n == nil {
		return nil
	}
	w := &stringWalk{}
	w.walk(n, t, key)
	return w.errs
}

type stringWalk struct {
	errs []error
}

// walk reads n as a value of type t at path, the field's name in an error.
// It goes only as deep as t, and no type of the file holds itself, so an
// alias that leads back to a node above ends the walk. It reads an alias
// each time it is used, as yaml/v2's decode, which ran first and bounds how
// often aliases may repeat, read it.
func (w *stringWalk) walk(n *yaml.Node, t reflect.Type, path string) {
	n = resolve(n)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		if what, loads, ok := coerced(n, false); ok {
			w.errs = append(w.errs, plainError(path, n.Value, "", what, loads))
		}
	case reflect.Struct:
		w.walkStruct(n, t, path)
	case reflect.Map:
		w.walkMap(n, t, path)
	case reflect.Slice:
		w.walkSlice(n, t, path)
	default: // a number or a bool field takes what YAML reads
	}
}

// walkStruct is walk of n as the struct t.
func (w *stringWalk) walkStruct(n *yaml.Node, t reflect.Type, path string) {
	fields := jsonFields(t)
	for _, p := range pairs(n, nil) {
		if ft, ok := fields[p.key.Value]; ok {
			w.walk(p.value, ft, join(path, p.key.Value))
		}
	}
}

// walkMap is walk of n as the map t, whose string keys are read too.
func (w *stringWalk) walkMap(n *yaml.Node, t reflect.Type, path string) {
	stringKeys := t.Key().Kind() == reflect.String
	for _, p := range pairs(n, nil) {
		if stringKeys {
			if what, loads, ok := coerced(p.key, true); ok {
				w.errs = append(w.errs, plainError(path, p.key.Value, "the name ", what, loads))
			}
		}
		w.walk(p.value, t.Elem(), join(path, p.key.Value))
	}
}

// walkSlice is walk of n as the slice t.
func (w *stringWalk) walkSlice(n *yaml.Node, t reflect.Type, path string) {
	if n.Kind != yaml.SequenceNode {
		return
	}
	for i, item := range n.Content {
		w.walk(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i))
	}
}

func plainError(path, text, noun, what, loads string) error {
	return fmt.Errorf("%s: YAML 1.1 reads %s%s, unquoted, as %s, so it would load as %q; write it quoted: %s", path, noun, text, what, loads, strconv.Quote(text))
}

// coerced reports whether the scalar n, a string field's value or, with key
// set, a map's string key, loads as a string other than the text written,
// and if so what YAML 1.1 reads it as ("the bool true", "the number 1000",
// "null") and the string it loads as. It decodes n alone with
// sigs.k8s.io/yaml, as the file is decoded, so it compares the very string
// a check would hold. y loads as "true" and 1e3 as "1000", so both are
// refused; 5, true and 20.04 load as written, so they are not. A quoted or
// block scalar is the text written, and so is a plain one over several
// lines, since no bool, number or null holds a line break.
func coerced(n *yaml.Node, key bool) (what, loads string, ok bool) {
	text, ok := plainText(n)
	if !ok {
		return "", "", false
	}
	loads, ok = loadsAs(text, key)
	if !ok || loads == n.Value {
		return "", "", false
	}
	return yamlReads(text, n.Tag), loads, true
}

// plainText is the text of n, with its tag when it has one, when it is a
// plain scalar on one line.
func plainText(n *yaml.Node) (string, bool) {
	const written = yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle | yaml.LiteralStyle | yaml.FoldedStyle
	if n.Kind != yaml.ScalarNode || n.Style&written != 0 || strings.ContainsAny(n.Value, "\n\r") {
		return "", false
	}
	if n.Style&yaml.TaggedStyle != 0 {
		return n.Tag + " " + n.Value, true
	}
	return n.Value, true
}

// loadsAs is the string text loads as, a map's key with key set, else a
// string field's value, and whether it decodes.
func loadsAs(text string, key bool) (string, bool) {
	if key {
		return keyLoadsAs(text)
	}
	var v struct {
		V string `json:"v"`
	}
	if sigsyaml.Unmarshal([]byte("v: "+text), &v) != nil {
		return "", false
	}
	return v.V, true
}

// keyLoadsAs is the string text loads as when it is a map's key.
func keyLoadsAs(text string) (string, bool) {
	var m map[string]int
	if sigsyaml.Unmarshal([]byte(text+": 0"), &m) != nil || len(m) != 1 {
		return "", false // a key that does not decode fails the whole file
	}
	var loads string
	for k := range m {
		loads = k
	}
	return loads, true
}

// yamlReads is what yaml/v2 reads text, with the tag tag, as: "null", "the
// bool true", "the number 1000", or the tag's name and the value.
func yamlReads(text, tag string) string {
	var v map[string]any
	// yaml/v2 names what the text is; it decodes, since the library, which
	// decodes with yaml/v2, just did.
	_ = yamlv2.Unmarshal([]byte("v: "+text), &v)
	switch x := v["v"].(type) {
	case nil:
		return "null"
	case bool:
		return fmt.Sprintf("the bool %t", x)
	case int, int64, uint64, float64:
		return fmt.Sprintf("the number %v", x)
	default:
		return fmt.Sprintf("the %s %q", strings.TrimPrefix(tag, "!!"), x)
	}
}

// pair is one key of a mapping and its value.
type pair struct{ key, value *yaml.Node }

// pairs returns the keys of the mapping n, after any aliases, as YAML 1.1
// merges them: the keys written in n, then those a << key brings in, each
// key once, the first one kept, as yaml/v2 does. visiting holds the mappings
// being merged, so a merge that leads back to one of them ends.
func pairs(n *yaml.Node, visiting map[*yaml.Node]bool) []pair {
	n = resolve(n)
	if n.Kind != yaml.MappingNode || visiting[n] {
		return nil
	}
	if visiting == nil {
		visiting = map[*yaml.Node]bool{}
	}
	visiting[n] = true
	defer delete(visiting, n)
	var own, merged []pair
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag != "!!merge" {
			own = append(own, pair{k, v})
		} else {
			merged = append(merged, mergedPairs(v, visiting)...)
		}
	}
	return firstOfEach(append(own, merged...))
}

// mergedPairs is the pairs a << key whose value is v brings in: those of
// one mapping, or of each in a sequence of them.
func mergedPairs(v *yaml.Node, visiting map[*yaml.Node]bool) []pair {
	v = resolve(v)
	if v.Kind != yaml.SequenceNode {
		return pairs(v, visiting)
	}
	var merged []pair
	for _, m := range v.Content {
		merged = append(merged, pairs(m, visiting)...)
	}
	return merged
}

// firstOfEach is ps with each key once, the first one kept.
func firstOfEach(ps []pair) []pair {
	var out []pair
	seen := map[string]bool{}
	for _, p := range ps {
		if !seen[p.key.Value] {
			seen[p.key.Value] = true
			out = append(out, p)
		}
	}
	return out
}

// resolve is the node the alias n names, or n when it is no alias. An alias
// names an anchored node, and an alias carries no anchor, so one step is all.
func resolve(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

// MaxExpandedBytes is the most a config may hold once its aliases are
// expanded: the bytes of every scalar it holds, and one for each node. A
// config with no alias holds about its own size, at most MaxConfigBytes, so
// only aliases reach this. One anchored scalar of 64 KiB aliased by 8000
// checks, 528 KB of config, expanded to 512 MB and ran the loader out of
// memory; the YAML library's own guard counts aliased nodes, not bytes.
const MaxExpandedBytes = 4 * MaxConfigBytes

// checkExpansion refuses a document whose aliases expand it past
// MaxExpandedBytes. Each node's size is counted once and an alias adds the
// size of the node it names, so a chain of aliases of aliases costs one pass
// over the tree, not the expansion. Each node's count is held at one over
// the limit, so a sum of its children, at most the nodes of a 1 MiB file
// times that, cannot overflow.
func checkExpansion(doc *yaml.Node) error {
	sizes := map[*yaml.Node]int64{}
	var size func(n *yaml.Node) int64
	size = func(n *yaml.Node) int64 {
		if s, ok := sizes[n]; ok {
			return s
		}
		// An alias inside the anchor it names counts as one node here; the
		// YAML library refuses it when it decodes.
		sizes[n] = 1
		s := int64(1) + int64(len(n.Value))
		switch {
		case n.Kind == yaml.AliasNode && n.Alias != nil:
			s = 1 + size(n.Alias)
		default:
			for _, c := range n.Content {
				s += size(c)
			}
		}
		s = min(s, MaxExpandedBytes+1)
		sizes[n] = s
		return s
	}
	if size(doc) > MaxExpandedBytes {
		return fmt.Errorf("aliases expand the file to more than %d bytes (%d MiB), the most a config may hold; write the repeated part out", MaxExpandedBytes, MaxExpandedBytes>>20)
	}
	return nil
}
