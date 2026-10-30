package yaml

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// build is Build without parsers, which no spec of these tests needs: the
// embedded steps are tested in internal/extract/core, through the facade.
func build(s Spec) (check.Extractor, error) { return Build(s, core.Parsers{}) }

// run builds s and extracts from root.
func run(t *testing.T, s Spec, root string) ([]check.Value, error) {
	t.Helper()
	return extracttest.Run(t, build, s, root)
}

// mustRun is run for an extraction that must succeed.
func mustRun(t *testing.T, s Spec, root string) []check.Value {
	t.Helper()
	return extracttest.MustRun(t, build, s, root)
}

// slowOnly skips t under -race, -short and Gremlins' mutation tag, as
// extracttest.SlowOnly says; make slow runs it from the Makefile's
// SLOW_TESTS.
func slowOnly(t *testing.T) {
	t.Helper()
	extracttest.SlowOnly(t)
}

func yamlSpec(path string) Spec { return Spec{File: "f.yaml", Path: path} }

// Each scalar keeps the text the file wrote, not what a decoder makes of it.
func TestYAMLScalarsKeepTheirText(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": `float: 1.0
yes: true
null1: null
null2: ~
empty:
quoted: "1.0"
single: 'it''s'
octal: 0o17
date: 2026-10-17
block: |
  line one
  line two
folded: >
  a
  b
`})
	for path, want := range map[string]string{
		"float":  "1.0@1",
		"yes":    "true@2",
		"null1":  "null@3",
		"null2":  "~@4",
		"empty":  "@5",
		"quoted": "1.0@6",
		"single": "it's@7",
		"octal":  "0o17@8",
		"date":   "2026-10-17@9",
		"block":  "line one\nline two\n@10",
		"folded": "a b\n@13",
	} {
		if got := extracttest.Texts(mustRun(t, yamlSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestYAMLPaths(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": `metadata:
  labels:
    app.kubernetes.io/name: web
spec:
  containers:
    - name: a
      image: a:1
    - name: b
      image: b:2
      ports: [80, 443]
`})
	for path, want := range map[string]string{
		"metadata.labels['app.kubernetes.io/name']": "web@3",
		"spec.containers[*].image":                  "a:1@7 b:2@9",
		"spec.containers[-1].ports[*]":              "80@10 443@10",
		"spec.containers[0].ports[*]":               "",
		"spec.absent":                               "",
		"$.spec.containers[5].image":                "",
	} {
		if got := extracttest.Texts(mustRun(t, yamlSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

// The planted break: a path that stops at a mapping and one that stops at a
// sequence. Neither is a scalar, so the rule is unknown rather than empty.
func TestYAMLPathOnAContainerIsAnError(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": "spec:\n  ports:\n    - 80\n"})
	_, err := run(t, yamlSpec("spec"), root)
	extracttest.WantError(t, err, `f.yaml: path "spec" selects a mapping at line 2, not a scalar; add [*] to take its members`)
	_, err = run(t, yamlSpec("spec.ports"), root)
	extracttest.WantError(t, err, `f.yaml: path "spec.ports" selects a sequence at line 3, not a scalar`)
}

func TestYAMLAliasAndMergeKey(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": `x-common: &common
  image: base:1
  restart: always
x-more: &more
  restart: never
  user: app
services:
  web:
    <<: [*common, *more]
    restart: unless-stopped
  db:
    <<: *common
  copy: *common
`})
	for path, want := range map[string]string{
		"services.web.image":   "base:1@2",
		"services.web.restart": "unless-stopped@10",
		"services.web.user":    "app@6",
		"services.db.restart":  "always@3",
		"services.copy.image":  "base:1@2",
		"services.web['<<']":   "",
	} {
		if got := extracttest.Texts(mustRun(t, yamlSpec(path), root)); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}

func TestYAMLSelectsDocuments(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": extracttest.Manifests})
	for sel, want := range map[[2]string]string{
		{"Deployment", ""}:    "2@7 3@13",
		{"Deployment", "web"}: "2@7",
		{"", "web"}:           "2@7 9@19",
		{"Service", ""}:       "9@19",
	} {
		s := Spec{File: "f.yaml", Path: "spec.replicas", Kind: sel[0], Name: sel[1]}
		if got := extracttest.Texts(mustRun(t, s, root)); got != want {
			t.Errorf("%v: got %q, want %q", sel, got, want)
		}
	}
}

// The planted breaks: no selector on a file of three documents, and a
// selector that matches none of them.
func TestYAMLDocumentSelectionRefuses(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": extracttest.Manifests})
	_, err := run(t, yamlSpec("spec.replicas"), root)
	extracttest.WantError(t, err, "f.yaml: holds 3 documents; set kind or name to select one")
	_, err = run(t, Spec{File: "f.yaml", Path: "spec.replicas", Kind: "Service", Name: "worker"}, root)
	extracttest.WantError(t, err, `f.yaml: no document has kind "Service" and name "worker"`)
}

func TestYAMLEmptyDocumentsAreSkipped(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{
		"one.yaml":   "---\n# a comment\n---\na: 1\n---\n",
		"empty.yaml": "",
		"null.yaml":  "--- null\n",
	})
	if got := extracttest.Texts(mustRun(t, Spec{File: "one.yaml", Path: "a"}, root)); got != "1@4" {
		t.Errorf("one.yaml: %q", got)
	}
	if got := mustRun(t, Spec{File: "empty.yaml", Path: "a"}, root); len(got) != 0 {
		t.Errorf("empty.yaml: %v", got)
	}
	// A document that says null is not empty: $ selects it.
	if got := extracttest.Texts(mustRun(t, Spec{File: "null.yaml", Path: "$"}, root)); got != "null@1" {
		t.Errorf("null.yaml: %q", got)
	}
}

// What the node tree accepts and decoding refuses, which the walk refuses
// itself. Each body is a planted break; the parser's own refusals come last.
func TestYAMLThatDoesNotDecodeIsAnError(t *testing.T) {
	for body, want := range map[string]string{
		"a: 1\na: 2\n":                          `line 2: key "a" is repeated in one mapping`,
		"a: 1\n'a': 2\n":                        `line 2: key "a" is repeated in one mapping`,
		"x: &k a\nm: {a: 1, *k: 2}\n":           `line 2: key "a" is repeated in one mapping`,
		"b: &b {x: 1}\nm: {<<: *b, <<: *b}\n":   `line 2: key "<<" is repeated in one mapping`,
		"m: {'<<': 1, <<: {x: 1}}\n":            `line 1: key "<<" is repeated in one mapping`,
		"? [a]\n: 1\n":                          "line 1: a key is a mapping or a sequence; errata reads only scalar keys",
		"x: &k {a: 1}\nm: {*k: 1}\n":            "line 2: a key is a mapping or a sequence; errata reads only scalar keys",
		"a: !!int x\n":                          `line 1: "x" is not a valid !!int`,
		"!!bool maybe: 1\n":                     `line 1: "maybe" is not a valid !!bool`,
		"a: !!binary '%%%'\n":                   `line 1: "%%%" is not a valid !!binary`,
		"a: &x [*x]\n":                          "line 1: alias *x is inside the anchor it names",
		"a: &x {<<: *x}\n":                      "line 1: alias *x is inside the anchor it names",
		"a: {<<: 1}\n":                          "line 1: a merge key (<<) takes a mapping, an alias of one, or a sequence of those",
		"s: &s [1]\na: {<<: *s}\n":              "line 2: a merge key (<<) takes a mapping, an alias of one, or a sequence of those",
		"m: &m {x: 1}\na: {<<: [*m, 1]}\n":      "line 2: a merge key (<<) takes a mapping, an alias of one, or a sequence of those",
		"m: &a x\nn: {*a: 1, k: &a y, *a: 2}\n": "line 2: alias key *a is repeated in one mapping",
		"a: *nowhere\n":                         "yaml: unknown anchor 'nowhere' referenced",
		"a: [\n":                                "yaml: line 1: did not find expected node content",
		"a: 1\n---\nb: [\n":                     "yaml: line 3: did not find expected node content",
	} {
		root := extracttest.Repo(t, map[string]string{"f.yaml": body})
		_, err := run(t, Spec{File: "f.yaml", Path: "a", Kind: "K"}, root)
		if err == nil || err.Error() != "f.yaml: "+want {
			t.Errorf("%q: error %v, want %q", body, err, "f.yaml: "+want)
		}
	}
}

// What decoding accepts, the walk accepts too: a merge key in each of its
// forms, a quoted "<<" key, keys that decode to the same value but are
// written differently, a tag the text fits, an anchor on a mapping key, an
// alias to an anchor in an earlier document, empty or not, and a later key's
// alias to an anchor inside a merge key's value.
func TestYAMLThatDecodesIsRead(t *testing.T) {
	for _, c := range []struct{ body, path, want string }{
		{"b: &b {x: 1}\na: {<<: *b}\n", "a.x", "1@1"},
		{"a: {<<: {x: 1}}\n", "a.x", "1@1"},
		{"b: &b {x: 1}\nc: &c {x: 2}\na: {<<: [*c, *b]}\n", "a.x", "2@2"},
		{"a: {'<<': 1, x: 2}\n", "a['<<']", "1@1"},
		{"a: {~: 1, null: 2, x: 3}\n", "a.null", "2@1"},
		{"a: {1: 1, 01: 2, x: !!int 3}\n", "a['01']", "2@1"},
		{"a: {&k x: 1, y: *k}\n", "a.y", "x@1"},
		{"--- &d {x: 9}\n--- {a: *d, kind: K}\n", "a.x", "9@1"},
		{"--- &e\n--- {a: {x: *e}}\n", "a.x", "@1"},
		{"x:\n  <<: &m {a: 1}\n  b: *m\n", "x.b.a", "1@2"},
		{"x: {<<: [&m {a: 1}], b: *m}\n", "x.b.a", "1@1"},
		{"x: {<<: {c: &m {a: 1}}, b: *m}\n", "x.b.a", "1@1"},
	} {
		root := extracttest.Repo(t, map[string]string{"f.yaml": c.body})
		got, err := run(t, Spec{File: "f.yaml", Path: c.path, Kind: map[bool]string{true: "K"}[strings.Contains(c.body, "kind")]}, root)
		if err != nil || extracttest.Texts(got) != c.want {
			t.Errorf("%q: got %s, %v, want %s", c.body, extracttest.Texts(got), err, c.want)
		}
		dec := yaml.NewDecoder(strings.NewReader(c.body))
		for {
			var v any
			if err := dec.Decode(&v); err != nil {
				if !errors.Is(err, io.EOF) {
					t.Errorf("%q: yaml.v3 refuses it: %v", c.body, err)
				}
				break
			}
		}
	}
}

// FuzzYAML found this: yaml.v3 puts the empty value of a lone ? on line 2 of
// a one-line file.
func TestYAMLEmptyNodeAtTheEndStaysInsideTheFile(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": "?", "g.yaml": "a:"})
	if got := extracttest.Texts(mustRun(t, Spec{File: "f.yaml", Path: "$[*]"}, root)); got != "@1" {
		t.Errorf("f.yaml: got %q", got)
	}
	if got := extracttest.Texts(mustRun(t, Spec{File: "g.yaml", Path: "a"}, root)); got != "@1" {
		t.Errorf("g.yaml: got %q", got)
	}
}

// A document whose kind is not a scalar, or whose metadata is not a mapping,
// matches no selector.
func TestYAMLSelectorSkipsDocumentsOfAnotherShape(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"f.yaml": "kind: [Deployment]\nmetadata: web\n---\n- 1\n---\nkind: Deployment\nmetadata: {name: web}\nv: ok\n"})
	for sel, want := range map[[2]string]string{
		{"Deployment", ""}: "ok@8",
		{"", "web"}:        "ok@8",
	} {
		s := Spec{File: "f.yaml", Path: "v", Kind: sel[0], Name: sel[1]}
		if got := extracttest.Texts(mustRun(t, s, root)); got != want {
			t.Errorf("%v: got %q, want %q", sel, got, want)
		}
	}
}

// Aliases and merge keys may add maxExpanded nodes to a file, and no more.
// An anchor of k items stands for k+1 nodes, and each alias of it adds them.
// The planted break is one more alias, which a second document adds, since
// the count is for the whole file.
func TestYAMLExpansionLimit(t *testing.T) {
	const items, aliases = 999, core.MaxExpanded / 1000
	doc := "a: &a [" + strings.TrimSuffix(strings.Repeat("1, ", items), ", ") + "]\nb: [" + strings.TrimSuffix(strings.Repeat("*a, ", aliases), ", ") + "]\nkind: K\n"
	got := mustRun(t, Spec{File: "f.yaml", Path: "b[-1][-1]"}, extracttest.Repo(t, map[string]string{"f.yaml": doc}))
	if extracttest.Texts(got) != "1@1" {
		t.Errorf("at the limit: got %s", extracttest.Texts(got))
	}
	more := doc + "---\nkind: L\nc: *a\n"
	_, err := run(t, Spec{File: "f.yaml", Path: "b[0][0]", Kind: "K"}, extracttest.Repo(t, map[string]string{"f.yaml": more}))
	want := "f.yaml: line 6: aliases and merge keys expand to more than 1000000 nodes, the limit; write the repeated part out, or a regex extractor can still read the file"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

// A merge key counts the nodes of what it names, so a chain of mappings that
// each merge the one before is refused once the copies would pass the limit,
// before they are made. The whole message is asserted, since expandKey's
// refusal starts with the same words: a chain refused there would pass a
// test of the start.
func TestYAMLMergeChainIsCounted(t *testing.T) {
	chain := func(n int) string {
		var b strings.Builder
		b.WriteString("- &a0 {k0: 1}\n")
		for i := 1; i < n; i++ {
			b.WriteString("- &a" + strconv.Itoa(i) + " {<<: *a" + strconv.Itoa(i-1) + ", k" + strconv.Itoa(i) + ": 1}\n")
		}
		return b.String()
	}
	got := mustRun(t, Spec{File: "f.yaml", Path: "[-1].k0"}, extracttest.Repo(t, map[string]string{"f.yaml": chain(100)}))
	if extracttest.Texts(got) != "1@1" {
		t.Errorf("a chain of 100: got %s", extracttest.Texts(got))
	}
	_, err := run(t, Spec{File: "f.yaml", Path: "[-1].k0"}, extracttest.Repo(t, map[string]string{"f.yaml": chain(1000)}))
	want := "f.yaml: line 817: aliases and merge keys expand to more than 1000000 nodes, the limit; write the repeated part out, or a regex extractor can still read the file"
	if err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

// A merge key hashes every key of the mappings it merges, so a merged key
// counts against maxExpanded a node for every mergeKeyBytes bytes it holds.
// The planted break: merges of two keys of 64 KiB each, one merge past the
// limit, which took 212 s at 50000 merges of keys of 2 MB before the count.
func TestYAMLMergedKeyBytesAreCounted(t *testing.T) {
	key := strings.Repeat("k", 64<<10)
	per := 2*len(key)/mergeKeyBytes + 5 // the bytes, and the five nodes the alias of m adds
	file := func(merges int) string {
		var b strings.Builder
		// A key past 1024 characters takes the explicit form, ? key.
		b.WriteString("m: &m\n  ? " + key + "a\n  : 1\n  ? " + key + "b\n  : 2\nrefs:\n")
		for range merges {
			b.WriteString("  - {<<: *m}\n")
		}
		return b.String()
	}
	under := core.MaxExpanded / per
	if _, err := Parse([]byte(file(under))); err != nil {
		t.Fatalf("%d merges: %v", under, err)
	}
	_, err := Parse([]byte(file(under + 1)))
	extracttest.WantError(t, err, fmt.Sprintf("line %d: aliases and merge keys expand to more than 1000000 nodes, the limit, counting an alias key or a merged key as a node for every 64 bytes; write the repeated part out, or a regex extractor can still read the file", under+7))
}

// An alias used as a key stands for its anchor's text, which every mapping
// that uses it hashes, so it counts against maxExpanded one node and one
// more for every mergeKeyBytes bytes of that text. The planted break: alias
// keys of 64 KiB one past the limit, which at 10000 mappings with a key of
// 20 MB took 32.5 s before the count.
func TestYAMLAliasKeyBytesAreCounted(t *testing.T) {
	key := strings.Repeat("k", 64<<10)
	per := 1 + len(key)/mergeKeyBytes
	file := func(mappings int) string {
		var b strings.Builder
		b.WriteString("k:\n  ? &k " + key + "\n  : 1\nrefs:\n")
		for range mappings {
			b.WriteString("  - {*k : 1}\n")
		}
		return b.String()
	}
	under := core.MaxExpanded / per
	if _, err := Parse([]byte(file(under))); err != nil {
		t.Fatalf("%d mappings: %v", under, err)
	}
	_, err := Parse([]byte(file(under + 1)))
	extracttest.WantError(t, err, fmt.Sprintf("line %d: aliases and merge keys expand to more than 1000000 nodes, the limit, counting an alias key or a merged key as a node for every 64 bytes; write the repeated part out, or a regex extractor can still read the file", under+5))
}

// A file that does not parse is an error that names the file and gives the
// parser's reason.
func TestYAMLFileThatDoesNotParseIsAnError(t *testing.T) {
	root := extracttest.Repo(t, map[string]string{"bad": "a: [\n"})
	_, err := run(t, Spec{File: "bad", Path: "a"}, root)
	if want := "bad: yaml: line 1: did not find expected node content"; err == nil || err.Error() != want {
		t.Errorf("error %v, want %q", err, want)
	}
}

// The depth of a value is the count of path steps that reach it. The reader
// reads a value at exactly core.MaxDepth and refuses one a level deeper,
// which is the planted break.
func TestYAMLDepthBoundary(t *testing.T) {
	deep := func(first string, n int) string { return first + strings.Repeat("[0]", n) }
	for _, c := range []struct {
		file, body, path string
	}{
		{"f.yaml", "a: " + extracttest.Nest(core.MaxDepth-1, "1") + "\n", deep("a", core.MaxDepth-1)},
	} {
		s := Spec{File: c.file, Path: c.path}
		if got := extracttest.Texts(mustRun(t, s, extracttest.Repo(t, map[string]string{c.file: c.body}))); got != "1@1" {
			t.Errorf("%s at depth %d: got %q", c.file, core.MaxDepth, got)
		}
		_, err := run(t, s, extracttest.Repo(t, map[string]string{c.file: strings.Replace(c.body, "1", "[1]", 1)}))
		want := c.file + ": line 1: nested more than 1000 levels deep, the limit; a regex extractor can still read the file"
		if err == nil || err.Error() != want {
			t.Errorf("error %v, want %q", err, want)
		}
	}
}

// Build refuses a file outside the repository and an empty path, under the
// yaml kind's name, and a read refuses a file it cannot open and one denser
// than the node limit allows. core's TestBuildRefusesFileOutsideRepository
// and TestNodeLimit find the same through every kind; this is the reader's
// own wiring of each, whose planted break is the refusal passed by.
func TestYAMLBuildAndReadRefuse(t *testing.T) {
	_, err := build(Spec{File: "../f.yaml", Path: "a"})
	extracttest.WantError(t, err, `yaml: file "../f.yaml" is outside the repository`)
	_, err = build(Spec{File: "f.yaml"})
	extracttest.WantError(t, err, "yaml: path is required")
	_, err = run(t, yamlSpec("a"), extracttest.Repo(t, nil))
	extracttest.WantError(t, err, "read f.yaml: no such file or directory")
	_, err = run(t, Spec{File: "f.yaml", Path: "a", MaxBytes: 70}, extracttest.Repo(t, map[string]string{"f.yaml": strings.Repeat("[", 60)}))
	extracttest.WantError(t, err, "f.yaml: the file can hold up to 124 nodes, over the limit of 10")
}

// ParseNoted notes each node once, an anchor's for its aliases too, with
// the tag the file wrote and whether it is a plain scalar with no tag: a
// quoted, a block or a tagged scalar is not plain, and a node the file
// wrote no tag on has none. Planted breaks: the note on every alias as
// well, which noted the anchored scalar twice; the tag yaml.v3 resolves
// noted, which gave "3.10" the tag !!float.
func TestParseNotedNotesTagsAndPlainScalars(t *testing.T) {
	type note struct {
		tag   string
		plain bool
	}
	got := map[string][]note{}
	docs, _, err := ParseNoted([]byte("a: &x 3.10\nb: '3.10'\nc: !!str 3.10\nd: |\n  t\ne: !reference [j, k]\nf: *x\n"), 0,
		func(n *core.Node, tag string, plain bool) {
			key := n.Text
			if n.Shape != core.Scalar {
				key = fmt.Sprintf("%s@%d", n.Shape, n.Line)
			}
			got[key] = append(got[key], note{tag, plain})
		})
	if err != nil || len(docs) != 1 {
		t.Fatalf("ParseNoted: %v, %d documents", err, len(docs))
	}
	want := map[string][]note{
		"3.10": {{"", true}, {"", false}, {"!!str", false}}, "t\n": {{"", false}},
		"j": {{"", true}}, "k": {{"", true}}, "a sequence@6": {{"!reference", false}}, "a mapping@1": {{"", false}},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("noted %v, want %v", got, want)
	}
	if docs[0].Field["f"] != docs[0].Field["a"] {
		t.Fatal("the alias is not its anchor's node")
	}
}

// ParseNoted starts its count of the nodes aliases and merge keys add at
// the count it is given and returns where it ends, so two files each under
// core.MaxExpanded are refused together. Planted break: the count started
// at 0, which read the second file.
func TestParseNotedCarriesTheAliasCount(t *testing.T) {
	var b strings.Builder
	b.WriteString("a: &a {")
	for i := range 100 {
		fmt.Fprintf(&b, "k%d: 1, ", i)
	}
	b.WriteString("}\n")
	for i := range 3000 {
		fmt.Fprintf(&b, "m%d: {<<: *a}\n", i)
	}
	data := []byte(b.String())
	_, used, err := ParseNoted(data, 0, nil)
	if err != nil || used < core.MaxExpanded/2 || used > core.MaxExpanded {
		t.Fatalf("one file: %v, count %d; want between %d and %d", err, used, core.MaxExpanded/2, core.MaxExpanded)
	}
	_, _, err = ParseNoted(data, used, nil)
	extracttest.WantError(t, err, "aliases and merge keys expand to more than 1000000 nodes, the limit")
}

// In a set, a file with no document of the kind and name adds no value,
// where one file is refused for it; a file of several documents without a
// selector is refused in a set too. Planted breaks: the set's files read
// as one file, which refused the Service file; a file of one file read as
// a file of a set, which passed the one without the kind.
func TestYAMLFilesSelectByKind(t *testing.T) {
	root := extracttest.Tracked(t, map[string]string{
		"k/svc.yaml":  "kind: Service\nmetadata: {name: web}\n",
		"k/apps.yaml": "kind: Application\nmetadata: {name: a}\nspec: {rev: '1'}\n---\nkind: Application\nmetadata: {name: b}\nspec: {rev: '2'}\n",
		"k/x/c.yaml":  "kind: Application\nmetadata: {name: c}\nspec: {rev: '3'}\n",
	})
	vs, err := extracttest.Run(t, build, Spec{Files: []string{"k/**/*.yaml"}, Path: "spec.rev", Kind: "Application"}, root)
	if err != nil || extracttest.Texts(vs) != "1@3 2@7 3@3" || vs[0].At.File != "k/apps.yaml" || vs[2].At.File != "k/x/c.yaml" {
		t.Errorf("got %v, %v", vs, err)
	}
	_, err = extracttest.Run(t, build, Spec{File: "k/svc.yaml", Path: "spec.rev", Kind: "Application"}, root)
	extracttest.WantError(t, err, `k/svc.yaml: no document has kind "Application"`)
	_, err = extracttest.Run(t, build, Spec{Files: []string{"k/*.yaml"}, Path: "kind"}, root)
	extracttest.WantError(t, err, "k/apps.yaml: holds 2 documents; set kind or name to select one")
}

// merged is the review's shape: an anchor of 100 keys that m mappings
// merge.
func merged(m int) string {
	var b strings.Builder
	b.WriteString("base: &k {")
	for i := range 99 {
		fmt.Fprintf(&b, "k%d: 0, ", i)
	}
	b.WriteString("z: 0}\nm:\n")
	for range m {
		b.WriteString("- {<<: *k}\n")
	}
	return b.String()
}

// The files of a set share the budget on what aliases and merge keys add,
// as they share max_bytes: a file whose merges add more than half the
// budget reads alone and in a set of one, and two of them are refused at
// the second, naming the shared limit. 613 such files at the full budget
// each ran 82 s on builder before. Planted break: each file's parse
// started at 0, which read both.
func TestYAMLFilesShareTheAliasBudget(t *testing.T) {
	one := merged(2600)
	_, n, err := ParseNoted([]byte(one), 0, nil)
	if err != nil || n <= core.MaxExpanded/2 || n > core.MaxExpanded {
		t.Fatalf("one file adds %d nodes, %v; want more than half of %d and at most all", n, err, core.MaxExpanded)
	}
	alone := extracttest.Tracked(t, map[string]string{"d/a.yaml": one})
	if _, err := extracttest.Run(t, build, Spec{Files: []string{"d/*.yaml"}, Path: "m[0].z"}, alone); err != nil {
		t.Fatalf("one file: %v", err)
	}
	two := extracttest.Tracked(t, map[string]string{"d/a.yaml": one, "d/b.yaml": one})
	_, err = extracttest.Run(t, build, Spec{Files: []string{"d/*.yaml"}, Path: "m[0].z"}, two)
	extracttest.WantError(t, err, "d/b.yaml: line ")
	extracttest.WantError(t, err, fmt.Sprintf("aliases and merge keys expand to more than %d nodes", core.MaxExpanded))
	extracttest.WantError(t, err, "; the files of a set share the limits on aliases and merge keys")
	if _, err := extracttest.Run(t, build, Spec{File: "d/b.yaml", Path: "m[0].z"}, two); err != nil {
		t.Errorf("the second file alone: %v", err)
	}
}

// A tracked file whose name holds a newline, matched by a glob, is refused
// before the reader opens it, with the name quoted, so the reader's error
// cannot print it raw and forge a line of the text report: here a mapping
// where a scalar is wanted, which the reader's error would name the file
// for. A file: with a newline is refused when the spec is built. The
// review's shape (wave 4 of v0.2.3). Planted breaks: the control
// character check removed from Files.List, which printed the reader's error
// with the ERRATUM line raw; and from CheckSource, which built the spec.
func TestYAMLRefusesAPathWithAControlCharacter(t *testing.T) {
	forged := "x\nERRATUM forged (repo)\ny.yaml"
	root := extracttest.Tracked(t, map[string]string{forged: "a: {b: 1}\n", "ok.yaml": "a: '2'\n"})
	_, err := run(t, Spec{Files: []string{"*.yaml"}, Path: "a"}, root)
	extracttest.WantError(t, err, `yaml: files lists the glob "*.yaml", which matches "x\nERRATUM forged (repo)\ny.yaml", a tracked path that holds a control character, a direction control or a line or paragraph separator, which a terminal or a report viewer would act on; rename the file`)
	if vs := mustRun(t, Spec{Files: []string{"o*.yaml"}, Path: "a"}, root); extracttest.Texts(vs) != "2@1" {
		t.Errorf("a glob that matches no such path: %s", extracttest.Texts(vs))
	}
	_, err = build(Spec{File: forged, Path: "a"})
	extracttest.WantError(t, err, `yaml: file "x\nERRATUM forged (repo)\ny.yaml" holds a control character, a direction control or a line or paragraph separator, which a terminal or a report viewer would act on; rename the file`)
}
