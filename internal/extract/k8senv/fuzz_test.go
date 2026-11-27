package k8senv

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Any manifest, YAML or JSON, reads without a crash into values each at a
// line of the file, for any variable and container the spec loads with.
// The seeds hold each key the reader reads.
func FuzzK8sEnv(f *testing.F) {
	f.Add(deployment, "PORT", "", false)
	f.Add("kind: List\nitems:\n- kind: Pod\n  spec:\n    containers:\n    - name: a\n      envFrom: [{prefix: P_, secretRef: {name: s}}, x]\n      env: [{name: P_A, valueFrom: {configMapKeyRef: {name: c, key: k}}}, {name: P_A, value: |\n          $(B)\n        }]\n", "P_A", "a", false)
	f.Add("x: &e {name: A, value: yes}\nkind: Pod\nspec: {containers: [{name: c, env: [*e, *e]}]}\n---\nkind: Pod\n", "A", "c", false)
	f.Add(`{"kind": "Pod", "spec": {"containers": [{"name": "c", "env": [{"name": "A", "value": "$$"}]}]}}`, "A", "", true)
	f.Fuzz(func(t *testing.T, data, variable, container string, json bool) {
		s := Spec{OneFile: core.OneFile{File: "m.yaml", MaxBytes: 1 << 16}, Variable: variable, Container: container}
		if json {
			s.File = "m.json"
		}
		x, err := build(s)
		if err != nil || len(data) > 1<<16 {
			return
		}
		vs, err := x.(extractor).fromData([]byte(data))
		if err != nil {
			return
		}
		last := core.LastLine([]byte(data))
		for _, v := range vs {
			if v.At.File != s.File || v.At.Line < 1 || v.At.Line > last || v.Origin == "" {
				t.Fatalf("%q at %s:%d of %d lines, from %q", v.Text, v.At.File, v.At.Line, last, v.Origin)
			}
		}
	})
}

// A generated manifest of Deployments and Pods, each container with env:
// entries of a few names, quoted values, some of them $(NAME), and envFrom
// sources: every value's line lies inside the file, a literal one's holds its
// text, and each container gives the last entry of A at the line it was
// written on, or, with no entry, one value for each envFrom source.
func TestPropertyValuesSitAtTheirLine(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var w extracttest.LineWriter
		var want []string
		for d := range rapid.IntRange(1, 3).Draw(t, "objects") {
			if d > 0 {
				w.Add("---")
			}
			pad := "  "
			if rapid.Bool().Draw(t, "pod") {
				w.Add("kind: Pod")
				w.Add("spec:")
				w.Add("  containers:")
			} else {
				w.Add("kind: Deployment")
				w.Add("spec:")
				w.Add("  template:")
				w.Add("    spec:")
				w.Add("      containers:")
				pad = "      "
			}
			for c := range rapid.IntRange(0, 3).Draw(t, "containers") {
				want = append(want, container(t, &w, pad, fmt.Sprintf("c%d", c))...)
			}
		}
		data := []byte(w.Text.String())
		x, err := build(spec("A"))
		if err != nil {
			t.Fatal(err)
		}
		vs, err := x.(extractor).fromData(data)
		if len(want) == 0 {
			if err == nil {
				t.Fatalf("no value set, and %d values read", len(vs))
			}
			return
		}
		if err != nil {
			t.Fatalf("%v\n%s", err, data)
		}
		lines := strings.Split(string(data), "\n")
		var got []string
		for _, v := range vs {
			extracttest.CheckLine(t, v, lines, data)
			if !v.NotLiteral && !strings.Contains(lines[v.At.Line-1], v.Text) {
				t.Fatalf("%q at line %d, %q", v.Text, v.At.Line, lines[v.At.Line-1])
			}
			got = append(got, fmt.Sprintf("%s@%d", v.Text, v.At.Line))
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("got  %v\nwant %v\n%s", got, want, data)
		}
	})
}

// container writes a container named name, with env: entries and envFrom
// sources, and returns the values it gives of A as text@line.
func container(t *rapid.T, w *extracttest.LineWriter, pad, name string) []string {
	w.Add("%s- name: %s", pad, name)
	var from []string
	if rapid.Bool().Draw(t, "envFrom") {
		w.Add("%s  envFrom:", pad)
		for range rapid.IntRange(1, 2).Draw(t, "sources") {
			cm := rapid.SampledFrom([]string{"cm", "other"}).Draw(t, "source")
			from = append(from, fmt.Sprintf("configMapRef %s@%d", cm, w.Add("%s  - configMapRef: {name: %s}", pad, cm)))
		}
	}
	var last string
	if n := rapid.IntRange(0, 4).Draw(t, "entries"); n > 0 {
		w.Add("%s  env:", pad)
		for range n {
			variable := rapid.SampledFrom([]string{"A", "B", "AA"}).Draw(t, "name")
			value := rapid.StringMatching(`[a-z0-9 :/$()]{0,8}`).Draw(t, "value")
			w.Add("%s  - name: %s", pad, variable)
			line := w.Add("%s    value: '%s'", pad, value)
			if variable == "A" {
				last = fmt.Sprintf("%s@%d", value, line)
			}
		}
	}
	if last != "" {
		return []string{last}
	}
	return from
}
