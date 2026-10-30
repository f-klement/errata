package core_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	"github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/toml"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// lines is each node of d with its line, in file order, as key=line for
// a member of a mapping and line for anything else.
func lines(d *check.Data) string {
	var out []string
	var walk func(key string, d *check.Data)
	walk = func(key string, d *check.Data) {
		out = append(out, fmt.Sprintf("%s%s:%d", key, d.At.File, d.At.Line))
		for i, it := range d.Items {
			k := ""
			if d.Kind == check.DataMap {
				k = d.Keys[i] + "="
			}
			walk(k, it)
		}
	}
	walk("", d)
	return strings.Join(out, " ")
}

// Each of yaml, json and toml with as: map returns each mapping the path
// selects whole, in file order: its keys in the order the file wrote
// them, a nested mapping, a sequence, and every scalar as the text a flat
// value has, each node at its own line. Planted breaks: the values of a
// mapping taken in Go's map order, which sorted the keys; every node at
// the mapping's line, which lost the line of automerge.
func TestAsMapReturnsEachMappingWhole(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"r.json": "{\"packageRules\": [\n  {\"matchPackageNames\": [\"a\",\n    \"b\"],\n   \"automerge\": false},\n  {\"prPriority\": 5, \"x\": {\"y\": null}}\n]}\n",
		"r.yaml": "packageRules:\n  - matchPackageNames: [a,\n      b]\n    automerge: false\n  - prPriority: 5\n    x: {y: null}\n",
		"r.toml": "[[packageRules]]\nmatchPackageNames = [\"a\",\n  \"b\"]\nautomerge = false\n[[packageRules]]\nprPriority = 5\nx = {y = \"null\"}\n",
	})
	for _, c := range []struct {
		spec  extract.Spec
		lines []string
	}{
		{extract.Of(json.Kind, json.Spec{File: "r.json", Path: "packageRules[*]", As: "map"}),
			[]string{"r.json:2 matchPackageNames=r.json:2 r.json:2 r.json:3 automerge=r.json:4", "r.json:5 prPriority=r.json:5 x=r.json:5 y=r.json:5"}},
		{extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "packageRules[*]", As: "map"}),
			[]string{"r.yaml:2 matchPackageNames=r.yaml:2 r.yaml:2 r.yaml:3 automerge=r.yaml:4", "r.yaml:5 prPriority=r.yaml:5 x=r.yaml:6 y=r.yaml:6"}},
		{extract.Of(toml.Kind, toml.Spec{File: "r.toml", Path: "packageRules[*]", As: "map"}),
			[]string{"r.toml:1 matchPackageNames=r.toml:2 r.toml:2 r.toml:3 automerge=r.toml:4", "r.toml:5 prPriority=r.toml:6 x=r.toml:7 y=r.toml:7"}},
	} {
		got := mustRun(t, c.spec, root)
		if len(got) != 2 {
			t.Fatalf("%s: %d values", c.spec.Describe().Label, len(got))
		}
		want := []string{`{"matchPackageNames": ["a", "b"], "automerge": "false"}`, `{"prPriority": "5", "x": {"y": "null"}}`}
		for i, v := range got {
			if v.Data == nil || v.Text != want[i] || v.Data.Show(1000) != want[i] || v.At != v.Data.At {
				t.Errorf("%s: value %d is %q at %s, Data %v", c.spec.Describe().Label, i, v.Text, v.At, v.Data)
				continue
			}
			if l := lines(v.Data); l != c.lines[i] {
				t.Errorf("%s: value %d at %s, want %s", c.spec.Describe().Label, i, l, c.lines[i])
			}
		}
		if !c.spec.Describe().Whole() {
			t.Errorf("%s: Maps is false", c.spec.Describe().Label)
		}
	}
	flat := extract.Of(json.Kind, json.Spec{File: "r.json", Path: "packageRules[*].automerge"})
	if got := mustRun(t, flat, root); flat.Describe().Whole() || len(got) != 1 || got[0].Data != nil || got[0].Text != "false" {
		t.Errorf("without as: %v", got)
	}
}

// A mapping's Text is its rendering cut at check.ShownBytes bytes, for a
// message, and its Data is whole. Planted break: the rendering left uncut,
// which held every byte of a long mapping twice.
func TestAsMapCutsTheText(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"r.json": `[{"a": "` + strings.Repeat("x", 1000) + `"}]`})
	got := mustRun(t, extract.Of(json.Kind, json.Spec{File: "r.json", Path: "[*]", As: "map"}), root)
	if len(got) != 1 || len(got[0].Text) != check.ShownBytes+3 || !strings.HasSuffix(got[0].Text, "...") || len(got[0].Data.Items[0].Text) != 1000 {
		t.Errorf("got %d bytes of text: %.40s", len(got[0].Text), got[0].Text)
	}
}

// as takes map or nothing; any other word does not load, for each kind.
// A path that selects a scalar or a sequence with as: map is refused at
// its line, as a mapping is for a flat value. Planted breaks: CheckAs
// taking any word as map; the shape test left out, which handed a
// sequence over as a mapping.
func TestAsMapRefusesWhatIsNoMapping(t *testing.T) {
	for _, s := range []extract.Spec{
		extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "a", As: "list"}),
		extract.Of(json.Kind, json.Spec{File: "r.json", Path: "a", As: "list"}),
		extract.Of(toml.Kind, toml.Spec{File: "r.toml", Path: "a", As: "list"}),
	} {
		_, err := extract.Build(s)
		want := s.Describe().Label + `: as "list" is not one errata reads; write map to hand assert each mapping the path selects whole, record to read the fields of each element it selects into one, or leave the key out for its scalars`
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v", s.Describe().Label, err)
		}
		if s.Describe().Whole() {
			t.Errorf("%s: as list counts as map", s.Describe().Label)
		}
	}
	root := extracttest.Repo(t, map[string]string{"r.yaml": "a: x\nb:\n  - 1\n"})
	for path, want := range map[string]string{
		"a": `r.yaml: path "a" selects a scalar at line 1, not a mapping; as: map hands assert each mapping the path selects, so add [*] to take a sequence's members`,
		"b": `r.yaml: path "b" selects a sequence at line 3, not a mapping;`,
	} {
		_, err := run(t, extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: path, As: "map"}), root)
		extracttest.WantError(t, err, want)
	}
	if got := mustRun(t, extract.Of(yaml.Kind, yaml.Spec{File: "r.yaml", Path: "$", As: "map"}), root); len(got) != 1 || got[0].Text != `{"a": "x", "b": ["1"]}` {
		t.Errorf("the document: %v", got)
	}
}

// A mapping in a document a string holds keeps the lines of the outer
// file, as a scalar does: in a literal block scalar, each node at its own
// line, and in a quoted string, every node where the string starts.
// Planted break: the nested nodes at the document's own lines, which put
// b at line 2 of the file.
func TestAsMapReadsEmbeddedDocuments(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"c.yaml": "data:\n  rules.json: |\n    [{\"a\": 1,\n      \"b\": [2]}]\n  q: '[{\"a\": 1,\n    \"b\": 2}]'\n"})
	for path, want := range map[string]string{
		"data['rules.json']": "c.yaml:3 a=c.yaml:3 b=c.yaml:4 c.yaml:4",
		"data.q":             "c.yaml:5 a=c.yaml:5 b=c.yaml:5",
	} {
		got := mustRun(t, extract.Of(yaml.Kind, yaml.Spec{File: "c.yaml", Path: path, As: "map", Embedded: steps("json", "[*]")}), root)
		if len(got) != 1 || lines(got[0].Data) != want {
			t.Errorf("%s: %v", path, got)
		} else if got[0].At.Line != got[0].Data.At.Line {
			t.Errorf("%s: the value at %s, its mapping at %s", path, got[0].At, got[0].Data.At)
		}
	}
}

// Each mapping of a file set is at its own file, in the files' order.
// Planted break: the set's values given the first file's name.
func TestAsMapReadsAFileSet(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"a/1.json": `{"n": "1"}`, "a/2.json": `{"n": "2"}`})
	got := mustRun(t, extract.Of(json.Kind, json.Spec{Files: []string{"a/1.json", "a/2.json"}, Path: "$", As: "map"}), root)
	if len(got) != 2 || got[0].Data.Items[0].At.File != "a/1.json" || got[1].Data.At.File != "a/2.json" || got[1].At.File != "a/2.json" {
		t.Errorf("got %v", got)
	}
}

// The nodes of the mappings an extractor returns count against MaxValues,
// each node one, and the bytes of their keys and scalars against
// max_bytes, in one file and over the files of a set together: YAML
// aliases can repeat a mapping without the file growing. Each refusal
// stops at the first node past the bound. Planted breaks: the nodes of a
// mapping counted as one value, which returned 991 mappings of 101 nodes; the bytes of
// the keys left out; a set counting its values, not their nodes.
func TestAsMapBoundsNodesAndBytes(t *testing.T) {
	var keys []string
	for i := range 100 {
		keys = append(keys, fmt.Sprintf("k%d: v", i))
	}
	aliases := func(n int) string {
		return "a: &x {" + strings.Join(keys, ", ") + "}\nl: [" + strings.TrimSuffix(strings.Repeat("*x, ", n), ", ") + "]\n"
	}
	long := "a: &x {" + strings.Repeat("k", 40) + ": " + strings.Repeat("v", 10) + "}\nl: [*x, *x, *x, *x, *x]\n"
	root := extracttest.Repo(t, map[string]string{
		"many.yaml":  aliases(991),
		"under.yaml": aliases(990),
		"long.yaml":  long,
		"fits.yaml":  "a: &x {" + strings.Repeat("k", 40) + ": v}\nl: [*x, *x, *x]\n",
		"s/1.json":   "[" + strings.TrimSuffix(strings.Repeat(`{"a": "1"}, `, 30000), ", ") + "]",
		"s/2.json":   "[" + strings.TrimSuffix(strings.Repeat(`{"a": "1"}, `, 30000), ", ") + "]",
		"b/1.yaml":   long,
		"b/2.yaml":   long,
	})
	got := mustRun(t, extract.Of(yaml.Kind, yaml.Spec{File: "under.yaml", Path: "l[*]", As: "map"}), root)
	if len(got) != 990 {
		t.Errorf("990 mappings of 101 nodes: %d", len(got))
	}
	_, err := run(t, extract.Of(yaml.Kind, yaml.Spec{File: "many.yaml", Path: "l[*]", As: "map"}), root)
	extracttest.WantError(t, err, fmt.Sprintf("many.yaml: the mappings the path selects hold more than %d nodes in all, the limit on an extractor's values; aliases can repeat a mapping, so narrow the path", core.MaxValues))
	_, err = run(t, extract.Of(yaml.Kind, yaml.Spec{File: "long.yaml", Path: "l[*]", As: "map", MaxBytes: 240}), root)
	extracttest.WantError(t, err, "long.yaml: the mappings the path selects hold more than 240 bytes of keys and scalars in all, the extractor's max_bytes")
	if got := mustRun(t, extract.Of(yaml.Kind, yaml.Spec{File: "fits.yaml", Path: "l[*]", As: "map", MaxBytes: 240}), root); len(got) != 3 {
		t.Errorf("123 bytes of keys and scalars under 240: %v", got)
	}
	_, err = run(t, extract.Of(json.Kind, json.Spec{Files: []string{"s/1.json", "s/2.json"}, Path: "[*]", As: "map"}), root)
	extracttest.WantError(t, err, fmt.Sprintf("json: the files hold more than %d values, the limit, each node of a mapping one", core.MaxValues))
	_, err = run(t, extract.Of(yaml.Kind, yaml.Spec{Files: []string{"b/1.yaml", "b/2.yaml"}, Path: "l[*]", As: "map", MaxBytes: 400}), root)
	extracttest.WantError(t, err, "yaml: the values of the files hold more than 400 bytes in all, the extractor's max_bytes")
}
