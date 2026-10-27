package core_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/theory/jsonpath"
	"github.com/theory/jsonpath/spec"
	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
	jsonreader "github.com/f-klement/errata/internal/extract/json"
	tomlreader "github.com/f-klement/errata/internal/extract/toml"
	yamlreader "github.com/f-klement/errata/internal/extract/yaml"
)

// A differential against github.com/theory/jsonpath, an RFC 9535
// implementation under the MIT licence. rapid draws a document and a path
// that starts with $, and errata reads the document through the json, the
// yaml and the toml reader. Where both accept the path, errata selects what
// the reference selects, in the same order: the reference's nodes are put in
// document order, which is the order RFC 9535 sets for an array and the one
// errata uses for a mapping, where RFC 9535 leaves the order open. A path
// with a construct outside errata's subset is one errata refuses, and a path
// the reference refuses errata refuses too.
//
// The documents hold numbers a double holds exactly, since the reference
// compares numbers as doubles and errata exactly, and no null, which TOML
// cannot write.
func TestFilterAgreesWithReference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		doc := referenceDoc(3).Draw(t, "doc")
		path, outside := referencePath(t)
		agreeWithReference(t, doc, path, outside)
	})
}

// The same differential on a list or a mapping whose members are spellings
// of a few values, many of them equal, so that a filter keeps several
// members and the order it keeps them in shows. A mapping's keys are
// written in a drawn order, k3 before k0 and so on, so the reference's
// nodes, put in document order, check errata's order and not a sorted one.
func TestFilterKeepsOrderAsReference(t *testing.T) {
	spellings := []string{`1`, `1.0`, `10e-1`, `0.1e1`, `"1"`, `"x"`, `true`}
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(2, 8).Draw(t, "members")
		list := value{list: true}
		for range size {
			v := value{text: rapid.SampledFrom(spellings).Draw(t, "member")}
			if rapid.Bool().Draw(t, "in a mapping") {
				v = value{keys: []string{"n"}, field: map[string]value{"n": v}}
			}
			list.items = append(list.items, v)
		}
		container := list
		if rapid.Bool().Draw(t, "mapping") {
			container = value{field: map[string]value{}}
			for i, at := range rapid.Permutation(slices.Collect(func(yield func(int) bool) {
				for i := range size {
					if !yield(i) {
						return
					}
				}
			})).Draw(t, "key order") {
				k := fmt.Sprintf("k%d", at)
				container.keys = append(container.keys, k)
				container.field[k] = list.items[i]
			}
		}
		doc := value{keys: []string{"a"}, field: map[string]value{"a": container}}
		lit := rapid.SampledFrom([]string{"1", "'1'", "'x'", "true"}).Draw(t, "literal")
		current := rapid.SampledFrom([]string{"@", "@.n"}).Draw(t, "current")
		agreeWithReference(t, doc, "$.a[?"+current+" == "+lit+"]", false)
	})
}

// agreeWithReference fails unless errata and the reference agree on path:
// on whether it loads, and on what it selects from doc, in order, through
// each of the three readers.
func agreeWithReference(t *rapid.T, doc value, path string, outside bool) {
	ref, refErr := jsonpath.Parse(path)
	p, err := core.ParsePath(path)
	switch {
	case refErr != nil && err == nil:
		t.Fatalf("errata reads %s, which RFC 9535 refuses: %v", path, refErr)
	case outside && err == nil:
		t.Fatalf("errata reads %s, which is outside its subset", path)
	case !outside && refErr == nil && err != nil:
		t.Fatalf("errata refuses %s: %v", path, err)
	case err != nil:
		return
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(toJSON(doc)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	located := ref.SelectLocated(v)
	block := toYAML(doc, "")
	if block == "" {
		block = "{}\n" // a block mapping of no keys is no document at all
	}
	readers := map[string]string{"json": toJSON(doc), "yaml": toJSON(doc), "yaml block": block, "toml": toTOML(doc)}
	for format, data := range readers {
		var root *core.Node
		switch format {
		case "json":
			root, err = jsonreader.Parse([]byte(data))
		case "yaml", "yaml block":
			var docs []*core.Node
			if docs, err = yamlreader.Parse([]byte(data)); err == nil {
				root = docs[0]
			}
		default:
			root, err = tomlreader.Parse([]byte(data))
		}
		if err != nil {
			t.Fatalf("%s refuses %s: %v", format, data, err)
		}
		nodes, err := p.Eval(root)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, n := range nodes {
			got = append(got, marshal(t, toAny(n)))
		}
		ordered := slices.Clone(located)
		slices.SortStableFunc(ordered, func(a, b *spec.LocatedNode) int {
			return slices.Compare(position(root, a.Path), position(root, b.Path))
		})
		var want []string
		for _, l := range ordered {
			want = append(want, marshal(t, l.Node))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s on %s %s: errata selects %v, the reference %v", path, format, data, got, want)
		}
	}
}

// value is a drawn document: a map[string]any whose keys keep their order in
// keys, a []any, or a scalar written as JSON writes it.
type value struct {
	keys  []string
	field map[string]value
	items []value
	text  string
	list  bool
}

var (
	referenceKeys    = []string{"a", "b", "k.x"}
	referenceScalars = []string{`1`, `1.0`, `10e-1`, `0.1e1`, `1.5`, `15e-1`, `-0`, `0`, `"x"`, `"1"`, `"true"`, `""`, `true`, `false`}
	referenceLits    = []string{`1`, `1.0`, `1.5`, `15e-1`, `1.5E0`, `-0`, `0`, `2`, `'x'`, `"1"`, `'true'`, `"null"`, `''`, `true`, `false`, `null`}
	referenceSteps   = []string{".a", ".b", "['k.x']", `["a"]`, "[0]", "[1]", "[-1]", "[5]"}
)

func referenceDoc(depth int) *rapid.Generator[value] {
	return rapid.Custom(func(t *rapid.T) value {
		return drawValue(t, depth, true)
	})
}

func drawValue(t *rapid.T, depth int, root bool) value {
	kind := rapid.IntRange(0, 2).Draw(t, "kind")
	switch {
	case root:
		kind = 2
	case depth == 0:
		kind = 0
	}
	switch kind {
	case 0:
		return value{text: rapid.SampledFrom(referenceScalars).Draw(t, "scalar")}
	case 1:
		v := value{list: true}
		for range rapid.IntRange(0, 4).Draw(t, "items") {
			v.items = append(v.items, drawValue(t, depth-1, false))
		}
		return v
	default:
		v := value{field: map[string]value{}}
		v.keys = rapid.SliceOfNDistinct(rapid.SampledFrom(referenceKeys), 0, 3, rapid.ID).Draw(t, "keys")
		for _, k := range v.keys {
			v.field[k] = drawValue(t, depth-1, false)
		}
		return v
	}
}

func toJSON(v value) string {
	switch {
	case v.list:
		var parts []string
		for _, item := range v.items {
			parts = append(parts, toJSON(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case v.field != nil:
		var parts []string
		for _, k := range v.keys {
			parts = append(parts, fmt.Sprintf("%q: %s", k, toJSON(v.field[k])))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return v.text
	}
}

// toYAML writes v in YAML's block syntax, each level indented two spaces
// more: a mapping as key: value lines, a list as - lines, an empty one in
// flow syntax. A string scalar is single-quoted, which YAML reads as the
// same string as JSON's double quotes for the strings drawn here; any other
// scalar is the same token as in JSON.
func toYAML(v value, indent string) string {
	scalar := func(v value) (string, bool) {
		switch {
		case v.list && len(v.items) == 0:
			return "[]", true
		case v.field != nil && len(v.keys) == 0:
			return "{}", true
		case v.list || v.field != nil:
			return "", false
		case strings.HasPrefix(v.text, `"`):
			return "'" + strings.Trim(v.text, `"`) + "'", true
		default:
			return v.text, true
		}
	}
	var b strings.Builder
	if v.list {
		for _, item := range v.items {
			if text, ok := scalar(item); ok {
				b.WriteString(indent + "- " + text + "\n")
			} else {
				b.WriteString(indent + "-\n" + toYAML(item, indent+"  "))
			}
		}
		return b.String()
	}
	for _, k := range v.keys {
		if text, ok := scalar(v.field[k]); ok {
			b.WriteString(indent + "'" + k + "': " + text + "\n")
		} else {
			b.WriteString(indent + "'" + k + "':\n" + toYAML(v.field[k], indent+"  "))
		}
	}
	return b.String()
}

// toTOML writes the root mapping as one key = value line for each key, in
// order, with inline tables and arrays below it. A scalar is the same token
// as in JSON, which TOML reads as the same value.
func toTOML(v value) string {
	var inline func(v value) string
	inline = func(v value) string {
		switch {
		case v.list:
			var parts []string
			for _, item := range v.items {
				parts = append(parts, inline(item))
			}
			return "[" + strings.Join(parts, ", ") + "]"
		case v.field != nil:
			var parts []string
			for _, k := range v.keys {
				parts = append(parts, fmt.Sprintf("%q = %s", k, inline(v.field[k])))
			}
			return "{" + strings.Join(parts, ", ") + "}"
		default:
			return v.text
		}
	}
	var b strings.Builder
	for _, k := range v.keys {
		fmt.Fprintf(&b, "%q = %s\n", k, inline(v.field[k]))
	}
	return b.String()
}

// referencePath draws a path, and reports whether it holds a construct
// outside errata's subset: one RFC 9535 allows, or one it refuses too. Its
// filters nest as deep as errata allows, MaxFilterDepth.
func referencePath(t *rapid.T) (string, bool) {
	outside := false
	// RFC 9535 allows blank space before each step after @. The reference
	// refuses it when the path is the right side of a comparison, as in
	// 1 == @ .a, so only a path on the left side has it.
	current := func(t *rapid.T, spaces bool) string {
		var b strings.Builder
		b.WriteString("@")
		for range rapid.IntRange(0, 3).Draw(t, "steps") {
			if spaces && rapid.Bool().Draw(t, "space") {
				b.WriteString(" ")
			}
			b.WriteString(rapid.SampledFrom(referenceSteps).Draw(t, "step"))
		}
		return b.String()
	}
	comparison := func(t *rapid.T) string {
		first := rapid.Bool().Draw(t, "literal first")
		c, l := current(t, !first), rapid.SampledFrom(referenceLits).Draw(t, "literal")
		if rapid.IntRange(0, 9).Draw(t, "outside") == 0 {
			outside = true
			return rapid.SampledFrom([]string{
				c + " != " + l, c + " < " + l, c + " <= " + l, c + " > " + l, c + " >= " + l,
				c + " == " + l + " || " + c + " == " + l, "!(" + c + " == " + l + ")", "(" + c + " == " + l + ")",
				c + " == " + c, l + " == " + l, "length(" + c + ") == " + l, c + " == $.a",
				"!(" + c + ")",
				// RFC 9535 refuses these as well.
				"!!" + c,
				c + " == 01", c + " == True", c + " = " + l, c + " == +1", c + " == 1.", c + " === " + l, c + " ==",
				"!" + c + " == " + l, "@[*] == " + l, "@[?@ == 1] == " + l,
			}).Draw(t, "construct")
		}
		if first {
			return l + " == " + c
		}
		return c + " == " + l
	}
	// A term is a comparison or a query: a path from @ whose steps may hold
	// [*] and filters, nested up to depth more, tested alone or after !.
	var filter func(t *rapid.T, depth int) string
	query := func(t *rapid.T, depth int) string {
		var b strings.Builder
		b.WriteString("@")
		for range rapid.IntRange(0, 3).Draw(t, "query steps") {
			if rapid.Bool().Draw(t, "space") {
				b.WriteString(" ")
			}
			switch rapid.IntRange(0, 3).Draw(t, "query step") {
			case 0:
				b.WriteString("[*]")
			case 1:
				if depth > 0 {
					b.WriteString(filter(t, depth-1))
					continue
				}
				fallthrough
			default:
				b.WriteString(rapid.SampledFrom(referenceSteps).Draw(t, "step"))
			}
		}
		return b.String()
	}
	term := func(t *rapid.T, depth int) string {
		switch rapid.IntRange(0, 3).Draw(t, "term") {
		case 0:
			return query(t, depth)
		case 1:
			return rapid.SampledFrom([]string{"!", "! "}).Draw(t, "not") + query(t, depth)
		default:
			return comparison(t)
		}
	}
	filter = func(t *rapid.T, depth int) string {
		var cs []string
		for range rapid.IntRange(1, 3).Draw(t, "terms") {
			cs = append(cs, term(t, depth))
		}
		open := rapid.SampledFrom([]string{"[?", "[ ?"}).Draw(t, "open")
		return open + strings.Join(cs, " && ") + "]"
	}
	var b strings.Builder
	b.WriteString("$")
	for range rapid.IntRange(1, 3).Draw(t, "segments") {
		switch rapid.IntRange(0, 3).Draw(t, "segment") {
		case 0:
			b.WriteString(rapid.SampledFrom(referenceSteps).Draw(t, "step"))
		case 1:
			b.WriteString("[*]")
		default:
			b.WriteString(filter(t, core.MaxFilterDepth-1))
		}
	}
	return b.String(), outside
}

// position is the place in root of a normalized path: the index of each key
// in its mapping's order, and each index as it is.
func position(root *core.Node, p spec.NormalizedPath) []int {
	var pos []int
	n := root
	for _, sel := range p {
		switch s := sel.(type) {
		case spec.Name:
			pos = append(pos, slices.Index(n.Keys, string(s)))
			n = n.Field[string(s)]
		case spec.Index:
			pos = append(pos, int(s))
			n = n.Items[int(s)]
		}
	}
	return pos
}

func toAny(n *core.Node) any {
	switch {
	case n.Shape == core.Mapping:
		m := map[string]any{}
		for _, k := range n.Keys {
			m[k] = toAny(n.Field[k])
		}
		return m
	case n.Shape == core.Sequence:
		s := []any{}
		for _, item := range n.Items {
			s = append(s, toAny(item))
		}
		return s
	case n.Type == core.TypeNumber:
		return json.Number(n.Text)
	case n.Type == core.TypeBool:
		return n.Text == "true"
	case n.Type == core.TypeNull:
		return nil
	default:
		return n.Text
	}
}

func marshal(t *rapid.T, v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(b.String())
}
