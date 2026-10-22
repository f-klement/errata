package yaml

import (
	"testing"

	"go.yaml.in/yaml/v3"
	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

func TestPropertyYAMLValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		doc := extracttest.GenDoc(t, 3, "")
		data, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		extracttest.CheckLeaves(t, Build, root, "f.yaml", data, doc, func(p string) Spec { return Spec{File: "f.yaml", Path: p} })
	})
}
