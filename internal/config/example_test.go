package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract"
	"github.com/f-klement/errata/internal/extract/core"
)

// exampleFile is the example config, from this package's directory.
const exampleFile = "../../examples/errata.example.yaml"

// exampleTimes is how many times the example shows each feature: twice, in
// two situations.
const exampleTimes = 2

// The example shows every feature errata reads twice, so a feature added
// without an example fails here. Every list comes from the code: the
// extractor kinds from the registry (extract.Keys), each kind's keys from
// its spec in the registry's schema (kindKeys), the keys of a check, of
// generated and of the top level from the structs the loader decodes into,
// the scopes, the compare values and the normalizations from check. The
// top level keys can each appear once only, since the file is one YAML
// document; defaults.run is shown twice by two program checks that take it,
// beside two that write their own run. Planted breaks: the second html
// extractor renamed to markdown, the witness key of migrations-are-numbered
// removed, a scope added to check.Scopes with no check of it, a key added
// to the yaml kind's spec, and the before of the second context removed.
func TestExampleShowsEveryFeatureTwice(t *testing.T) {
	doc := exampleDoc(t)
	got := tally(t, doc)
	want := map[string][]string{
		"top level key":   jsonKeys(reflect.TypeFor[file]()),
		"check key":       jsonKeys(reflect.TypeFor[checkEntry]()),
		"generated key":   jsonKeys(reflect.TypeFor[generatedEntry]()),
		"extractor kind":  extract.Keys(),
		"scope":           strs(check.Scopes),
		"compare value":   strs(slices.DeleteFunc(slices.Clone(check.Compares), func(c check.Compare) bool { return c == (check.Rule{}).Comparison() })),
		"normalize value": strs(check.Normalizations),
		"program check":   {"with its own run", "with defaults.run"},
		"defaults key":    jsonKeys(reflect.TypeFor[defaults]()),
		"rule key":        ruleKeys,
	}
	want["kind key"], want["bound key"] = kindKeys()
	for what, names := range want {
		if len(names) == 0 {
			t.Errorf("the code lists no %s, so the example is held to none", what)
		}
		least := exampleTimes
		if what == "top level key" || what == "defaults key" {
			least = 1
		}
		for _, name := range names {
			if n := got[what][name]; n < least {
				t.Errorf("the example shows the %s %q %d times, want %d or more", what, name, n, least)
			}
		}
	}
}

// Above each check a comment says the claim the check makes and names the
// reference sections that explain its keys, as anchors of docs/reference.md.
// Planted breaks: a check's Claim: dropped, and a section's anchor changed to
// one no heading of docs/reference.md has.
func TestExampleCommentsNameTheirSections(t *testing.T) {
	doc := exampleDoc(t)
	anchors := markdownAnchors(t, "../../docs/reference.md")
	see := regexp.MustCompile(`(?m)^# See docs/reference\.md(#.+)\.$`)
	claim := regexp.MustCompile(`(?m)^# Claim: `)
	checks := member(doc, "checks")
	if checks == nil || len(checks.Content) == 0 {
		t.Fatal("the example has no checks")
	}
	for _, c := range checks.Content {
		id := member(c, "id").Value
		comment := c.HeadComment
		if !claim.MatchString(comment) {
			t.Errorf("check %s: the comment above it does not start a line with Claim:, saying what it checks", id)
		}
		m := see.FindStringSubmatch(comment)
		if m == nil {
			t.Errorf("check %s: the comment above it names no section, as a line # See docs/reference.md#<anchor>; #<anchor>.", id)
			continue
		}
		for anchor := range strings.SplitSeq(m[1], "; ") {
			if err := anchors.refuses(anchor); err != nil {
				t.Errorf("check %s: %v", id, err)
			}
		}
	}
}

// The anchors come from checks/markdown.awk, the reading of Markdown the
// link check uses, so the example's comments and the link check hold one
// rule. Each row is a shape on which a reading of its own went wrong (delta
// review 2 of #174, E1): link markup in a heading, a fence of one character
// holding a line of the other, an indented fence, and a heading the awk
// cannot be sure GitHub anchors as it does. Planted breaks: markdown.awk's
// slug keeping link markup, and refuses() taking an unsure anchor.
func TestExampleAnchorsFollowTheLinkCheck(t *testing.T) {
	md := filepath.Join(t.TempDir(), "doc.md")
	body := "## See [Records](#records)\n" +
		"~~~\n```\n~~~\n## After nested\n" +
		"  ```\n## Indented fence\n  ```\n" +
		"## Café note\n" +
		"## A <b>bold</b> word\n" +
		"## Plain\n## Plain\n"
	if err := os.WriteFile(md, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	anchors := markdownAnchors(t, md)
	for _, tc := range []struct {
		anchor string
		refuse string // "" when the anchor is taken
	}{
		{"#see-records", ""},
		{"#see-recordsrecords", "no heading"},
		{"#after-nested", ""},
		{"#indented-fence", "no heading"},
		{"#café-note", "unsure"},
		{"#a-bboldb-word", "unsure"},
		{"#plain", ""},
		{"#plain-1", ""},
		{"plain", "no #"},
	} {
		err := anchors.refuses(tc.anchor)
		switch {
		case tc.refuse == "" && err != nil:
			t.Errorf("%s: refused, want it taken: %v", tc.anchor, err)
		case tc.refuse != "" && (err == nil || !strings.Contains(err.Error(), tc.refuse)):
			t.Errorf("%s: %v, want a refusal naming %q", tc.anchor, err, tc.refuse)
		}
	}
}

// anchorSet maps each anchor of a Markdown file's headings to whether
// checks/markdown.awk is sure GitHub writes it the same way.
type anchorSet struct {
	file string
	sure map[string]bool
}

// refuses says why anchor, written #name, names no heading the link check
// would take: no # in front, no heading with that anchor, or a heading whose
// anchor the awk marks unsure, which the link check refuses too.
func (s anchorSet) refuses(anchor string) error {
	name, ok := strings.CutPrefix(anchor, "#")
	if !ok {
		return fmt.Errorf("%q has no # in front, so it is no anchor of %s", anchor, s.file)
	}
	sure, found := s.sure[name]
	switch {
	case !found:
		return fmt.Errorf("%q is the anchor of no heading of %s", anchor, s.file)
	case !sure:
		return fmt.Errorf("%q is the anchor of a heading of %s that checks/markdown.awk marks unsure: GitHub may anchor it otherwise", anchor, s.file)
	}
	return nil
}

// anchorDriver prints each heading's anchor of the file named first, and 1
// for a sure one or 2 for an unsure one, through checks/markdown.awk's
// anchors().
const anchorDriver = `BEGIN {
	f = ARGV[1]; ARGV[1] = ""
	anchors(f)
	for (i = 1; i <= heads[f]; i++) print hanchor[f, i] "\t" has[f, hanchor[f, i]]
	exit
}
`

// markdownAnchors reads the anchors of the Markdown file at path with
// checks/markdown.awk, run as checks/relative-links.sh runs it.
func markdownAnchors(t *testing.T, path string) anchorSet {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "driver.awk")
	if err := os.WriteFile(driver, []byte(anchorDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "awk", "-v", "q='", "-f", "../../checks/markdown.awk", "-f", driver, path)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("awk over %s: %v", path, err)
	}
	s := anchorSet{file: filepath.Base(path), sure: map[string]bool{}}
	for line := range strings.Lines(string(out)) {
		name, flag, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "\t")
		if !ok || (flag != "1" && flag != "2") {
			t.Fatalf("awk over %s printed %q, want an anchor, a tab and 1 or 2", path, line)
		}
		s.sure[name] = flag == "1"
	}
	if len(s.sure) == 0 {
		t.Fatalf("awk found no heading in %s", path)
	}
	return s
}

// exampleDoc is the example's top level mapping.
func exampleDoc(t *testing.T) *yaml.Node {
	t.Helper()
	raw, err := os.ReadFile(exampleFile)
	if err != nil {
		t.Fatal(err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		t.Fatal("the example is not one YAML document holding a mapping")
	}
	return root.Content[0]
}

// tally counts what the example shows, by what it is and then by name.
func tally(t *testing.T, doc *yaml.Node) map[string]map[string]int {
	t.Helper()
	got := map[string]map[string]int{}
	add := func(what, name string) {
		if got[what] == nil {
			got[what] = map[string]int{}
		}
		got[what][name]++
	}
	for key := range keysOf(doc) {
		add("top level key", key)
	}
	if d := member(doc, "defaults"); d != nil {
		for key := range keysOf(d) {
			add("defaults key", key)
		}
	}
	checks := member(doc, "checks")
	if checks == nil {
		t.Fatal("the example has no checks key")
	}
	for _, c := range checks.Content {
		keys := keysOf(c)
		for key := range keys {
			add("check key", key)
			if slices.Contains(ruleKeys, key) {
				add("rule key", key)
			}
		}
		if s := member(c, "scope"); s != nil {
			add("scope", s.Value)
		}
		if v := member(c, "compare"); v != nil {
			add("compare value", v.Value)
		}
		if n := member(c, "normalize"); n != nil {
			for _, v := range n.Content {
				add("normalize value", v.Value)
			}
		}
		if g := member(c, "generated"); g != nil {
			for key := range keysOf(g) {
				add("generated key", key)
			}
		}
		if x := member(c, "extract"); x != nil {
			for i := 1; i < len(x.Content); i += 2 {
				for kind, spec := range keysOf(x.Content[i]) {
					add("extractor kind", kind)
					for key, v := range keysOf(spec) {
						add("kind key", kind+"."+key)
						add("bound key", key)
						subs := []*yaml.Node{v}
						if v.Kind == yaml.SequenceNode {
							subs = v.Content
						}
						for _, sub := range subs {
							for subkey := range keysOf(sub) {
								add("kind key", kind+"."+key+"."+subkey)
							}
						}
					}
				}
			}
		}
		_, rule := keys["extract"]
		_, generated := keys["generated"]
		if !rule && !generated {
			if _, own := keys["run"]; own {
				add("program check", "with its own run")
			} else {
				add("program check", "with defaults.run")
			}
		}
	}
	return got
}

// keysOf is the keys of the mapping m, each with its value.
func keysOf(m *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if m.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		out[m.Content[i].Value] = m.Content[i+1]
	}
	return out
}

// member is the value of key in the mapping m, nil when m has no such key.
func member(m *yaml.Node, key string) *yaml.Node { return keysOf(m)[key] }

// jsonKeys is the key each field of the struct s decodes, as its json tag
// names it.
func jsonKeys(s reflect.Type) []string {
	var out []string
	for f := range s.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

// kindKeys are the keys of each extractor kind's spec, as the registry's
// schema (extract.Schema) has them, each written kind.key, and a key of a
// struct below a key, such as regex.context.with, written kind.key.sub.
// The key a spec's Bound returns, max_bytes in every kind that has one, is
// left out of them and named in bound instead: it is one limit with one
// meaning in each kind (docs/reference.md#limits), so two examples in all show it,
// where two in each kind would put it in most of the example's extractors.
func kindKeys() (each, bound []string) {
	for f := range extract.Schema().Fields() {
		kind, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		spec := f.Type.Elem()
		for _, k := range specKeys(spec, "", nil) {
			if k.bounds(spec) {
				if !slices.Contains(bound, k.name) {
					bound = append(bound, k.name)
				}
				continue
			}
			each = append(each, kind+"."+k.name)
		}
	}
	return each, bound
}

// specKey is one key of a spec: its name and the index of its field.
type specKey struct {
	name  string
	index []int
}

// bounds reports whether k is the key whose value a spec of type spec
// returns from Bound (core.Bounded): a spec with k set, and nothing else,
// bounds its reads by k's value.
func (k specKey) bounds(spec reflect.Type) bool {
	if len(k.index) == 0 {
		return false
	}
	v := reflect.New(spec).Elem()
	f, err := v.FieldByIndexErr(k.index)
	if err != nil || f.Kind() != reflect.Int64 {
		return false
	}
	const probe = 12345
	f.SetInt(probe)
	b, ok := v.Interface().(core.Bounded)
	return ok && b.Bound() == probe
}

// specKeys are the json keys of the struct s, each after prefix, with the
// index of its field below at: an embedded struct's keys as s's own, as
// encoding/json reads them, and the keys of a struct a field holds,
// directly, through a pointer or in a slice, after the field's key and a
// dot, with no index, since no Bound reads them.
func specKeys(s reflect.Type, prefix string, at []int) []specKey {
	var out []specKey
	for f := range s.Fields() {
		t := f.Type
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		index := append(slices.Clone(at), f.Index...)
		if f.Anonymous && t.Kind() == reflect.Struct {
			out = append(out, specKeys(t, prefix, index)...)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" || !f.IsExported() {
			continue
		}
		if prefix != "" {
			index = nil
		}
		out = append(out, specKey{prefix + name, index})
		if t.Kind() == reflect.Struct {
			out = append(out, specKeys(t, prefix+name+".", nil)...)
		}
	}
	return out
}

// strs is each value of vs as its string.
func strs[S ~string](vs []S) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = string(v)
	}
	return out
}
