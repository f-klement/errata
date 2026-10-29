package report

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/config"
)

// listSection is the README's section on errata list.
func listSection(t *testing.T) string {
	t.Helper()
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(readme), "\n### Listing the checks\n")
	if !ok {
		t.Fatal("README has no section Listing the checks")
	}
	section, _, _ := strings.Cut(after, "\n### ")
	return section
}

// fenced is the text of the first block of section fenced as lang.
func fenced(t *testing.T, section, lang string) string {
	t.Helper()
	_, block, ok := strings.Cut(section, "```"+lang+"\n")
	if !ok {
		t.Fatalf("the README's section has no %s block", lang)
	}
	block, _, _ = strings.Cut(block, "```")
	return block
}

// The README's example is what errata list prints: its errata.yaml, loaded
// by the loader errata runs with from a root of its own, listed as JSON,
// equals the JSON the README shows, at every depth, extractor entries and
// defaults included. The planted break: renaming at.outside to external in
// ListJSON, which TestListJSONKeysAreDocumented also catches; and the
// README's example without the replicas extractor's max_bytes.
func TestListJSONExampleIsWhatListPrints(t *testing.T) {
	section := listSection(t)
	root := t.TempDir()
	path := filepath.Join(root, config.DefaultFile)
	if err := os.WriteFile(path, []byte(fenced(t, section, "yaml")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root, path)
	if err != nil {
		t.Fatalf("the README's errata.yaml does not load: %v", err)
	}
	var buf bytes.Buffer
	if err := (ListJSON{}).WriteList(&buf, cfg.Checks, cfg.Required); err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(fenced(t, section, "json")), &want); err != nil {
		t.Fatalf("the README's example is not JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("errata list prints\n%s\nfor the README's errata.yaml, and the README shows\n%s", buf.String(), fenced(t, section, "json"))
	}
}

// keyPaths adds to into every key of v that the README's table names, with
// prefix before it: the keys of the document and of each check as they are,
// and those of any object further in as at.file and relation.agree, however
// deep. An extractor's entry is errata.yaml's, which the section on
// extractors documents, so the keys under extract are not walked.
func keyPaths(v map[string]any, prefix string, into map[string]bool) {
	for k, x := range v {
		path := prefix + k
		into[path] = true
		switch x := x.(type) {
		case map[string]any:
			if k != "extract" {
				keyPaths(x, path+".", into)
			}
		case []any:
			// A list of objects, such as checks, holds keys of the level
			// the list is on.
			for _, e := range x {
				if m, ok := e.(map[string]any); ok {
					keyPaths(m, prefix, into)
				}
			}
		}
	}
}

// The README's table documents every key the JSON holds, nested ones
// included, and no other, for a check of each kind. A key added to the
// output or renamed without the README fails here. The planted breaks: the
// table without its row for at, ListJSON writing at.outside as external,
// and the table without its row for generated.max_bytes.
func TestListJSONKeysAreDocumented(t *testing.T) {
	var buf bytes.Buffer
	if err := (ListJSON{}).WriteList(&buf, listChecks(), nil); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	written := map[string]bool{}
	keyPaths(doc, "", written)

	documented := map[string]bool{}
	row := regexp.MustCompile("(?m)^\\| (`[^|]+) \\|")
	for _, m := range row.FindAllStringSubmatch(listSection(t), -1) {
		for _, name := range strings.Split(m[1], ", ") {
			documented[strings.Trim(name, "`")] = true
		}
	}
	if !maps.Equal(written, documented) {
		t.Errorf("the JSON holds keys %v, the README's table documents %v", slices.Sorted(maps.Keys(written)), slices.Sorted(maps.Keys(documented)))
	}
}

// keyPaths walks every object but an extractor's entry, however deep, so a
// nested object a later shape adds has its keys held by the README's table
// too. The planted break: walking only at and relation, by name.
func TestKeyPathsWalkEveryObject(t *testing.T) {
	got := map[string]bool{}
	keyPaths(map[string]any{
		"version":  1,
		"provider": map[string]any{"name": "k", "read": map[string]any{"every": "1m"}},
		"checks":   []any{map[string]any{"id": "a", "at": map[string]any{"file": "f"}, "extract": map[string]any{"x": map[string]any{"yaml": 1}}}},
	}, "", got)
	want := []string{"at", "at.file", "checks", "extract", "id", "provider", "provider.name", "provider.read", "provider.read.every", "version"}
	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, want) {
		t.Errorf("keyPaths = %v, want %v", keys, want)
	}
}
