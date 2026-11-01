package toolversion

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// What a walk for node's pins costs over 1000 directories, each with an
// .nvmrc: a Resolved and a read for each pin.
func BenchmarkToolVersionWalk(b *testing.B) {
	root := b.TempDir()
	for i := range 1000 {
		dir := filepath.Join(root, "p", strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".nvmrc"), []byte("20.11.1\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	x, err := build(Spec{Tool: "node"})
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if vs, err := x.Extract(b.Context(), root); err != nil || len(vs) != 1000 {
			b.Fatalf("%d values, %v", len(vs), err)
		}
	}
}
