package json

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
)

// The JSON reader accepts nothing that encoding/json calls invalid, reads
// the values encoding/json decodes, and refuses valid JSON only for a
// repeated key or for nesting too deep.
func FuzzJSON(f *testing.F) {
	f.Add([]byte(`{"a": [1, 2e3, {"b": null}], "c": "é"}`))
	f.Add([]byte(`{"a": 1, "a": 2}`))
	f.Add([]byte(" 1 "))
	f.Add([]byte("[true, false, null, \"\\u00e9\\ud800\", -0.0e+1]"))
	f.Fuzz(func(t *testing.T, data []byte) {
		tree, err := Parse(data)
		valid := json.Valid(data)
		if err != nil {
			if valid && !strings.Contains(err.Error(), "is repeated") && !strings.Contains(err.Error(), "levels deep") {
				t.Fatalf("valid JSON refused: %v", err)
			}
			return
		}
		if !valid {
			t.Fatal("invalid JSON accepted")
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var want any
		if err := dec.Decode(&want); err != nil {
			t.Fatal(err)
		}
		if diff := coretest.CompareDecoded("$", tree, want); diff != "" {
			t.Fatalf("the reader reads values encoding/json does not: %s", diff)
		}
		coretest.WalkLines(t, tree, data)
		if n, bound := coretest.TreeNodes(tree, map[*core.Node]bool{}), core.JSONFormat.CountNodes(data); n > bound {
			t.Fatalf("%d nodes, over the bound of %d", n, bound)
		}
	})
}
