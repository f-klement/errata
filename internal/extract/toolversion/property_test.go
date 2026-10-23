package toolversion

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// version draws a version as files write them.
var version = rapid.StringMatching(`[a-z0-9][a-z0-9.:/*_+-]{0,8}`)

// checkValues fails unless got is want, each value at a line of data that
// holds its text.
func checkValues(t *rapid.T, got []check.Value, want []string, data string) {
	lines := strings.Split(data, "\n")
	var texts []string
	for _, v := range got {
		extracttest.CheckLine(t, v, lines, []byte(data))
		if !strings.Contains(lines[v.At.Line-1], v.Text) {
			t.Fatalf("%q at line %d, which is %q\n%s", v.Text, v.At.Line, lines[v.At.Line-1], data)
		}
		texts = append(texts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
	}
	if strings.Join(texts, " ") != strings.Join(want, " ") {
		t.Fatalf("got %q, want %q\n%s", texts, want, data)
	}
}

// drawFiller writes lines that pin nothing: blank ones, comments, and in a
// .tool-versions file other tools' lines.
func drawFiller(t *rapid.T, w *extracttest.LineWriter, other bool) {
	for range rapid.IntRange(0, 2).Draw(t, "filler") {
		switch rapid.IntRange(0, 2).Draw(t, "kind") {
		case 0:
			w.Add("%s", rapid.SampledFrom([]string{"", "  ", "\t", "\r"}).Draw(t, "blank"))
		case 1:
			w.Add("%s# %s", rapid.SampledFrom([]string{"", " "}).Draw(t, "indent"), version.Draw(t, "comment"))
		default:
			if other {
				w.Add("python %s", version.Draw(t, "other"))
			}
		}
	}
}

// Every version of a .tool-versions file is a value, at the line of its
// tool, in file order, whichever spelling the line uses, whatever comments,
// blank lines, CRLF and other tools are around it.
func TestPropertyToolVersionsValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		var w extracttest.LineWriter
		var want []string
		for range rapid.IntRange(0, 6).Draw(t, "lines") {
			drawFiller(t, &w, true)
			name := rapid.SampledFrom([]string{"node", "nodejs"}).Draw(t, "name")
			vs := rapid.SliceOfN(version, 0, 3).Draw(t, "versions")
			sep := rapid.SampledFrom([]string{" ", "\t", "  "}).Draw(t, "sep")
			end := rapid.SampledFrom([]string{"", " # c", "\r", " #x y"}).Draw(t, "end")
			n := w.Add("%s%s%s%s", name, sep, strings.Join(vs, sep), end)
			for _, v := range vs {
				want = append(want, fmt.Sprintf("%s@%d", v, n))
			}
		}
		data := w.Text.String()
		writeFile(t, root, ".tool-versions", data)
		got := extracttest.MustExtract(t, build, Spec{Tool: "node", Files: []string{".tool-versions"}}, root)
		checkValues(t, got, want, data)
	})
}

// A file of one version gives the first one, at its line, past blank lines
// and comments, and a later line is no value.
func TestPropertySingleVersionFilesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		k := rapid.SampledFrom([]*fileKind{kindOf(".nvmrc"), kindOf(".ruby-version"), kindOf(".bazelversion"), kindOf("VERSION")}).Draw(t, "kind")
		var w extracttest.LineWriter
		drawFiller(t, &w, false)
		v := version.Draw(t, "version")
		n := w.Add("%s%s%s", rapid.SampledFrom([]string{"", " "}).Draw(t, "indent"), v, rapid.SampledFrom([]string{"", "\r", " # c", " other"}).Draw(t, "end"))
		drawFiller(t, &w, false)
		if rapid.Bool().Draw(t, "later") {
			w.Add("%s", version.Draw(t, "later version"))
		}
		data := w.Text.String()
		writeFile(t, root, k.name, data)
		tool := "x"
		if k.tools != nil {
			tool = k.tools[0]
		}
		got := extracttest.MustExtract(t, build, Spec{Tool: tool, Files: []string{k.name}}, root)
		checkValues(t, got, []string{fmt.Sprintf("%s@%d", v, n)}, data)
	})
}

// The go and toolchain directives of a go.mod come back at their lines,
// whatever comments, blank lines and blocks come before and between them:
// modfile's positions are exact.
func TestPropertyGoModDirectivesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		var w extracttest.LineWriter
		filler := func() {
			for range rapid.IntRange(0, 2).Draw(t, "filler") {
				switch rapid.IntRange(0, 3).Draw(t, "kind") {
				case 0:
					w.Add("")
				case 1:
					w.Add("// go 1.1 toolchain go1.1")
				case 2:
					w.Add("require (\n\texample.com/a v1.0.0 // c\n\n\texample.com/b v1.2.3\n)")
					w.Line += 4
				default:
					w.Add("exclude example.com/c v0.1.0")
				}
			}
		}
		filler()
		w.Add("module example.com/m%s", rapid.SampledFrom([]string{"", " // m", "\r"}).Draw(t, "end"))
		var want []string
		goVersion := rapid.SampledFrom([]string{"1.21", "1.22.0", "1.23rc1", "1.27.2"}).Draw(t, "go")
		toolchain := rapid.SampledFrom([]string{"", "go1.22.3", "default", "go1.27.3-custom"}).Draw(t, "toolchain")
		directives := []func(){
			func() {
				filler()
				want = append(want, fmt.Sprintf("%s@%d", goVersion, w.Add("go %s", goVersion)))
			},
		}
		if toolchain != "" {
			directives = append(directives, func() {
				filler()
				want = append(want, fmt.Sprintf("%s@%d", toolchain, w.Add("toolchain\t%s // pinned", toolchain)))
			})
		}
		if rapid.Bool().Draw(t, "toolchain first") && len(directives) == 2 {
			directives[0], directives[1] = directives[1], directives[0]
		}
		for _, d := range directives {
			d()
		}
		filler()
		data := w.Text.String()
		// The toolchain is the pin when there is one, without its go; the go
		// directive only when there is none.
		if len(want) == 2 {
			for _, w := range want {
				if !strings.HasPrefix(w, goVersion+"@") {
					want = []string{strings.TrimPrefix(w, "go")}
				}
			}
		}
		writeFile(t, root, "go.mod", data)
		got := extracttest.MustExtract(t, build, Spec{Tool: "go", Files: []string{"go.mod"}}, root)
		checkValues(t, got, want, data)
	})
}
