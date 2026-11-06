package jsonc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
)

// JSON is JSONC and JSONC is JSON5: each dialect reads the values
// encoding/json decodes from a file it calls valid, refusing it only for a
// repeated key or for nesting too deep, and JSON5 reads every file JSONC
// reads into the same tree. Every node of a tree either reads starts at a
// line of the file, and the tree holds no more nodes than the node limit
// counts.
func FuzzJSONC(f *testing.F) {
	f.Add([]byte(`{"a": [1, 2e3, {"b": null}], "c": "é", /* x */}`))
	f.Add([]byte("// c\n{a: 'x', b: 0x1F, c: .5, d: +Infinity, \\u0061e: 1,}"))
	f.Add([]byte(`{"a": 1, "a": 2}`))
	f.Add([]byte(" 1 // end"))
	f.Add([]byte("['a\\\nb', \"\\x41\\0\", NaN]"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, errC := ParseJSONC(data)
		five, err5 := ParseJSON5(data)
		if json.Valid(data) {
			for name, err := range map[string]error{"jsonc": errC, "json5": err5} {
				if err != nil && !strings.Contains(err.Error(), "is repeated") && !strings.Contains(err.Error(), "levels deep") {
					t.Fatalf("%s refused valid JSON: %v", name, err)
				}
			}
			if errC == nil {
				dec := json.NewDecoder(bytes.NewReader(data))
				dec.UseNumber()
				var want any
				if err := dec.Decode(&want); err != nil {
					t.Fatal(err)
				}
				if diff := coretest.CompareDecoded("$", c, want); diff != "" {
					t.Fatalf("jsonc reads values encoding/json does not: %s", diff)
				}
			}
		}
		if errC == nil {
			if err5 != nil {
				t.Fatalf("json5 refused what jsonc reads: %v", err5)
			}
			if err5 == nil {
				if diff := sameTree("$", c, five); diff != "" {
					t.Fatalf("json5 and jsonc read different trees: %s", diff)
				}
			}
		}
		for _, tree := range []*core.Node{c, five} {
			if tree == nil {
				continue
			}
			coretest.WalkLines(t, tree, data)
			if n, bound := coretest.TreeNodes(tree, map[*core.Node]bool{}), core.JSONFormat.CountNodes(data); n > bound {
				t.Fatalf("%d nodes, over the bound of %d", n, bound)
			}
		}
	})
}

// sameTree describes where a and b differ, or returns "" when they agree.
func sameTree(at string, a, b *core.Node) string {
	if a.Shape != b.Shape || a.Type != b.Type || a.Text != b.Text || a.Line != b.Line || len(a.Keys) != len(b.Keys) || len(a.Items) != len(b.Items) {
		return fmt.Sprintf("%s: %+v and %+v", at, *a, *b)
	}
	for i, k := range a.Keys {
		if k != b.Keys[i] {
			return fmt.Sprintf("%s: key %q and %q", at, k, b.Keys[i])
		}
		if d := sameTree(at+"."+k, a.Vals[i], b.Vals[i]); d != "" {
			return d
		}
	}
	for i := range a.Items {
		if d := sameTree(fmt.Sprintf("%s[%d]", at, i), a.Items[i], b.Items[i]); d != "" {
			return d
		}
	}
	return ""
}
