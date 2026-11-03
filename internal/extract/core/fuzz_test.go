package core_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	jsonreader "github.com/f-klement/errata/internal/extract/json"
	tomlreader "github.com/f-klement/errata/internal/extract/toml"
	yamlreader "github.com/f-klement/errata/internal/extract/yaml"
)

// A path either parses or names a column inside it; it never panics, and
// evaluating it on any tree does not either. A filter only removes: with
// every filter replaced by [*], the path selects the same nodes and more, in
// the same order. A query and the same query after ! split what the rest of
// their filter keeps.
func FuzzPath(f *testing.F) {
	for _, s := range []string{
		"$", "a.b[0]", "$['a.b'][*][-1]", `["x\"y"]`, "a..b", "a[01]", "é[?x]",
		"a[?@.n == 1].s", "a[?@.s == 'x' && @.n == 1e0]", "a[?'x' == @.s].b", "$[?@ == 'x']",
		"a[?@[0] == null]", "a[?@.b == true][?@ == 1]", "a[?@.b != 1]", "b[?@.n == 1].n", "a[?@.n == -0.0e+1 && @['s'] == \"x\"]",
		"a[?@.b]", "a[?!@.b].s", "a[?@[?@ == 'x']]", "a[?@.t[?@ == 'docker'] && !@.b]", "a[?@.p[?@.port == 6443]].n",
		"$[?@[*][?@.n == 1]]", "a[?@[*]]", "a[? ! @ [ * ] ]", "a[?@[?@[?@[?@ == 1]]]]", "a[?@[?@[?@[?@[?@ == 1]]]]]",
		"a[?!@.b == 1]", "a[?@.b[*] == 1]",
	} {
		f.Add(s)
	}
	trees, err := yamlreader.Parse([]byte(`a:
  - {n: 1, s: x, b: true}
  - {n: "1", s: x}
  - {n: 1.0, s: y, b: True}
  - {n: 0x1, s: ~}
  - {t: [docker, helm], p: [{port: 6443}, {port: 80}], n: 2}
  - {t: [helm], b: null, p: [{port: 443}]}
  - [x, 1, null]
  - x
  - 1
b: {k: {n: 1}, l: x, m: {n: 1.0}}
`))
	if err != nil {
		f.Fatal(err)
	}
	column := regexp.MustCompile(`^: column (\d+): `)
	f.Fuzz(func(t *testing.T, s string) {
		p, err := core.ParsePath(s)
		if err != nil {
			// The refusal quotes the path as every refusal does, clipped
			// when it is long, and then names a column.
			rest, quoted := strings.CutPrefix(err.Error(), "path "+core.Quote(s))
			m := column.FindStringSubmatch(rest)
			if !quoted || m == nil {
				t.Fatalf("error %q does not name the path and a column", err)
			}
			if col, _ := strconv.Atoi(m[1]); col < 1 || col > utf8.RuneCountInString(s)+1 {
				t.Fatalf("error %q names a column outside the path", err)
			}
			return
		}
		if _, err := (core.Source{File: "f", Limit: core.DefaultMaxBytes}).Values(p, trees...); err != nil && !strings.Contains(err.Error(), "not a scalar") {
			t.Fatalf("values: %v", err)
		}
		core.CheckFilterSelectsLess(t, p, trees)
		core.CheckQuerySplits(t, p, trees)
	})
}

// FuzzNumber checks the type and the value errata gives a scalar against
// what encoding/json, yaml.v3 and go-toml decode from the same text, as the
// value of one key. A number errata reads must be the one the decoder reads,
// up to the double the decoder rounds it to: zero exactly when the decoder's
// is zero, whatever the exponent. An infinity or a NaN equals no literal.
func FuzzNumber(f *testing.F) {
	for _, s := range []string{
		"0", "-0", "8080", "0e1000000000000000", "-0.0E-99999999999999999999", "1e400", "1e-400",
		"0x1F", "-0x1F", "017", "0o17", "0b101", "1_000", "8.08e3", ".5", "1.", "+12", "007.5",
		"18446744073709551615", "0xFFFFFFFFFFFFFFFF", "12345678901234567890123", "True", "NULL", "~",
		"'8080'", `"8080"`, "!!str 1", "!!float 1", "!!int '0x10'", ".inf", "-.Inf", ".nan", "inf", "-nan",
		"1979-05-27", "1979-05-27T07:32:00Z", "07:32:00", "true", "null", "x",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		var j map[string]any
		data := `{"v": ` + text + `}`
		if json.Unmarshal([]byte(data), &j) == nil {
			root, err := jsonreader.Parse([]byte(data))
			if err != nil {
				t.Fatalf("encoding/json reads %s, errata refuses it: %v", data, err)
			}
			core.CheckTyped(t, "json", root.Field["v"], j["v"])
		}
		var y map[string]any
		data = "v: " + text + "\n"
		if yaml.Unmarshal([]byte(data), &y) == nil {
			if docs, err := yamlreader.Parse([]byte(data)); err == nil && len(docs) == 1 && docs[0].Field["v"] != nil {
				core.CheckTyped(t, "yaml", docs[0].Field["v"], y["v"])
			}
		}
		var m map[string]any
		data = "v = " + text + "\n"
		if toml.Unmarshal([]byte(data), &m) == nil {
			if root, err := tomlreader.Parse([]byte(data)); err == nil && root.Field["v"] != nil {
				core.CheckTyped(t, "toml", root.Field["v"], m["v"])
			}
		}
	})
}

// FuzzExtract runs an extractor end to end after the read: the parse, the
// document selection, the path, up to two embedded steps and the values.
// Whatever the file, the paths and the steps' formats, it returns values at
// lines inside the file and no more than maxValues of them, or an error and
// no values. A value that comes through YAML literal block scalars alone
// is reported at the line that holds it, as checkOwnLines says. steps picks
// how many embedded steps the spec takes, 0, 1 or 2, and f1 and f2 their
// formats.
func FuzzExtract(f *testing.F) {
	embedFormats := [3]string{"yaml", "json", "toml"}
	f.Add(uint8(0), []byte(extracttest.Manifests), "spec.replicas", "Deployment", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(0), []byte("a: &x {b: [1, 2]}\nc:\n  <<: *x\n  d: *x\n"), "c[*][-1]", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(0), []byte("kind: K\nmetadata: {name: n}\nv: [x]\n"), "$.v[*]", "K", "n", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(1), []byte(`{"a": [1, {"b": "x"}], "c": null}`), "a[*].b", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(2), []byte(extracttest.RunnerConfig), "runners[*].kubernetes['node_selector'][*]", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(0), []byte(extracttest.Manifests), "metadata[?@ == 'web']", "Deployment", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(1), []byte(`{"a": [{"n": 1e3, "s": "x"}, {"n": "1000"}]}`), "a[?@.n == 1000].s", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(2), []byte("[[d]]\nn = 0x10\ns = 'x'\n[[d]]\nn = '16'\n"), "d[?@.n == 16 && @.s == 'x'].s", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(0), []byte("a: [{n: 1, s: x}, {n: 2}, {n: 1.0, s: y}, [1], 1]\nb: {k: {n: 1}, l: {n: 0e9}}\n"), "$[*][?@.n == 1].s", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(1), []byte(`{"a": [{"n": 0e1000000000000000}, {"n": 0}, {"n": 1e3}]}`), "a[?@.n == 0]", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(0), []byte("r:\n  - {d: [docker], m: 1d}\n  - {d: [helm], m: 2d}\n  - {d: [docker]}\n"), "r[?@.m && @.d[?@ == 'docker']].m", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(1), []byte(`{"e": [{"ports": [{"port": 6443}], "to": ["x"]}, {"ports": [{"port": 80}], "to": ["y"]}]}`), "e[?@.ports[?@.port == 6443]].to[*]", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(2), []byte("[[r]]\nnames = ['alpine/k8s']\nallowed = '<1.38'\n[[r]]\nnames = ['x']\n"), "r[?@.names[?@ == 'alpine/k8s'] && !@.enabled].allowed", "", "", uint8(0), "", uint8(0), "", uint8(0))
	f.Add(uint8(0), []byte("c: |\n  a: |\n    [t]\n    v = 3\n"), "c", "", "", uint8(0), "a", uint8(2), "t.v", uint8(2))
	f.Add(uint8(0), []byte("s: &s 'x = 1'\nl: [*s, *s]\n"), "l[*]", "", "", uint8(2), "x", uint8(0), "", uint8(1))
	f.Add(uint8(0), []byte("a: 0\rb: 1\rs: |\n  y = 2"), "s", "", "", uint8(2), "y", uint8(0), "", uint8(1))
	f.Add(uint8(0), []byte("a: 0\ns: &a\n  |\n  x = 1\nt: *a\n"), "t", "", "", uint8(2), "x", uint8(0), "", uint8(1))
	f.Add(uint8(1), []byte(`{"cfg": "a: |\n  x = 1\n", "e": ""}`), "cfg", "", "", uint8(0), "a", uint8(2), "x", uint8(2))
	f.Add(uint8(2), []byte("cfg = '''\n{\"a\": [\"b: 1\"]}\n'''\n"), "cfg", "", "", uint8(1), "a[*]", uint8(0), "b", uint8(2))
	f.Add(uint8(0), []byte("a: 0\ns: &a\n  |\n  x: 0\n  y: 2\nt: [*a]\n"), "s", "", "", uint8(0), "y", uint8(0), "", uint8(1))
	f.Add(uint8(0), []byte("a: 0\ns: &a\n  |\n  x: 0\n  y: 2\nt: [*a]\n"), "t[*]", "", "", uint8(0), "y", uint8(0), "", uint8(1))
	f.Add(uint8(0), []byte("a: 0\nc: !!str\n\n  |\n  x: 0\n  a: |\n    b: 1\n    v: [3, 4]\n"), "c", "", "", uint8(0), "a", uint8(0), "v[*]", uint8(2))
	// The line under the | holds the value, and the | line a comment of
	// the same text, which a test for the text alone would accept.
	f.Add(uint8(0), []byte("s: &a\n  | # 2\n  y: 2\n"), "s", "", "", uint8(0), "y", uint8(0), "", uint8(1))
	// A TOML document in a block whose | stands below its anchor, read
	// through an alias; and a file of CR LF lines.
	f.Add(uint8(0), []byte("a: 0\ns: &a\n  |\n  x = 1\n  y = 2\nt: *a\n"), "t", "", "", uint8(2), "y", uint8(0), "", uint8(1))
	f.Add(uint8(0), []byte("a: 0\r\ns: |\r\n  x: 0\r\n  y: 2\r\n"), "s", "", "", uint8(0), "y", uint8(0), "", uint8(1))
	f.Fuzz(func(t *testing.T, format uint8, data []byte, path, kind, name string, f1 uint8, p1 string, f2 uint8, p2 string, steps uint8) {
		var embedded []core.Embedded
		if steps%3 > 0 {
			embedded = append(embedded, core.Embedded{Format: embedFormats[f1%3], Path: p1})
		}
		if steps%3 > 1 {
			embedded = append(embedded, core.Embedded{Format: embedFormats[f2%3], Path: p2})
		}
		var s extract.Spec
		ys := yamlreader.Spec{File: "f", Path: path, Kind: kind, Name: name, Embedded: embedded}
		switch format % 3 {
		case 0:
			s = extract.Of(yamlreader.Kind, ys)
		case 1:
			s = extract.Of(jsonreader.Kind, jsonreader.Spec{File: "f", Path: path, Embedded: embedded})
		default:
			s = extract.Of(tomlreader.Kind, tomlreader.Spec{File: "f", Path: path, Embedded: embedded})
		}
		e, err := extract.Build(s)
		if err != nil {
			return
		}
		if p, roots := extractorRoots(format%3, path, data); roots != nil {
			core.CheckFilterSelectsLess(t, p, roots)
			core.CheckQuerySplits(t, p, roots)
		}
		got, err := e.(fromData).FromData(data)
		if err != nil {
			if got != nil {
				t.Fatalf("values %v with error %v", got, err)
			}
			return
		}
		if len(got) > core.MaxValues {
			t.Fatalf("%d values, over the limit", len(got))
		}
		count := strings.Count(string(data), "\n") + 1
		for _, v := range got {
			if v.At.Line < 1 || v.At.Line > count || v.At.File != "f" {
				t.Fatalf("value %q at %s:%d of %d lines", v.Text, v.At.File, v.At.Line, count)
			}
		}
		if format%3 == 0 {
			checkOwnLines(t, data, ys, got)
		}
	})
}

// checkOwnLines checks that got, the values of the yaml spec s on data,
// stand on the lines of data they are reported at, where errata reports a
// value at its own line: every string on its way is a YAML literal block
// scalar, so every step but the last reads YAML, and the last reads any
// format. A literal block keeps its lines and strips only their indentation,
// so the file's line is spaces followed by the line of the document the
// value was read at, at any depth. It checks a file whose lines break at \n,
// CR LF among them, and leaves out a file with a lone CR, a NEL, an LS or a
// PS: yaml.v3 breaks a line at each, errata counts none, and the reading
// clamps a line they push past the file's end to its last line. It leaves
// out a UTF-16 file too, in which errata looks for no |.
//
// It reads the chain again a step at a time, each document parsed alone.
// Whether a string is a literal block comes from yaml.v3, not from errata's
// Block, and a literal block whose node has a Block of 0 fails the check,
// since its values would then be reported at the string's start. The
// values come in the order of got, so it checks got only when the two
// agree in number and in text.
func checkOwnLines(t *testing.T, data []byte, s yamlreader.Spec, got []check.Value) {
	t.Helper()
	if len(s.Embedded) == 0 || bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\r")) ||
		bytes.ContainsAny(data, "\u0085  ") || bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return
	}
	paths := make([]core.Path, len(s.Embedded))
	for i, st := range s.Embedded {
		p, err := core.ParsePath(st.Path)
		if err != nil || (i < len(s.Embedded)-1 && st.Format != "yaml") {
			return
		}
		paths[i] = p
	}
	outer := s
	outer.Embedded = nil
	e, err := extract.Build(extract.Of(yamlreader.Kind, outer))
	if err != nil {
		return
	}
	vals, err := e.(fromData).FromData(data)
	if err != nil {
		return
	}
	_, nodes, err := parseDocument("yaml", string(data))
	if err != nil {
		return
	}
	reached := make([]reachedValue, len(vals))
	for i, v := range vals {
		reached[i] = reachedValue{v: v, doc: string(data), nodes: nodes, blocks: true}
	}
	for i, p := range paths {
		var next []reachedValue
		for _, r := range reached {
			literal, ok := r.literal(t)
			if !ok {
				return
			}
			docs, nodes, err := parseDocument(s.Embedded[i].Format, r.v.Text)
			if err != nil {
				return
			}
			sel, err := (core.Source{File: "f", Limit: core.DefaultMaxBytes}).Values(p, docs...)
			if err != nil {
				return
			}
			for _, v := range sel {
				next = append(next, reachedValue{v: v, doc: r.v.Text, nodes: nodes, blocks: r.blocks && literal})
			}
		}
		reached = next
	}
	if len(reached) != len(got) {
		return
	}
	lines := strings.Split(string(data), "\n")
	for i, r := range reached {
		if got[i].Text != r.v.Text {
			return
		}
		if !r.blocks {
			continue
		}
		fl, dl := strings.TrimSuffix(lines[got[i].At.Line-1], "\r"), lineOf(r.doc, r.v.At.Line)
		if !strings.HasSuffix(fl, dl) || strings.Trim(fl[:len(fl)-len(dl)], " ") != "" {
			t.Fatalf("value %q reported at line %d, which reads %q, not its document's line %q", got[i].Text, got[i].At.Line, fl, dl)
		}
	}
}

// reachedValue is a value checkOwnLines read at one step: its document's
// text, the nodes errata built for it when it is YAML, and whether every
// string on its way is a literal block scalar.
type reachedValue struct {
	v      check.Value
	doc    string
	nodes  []*core.Node
	blocks bool
}

// literal is whether the value, a string of a YAML document, is a literal
// block scalar as yaml.v3 reads the document: the one scalar of its text is
// written with |. It fails t when errata gave that scalar a Block of 0. ok
// is false when yaml.v3 does not read the document, when scalars of the
// text differ in style, or when the block holds an LS or a PS, which errata
// reports at the string's start by design.
func (r reachedValue) literal(t *testing.T) (literal, ok bool) {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(r.doc))
	styles := map[bool]int{}
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, false
		}
		countStyles(&doc, r.v.Text, styles)
	}
	if len(styles) != 1 {
		return false, false
	}
	if styles[false] != 0 {
		return false, true
	}
	if styles[true] != 1 || strings.ContainsAny(r.v.Text, "  ") {
		return false, false
	}
	for _, n := range r.nodes {
		if n.Shape == core.Scalar && n.Text == r.v.Text && n.Line == r.v.At.Line && n.Block == 0 {
			t.Fatalf("value %q at line %d is a literal block scalar with a Block of 0", r.v.Text, r.v.At.Line)
		}
	}
	return true, true
}

// countStyles counts the scalars under y whose value is text, by whether
// each is a literal block scalar. An alias is counted where its anchor
// stands.
func countStyles(y *yaml.Node, text string, styles map[bool]int) {
	if y.Kind == yaml.ScalarNode && y.Value == text {
		styles[y.Style&yaml.LiteralStyle != 0]++
	}
	if y.Kind == yaml.AliasNode {
		return
	}
	for _, c := range y.Content {
		countStyles(c, text, styles)
	}
}

// parseDocument parses text in format, as a step does, and for YAML gives
// every node errata built, which literal looks up Block on.
func parseDocument(format, text string) ([]*core.Node, []*core.Node, error) {
	switch format {
	case "json":
		root, err := jsonreader.Parse([]byte(text))
		return []*core.Node{root}, nil, err
	case "toml":
		root, err := tomlreader.Parse([]byte(text))
		return []*core.Node{root}, nil, err
	}
	var nodes []*core.Node
	docs, _, err := yamlreader.ParseNoted([]byte(text), 0, func(n *core.Node, _ string, _ bool) { nodes = append(nodes, n) })
	if err == nil && len(docs) > 1 {
		err = errors.New("more than one document")
	}
	return docs, nodes, err
}

// lineOf is line k of text, counted from 1, or "" past its end.
func lineOf(text string, k int) string {
	lines := strings.Split(text, "\n")
	if k < 1 || k > len(lines) {
		return ""
	}
	return lines[k-1]
}

// extractorRoots is the path of a yaml (format 0), json (1) or toml (2)
// spec, path, and the documents
// its extractor evaluates the path on, or nil when the extractor refuses data
// before it parses. A YAML file of several documents gives every document,
// where the extractor evaluates the path on the ones its kind and name
// select: a filter only removes, from any set of roots.
func extractorRoots(format uint8, path string, data []byte) (core.Path, []*core.Node) {
	src := core.Source{File: "f", Limit: core.DefaultMaxBytes}
	f, parse := core.YAMLFormat, yamlreader.Parse
	switch format {
	case 1:
		f, parse = core.JSONFormat, oneRoot(jsonreader.Parse)
	case 2:
		f, parse = core.TOMLFormat, oneRoot(tomlreader.Parse)
	}
	p, err := core.ParsePath(path)
	if err != nil || src.CheckNodes(data, f) != nil {
		return p, nil
	}
	roots, err := parse(data)
	if err != nil {
		return p, nil
	}
	return p, roots
}

// oneRoot is parse returning its tree as the one root.
func oneRoot(parse func([]byte) (*core.Node, error)) func([]byte) ([]*core.Node, error) {
	return func(data []byte) ([]*core.Node, error) {
		root, err := parse(data)
		if err != nil {
			return nil, err
		}
		return []*core.Node{root}, nil
	}
}
