package extracttest

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
)

// GenDoc draws a document: mappings and sequences of plain strings. A key may
// hold a dot or a dash, so leaf paths need the bracket form.
func GenDoc(t *rapid.T, depth int, label string) any {
	if depth == 0 || rapid.IntRange(0, 2).Draw(t, label+"kind") == 0 {
		return rapid.StringMatching(`[a-z0-9][a-z0-9._:-]{0,8}`).Draw(t, label+"leaf")
	}
	n := rapid.IntRange(0, 4).Draw(t, label+"n")
	if rapid.Bool().Draw(t, label+"list") {
		list := make([]any, n)
		for i := range list {
			list[i] = GenDoc(t, depth-1, label+strconv.Itoa(i))
		}
		return list
	}
	m := map[string]any{}
	for i := range n {
		m[rapid.StringMatching(`[a-z][a-z.-]{0,5}`).Draw(t, label+"key")] = GenDoc(t, depth-1, label+strconv.Itoa(i))
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

// CheckLeaves extracts every leaf of doc from the file at name, with the
// extractor build makes of spec(path), and checks that it comes back once,
// with its text, at a line of the file that holds that text.
func CheckLeaves[S any](t *rapid.T, build func(S) (check.Extractor, error), root, name string, data []byte, doc any, spec func(path string) S) {
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileLines := strings.Split(string(data), "\n")
	want := map[string]string{}
	leaves(doc, "$", want)
	for path, text := range want {
		e, err := build(spec(path))
		if err != nil {
			t.Fatalf("Build %s: %v", path, err)
		}
		got, err := e.Extract(context.Background(), root)
		if err != nil {
			t.Fatalf("%s: %v\n%s", path, err, data)
		}
		if len(got) != 1 || got[0].Text != text {
			t.Fatalf("%s: got %v, want %q\n%s", path, got, text, data)
		}
		CheckLine(t, got[0], fileLines, data)
		if !strings.Contains(fileLines[got[0].At.Line-1], text) {
			t.Fatalf("%s: line %d is %q, which does not hold %q\n%s", path, got[0].At.Line, fileLines[got[0].At.Line-1], text, data)
		}
	}
}

// CheckLine fails unless v sits at one of the file's lines.
func CheckLine(t *rapid.T, v check.Value, fileLines []string, data []byte) {
	if v.At.Line < 1 || v.At.Line > len(fileLines) {
		t.Fatalf("value %q at line %d, outside the file's %d lines\n%s", v.Text, v.At.Line, len(fileLines), data)
	}
}

// MustExtract builds s with build and extracts from root, failing t on an
// error.
func MustExtract[S any](t *rapid.T, build func(S) (check.Extractor, error), s S, root string) []check.Value {
	e, err := build(s)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.Extract(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
