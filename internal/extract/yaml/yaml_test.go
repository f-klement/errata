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

// run builds s and extracts from root.
func run(t *testing.T, s Spec, root string) ([]check.Value, error) {
	t.Helper()
	return extracttest.Run(t, Build, s, root)
}

// mustRun is run for an extraction that must succeed.
func mustRun(t *testing.T, s Spec, root string) []check.Value {
	t.Helper()
	return extracttest.MustRun(t, Build, s, root)
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
	_, err := Build(Spec{File: "../f.yaml", Path: "a"})
	extracttest.WantError(t, err, `yaml: file "../f.yaml" is outside the repository`)
	_, err = Build(Spec{File: "f.yaml"})
	extracttest.WantError(t, err, "yaml: path is required")
	_, err = run(t, yamlSpec("a"), extracttest.Repo(t, nil))
	extracttest.WantError(t, err, "read f.yaml: no such file or directory")
	_, err = run(t, Spec{File: "f.yaml", Path: "a", MaxBytes: 70}, extracttest.Repo(t, map[string]string{"f.yaml": strings.Repeat("[", 60)}))
	extracttest.WantError(t, err, "f.yaml: the file can hold up to 124 nodes, over the limit of 10")
}
