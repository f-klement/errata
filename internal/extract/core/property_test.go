package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

func TestPropertyRegexValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		data := []byte(rapid.StringMatching(`([a-z =\t]{0,12}\n?){0,8}`).Draw(t, "text"))
		if err := os.WriteFile(filepath.Join(root, "f.txt"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		pattern := rapid.SampledFrom([]string{`([a-z]+)`, `^([a-z]*)`, `([a-z]*)$`, `= *(\w*)`}).Draw(t, "pattern")
		got := extracttest.MustExtract(t, func(p string) (check.Extractor, error) { return CompileSearchAt("regex", sourceOf("f.txt"), p) }, pattern, root)
		fileLines := strings.Split(string(data), "\n")
		for _, v := range got {
			extracttest.CheckLine(t, v, fileLines, data)
			if !strings.Contains(fileLines[v.At.Line-1], v.Text) {
				t.Fatalf("line %d is %q, which does not hold %q", v.At.Line, fileLines[v.At.Line-1], v.Text)
			}
		}
	})
}
