package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"
	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

// genDoc draws a document: mappings and sequences of plain strings. A key may
// hold a dot or a dash, so leaf paths need the bracket form.
func genDoc(t *rapid.T, depth int, label string) any {
	if depth == 0 || rapid.IntRange(0, 2).Draw(t, label+"kind") == 0 {
		return rapid.StringMatching(`[a-z0-9][a-z0-9._:-]{0,8}`).Draw(t, label+"leaf")
	}
	n := rapid.IntRange(0, 4).Draw(t, label+"n")
	if rapid.Bool().Draw(t, label+"list") {
		list := make([]any, n)
		for i := range list {
			list[i] = genDoc(t, depth-1, label+strconv.Itoa(i))
		}
		return list
	}
	m := map[string]any{}
	for i := range n {
		m[rapid.StringMatching(`[a-z][a-z.-]{0,5}`).Draw(t, label+"key")] = genDoc(t, depth-1, label+strconv.Itoa(i))
	}
	return m
}

// leaves lists the path and text of every scalar in doc.
func leaves(doc any, path string, out map[string]string) {
	switch d := doc.(type) {
	case map[string]any:
		for k, v := range d {
			leaves(v, path+"['"+k+"']", out)
		}
	case []any:
		for i, v := range d {
			leaves(v, path+"["+strconv.Itoa(i)+"]", out)
		}
	case string:
		out[path] = d
	}
}

// checkLeaves extracts every leaf of doc from the file at name and checks
// that it comes back once, with its text, at a line of the file that holds
// that text.
func checkLeaves(t *rapid.T, root, name string, data []byte, doc any, build func(path string) Spec) {
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileLines := strings.Split(string(data), "\n")
	want := map[string]string{}
	leaves(doc, "$", want)
	for path, text := range want {
		e, err := Build(build(path))
		if err != nil {
			t.Fatalf("Build %s: %v", path, err)
		}
		got, err := e.Extract(root)
		if err != nil {
			t.Fatalf("%s: %v\n%s", path, err, data)
		}
		if len(got) != 1 || got[0].Text != text {
			t.Fatalf("%s: got %v, want %q\n%s", path, got, text, data)
		}
		checkLine(t, got[0], fileLines, data)
		if !strings.Contains(fileLines[got[0].At.Line-1], text) {
			t.Fatalf("%s: line %d is %q, which does not hold %q\n%s", path, got[0].At.Line, fileLines[got[0].At.Line-1], text, data)
		}
	}
}

func checkLine(t *rapid.T, v check.Value, fileLines []string, data []byte) {
	if v.At.Line < 1 || v.At.Line > len(fileLines) {
		t.Fatalf("value %q at line %d, outside the file's %d lines\n%s", v.Text, v.At.Line, len(fileLines), data)
	}
}

func TestPropertyYAMLValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		doc := genDoc(t, 3, "")
		data, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		checkLeaves(t, root, "f.yaml", data, doc, func(p string) Spec { return Spec{YAML: &YAML{File: "f.yaml", Path: p}} })
	})
}

func TestPropertyJSONValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		doc := genDoc(t, 3, "")
		data, err := json.Marshal(doc)
		if rapid.Bool().Draw(t, "indent") {
			data, err = json.MarshalIndent(doc, "", "  ")
		}
		if err != nil {
			t.Fatal(err)
		}
		checkLeaves(t, root, "f.json", data, doc, func(p string) Spec { return Spec{JSON: &JSON{File: "f.json", Path: p}} })
	})
}

func TestPropertyTOMLValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		// TOML holds only a table at the top.
		doc := map[string]any{"top": genDoc(t, 3, "")}
		data, err := toml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		checkLeaves(t, root, "f.toml", data, doc, func(p string) Spec { return Spec{TOML: &TOML{File: "f.toml", Path: p}} })
	})
}

func TestPropertyRegexValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		data := []byte(rapid.StringMatching(`([a-z =\t]{0,12}\n?){0,8}`).Draw(t, "text"))
		if err := os.WriteFile(filepath.Join(root, "f.txt"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		pattern := rapid.SampledFrom([]string{`([a-z]+)`, `^([a-z]*)`, `([a-z]*)$`, `= *(\w*)`}).Draw(t, "pattern")
		got := mustExtract(t, Spec{Regex: &Regex{File: "f.txt", Pattern: pattern}}, root)
		fileLines := strings.Split(string(data), "\n")
		for _, v := range got {
			checkLine(t, v, fileLines, data)
			if !strings.Contains(fileLines[v.At.Line-1], v.Text) {
				t.Fatalf("line %d is %q, which does not hold %q", v.At.Line, fileLines[v.At.Line-1], v.Text)
			}
		}
	})
}

func mustExtract(t *rapid.T, s Spec, root string) []check.Value {
	e, err := Build(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
