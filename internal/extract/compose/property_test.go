package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// layer is one file of the project the property draws: its name, its
// text, and the line each variable it sets stands at.
type layer struct {
	name string
	text strings.Builder
	line int
	at   map[string]int
}

func (l *layer) add(format string, args ...any) int {
	l.line++
	fmt.Fprintf(&l.text, format+"\n", args...)
	return l.line
}

// Each variable of a service's environment stands at the line of the last
// file compose merges that sets it, whatever the files set: the override
// over the main file, the main file over the service it extends, each in
// the map or the list form. And every value lies inside the file it names.
func TestPropertyVariableAtTheLastFileThatSetsIt(tt *testing.T) {
	root := tt.TempDir()
	rapid.Check(tt, func(t *rapid.T) {
		names := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Z][A-Z0-9_]{0,4}`), 1, 6, rapid.ID[string]).Draw(t, "names")
		base := &layer{name: "base.yaml", at: map[string]int{}}
		main := &layer{name: "compose.yaml", at: map[string]int{}}
		over := &layer{name: "compose.override.yaml", at: map[string]int{}}
		base.add("services:\n  base:\n    image: b")
		main.add("services:\n  web:\n    extends: {file: base.yaml, service: base}")
		over.add("services:\n  web:")
		base.line, main.line, over.line = 3, 3, 2
		for _, l := range []*layer{base, main, over} {
			drawEnvironment(t, l, names)
		}
		for _, l := range []*layer{base, main, over} {
			if err := os.WriteFile(filepath.Join(root, l.name), []byte(l.text.String()), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		x, err := Build(Spec{OneFile: oneFile("compose.yaml"), Overrides: []string{"compose.override.yaml"}, Service: "web", Key: "environment"}, yamlParse)
		if err != nil {
			t.Fatal(err)
		}
		vs, err := x.Extract(tt.Context(), root)
		if err != nil {
			t.Fatal(err)
		}
		insideFiles(t, vs, map[string]string{base.name: base.text.String(), main.name: main.text.String(), over.name: over.text.String()})
		for _, v := range vs {
			want := winner(strings.TrimPrefix(v.Key, "environment."), base, main, over)
			if got := fmt.Sprintf("%s:%d", v.At.File, v.At.Line); got != want {
				t.Fatalf("%s at %s, want %s\n%s---\n%s---\n%s", v.Key, got, want, base.text.String(), main.text.String(), over.text.String())
			}
		}
	})
}

// drawEnvironment writes, at the end of l, an environment: of some of
// names, in the map form or the list form.
func drawEnvironment(t *rapid.T, l *layer, names []string) {
	set := rapid.SliceOfDistinct(rapid.SampledFrom(names), rapid.ID[string]).Draw(t, l.name)
	if len(set) == 0 {
		return
	}
	list := rapid.Bool().Draw(t, l.name+" list")
	l.add("    environment:")
	for _, n := range set {
		if list {
			l.at[n] = l.add("      - %s=%s", n, l.name)
		} else {
			l.at[n] = l.add("      %s: %q", n, l.name)
		}
	}
}

// winner is the place of the last layer that sets name.
func winner(name string, layers ...*layer) string {
	for i := len(layers) - 1; i >= 0; i-- {
		if line, ok := layers[i].at[name]; ok {
			return fmt.Sprintf("%s:%d", layers[i].name, line)
		}
	}
	return "nowhere"
}

// insideFiles fails t unless each value names one of files, at a line
// inside it.
func insideFiles(t interface{ Fatalf(string, ...any) }, vs []check.Value, files map[string]string) {
	for _, v := range vs {
		body, ok := files[v.At.File]
		if !ok || v.At.Line < 1 || v.At.Line > core.LastLine([]byte(body)) {
			t.Fatalf("%q at %s:%d, outside the files read", v.Text, v.At.File, v.At.Line)
		}
	}
}
