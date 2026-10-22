package toml

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

func TestPropertyTOMLValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		// TOML holds only a table at the top.
		doc := map[string]any{"top": extracttest.GenDoc(t, 3, "")}
		data, err := toml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		extracttest.CheckLeaves(t, Build, root, "f.toml", data, doc, func(p string) Spec { return Spec{File: "f.toml", Path: p} })
	})
}
