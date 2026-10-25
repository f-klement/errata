package jenkins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/extracttest"
)

// A generated Jenkinsfile of env.X = and def X = assignments of a few
// variables, in every string form, among lines that only look like them (a
// comment, a string, a comment or a string over several lines), and
// perhaps a pipeline's environment block at its end: every value's line
// lies inside the file, and each variable's values are the ones written,
// at the lines they were written on, in order. The environment block's
// value is the only one of its variable, and a def X = counts only when no
// env.X = sets X.
func TestPropertyJenkinsValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		names := []string{"A", "B", "C_1"}
		var w extracttest.LineWriter
		want := map[string][]string{}
		envSets := map[string]bool{}
		for range rapid.IntRange(1, 12).Draw(t, "n") {
			name := rapid.SampledFrom(names).Draw(t, "name")
			noise := rapid.StringMatching(`[a-z0-9 ]{0,8}`).Draw(t, "noise")
			switch rapid.IntRange(0, 4).Draw(t, "kind") {
			case 0:
				w.Add("// env.%s = '%s'", name, noise)
			case 1:
				w.Add("echo \"env.%s = '%s'\"", name, noise)
			case 2:
				w.Add("/* def %s = '%s'\n */", name, noise)
				w.Line++
			case 3:
				w.Add("def other = '''\nenv.%s = '%s'\n'''", name, noise)
				w.Line += 2
			}
			value := rapid.StringMatching(`[a-z0-9 .:/=#{}*-]{0,10}`).Draw(t, "value")
			form := rapid.SampledFrom([]string{"env.%s = '%s'", "def %s = '%s'", "env.%s = \"%s\"", "String %s = '%s'",
				"script { env.%s = '%s' }", "env.%s = '''%s'''", "env.%s = \"\"\"%s\"\"\""}).Draw(t, "form")
			// A line break inside a triple quoted value, which moves every
			// line after it.
			if strings.Contains(form, "''%s''") || strings.Contains(form, "\"\"%s\"\"") {
				if rapid.Bool().Draw(t, "break") {
					value += "\nmore"
				}
			}
			line := w.Add(form, name, value)
			w.Line += strings.Count(value, "\n")
			isEnv := strings.Contains(form, "env.")
			if isEnv && !envSets[name] {
				// An env.X = drops the def X = before it.
				envSets[name] = true
				want[name] = nil
			}
			if isEnv || !envSets[name] {
				want[name] = append(want[name], fmt.Sprintf("%s@%d", value, line))
			}
		}
		if rapid.Bool().Draw(t, "environment") {
			name := rapid.SampledFrom(names).Draw(t, "env name")
			value := rapid.StringMatching(`[a-z0-9 .:/=#{}*-]{0,10}`).Draw(t, "env value")
			w.Add("pipeline {")
			w.Add("  agent any")
			w.Add("  environment {")
			if rapid.Bool().Draw(t, "env noise") {
				w.Add("    // %s = 'comment'", name)
			}
			line := w.Add("    %s = '%s'", name, value)
			w.Add("  }")
			w.Add("}")
			want[name] = []string{fmt.Sprintf("%s@%d", value, line)}
		}
		data := w.Text.String()
		if err := os.WriteFile(filepath.Join(root, "Jenkinsfile"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		fileLines := strings.Split(data, "\n")
		for _, name := range names {
			if len(want[name]) == 0 {
				continue
			}
			got := extracttest.MustExtract(t, Build, Spec{File: "Jenkinsfile", Variable: name}, root)
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
