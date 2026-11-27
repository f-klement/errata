package workload

import (
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// expand is the kubelet's expansion of the variable references in input
// (k8s.io/kubernetes third_party/forked/golang/expansion, Expand), with a
// mapping that defines every name but the empty one, which no variable
// has: the oracle Expands is held to.
func expand(input string) string {
	var buf strings.Builder
	checkpoint := 0
	for cursor := 0; cursor < len(input); cursor++ {
		if input[cursor] == '$' && cursor+1 < len(input) {
			buf.WriteString(input[checkpoint:cursor])
			read, isVar, advance := readVariableName(input[cursor+1:])
			switch {
			case isVar && read != "":
				buf.WriteString("<" + read + ">")
			case isVar:
				buf.WriteString("$()")
			default:
				buf.WriteString(read)
			}
			cursor += advance
			checkpoint = cursor + 1
		}
	}
	return buf.String() + input[checkpoint:]
}

// readVariableName is the expansion package's tryReadVariableName.
func readVariableName(input string) (string, bool, int) {
	switch input[0] {
	case '$':
		return input[0:1], false, 1
	case '(':
		for i := 1; i < len(input); i++ {
			if input[i] == ')' {
				return input[1:i], true, i + 1
			}
		}
		return "$(", false, 1
	}
	return "$" + input[0:1], false, 1
}

// Expands holds exactly where the kubelet's expansion changes the text.
// Planted break: Expands taking $() as a reference.
func FuzzExpands(f *testing.F) {
	for _, s := range []string{"", "$", "$$", "$(a)", "$()", "$(", "a$(b $$", "$()$(b)", "$x$$"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := Expands(s), expand(s) != s; got != want {
			t.Fatalf("Expands(%q) = %v, the kubelet changes it: %v", s, got, want)
		}
	})
}

// Every container Of finds in a document is a mapping of one of the
// groups of its pod spec, named by its name, at a line inside the file.
// Planted break: Of keeping a scalar item.
func FuzzOf(f *testing.F) {
	for _, s := range []string{
		"kind: Pod\nspec:\n  containers:\n  - name: a\n  - x\n",
		"apiVersion: batch/v1\nkind: CronJob\nspec:\n  jobTemplate:\n    spec:\n      template:\n        spec:\n          initContainers: [{name: i}]\n",
		"kind: Deployment\nspec:\n  template:\n    spec:\n      ephemeralContainers:\n      - &a {name: e}\n      - *a\n",
		"kind: PodTemplate\napiVersion: v1\ntemplate: {spec: {containers: [{name: [x]}, 3]}}\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		docs, err := yaml.Parse([]byte(s))
		if err != nil {
			return
		}
		last := core.LastLine([]byte(s))
		for _, d := range docs {
			spec := PodSpec(d)
			for _, c := range Of(d) {
				if c.Node.Shape != core.Mapping || c.Line < 1 || c.Line > last || c.Line != c.Node.Line {
					t.Fatalf("%q: a container at line %d of %d, of shape %v", s, c.Line, last, c.Node.Shape)
				}
				if spec == nil || spec.Field[c.Group.Key()] == nil {
					t.Fatalf("%q: a container of %s, which the pod spec does not hold", s, c.Group.Key())
				}
				name := c.Node.Field["name"]
				if (name != nil && name.Shape == core.Scalar && name.Text != c.Name) || (c.Name != "" && name == nil) {
					t.Fatalf("%q: the container named %q", s, c.Name)
				}
			}
		}
	})
}
