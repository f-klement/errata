package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A generated script of assignments and defaults of a few variables, in
// every form, among lines that only look like them (a comment, a string, a
// quoted here-document): every value's line lies inside the file, and each
// variable's values are the ones written, at the lines they were written
// on, in order.
func TestPropertyShellValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		names := []string{"A", "B", "C_1"}
		var w extracttest.LineWriter
		want := map[string][]string{}
		for range rapid.IntRange(1, 12).Draw(t, "n") {
			name := rapid.SampledFrom(names).Draw(t, "name")
			value := rapid.StringMatching(`[a-z0-9 .:/=#{}*-]{0,10}`).Draw(t, "value")
			switch rapid.IntRange(0, 3).Draw(t, "noise") {
			case 0:
				w.Add("# %s='%s'", name, value)
			case 1:
				w.Add("echo \"%s=%s\" '${%s:-x}'", name, value, name)
			case 2:
				w.Add("cat <<'EOF'\n%s=%s\nEOF", name, value)
				w.Line += 2
			}
			form := rapid.SampledFrom([]string{"%s='%s'", "export %s='%s'", "readonly %s='%s'", "declare -x %s='%s'",
				"f() { local %s='%s'; }", ": \"${%s:-%s}\"", ": ${%s:='%s'}", "%s='%s' cmd"}).Draw(t, "form")
			if strings.Contains(form, `"${`) && strings.ContainsAny(value, "{}") {
				form = ": ${%s:='%s'}"
			}
			// A line break inside the quoted value, which moves every line
			// after it.
			if strings.Contains(form, "'%s'") && rapid.Bool().Draw(t, "break") {
				value += "\nmore"
			}
			line := w.Add(form, name, value)
			w.Line += strings.Count(value, "\n")
			want[name] = append(want[name], fmt.Sprintf("%s@%d", value, line))
		}
		data := w.Text.String()
		if err := os.WriteFile(filepath.Join(root, "s.sh"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		fileLines := strings.Split(data, "\n")
		for _, name := range names {
			got := extracttest.MustExtract(t, Build, Spec{File: "s.sh", Name: name}, root)
			var texts []string
			for _, v := range got {
				extracttest.CheckLine(t, v, fileLines, []byte(data))
				if v.NotLiteral {
					t.Fatalf("%s: %q is marked not literal", name, v.Text)
				}
				texts = append(texts, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
			}
			if g, w := strings.Join(texts, " "), strings.Join(want[name], " "); g != w {
				t.Fatalf("%s: got %q, want %q\n%s", name, g, w, data)
			}
		}
	})
}
