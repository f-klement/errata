package toml

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// The TOML reader accepts a file exactly when go-toml decodes it, except for
// a file nested deeper than maxDepth.
func FuzzTOML(f *testing.F) {
	f.Add([]byte(extracttest.RunnerConfig))
	f.Add([]byte("a.b = [1, {c = 'd'}]\n[[e]]\n[e.f]\ng = 1979-05-27\n"))
	f.Add([]byte("[a.b]\n[a]\nb.c = 1\n"))
	f.Add([]byte("[[a]]\n[a.b]\n[[a]]\n[a.b]\nx = 0x7fffffffffffffff\n"))
	f.Add([]byte("x = 1979-05-27T07:32:00+23:59\ny = 07:32:00.5\nz = -1e308\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		tree, err := Parse(data)
		var decoded map[string]any
		derr := toml.Unmarshal(data, &decoded)
		if err != nil {
			if derr == nil && !strings.Contains(err.Error(), "levels deep") {
				t.Fatalf("TOML that decodes refused: %v", err)
			}
			return
		}
		if derr != nil {
			t.Fatalf("TOML that does not decode accepted: %v", derr)
		}
		if diff := coretest.CompareDecoded("$", tree, decoded); diff != "" {
			t.Fatalf("the reader reads values go-toml does not: %s", diff)
		}
		coretest.WalkLines(t, tree, data)
		// The node limit's count bounds the tree and go-toml's nodes for
		// the largest expression, which is all the parser holds at once.
		most := 0
		var p unstable.Parser
		p.Reset(data)
		for p.NextExpression() {
			most = max(most, exprNodes(p.Expression()))
		}
		if n, bound := coretest.TreeNodes(tree, map[*core.Node]bool{})+most, core.TOMLFormat.CountNodes(data); n > bound {
			t.Fatalf("%d nodes, over the bound of %d", n, bound)
		}
	})
}

// exprNodes counts the nodes of one go-toml expression.
func exprNodes(n *unstable.Node) int {
	c := 1
	it := n.Children()
	for it.Next() {
		c += exprNodes(it.Node())
	}
	return c
}
