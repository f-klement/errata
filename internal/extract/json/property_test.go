package json

import (
	"encoding/json"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

func TestPropertyJSONValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		doc := extracttest.GenDoc(t, 3, "")
		data, err := json.Marshal(doc)
		if rapid.Bool().Draw(t, "indent") {
			data, err = json.MarshalIndent(doc, "", "  ")
		}
		if err != nil {
			t.Fatal(err)
		}
		extracttest.CheckLeaves(t, Build, root, "f.json", data, doc, func(p string) Spec { return Spec{File: "f.json", Path: p} })
	})
}
