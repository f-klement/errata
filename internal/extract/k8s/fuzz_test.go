package k8s

import (
	"testing"

	goyaml "go.yaml.in/yaml/v3"
	"pgregory.net/rapid"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
)

// Any manifest reads without a crash, into values each at a line of the
// file, for any selector and path the spec loads with, in YAML and JSON.
// The seeds hold a List, a workload with each group, ports, volumes and
// mounts, and an alias. Planted break: a value's line taken from the
// object rather than the node.
func FuzzK8s(f *testing.F) {
	seeds := []struct{ body, kind, container, port, volume, path string }{
		{"kind: List\nitems:\n- kind: Pod\n  spec: {containers: [{name: a, image: x}]}\n", "Pod", "a", "", "", "image"},
		{"kind: Deployment\nspec:\n  template:\n    spec:\n      initContainers: [{name: i, ports: [{name: p, containerPort: 1}]}]\n", "Deployment", "i", "p", "", "containerPort"},
		{"kind: CronJob\napiVersion: batch/v1\nspec: {jobTemplate: {spec: {template: {spec: {volumes: [{name: v, x: 1}]}}}}}\n", "CronJob", "", "", "v", "x"},
		{"x: &a {name: c, command: [\"$(A)\"]}\nkind: Pod\nspec: {containers: [*a]}\n---\nkind: Pod\n", "Pod", "c", "", "", "command[*]"},
		{"{\"kind\": \"Pod\", \"metadata\": {\"namespace\": \"n\"}}", "Pod", "", "", "", "metadata.namespace"},
	}
	for _, s := range seeds {
		f.Add(s.body, s.kind, s.container, s.port, s.volume, s.path, false)
	}
	f.Fuzz(func(t *testing.T, body, kind, container, port, volume, path string, json bool) {
		file := "m.yaml"
		if json {
			file = "m.json"
		}
		s := Spec{Where: core.Where{File: file, MaxBytes: 1 << 16}, Object: Object{Kind: kind}, Container: container, Port: port, Volume: volume, Path: path}
		x, err := build(s)
		if err != nil || int64(len(body)) > s.MaxBytes {
			return
		}
		got, err := x.(core.FileReader).FromShared([]byte(body), &core.Spent{})
		if err != nil {
			return
		}
		lines := core.LastLine([]byte(body))
		for _, v := range got {
			if v.At.File != file || v.At.Line < 1 || v.At.Line > lines {
				t.Fatalf("%q at %s:%d, outside the file", v.Text, v.At.File, v.At.Line)
			}
		}
	})
}

// Every scalar of an object, and of a container selected by name, comes
// back once, at a line of the file that holds its text.
func TestPropertyK8sValuesSitAtTheirLine(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		inner := extracttest.GenDoc(t, 3, "")
		obj := map[string]any{"kind": "Pod", "x": inner}
		data, err := goyaml.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		extracttest.CheckLeaves(t, build, root, "o.yaml", data, obj, func(p string) Spec {
			return Spec{Where: core.Where{File: "o.yaml"}, Object: Object{Kind: "Pod"}, Path: p}
		})
		c := map[string]any{"name": "c", "x": inner}
		data, err = goyaml.Marshal(map[string]any{"kind": "Pod", "spec": map[string]any{"containers": []any{c}}})
		if err != nil {
			t.Fatal(err)
		}
		extracttest.CheckLeaves(t, build, root, "c.yaml", data, c, func(p string) Spec {
			return Spec{Where: core.Where{File: "c.yaml"}, Object: Object{Kind: "Pod"}, Container: "c", Path: p}
		})
	})
}
