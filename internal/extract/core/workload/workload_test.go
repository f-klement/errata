package workload

import (
	"fmt"
	"strings"
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/yaml"
)

// parse is the one document of src, as the yaml reader parses it.
func parse(t *testing.T, src string) *core.Node {
	t.Helper()
	docs, err := yaml.Parse([]byte(src))
	if err != nil || len(docs) != 1 {
		t.Fatalf("parse: %d documents, %v", len(docs), err)
	}
	return docs[0]
}

// containers is the groups, names and lines Of walks, as one line.
func containers(obj *core.Node) string {
	var parts []string
	for _, c := range Of(obj) {
		parts = append(parts, fmt.Sprintf("%s/%s@%d", c.Group.Key(), c.Name, c.Line))
	}
	return strings.Join(parts, " ")
}

// pod is a pod spec with one container of each group, indented by pad
// below a line that holds its key.
func pod(pad string) string {
	return strings.ReplaceAll(`
  initContainers:
  - name: init
    image: busybox
  containers:
  - name: api
    image: api:1
  - name: side
    image: side:1
  ephemeralContainers:
  - name: debug
    image: busybox
`, "\n  ", "\n"+pad)
}

// Of finds the pod spec of every built in workload kind, where the kind
// puts it, and walks containers, then init, then ephemeral containers,
// each at its item's line. Planted breaks: the CronJob row sent to
// spec.template.spec, the PodTemplate row dropped, Pod's spec read at
// spec.template.spec, and the group order turned.
func TestOfFindsEveryWorkloadKindsPodSpec(t *testing.T) {
	for _, c := range []struct{ name, head, pad string }{
		{"Pod", "apiVersion: v1\nkind: Pod\nspec:", "  "},
		{"Deployment", "apiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:", "      "},
		{"StatefulSet", "apiVersion: apps/v1\nkind: StatefulSet\nspec:\n  template:\n    spec:", "      "},
		{"DaemonSet", "apiVersion: apps/v1\nkind: DaemonSet\nspec:\n  template:\n    spec:", "      "},
		{"ReplicaSet", "apiVersion: apps/v1\nkind: ReplicaSet\nspec:\n  template:\n    spec:", "      "},
		{"Job", "apiVersion: batch/v1\nkind: Job\nspec:\n  template:\n    spec:", "      "},
		{"ReplicationController", "apiVersion: v1\nkind: ReplicationController\nspec:\n  template:\n    spec:", "      "},
		{"Rollout", "apiVersion: argoproj.io/v1alpha1\nkind: Rollout\nspec:\n  template:\n    spec:", "      "},
		{"PodTemplate", "apiVersion: v1\nkind: PodTemplate\ntemplate:\n  spec:", "    "},
		{"CronJob", "apiVersion: batch/v1\nkind: CronJob\nspec:\n  jobTemplate:\n    spec:\n      template:\n        spec:", "          "},
	} {
		obj := parse(t, c.head+pod(c.pad))
		first := strings.Count(c.head, "\n") + 3
		want := fmt.Sprintf("containers/api@%d containers/side@%d initContainers/init@%d ephemeralContainers/debug@%d", first+3, first+5, first, first+8)
		if got := containers(obj); got != want {
			t.Errorf("%s: Of = %q, want %q", c.name, got, want)
		}
		if spec := PodSpec(obj); spec == nil || spec.Field["containers"] == nil {
			t.Errorf("%s: PodSpec is not the pod spec", c.name)
		}
	}
}

// A Pod, a PodTemplate or a CronJob of another group is a custom resource,
// read at spec.template.spec as any other kind; the core group's
// apiVersion has no slash, and batch/v1beta1 is a CronJob's. Planted
// breaks: apiGroup returning the whole apiVersion, the group test of each
// special kind dropped.
func TestOfReadsAKindOfAnotherGroupAsACustomResource(t *testing.T) {
	both := "spec:\n  containers:\n  - name: top\n  template:\n    spec:\n      containers:\n      - name: deep\n"
	for _, c := range []struct{ head, want string }{
		{"apiVersion: v1\nkind: Pod\n", "containers/top@5"},
		{"kind: Pod\n", "containers/top@4"},
		{"apiVersion: example.com/v1\nkind: Pod\n", "containers/deep@9"},
		{"apiVersion: v1\nkind: CronJob\n", "containers/deep@9"},
		{"apiVersion: example.com/v1\nkind: PodTemplate\n", "containers/deep@9"},
		{"apiVersion: batch/v1beta1\nkind: CronJob\nspec:\n  jobTemplate:\n    spec:\n      template:\n        spec:\n          containers:\n          - name: job\n", "containers/job@9"},
	} {
		src := c.head
		if !strings.Contains(src, "spec:") {
			src += both
		}
		if got := containers(parse(t, src)); got != c.want {
			t.Errorf("%q: Of = %q, want %q", c.head, got, c.want)
		}
	}
	for apiVersion, want := range map[string]string{"v1": "", "apps/v1": "apps", "": "", "a/b/c": "a"} {
		if got := apiGroup(apiVersion); got != want {
			t.Errorf("apiGroup(%q) = %q, want %q", apiVersion, got, want)
		}
	}
}

// An object with no pod spec, or one whose steps are not mappings, has no
// containers; an item that is not a mapping is passed over, and a
// container with no name, or a name that is not a scalar, has the name "".
// Planted breaks: mapping's shape test of a step dropped, Of keeping a
// scalar item, text reading a mapping's name.
func TestOfPassesOverWhatIsNoContainer(t *testing.T) {
	for _, src := range []string{
		"kind: Service\nspec:\n  ports: []\n",
		"kind: Deployment\nspec: 3\n",
		"kind: Deployment\nspec:\n  template: [a]\n",
		"kind: Deployment\nspec:\n  template:\n    spec: x\n",
		"kind: Pod\nspec:\n  containers: {a: b}\n",
		"[1, 2]\n",
		"x\n",
	} {
		obj := parse(t, src)
		if got := Of(obj); got != nil {
			t.Errorf("%q: Of = %v, want none", src, got)
		}
	}
	if PodSpec(parse(t, "kind: Deployment\nspec: 3\n")) != nil || PodSpec(parse(t, "kind: Pod\n")) != nil {
		t.Error("PodSpec found a pod spec where the steps are no mapping")
	}
	if PodSpec(nil) != nil || Of(nil) != nil {
		t.Error("a nil object has a pod spec")
	}
	got := containers(parse(t, "kind: Pod\nspec:\n  containers:\n  - x\n  - image: a\n  - name: [n]\n  - name: b\n"))
	if want := "containers/@5 containers/@6 containers/b@7"; got != want {
		t.Errorf("Of = %q, want %q", got, want)
	}
}

// A List has no pod spec, so Of finds no container in one that writes
// spec.template.spec beside its items: kubectl applies the items alone. A
// kind that ends in List with no items sequence is read as any other kind.
// Planted break: PodSpec without the List check.
func TestOfFindsNoContainerInAList(t *testing.T) {
	stray := "spec:\n  template:\n    spec:\n      containers:\n      - name: stray\n"
	for _, head := range []string{"kind: List\nitems: []\n", "apiVersion: apps/v1\nkind: DeploymentList\nitems:\n- kind: Pod\n"} {
		obj := parse(t, head+stray)
		if got := Of(obj); got != nil {
			t.Errorf("%q: Of = %v, want none", head, containers(obj))
		}
		if PodSpec(obj) != nil {
			t.Errorf("%q: a List has a pod spec", head)
		}
	}
	if got := containers(parse(t, "kind: FooList\n"+stray)); got != "containers/stray@6" {
		t.Errorf("a kind ending in List with no items: Of = %q, want containers/stray@6", got)
	}
}

// Every container Of returns is the item's own mapping, so a reader
// selects its keys from it.
func TestOfHandsTheContainersMapping(t *testing.T) {
	cs := Of(parse(t, "kind: Pod\nspec:\n  containers:\n  - name: a\n    image: x:1\n"))
	if len(cs) != 1 || cs[0].Node.Field["image"].Text != "x:1" || cs[0].Group != Regular {
		t.Fatalf("Of = %+v", cs)
	}
}

// Group.Key names each group as a pod spec writes it.
func TestGroupKey(t *testing.T) {
	for g, want := range map[Group]string{Regular: "containers", Init: "initContainers", Ephemeral: "ephemeralContainers"} {
		if got := g.Key(); got != want {
			t.Errorf("Key(%d) = %q, want %q", g, got, want)
		}
	}
}

// Expands holds where the kubelet changes the text and not where it
// keeps it, the cases its expansion package tests. Planted breaks: the $$
// case dropped, a $( with no ) taken as a reference, $() taken as one,
// the scan stopped at the first $( with no ).
func TestExpands(t *testing.T) {
	for text, want := range map[string]bool{
		"":             false,
		"plain":        false,
		"$":            false,
		"a$":           false,
		"$a":           false,
		"$(":           false,
		"$(a":          false,
		"$()":          false,
		"$()$(":        false,
		"$(a)":         true,
		"x$(VAR_1)y":   true,
		"$$":           true,
		"$$(a)":        true,
		"a$(b $$":      true,
		"$()$(b)":      true,
		"${HOME}":      false,
		"echo $HOME":   false,
		"$(a b c) end": true,
	} {
		if got := Expands(text); got != want {
			t.Errorf("Expands(%q) = %v, want %v", text, got, want)
		}
		if got := expand(text) != text; got != want {
			t.Errorf("the kubelet's expansion of %q changes it: %v, want %v", text, got, want)
		}
	}
}

// JSON takes a name that ends in .json once a template suffix comes off,
// and reads every other name as YAML. Planted breaks: the template suffix
// left on, the comparison made without case.
func TestJSONByName(t *testing.T) {
	for file, want := range map[string]bool{
		"deploy.json":         true,
		"a/b/deploy.json":     true,
		"deploy.json.j2":      true,
		"deploy.json.tmpl":    true,
		"deploy.yaml":         false,
		"deploy.yml":          false,
		"deploy.yaml.j2":      false,
		"deploy.JSON":         false,
		"deploy.json5":        false,
		"json":                false,
		"deploy.json.d/x.yml": false,
	} {
		if got := JSON(file); got != want {
			t.Errorf("JSON(%q) = %v, want %v", file, got, want)
		}
	}
}

// objectKinds is the kind and line of each object Objects returns, as one
// line.
func objectKinds(objs []*core.Node) string {
	var parts []string
	for _, o := range objs {
		parts = append(parts, fmt.Sprintf("%s@%d", text(o, "kind"), o.Line))
	}
	return strings.Join(parts, " ")
}

// Objects is each document that is a mapping, and after a List its items,
// nested Lists too, in the file's order. A kind that does not end in List,
// and a List whose items are no sequence, hold no objects. Planted breaks:
// the items not read, a nested List's items not read, the List left out
// of the result, the suffix test dropped.
func TestObjectsWalksDocumentsAndListItems(t *testing.T) {
	docs, err := yaml.Parse([]byte(`kind: Pod
---
- not an object
---
kind: List
items:
- kind: Service
- plain
- kind: DeploymentList
  items:
  - kind: Deployment
- kind: ConfigMap
---
kind: Secret
items:
- kind: Pod
---
kind: PodList
items: {kind: Pod}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := "Pod@1 List@5 Service@7 DeploymentList@9 Deployment@11 ConfigMap@12 Secret@14 PodList@18"
	if got := objectKinds(Objects(docs)); got != want {
		t.Errorf("Objects = %q, want %q", got, want)
	}
	if got := Objects(nil); got != nil {
		t.Errorf("Objects(nil) = %v", got)
	}
}
