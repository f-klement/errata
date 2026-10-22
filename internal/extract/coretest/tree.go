// Package coretest holds the checks the tests of several readers make of
// core's types: of a tree of core.Node, where its nodes start and whether
// it holds what encoding/json or go-toml decodes from the same file, and of
// an entry reader, what its work meter charges. Only test files import it.
// It is apart from extracttest because it imports core, whose own tests
// import extracttest.
package coretest

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/f-klement/errata/internal/extract/core"
)

// WalkLines fails if any node of the tree from n starts outside data's lines.
// The tree may share a node through a YAML alias, so each is visited once.
func WalkLines(t *testing.T, n *core.Node, data []byte) {
	t.Helper()
	count := strings.Count(string(data), "\n") + 1
	seen := map[*core.Node]bool{}
	stack := []*core.Node{n}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		seen[n] = true
		if n.Line < 1 || n.Line > count {
			t.Fatalf("node %q at line %d, outside the file's %d lines", n.Text, n.Line, count)
		}
		for _, k := range n.Keys {
			stack = append(stack, n.Field[k])
		}
		stack = append(stack, n.Items...)
	}
}

// CompareDecoded describes where the tree n differs from v, what
// encoding/json or go-toml decodes, or returns "" when they agree. A string
// is the scalar's text; a JSON number is its text too. Any other scalar the
// decoder made, a TOML number, date or bool, or a JSON bool or null, is what
// the scalar's text decodes to, alone, as a TOML value or as JSON.
func CompareDecoded(at string, n *core.Node, v any) string {
	switch v := v.(type) {
	case map[string]any:
		if n.Shape != core.Mapping || len(n.Keys) != len(v) {
			return fmt.Sprintf("%s: %s of %d keys, the decoder's mapping of %d", at, n.Shape, len(n.Keys), len(v))
		}
		for _, k := range n.Keys {
			want, ok := v[k]
			if !ok {
				return fmt.Sprintf("%s: key %q, which the decoder does not have", at, k)
			}
			if diff := CompareDecoded(at+"["+strconv.Quote(k)+"]", n.Field[k], want); diff != "" {
				return diff
			}
		}
		return ""
	case []any:
		if n.Shape != core.Sequence || len(n.Items) != len(v) {
			return fmt.Sprintf("%s: %s of %d items, the decoder's sequence of %d", at, n.Shape, len(n.Items), len(v))
		}
		for i, item := range n.Items {
			if diff := CompareDecoded(fmt.Sprintf("%s[%d]", at, i), item, v[i]); diff != "" {
				return diff
			}
		}
		return ""
	}
	if n.Shape != core.Scalar {
		return fmt.Sprintf("%s: %s, the decoder's scalar %v", at, n.Shape, v)
	}
	switch v := v.(type) {
	case string:
		if n.Text != v {
			return fmt.Sprintf("%s: %q, the decoder's %q", at, n.Text, v)
		}
	case json.Number:
		if n.Text != string(v) {
			return fmt.Sprintf("%s: %q, the decoder's %q", at, n.Text, v)
		}
	case nil, bool:
		var alone any
		if err := json.Unmarshal([]byte(n.Text), &alone); err != nil || alone != v {
			return fmt.Sprintf("%s: %q, the decoder's %v", at, n.Text, v)
		}
	default:
		var alone map[string]any
		if err := toml.Unmarshal([]byte("v = "+n.Text), &alone); err != nil || !sameTOML(alone["v"], v) {
			return fmt.Sprintf("%s: %q, the decoder's %v", at, n.Text, v)
		}
	}
	return ""
}

// sameTOML reports whether two decoded TOML scalars are equal, NaN to NaN.
func sameTOML(a, b any) bool {
	if fa, ok := a.(float64); ok {
		if fb, ok := b.(float64); ok && math.IsNaN(fa) && math.IsNaN(fb) {
			return true
		}
	}
	return reflect.DeepEqual(a, b)
}

// TreeNodes counts the nodes of a tree, a node shared by aliases once.
func TreeNodes(n *core.Node, seen map[*core.Node]bool) int {
	if seen[n] {
		return 0
	}
	seen[n] = true
	c := 1
	for _, k := range n.Keys {
		c += TreeNodes(n.Field[k], seen)
	}
	for _, item := range n.Items {
		c += TreeNodes(item, seen)
	}
	return c
}
