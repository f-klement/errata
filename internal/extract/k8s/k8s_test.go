package k8s

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	ejson "github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// parsers are the yaml and json readers' parsers, as the facade hands them.
var parsers = core.Parsers{
	YAML: func(data []byte, expanded int) ([]*core.Node, int, error) {
		return yaml.ParseNoted(data, expanded, nil)
	},
	JSON: ejson.Parse,
}

// build is Build with those parsers.
func build(s Spec) (check.Extractor, error) { return Build(s, parsers) }

// values lists the values as "text@line", with a ! after a not literal
// value and the file in brackets after one not in m.yaml.
func values(vs []check.Value) string {
	var out []string
	for _, v := range vs {
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		if v.At.File != "m.yaml" {
			mark += "(" + v.At.File + ")"
		}
		out = append(out, fmt.Sprintf("%s%s@%d", v.Text, mark, v.At.Line))
	}
	return strings.Join(out, " ")
}

// run reads s, with file m.yaml when it names none, in a repository of
// files, and fails t on an error.
func run(t *testing.T, files map[string]string, s Spec) string {
	t.Helper()
	if s.File == "" && s.Files == nil {
		s.File = "m.yaml"
	}
	return values(extracttest.MustRun(t, build, s, extracttest.Tracked(t, files)))
}

// manifests is a multi document file of two Deployments of one name in two
// namespaces, one of them with none written, a Service, and a List.
const manifests = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  replicas: 3
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  replicas: 1
---
apiVersion: v1
kind: Service
metadata: {name: api, namespace: prod}
spec:
  replicas: svc
---
apiVersion: v1
kind: List
items:
- apiVersion: extensions/v1beta1
  kind: Deployment
  metadata: {name: web, namespace: prod}
  spec: {replicas: 5}
`

// Objects are selected by kind, apiVersion, namespace and name across the
// documents of a file and the items of a List; a namespace left out
// matches every one, and default matches an object that writes none. Two
// objects one selector matches both return values. Planted breaks: the
// List's items not read, the default namespace dropped, apiVersion not
// compared, name not compared.
func TestObjectsAreSelectedByTheirFields(t *testing.T) {
	files := map[string]string{"m.yaml": manifests}
	for _, c := range []struct {
		obj  Object
		want string
	}{
		{Object{Kind: "Deployment", Name: "api"}, "3@7 1@14"},
		{Object{Kind: "Deployment", Name: "api", Namespace: "prod"}, "3@7"},
		{Object{Kind: "Deployment", Name: "api", Namespace: "default"}, "1@14"},
		{Object{Kind: "Deployment"}, "3@7 1@14 5@28"},
		{Object{Kind: "Deployment", APIVersion: "extensions/v1beta1"}, "5@28"},
		{Object{Kind: "Deployment", Namespace: "prod"}, "3@7 5@28"},
		{Object{Kind: "Service", Name: "api"}, "svc@20"},
		{Object{Kind: "List"}, ""},
	} {
		s := Spec{Object: c.obj, Path: "spec.replicas"}
		if c.obj.Kind == "List" {
			s.Path = "kind"
			c.want = "List@23"
		}
		if got := run(t, files, s); got != c.want {
			t.Errorf("%s: got %q, want %q", c.obj, got, c.want)
		}
	}
}

// files: reads every manifest a glob names, YAML and JSON alike, and a file
// with no object the selector matches adds none. Planted breaks: a .json
// file parsed as YAML, which refuses JSON's escape \/, a file
// of a set with no match refused.
func TestObjectsAcrossFilesAndJSON(t *testing.T) {
	files := map[string]string{
		"deploy/a.yaml":  "kind: Deployment\nmetadata: {name: a}\nspec: {replicas: 2}\n",
		"deploy/b.json":  "{\n\t\"kind\": \"Deployment\",\n\t\"metadata\": {\"name\": \"b\", \"annotations\": {\"u\": \"a\\/b\"}},\n\t\"spec\": {\"replicas\": 4}\n}\n",
		"deploy/c.yaml":  "kind: ConfigMap\nmetadata: {name: c}\n",
		"deploy/d.yml":   "kind: Deployment\nmetadata: {name: d}\nspec: {replicas: 6}\n",
		"other/e.yaml":   "kind: Deployment\nspec: {replicas: 9}\n",
		"deploy/list.js": "not read",
	}
	got := run(t, files, Spec{Where: core.Where{Files: []string{"deploy/*.yaml", "deploy/*.json", "deploy/*.yml"}}, Object: Object{Kind: "Deployment"}, Path: "spec.replicas"})
	if want := "2(deploy/a.yaml)@3 4(deploy/b.json)@4 6(deploy/d.yml)@3"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// workloads are a manifest of each built in workload kind, with the line
// its pod spec's first key stands on, given its pod spec indented by pad.
var workloads = []struct{ kind, head, pad string }{
	{"Pod", "apiVersion: v1\nkind: Pod\nmetadata: {name: w}\nspec:", "  "},
	{"Deployment", "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: w}\nspec:\n  template:\n    spec:", "      "},
	{"StatefulSet", "apiVersion: apps/v1\nkind: StatefulSet\nmetadata: {name: w}\nspec:\n  template:\n    spec:", "      "},
	{"DaemonSet", "apiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: w}\nspec:\n  template:\n    spec:", "      "},
	{"ReplicaSet", "apiVersion: apps/v1\nkind: ReplicaSet\nmetadata: {name: w}\nspec:\n  template:\n    spec:", "      "},
	{"Job", "apiVersion: batch/v1\nkind: Job\nmetadata: {name: w}\nspec:\n  template:\n    spec:", "      "},
	{"CronJob", "apiVersion: batch/v1\nkind: CronJob\nmetadata: {name: w}\nspec:\n  jobTemplate:\n    spec:\n      template:\n        spec:", "          "},
	{"Rollout", "apiVersion: argoproj.io/v1alpha1\nkind: Rollout\nmetadata: {name: w}\nspec:\n  template:\n    spec:", "      "},
}

// podSpec is a pod spec with an init container, two containers with ports
// and mounts, and two volumes, its lines indented by pad.
const podSpec = `
  initContainers:
  - name: init
    image: busybox:1.36
  containers:
  - name: api
    image: registry.example.com/api:1.2.3@sha256:abc
    ports:
    - {name: http, containerPort: 8080}
    - {name: metrics, containerPort: 9090}
    volumeMounts:
    - {name: data, mountPath: /data}
  - name: side
    image: side:2
  volumes:
  - name: data
    emptyDir: {}
  - name: cache
    emptyDir: {medium: Memory}
`

// Containers, init containers, ports, volumes and a container's mount of
// a volume are selected by name in every workload kind, the image kept as
// written. Planted breaks: the container compared by index, port read
// from the pod spec, the volume mount and the pod's volume swapped.
func TestContainersPortsAndVolumesByName(t *testing.T) {
	for _, w := range workloads {
		src := w.head + strings.ReplaceAll(podSpec, "\n  ", "\n"+w.pad)
		at := strings.Count(w.head, "\n") + 2 // the line of initContainers
		files := map[string]string{"m.yaml": src}
		obj := Object{Kind: w.kind, Name: "w"}
		for _, c := range []struct {
			s    Spec
			want string
		}{
			{Spec{Object: obj, Container: "api", Path: "image"}, fmt.Sprintf("registry.example.com/api:1.2.3@sha256:abc@%d", at+5)},
			{Spec{Object: obj, Container: "init", Path: "image"}, fmt.Sprintf("busybox:1.36@%d", at+2)},
			{Spec{Object: obj, Container: "side", Path: "image"}, fmt.Sprintf("side:2@%d", at+12)},
			{Spec{Object: obj, Container: "api", Port: "metrics", Path: "containerPort"}, fmt.Sprintf("9090@%d", at+8)},
			{Spec{Object: obj, Container: "api", Volume: "data", Path: "mountPath"}, fmt.Sprintf("/data@%d", at+10)},
			{Spec{Object: obj, Volume: "cache", Path: "emptyDir.medium"}, fmt.Sprintf("Memory@%d", at+17)},
		} {
			if got := run(t, files, c.s); got != c.want {
				t.Errorf("%s %+v: got %q, want %q", w.kind, c.s, got, c.want)
			}
		}
	}
}

// A custom resource is read with the plain path grammar, from the object.
func TestCustomResourceByPath(t *testing.T) {
	src := "apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata: {name: web, namespace: prod}\nspec:\n  dnsNames: [a.example.com, b.example.com]\n"
	got := run(t, map[string]string{"m.yaml": src}, Spec{Object: Object{Kind: "Certificate", Namespace: "prod"}, Path: "spec.dnsNames[*]"})
	if want := "a.example.com@5 b.example.com@5"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A command, an arg, an env value or a subPathExpr the kubelet expands is
// not literal, also when a path reaches it through a List's items, and so
// is a value a template file computes or holds in a block; every other
// value is literal. Planted breaks: markExpanded dropped, the env value not
// marked, the subPathExpr not marked, the ephemeral containers passed
// over, only the selected objects marked, markTemplated dropped.
func TestNotLiteral(t *testing.T) {
	src := `kind: Pod
metadata: {name: p}
spec:
  containers:
  - name: c
    command: [sh, "$(SHELL_FLAGS)"]
    args: ["a$$b", plain]
    env:
    - {name: A, value: "$(B)/x"}
    - {name: B, value: "$(no"}
    volumeMounts:
    - {name: d, subPathExpr: "$(POD)"}
    - {name: e, subPathExpr: logs}
  ephemeralContainers:
  - name: debug
    args: ["$(X)"]
`
	files := map[string]string{"m.yaml": src, "l.yaml": "kind: List\nitems:\n- " + strings.ReplaceAll(src, "\n", "\n  ")}
	for path, want := range map[string]string{
		"command[*]":                  "sh@6 $(SHELL_FLAGS)!@6",
		"args[*]":                     "a$$b!@7 plain@7",
		"env[*].value":                "$(B)/x!@9 $(no@10",
		"volumeMounts[*].subPathExpr": "$(POD)!@12 logs@13",
	} {
		if got := run(t, files, Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod"}, Container: "c", Path: path}); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
	got := run(t, files, Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod"}, Container: "debug", Path: "args[*]"})
	if want := "$(X)!@16"; got != want {
		t.Errorf("the ephemeral container: got %q, want %q", got, want)
	}
	got = run(t, files, Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod"}, Container: "c", Volume: "d", Path: "subPathExpr"})
	if want := "$(POD)!@12"; got != want {
		t.Errorf("the mount d: got %q, want %q", got, want)
	}
	got = run(t, files, Spec{Where: core.Where{File: "l.yaml"}, Object: Object{Kind: "List"}, Path: "items[*].spec.containers[*].command[*]"})
	if want := "sh(l.yaml)@8 $(SHELL_FLAGS)!(l.yaml)@8"; got != want {
		t.Errorf("through the List: got %q, want %q", got, want)
	}
	tmpl := "kind: Deployment\nmetadata: {name: d}\nspec:\n  replicas: \"{{ replicas }}\"\n  paused: false\n"
	got = run(t, map[string]string{"d.yaml.j2": tmpl}, Spec{Where: core.Where{File: "d.yaml.j2"}, Object: Object{Kind: "Deployment"}, Path: "spec[*]"})
	if want := "{{ replicas }}!(d.yaml.j2)@4 false(d.yaml.j2)@5"; got != want {
		t.Errorf("template: got %q, want %q", got, want)
	}
}

// The kubelet expands a container's text, not an anchor's: a scalar an
// alias shares between a container and an annotation is marked in the
// container alone, whichever of the two writes the anchor. A pod spec two
// objects share through an alias is marked in both, and an env entry or
// mount the container reaches through an alias keeps its other fields.
// Planted breaks: the scalar marked in place, the container's list marked
// in place, the env entry marked in place, the entry's fields or its
// values in order shared with the copy.
func TestNotLiteralOnlyInTheContainer(t *testing.T) {
	src := `kind: Pod
metadata:
  name: p
  annotations:
    flags: &flags "$(FLAGS)"
spec: &spec
  containers:
  - name: c
    command: [*flags]
    args: &cmd ["$(RUN)"]
    env:
    - &entry {name: A, value: "$(B)"}
---
kind: Pod
metadata:
  name: q
  annotations: {entry: *entry, args: *cmd}
spec: *spec
`
	files := map[string]string{"m.yaml": src}
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{Object: Object{Kind: "Pod", Name: "p"}, Path: "metadata.annotations.flags"}, "$(FLAGS)@5"},
		{Spec{Object: Object{Kind: "Pod", Name: "p"}, Container: "c", Path: "command[*]"}, "$(FLAGS)!@5"},
		{Spec{Object: Object{Kind: "Pod"}, Container: "c", Path: "args[*]"}, "$(RUN)!@10 $(RUN)!@10"},
		{Spec{Object: Object{Kind: "Pod", Name: "q"}, Path: "metadata.annotations.args[*]"}, "$(RUN)@10"},
		{Spec{Object: Object{Kind: "Pod", Name: "q"}, Path: "metadata.annotations.entry.value"}, "$(B)@12"},
		{Spec{Object: Object{Kind: "Pod", Name: "q"}, Path: "metadata.annotations.entry[*]"}, "A@12 $(B)@12"},
		{Spec{Object: Object{Kind: "Pod", Name: "q"}, Container: "c", Path: "env[*].name"}, "A@12"},
		{Spec{Object: Object{Kind: "Pod", Name: "q"}, Container: "c", Path: "env[*].value"}, "$(B)!@12"},
	} {
		if got := run(t, files, c.s); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.s, got, c.want)
		}
	}
}

// Two containers of one name, one of them without the port or the mount,
// return the one that has it; Kubernetes refuses such a pod, but the rule
// still names a port the file holds. Planted break: the first container
// that lacks it refusing the rule.
func TestPortOfTwoContainersOfOneName(t *testing.T) {
	src := "kind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - name: c\n  - name: c\n    ports: [{name: http, containerPort: 80}]\n    volumeMounts: [{name: d, mountPath: /d}]\n"
	files := map[string]string{"m.yaml": src}
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{Object: Object{Kind: "Pod"}, Container: "c", Port: "http", Path: "containerPort"}, "80@7"},
		{Spec{Object: Object{Kind: "Pod"}, Container: "c", Volume: "d", Path: "mountPath"}, "/d@8"},
	} {
		if got := run(t, files, c.s); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.s, got, c.want)
		}
	}
}

// An object that writes no namespace is in default, a cluster scoped one
// too: the reader cannot know which kinds the API server holds outside any
// namespace, and the reference says so. Planted break: default dropped for
// an object that writes no namespace.
func TestClusterScopedObjectIsInDefault(t *testing.T) {
	src := "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata: {name: reader}\nrules: [{verbs: [get]}]\n"
	got := run(t, map[string]string{"m.yaml": src}, Spec{Object: Object{Kind: "ClusterRole", Namespace: "default"}, Path: "rules[*].verbs[*]"})
	if want := "get@4"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Build refuses what reads nothing it can name: no kind, a port with no
// container, a port and a volume together, a name over the limit, no path,
// and a file outside the repository. Each refusal names its reason, and a
// name of the limit's length loads.
func TestBuildRefuses(t *testing.T) {
	obj := Object{Kind: "Pod"}
	long := strings.Repeat("a", maxName+1)
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{Where: core.Where{File: "m.yaml"}, Path: "a"}, "k8s: object.kind is required"},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: obj, Port: "http", Path: "a"}, "k8s: port is a container's"},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: obj, Container: "c", Port: "http", Volume: "v", Path: "a"}, "k8s: set at most one of port and volume"},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod", Name: long}, Path: "a"}, "k8s: object.name \"aaaa"},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: obj, Volume: long, Path: "a"}, "k8s: volume \"aaaa"},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: obj}, "k8s: path is required"},
		{Spec{Where: core.Where{File: "../m.yaml"}, Object: obj, Path: "a"}, "is outside the repository"},
		{Spec{Where: core.Where{File: "m.yaml", Files: []string{"x"}}, Object: obj, Path: "a"}, "file and files are both set"},
	} {
		_, err := build(c.s)
		extracttest.WantError(t, err, c.want)
	}
	if _, err := build(Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod", Name: long[1:]}, Path: "a"}); err != nil {
		t.Errorf("a name of %d bytes, the limit, is refused: %v", maxName, err)
	}
}

// A read refuses a file that does not parse, a file of file: with no object
// the selector matches, and an object without the container, port or
// volume named, naming what it holds. Planted breaks: the parse error
// dropped, the no object refusal dropped, the names left out.
func TestReadRefuses(t *testing.T) {
	files := map[string]string{
		"m.yaml":    "kind: Pod\nmetadata: {name: p}\nspec:\n  containers:\n  - name: a\n    ports: [{name: http}]\n  - name: b\n  volumes: [{name: v}]\n",
		"bad.yaml":  "kind: Pod\nspec: [\n",
		"bad.json":  "{\"kind\": \"Pod\",}\n",
		"many.yaml": "a: 1\nb: 2\n",
	}
	root := extracttest.Repo(t, files)
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{Where: core.Where{File: "bad.yaml"}, Object: Object{Kind: "Pod"}, Path: "a"}, "bad.yaml: "},
		{Spec{Where: core.Where{File: "bad.json"}, Object: Object{Kind: "Pod"}, Path: "a"}, "bad.json: "},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod", Name: "q", Namespace: "x", APIVersion: "v1"}, Path: "a"}, `m.yaml: no object is of kind "Pod", apiVersion "v1", namespace "x", name "q"`},
		{Spec{Where: core.Where{File: "many.yaml"}, Object: Object{Kind: "Pod"}, Path: "a"}, `many.yaml: no object is of kind "Pod"`},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod"}, Container: "c", Path: "a"}, `the object of kind "Pod" at line 1 has no container "c"; its containers are "a", "b"`},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod"}, Container: "a", Port: "grpc", Path: "a"}, `the container "a" of the object of kind "Pod" at line 1 has no port "grpc" under ports; its names there are "http"`},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod"}, Container: "b", Volume: "v", Path: "a"}, `has no volume mount "v" under volumeMounts; its names there are none`},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod"}, Volume: "w", Path: "a"}, `has no volume "w" under volumes; its names there are "v"`},
		{Spec{Where: core.Where{File: "m.yaml"}, Object: Object{Kind: "Pod"}, Path: "spec"}, "not a scalar"},
		{Spec{Where: core.Where{File: "gone.yaml"}, Object: Object{Kind: "Pod"}, Path: "a"}, "gone.yaml"},
	} {
		_, err := extracttest.Run(t, build, c.s, root)
		extracttest.WantError(t, err, c.want)
	}
}

// Place drops max_bytes and cleans the files; Resolve writes max_bytes out
// and copies the files; Describe says values may be not literal.
func TestKindFunctions(t *testing.T) {
	s := Spec{Where: core.Where{Files: []string{"b/*.yaml", "./a/*.yaml"}, MaxBytes: 7}, Object: Object{Kind: "Pod"}, Path: "a"}
	p := Place(s)
	if p.MaxBytes != 0 || strings.Join(p.Files, ",") != "a/*.yaml,b/*.yaml" || p.Object != s.Object {
		t.Errorf("Place = %+v", p)
	}
	r := Resolve(Spec{Where: core.Where{Files: s.Files}})
	r.Files[0] = "x"
	if s.Files[0] == "x" || r.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve shares its files or keeps max_bytes 0: %+v", r)
	}
	if d := Describe(s); d.Label != "k8s" || !d.NotLiteral {
		t.Errorf("Describe = %+v", d)
	}
}
