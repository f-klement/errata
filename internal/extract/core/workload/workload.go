// Package workload is the walk from a Kubernetes object, as core's tree
// holds it, to the containers of its pod spec: the pod spec of a Pod, of a
// CronJob's job template, of a PodTemplate, and of every other kind under
// spec.template, the workload kinds (Deployment, StatefulSet, DaemonSet,
// ReplicaSet, Job, ReplicationController) and any custom resource built the
// same way. It also holds what a manifest file is made of: which name
// parses as JSON, and the objects of its documents and of the Lists among
// them. Readers of Kubernetes manifests share it, since no reader
// imports another. It reads a tree a reader parsed and opens no file.
package workload

import (
	"path"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// Group is a list of a pod spec's containers, by the key it is written
// under.
type Group uint8

// The groups of a pod spec, in the order Of walks them.
const (
	Regular   Group = iota // containers
	Init                   // initContainers
	Ephemeral              // ephemeralContainers
)

// groups are the keys of the groups, by Group.
var groups = [...]string{Regular: "containers", Init: "initContainers", Ephemeral: "ephemeralContainers"}

// Key is the key g is written under in a pod spec, such as initContainers.
func (g Group) Key() string { return groups[g] }

// Container is one container of a pod spec.
type Container struct {
	Group Group
	// Name is the text of the container's name, "" when it has none or
	// its name is not a scalar. Kubernetes holds a name unique across
	// the three groups of a pod, but the walk does not: two containers of
	// one name are both returned.
	Name string
	// Line is where the container's item starts, counted from 1.
	Line int
	// Node is the container's mapping, whose keys, such as image, env or
	// ports, a reader selects from.
	Node *core.Node
}

// PodSpec is the pod spec of obj, or nil when obj has none: the mapping at
// spec of a Pod, at template.spec of a PodTemplate, at
// spec.jobTemplate.spec.template.spec of a CronJob, and at
// spec.template.spec of any other kind. A Pod and a PodTemplate are of the
// core group, whose apiVersion has no group before a slash, and a CronJob
// of the batch group; a kind of that name in another group, a custom
// resource, is read as any other kind is. A List has none: kubectl applies
// its items and nothing else of it, so a pod spec it writes beside them is
// no pod's.
func PodSpec(obj *core.Node) *core.Node {
	if isList(obj) {
		return nil
	}
	return mapping(obj, specPath(obj)...)
}

// specPath is the keys from obj to its pod spec, by its kind and group.
func specPath(obj *core.Node) []string {
	kind, group := text(obj, "kind"), apiGroup(text(obj, "apiVersion"))
	switch {
	case kind == "Pod" && group == "":
		return []string{"spec"}
	case kind == "PodTemplate" && group == "":
		return []string{"template", "spec"}
	case kind == "CronJob" && group == "batch":
		return []string{"spec", "jobTemplate", "spec", "template", "spec"}
	}
	return []string{"spec", "template", "spec"}
}

// apiGroup is the group of an apiVersion, the part before its slash: ""
// for the core group's v1, and "apps" for apps/v1.
func apiGroup(apiVersion string) string {
	group, _, _ := strings.Cut(apiVersion, "/")
	if group == apiVersion {
		return ""
	}
	return group
}

// Of is every container of obj's pod spec, in the order of Group and, in
// each group, of the file: an item that is not a mapping is passed over.
// An object with no pod spec has none.
func Of(obj *core.Node) []Container {
	spec := PodSpec(obj)
	if spec == nil {
		return nil
	}
	var out []Container
	for g := range groups {
		list := spec.Field[groups[g]]
		if list == nil || list.Shape != core.Sequence {
			continue
		}
		for _, item := range list.Items {
			if item.Shape == core.Mapping {
				out = append(out, Container{Group: Group(g), Name: text(item, "name"), Line: item.Line, Node: item})
			}
		}
	}
	return out
}

// mapping is the mapping at keys from n, or nil when a step is missing or
// not a mapping.
func mapping(n *core.Node, keys ...string) *core.Node {
	for _, k := range keys {
		if n == nil || n.Shape != core.Mapping {
			return nil
		}
		n = n.Field[k]
	}
	if n == nil || n.Shape != core.Mapping {
		return nil
	}
	return n
}

// text is the text of the scalar under key in the mapping n, or "".
func text(n *core.Node, key string) string {
	if n == nil || n.Shape != core.Mapping {
		return ""
	}
	v := n.Field[key]
	if v == nil || v.Shape != core.Scalar {
		return ""
	}
	return v.Text
}

// JSON reports whether a manifest file named file parses as JSON: its
// name ends in .json once a template suffix comes off (core.Untemplated).
// So deploy.json and deploy.json.j2, which the engine renders to
// deploy.json, read as JSON, and any other name reads as YAML: .yaml,
// .yml, a .yaml.tmpl, and also .JSON and .json5, whose case and extension
// kubectl's own list of manifest extensions (.json, .yaml, .yml) does not
// hold. A reader that parses a JSON name with YAML refuses what only JSON
// writes, such as the escape \/.
func JSON(file string) bool {
	return path.Ext(core.Untemplated(file)) == ".json"
}

// Objects is every object of docs, the documents a reader parsed from one
// manifest, in the file's order: each document that is a mapping, and after
// a List the items it holds, each read as an object in turn, so a List
// inside a List is walked too. A List is what kubectl reads as one: an
// object whose kind ends in List, such as v1 List or DeploymentList, and
// whose items are a sequence. The List itself is an object of the result,
// and an item that is not a mapping is passed over. The walk ends, since
// a parsed tree holds no cycle: the yaml reader refuses an alias inside
// its own anchor.
func Objects(docs []*core.Node) []*core.Node {
	return objects(docs, nil)
}

// objects appends to out every mapping of nodes and, after each List, its
// items in turn.
func objects(nodes []*core.Node, out []*core.Node) []*core.Node {
	for _, n := range nodes {
		if n.Shape != core.Mapping {
			continue
		}
		out = append(out, n)
		if isList(n) {
			out = objects(n.Field["items"].Items, out)
		}
	}
	return out
}

// isList reports whether n is a List as kubectl reads one: a mapping whose
// kind ends in List and whose items are a sequence.
func isList(n *core.Node) bool {
	if !strings.HasSuffix(text(n, "kind"), "List") {
		return false
	}
	items := n.Field["items"]
	return items != nil && items.Shape == core.Sequence
}

// Expands reports whether the kubelet may change text when it expands the
// variable references of a container's command, args, env value or volume
// mount's subPathExpr: text holds $$, which it reads as one $, or a
// $(NAME) with a name in it, a reference it replaces with the variable's
// value when the container defines it. A value that holds either is not the text the container
// sees, so a reader marks it not literal. A $( with no ) after it, a $()
// with no name, and a $ before any other character, stay as written.
func Expands(text string) bool {
	for i := 0; i+1 < len(text); i++ {
		if text[i] != '$' {
			continue
		}
		switch text[i+1] {
		case '$':
			return true
		case '(':
			if strings.IndexByte(text[i+2:], ')') > 0 {
				return true
			}
		}
	}
	return false
}
