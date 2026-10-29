package core_test

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"

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
// no values. steps picks how many embedded steps the spec takes, 0, 1 or 2,
// and f1 and f2 their formats.
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
	f.Fuzz(func(t *testing.T, format uint8, data []byte, path, kind, name string, f1 uint8, p1 string, f2 uint8, p2 string, steps uint8) {
		var embedded []core.Embedded
		if steps%3 > 0 {
			embedded = append(embedded, core.Embedded{Format: embedFormats[f1%3], Path: p1})
		}
		if steps%3 > 1 {
			embedded = append(embedded, core.Embedded{Format: embedFormats[f2%3], Path: p2})
		}
		var s extract.Spec
		switch format % 3 {
		case 0:
			s = extract.Of(yamlreader.Kind, yamlreader.Spec{File: "f", Path: path, Kind: kind, Name: name, Embedded: embedded})
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
	})
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
