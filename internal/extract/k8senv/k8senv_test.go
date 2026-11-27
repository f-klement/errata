package k8senv

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	yamlv2 "go.yaml.in/yaml/v2"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/extracttest"
	ejson "github.com/f-klement/errata/internal/extract/json"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// parsers are the parsers the facade hands the reader.
var parsers = core.Parsers{Noted: yaml.ParseNoted, JSON: ejson.Parse}

func build(s Spec) (check.Extractor, error) { return Build(s, parsers) }

// read builds s over a repository holding the file s names with data, and
// returns each value as text@line [origin], a not literal one marked !.
func read(t *testing.T, s Spec, data string) (string, error) {
	t.Helper()
	root := extracttest.Repo(t, map[string]string{s.File: data})
	vs, err := extracttest.Run(t, build, s, root)
	var out []string
	for _, v := range vs {
		if v.At.File != s.File {
			t.Errorf("%q at %s, not %s", v.Text, v.At.File, s.File)
		}
		mark := ""
		if v.NotLiteral {
			mark = "!"
		}
		out = append(out, fmt.Sprintf("%s%s@%d [%s]", mark, v.Text, v.At.Line, v.Origin))
	}
	return strings.Join(out, " "), err
}

func spec(variable string) Spec {
	return Spec{OneFile: core.OneFile{File: "deploy.yaml"}, Variable: variable}
}

const deployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    spec:
      initContainers:
      - name: migrate
        env:
        - name: PORT
          value: "1"
      containers:
      - name: web
        env:
        - name: PORT
          value: "8080"
        - name: OTHER
          value: x
      - name: side
        image: side:1
      ephemeralContainers:
      - name: debug
        env: [{name: PORT, value: '9'}]
`

// Each container of every group that sets the variable gives its value, at
// the line of the value, naming the object and the container; one that
// does not set it gives none. Planted breaks: the init or ephemeral group
// left out of the origin's words; the namespace dropped from the origin.
func TestEveryContainerGivesItsValue(t *testing.T) {
	got, err := read(t, spec("PORT"), deployment)
	if err != nil {
		t.Fatal(err)
	}
	want := `8080@18 [Deployment "prod/api" container "web"] 1@13 [Deployment "prod/api" init container "migrate"] 9@25 [Deployment "prod/api" ephemeral container "debug"]`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	s := spec("PORT")
	s.Container = "migrate"
	if got, err := read(t, s, deployment); err != nil || got != `1@13 [Deployment "prod/api" init container "migrate"]` {
		t.Errorf("container migrate: %s, %v", got, err)
	}
}

// The kubelet sets envFrom first and env: over it, each entry over the ones
// before, so the last env: entry of the name is the one value, and envFrom
// counts only when no entry sets the name; then each source whose prefix
// starts the name, and leaves a key, may give it, not literal. Planted
// breaks: the first entry kept; envFrom read beside env:; the prefix not
// checked; a prefix equal to the name taken.
func TestLastEntryWinsOverEnvFrom(t *testing.T) {
	pod := `kind: Pod
apiVersion: v1
metadata: {name: p}
spec:
  containers:
  - name: a
    envFrom:
    - configMapRef: {name: cfg}
    env:
    - {name: PORT, value: "1"}
    - {name: PORT, value: "2"}
  - name: b
    envFrom:
    - configMapRef: {name: cfg}
    - prefix: APP_
      secretRef: {name: s}
    - prefix: PORT
      configMapRef: {name: whole}
    - prefix: X_
      configMapRef: {name: other}
    - secretRef: {}
    - [x]
    - {prefix: ""}
`
	got, err := read(t, spec("PORT"), pod)
	if err != nil {
		t.Fatal(err)
	}
	want := `2@11 [Pod "p" container "a"] !configMapRef cfg@14 [Pod "p" container "b"] !secretRef@21 [Pod "p" container "b"] !envFrom@23 [Pod "p" container "b"]`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	got, err = read(t, spec("APP_PORT"), pod)
	if err != nil {
		t.Fatal(err)
	}
	want = `!configMapRef cfg@8 [Pod "p" container "a"] !configMapRef cfg@14 [Pod "p" container "b"] !secretRef s@15 [Pod "p" container "b"] !secretRef@21 [Pod "p" container "b"] !envFrom@23 [Pod "p" container "b"]`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A value from valueFrom is not literal, at the line of its key, its text
// naming its source; so is a value the kubelet expands. An entry with no
// value, or a null one, sets the empty string, literal. Planted breaks:
// valueFrom read as literal; the expansion not marked; a null valueFrom
// taken for a source.
func TestValueFromAndExpansionAreNotLiteral(t *testing.T) {
	pod := `kind: Pod
spec:
  containers:
  - name: a
    env:
    - name: A
      valueFrom:
        secretKeyRef: {name: db, key: password}
  - name: b
    env:
    - name: A
      valueFrom: {fieldRef: {fieldPath: metadata.name}}
  - name: c
    env:
    - name: A
      value: "$(HOST):80"
  - name: d
    env:
    - name: A
      value: "$$x"
  - name: e
    env:
    - name: A
  - name: f
    env:
    - name: A
      value:
      valueFrom: null
  - name: g
    env:
    - name: A
      valueFrom: {}
  - name: h
    env:
    - name: A
      valueFrom: {resourceFieldRef: {containerName: x, resource: limits.cpu}}
  - name: i
    env:
    - name: A
      value: "$(x"
  - name: j
    env:
    - name: A
      valueFrom: x
`
	got, err := read(t, spec("A"), pod)
	if err != nil {
		t.Fatal(err)
	}
	want := `!secretKeyRef db/password@7 [Pod container "a"] !fieldRef metadata.name@12 [Pod container "b"] !$(HOST):80@16 [Pod container "c"] !$$x@20 [Pod container "d"] @23 [Pod container "e"] @27 [Pod container "f"] !valueFrom@32 [Pod container "g"] !resourceFieldRef limits.cpu@36 [Pod container "h"] $(x@40 [Pod container "i"] !valueFrom@44 [Pod container "j"]`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// Every document of a YAML file counts, and so does each item of a List, a
// List inside a List too, and the one object of a JSON file. An object
// with no kind is named object, and a template is read as the file it
// renders. Planted breaks: the documents after the first dropped; a List's
// items not read; a .json file read as YAML, which takes value: yes in it;
// the template suffix left on.
func TestDocumentsListsAndJSON(t *testing.T) {
	src := `kind: Service
---
kind: List
items:
- kind: DeploymentList
  items:
  - kind: Deployment
    metadata: {name: a}
    spec: {template: {spec: {containers: [{name: c, env: [{name: A, value: "1"}]}]}}}
- kind: Pod
  spec: {containers: [{name: c, env: [{name: A, value: "2"}]}]}
---
spec: {template: {spec: {containers: [{name: c, env: [{name: A, value: "3"}]}]}}}
`
	got, err := read(t, spec("A"), src)
	if err != nil {
		t.Fatal(err)
	}
	if want := `1@9 [Deployment "a" container "c"] 2@11 [Pod container "c"] 3@13 [object container "c"]`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	js := `{"kind": "List", "items": [
  {"kind": "Pod", "metadata": {"name": "p"},
   "spec": {"containers": [{"name": "c", "env": [
     {"name": "A", "value": "yes"}]}]}}]}
`
	s := spec("A")
	s.File = "pod.json"
	if got, err := read(t, s, js); err != nil || got != `yes@4 [Pod "p" container "c"]` {
		t.Errorf("JSON: %s, %v", got, err)
	}
	_, err = read(t, s, `{"kind": "Pod", "spec": {"containers": [{"name": "c", "env": [{"name": "A", "value": 1}]}]}}`)
	extracttest.WantError(t, err, "pod.json: line 1: the env: value of \"A\" is a number to Kubernetes")
	_, err = read(t, s, `{"kind": `)
	extracttest.WantError(t, err, "pod.json: ")
	// A template is read as the file it renders (workload.JSON), so YAML in
	// a .json.j2 is unknown.
	s.File = "pod.json.j2"
	_, err = read(t, s, "kind: Pod\nspec: {containers: [{name: c, env: [{name: A, value: x}]}]}\n")
	extracttest.WantError(t, err, "pod.json.j2: ")
	for _, notList := range []string{"kind: Lists\nitems: [{kind: Pod, spec: {containers: [{name: c, env: [{name: A}]}]}}]\n", "kind: List\nitems: {a: b}\n", "kind: [List]\nitems: []\n"} {
		_, err := read(t, spec("A"), notList)
		var none *check.NoValue
		if !errors.As(err, &none) {
			t.Errorf("%q: %v, want no value", notList, err)
		}
	}
}

// What the API server refuses as an env: name or value, a string field, makes
// the file unknown, naming the line: a number, a boolean, a word YAML 1.1
// reads as one, a mapping and a sequence. A quoted word, a tagged !!str, a
// date and a null are strings. Planted breaks: the YAML 1.1 words not
// noted; the number case dropped.
func TestWhatKubernetesRefusesIsUnknown(t *testing.T) {
	head := "kind: Pod\nspec:\n  containers:\n  - name: c\n    env:\n    - name: A\n"
	for value, want := range map[string]string{
		"8080":   "a number",
		"1.5":    "a number",
		"true":   "a boolean",
		"yes":    "a boolean",
		"Off":    "a boolean",
		"{a: b}": "a mapping",
		"[a]":    "a sequence",
	} {
		_, err := read(t, spec("A"), head+"      value: "+value+"\n")
		extracttest.WantError(t, err, `deploy.yaml: line 7: the env: value of "A" is `+want+" to Kubernetes, which refuses the object: an env: value is a string, so quote it")
	}
	for value, want := range map[string]string{`"yes"`: "yes", "!!str yes": "yes", "2001-12-14": "2001-12-14", "~": "", "yEs": "yEs", "'8080'": "8080"} {
		got, err := read(t, spec("A"), head+"      value: "+value+"\n")
		if err != nil || got != want+`@7 [Pod container "c"]` {
			t.Errorf("value %s: %s, %v; want %q", value, got, err, want)
		}
	}
	_, err := read(t, spec("on"), "kind: Pod\nspec:\n  containers:\n  - name: c\n    env:\n    - {name: on, value: x}\n")
	extracttest.WantError(t, err, `line 6: the env: name of "on" is a boolean to Kubernetes`)
}

// yaml11Bools are exactly the words yaml/v2, which sigs.k8s.io/yaml reads
// with, takes as a boolean and errata's YAML 1.2 tree as a string, in every
// case of the words YAML 1.1 knows. Planted break: a word dropped.
func TestYAML11BoolsAreYAMLv2s(t *testing.T) {
	for _, word := range []string{"y", "yes", "n", "no", "on", "off", "true", "false"} {
		for _, w := range cases(word) {
			var v map[string]any
			if err := yamlv2.Unmarshal([]byte("v: "+w), &v); err != nil {
				t.Fatal(err)
			}
			_, isBool := v["v"].(bool)
			docs, err := yaml.Parse([]byte(w))
			if err != nil {
				t.Fatal(err)
			}
			want := isBool && docs[0].Type == core.TypeString
			if yaml11Bools[w] != want {
				t.Errorf("%s: in the set %v, yaml/v2 reads it as a boolean and YAML 1.2 as a string %v", w, yaml11Bools[w], want)
			}
		}
	}
}

// cases is every spelling of word in upper and lower case.
func cases(word string) []string {
	out := []string{""}
	for _, r := range word {
		var next []string
		for _, s := range out {
			next = append(next, s+strings.ToLower(string(r)), s+strings.ToUpper(string(r)))
		}
		out = next
	}
	return out
}

// A file with no container that sets the variable is check.NoValue, which
// names the variable; a file that does not parse, or holds more nodes than
// its limit, is unknown, and so is YAML in a .json file. Planted breaks:
// the node limit not checked; a .json file read as YAML.
func TestNoValueAndUnknown(t *testing.T) {
	_, err := read(t, spec("A"), deployment)
	var none *check.NoValue
	if !errors.As(err, &none) || !strings.Contains(none.Reason, `deploy.yaml: no container of its workloads sets "A"`) {
		t.Errorf("%v, want no value", err)
	}
	_, err = read(t, spec("A"), "kind: Pod\nspec: [\n")
	extracttest.WantError(t, err, "deploy.yaml: ")
	s := spec("A")
	s.MaxBytes = 70
	_, err = read(t, s, strings.Repeat("[", 60))
	if !errors.Is(err, core.ErrTooDense) {
		t.Errorf("%v: is not core.ErrTooDense", err)
	}
	s.File = "a.json"
	_, err = read(t, s, strings.Repeat("[", 60))
	if !errors.Is(err, core.ErrTooDense) {
		t.Errorf("JSON: %v: is not core.ErrTooDense", err)
	}
	// kubectl reads a .json file as JSON alone, so YAML in one is unknown.
	s.MaxBytes = 1 << 16
	_, err = read(t, s, "kind: Pod\nspec: {containers: [{name: c, env: [{name: A, value: x}]}]}\n")
	extracttest.WantError(t, err, "a.json: ")
}

// The extractor returns at most its value limit, and refuses past it,
// naming core.MaxValues. Planted break: the bound one more.
func TestValueLimit(t *testing.T) {
	x, err := build(spec("A"))
	if err != nil {
		t.Fatal(err)
	}
	e := x.(extractor)
	e.valueLimit = 2
	pod := func(n int) []byte {
		return []byte("kind: Pod\nspec:\n  containers:\n" + strings.Repeat("  - {name: c, env: [{name: A, value: x}]}\n", n))
	}
	if vs, err := e.fromData(pod(2)); err != nil || len(vs) != 2 {
		t.Errorf("2 values under a limit of 2: %d, %v", len(vs), err)
	}
	_, err = e.fromData(pod(3))
	extracttest.WantError(t, err, fmt.Sprintf(`deploy.yaml: the file holds more than %d values of "A", the limit`, core.MaxValues))
	_, err = e.fromData([]byte("kind: Pod\nspec:\n  containers:\n  - {name: c, envFrom: [{configMapRef: {name: a}}, {configMapRef: {name: b}}, {configMapRef: {name: c}}]}\n"))
	extracttest.WantError(t, err, "the limit")
}

// Build refuses a spec Kubernetes or errata cannot read. Planted breaks:
// = let through; the container's bound dropped.
func TestBuildRefuses(t *testing.T) {
	long := strings.Repeat("A", maxName+1)
	for _, c := range []struct {
		s    Spec
		want string
	}{
		{Spec{Variable: "A"}, "k8s-env: file is required"},
		{Spec{OneFile: core.OneFile{File: "../x.yaml"}, Variable: "A"}, "outside the repository"},
		{spec(""), "k8s-env: variable is required"},
		{spec("A=B"), `k8s-env: variable "A=B" is no name Kubernetes accepts`},
		{spec("A\tB"), "is no name Kubernetes accepts"},
		{spec("Ä"), "is no name Kubernetes accepts"},
		{spec(long), "is no name Kubernetes accepts"},
		{Spec{OneFile: core.OneFile{File: "a.yaml"}, Variable: "A", Container: long}, "k8s-env: container \"AAAA"},
	} {
		_, err := build(c.s)
		extracttest.WantError(t, err, c.want)
	}
	for _, ok := range []Spec{spec("my.var-1 ~"), spec(strings.Repeat("A", maxName)), {OneFile: core.OneFile{File: "a"}, Variable: "A", Container: long[1:]}} {
		if _, err := build(ok); err != nil {
			t.Errorf("%q: %v", ok.Variable, err)
		}
	}
}

// Place cleans the file and drops max_bytes, keeping the variable and the
// container; Resolve writes max_bytes out. The env kind reads with Build's
// extractor over every container, and k8s-env chooses no file by its name.
func TestPlaceResolveAndEnvRead(t *testing.T) {
	s := Spec{OneFile: core.OneFile{File: "./a.yaml", MaxBytes: 5}, Variable: "A", Container: "c"}
	if p := Place(s); p.File != "a.yaml" || p.MaxBytes != 0 || p.Variable != "A" || p.Container != "c" {
		t.Errorf("Place: %+v", p)
	}
	if r := Resolve(spec("A")); r.MaxBytes != core.DefaultMaxBytes {
		t.Errorf("Resolve: %+v", r)
	}
	if d := Describe(s); d.Label != "k8s-env" || !d.NotLiteral {
		t.Errorf("Describe: %+v", d)
	}
	if chooses("deploy/api.yaml") || Kind.Env.Order != 70 {
		t.Error("k8s-env chooses a file by its name, or moved in env's order")
	}
	x, err := Kind.EnvRead("a.yaml", "A", 7, parsers)
	if err != nil {
		t.Fatal(err)
	}
	if e := x.(extractor); e.File != "a.yaml" || e.Limit != 7 || e.variable != "A" || e.container != "" {
		t.Errorf("EnvRead built %+v", e)
	}
	if _, err := Kind.EnvRead("a.yaml", "A=", 7, parsers); err == nil {
		t.Error("EnvRead took the name A=")
	}
}

// An origin writes a kind bare only when it is an identifier of 63 bytes at
// most, and quotes and clips any other, so the file's text puts no line
// break, escape sequence or line of its own length into a report. Planted
// breaks: the quote dropped; the length bound dropped.
func TestOriginQuotesAKindThatIsNoIdentifier(t *testing.T) {
	long := strings.Repeat("P", 100000)
	for kind, want := range map[string]string{
		`"Pod\n::error file=x::owned\e[2J"`: `"Pod\n::error file=x::owned\x1b[2J"`,
		`"Pod Template"`:                    `"Pod Template"`,
		`"9Pod"`:                            `"9Pod"`,
		`"Pod-x"`:                           `"Pod-x"`,
		strings.Repeat("P", 63):             strings.Repeat("P", 63),
		strings.Repeat("P", 64):             core.Quote(strings.Repeat("P", 64)),
		long:                                core.Quote(long),
	} {
		got, err := read(t, spec("A"), "kind: "+kind+"\nspec: {template: {spec: {containers: [{name: c, env: [{name: A, value: x}]}]}}}\n")
		if err != nil {
			t.Fatal(err)
		}
		if w := "x@2 [" + want + ` container "c"]`; got != w {
			t.Errorf("kind %.80s:\ngot  %.200s\nwant %.200s", kind, got, w)
		}
	}
	if len(core.Quote(long)) > 1024 {
		t.Errorf("a quoted kind is %d bytes long", len(core.Quote(long)))
	}
}

// A container: that no workload of the file holds is an error naming it,
// so allow_empty does not pass a misspelt name; a container of that name
// that does not set the variable is no value. Planted breaks: the check
// dropped; a container of another name counted as held.
func TestContainerNoWorkloadHoldsIsAnError(t *testing.T) {
	s := spec("PORT")
	s.Container = "wbe"
	_, err := read(t, s, deployment)
	extracttest.WantError(t, err, `deploy.yaml: no workload holds a container "wbe"`)
	var none *check.NoValue
	if errors.As(err, &none) {
		t.Errorf("%v: no value, want an error", err)
	}
	s.Container = "side"
	_, err = read(t, s, deployment)
	if !errors.As(err, &none) {
		t.Errorf("container side: %v, want no value", err)
	}
}

// Every env: entry of the variable's name is held to what the API server
// takes, not the last alone: a name or value that is no string, and a value
// that is not empty beside a valueFrom, make the file unknown. An empty or
// null value beside a valueFrom is taken, and an entry of another name is
// not read. Planted breaks: the last entry alone read; the value and
// valueFrom check dropped; an empty value beside valueFrom refused.
func TestEveryEntryOfTheNameIsChecked(t *testing.T) {
	head := "kind: Pod\nspec:\n  containers:\n  - name: c\n    env:\n"
	from := `valueFrom: {fieldRef: {fieldPath: x}}`
	for entries, want := range map[string]string{
		"    - {name: A, value: 1}\n    - {name: A, value: x}\n":         `line 6: the env: value of "A" is a number to Kubernetes`,
		"    - {name: A, value: \"4\", " + from + "}\n    - {name: A}\n": `deploy.yaml: line 6: the env: entry of "A" writes both value and valueFrom, and Kubernetes refuses the object`,
		"    - {name: A}\n    - {name: A, value: \"4\", " + from + "}\n": `line 7: the env: entry of "A" writes both value and valueFrom`,
	} {
		_, err := read(t, spec("A"), head+entries)
		extracttest.WantError(t, err, want)
	}
	for entries, want := range map[string]string{
		"    - {name: A, value: \"\", " + from + "}\n":           `!fieldRef x@6 [Pod container "c"]`,
		"    - {name: A, value: ~, " + from + "}\n":              `!fieldRef x@6 [Pod container "c"]`,
		"    - {name: B, value: 1}\n    - {name: A, value: x}\n": `x@7 [Pod container "c"]`,
	} {
		got, err := read(t, spec("A"), head+entries)
		if err != nil || got != want {
			t.Errorf("%s: %s, %v; want %s", entries, got, err, want)
		}
	}
}
