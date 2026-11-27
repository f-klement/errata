// Package k8s is the k8s extractor: a path in the Kubernetes objects of
// manifest files, each object selected by its apiVersion, kind, namespace
// and name, across multi document YAML, JSON and List kinds, and in a
// workload object one container, one of its ports or one volume by name.
// It reads the files as they are written: the objects Helm and Kustomize
// render are the provider's (#16), and it touches no cluster. A value the
// kubelet expands, such as $(PORT) in a command, and a value a template
// file computes, are not literal.
package k8s

import (
	"errors"
	"fmt"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads Path in every object of the file, or the files, that Object
// selects. With Container, Path is read in that container of the object's
// pod spec, with Port in that port of the container, and with Volume in
// the pod's volume of that name, or with Container in the container's
// mount of it.
type Spec struct {
	core.Where
	Object    Object `json:"object"`
	Container string `json:"container,omitempty"`
	Port      string `json:"port,omitempty"`
	Volume    string `json:"volume,omitempty"`
	Path      string `json:"path"`
}

// Object selects objects by the fields they write: Kind always, and each of
// the others when it is set. Namespace matches the object's
// metadata.namespace, or default for an object that writes none, the
// namespace kubectl apply puts it in with no -n and no namespace in the
// context; left out, it matches every namespace.
type Object struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

// kindKey is the kind's key.
const kindKey = "k8s"

// Kind is the k8s kind, which a rule writes under the key k8s.
var Kind = core.Kind[Spec]{Key: kindKey, Place: Place, Resolve: Resolve, Describe: Describe}

// Place is s with only what decides where it reads: its where part as
// core.Where.Place writes it.
func Place(s Spec) Spec {
	s.Where = s.Place()
	return s
}

// Resolve is s with its max_bytes written out and its files copied, for
// errata list.
func Resolve(s Spec) Spec {
	s.Where = s.Resolved()
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since the kubelet expands $(NAME) and a template file computes
// what its tags hold.
func Describe(Spec) check.Description {
	return check.Description{Label: kindKey, NotLiteral: true}
}

// maxName is the longest apiVersion, kind, namespace, name, container, port
// or volume that loads, in bytes. Kubernetes allows none over 316 (a
// group of 253 and a version of 63), so the bound refuses no real one and
// keeps each comparison a constant cost.
const maxName = 1024

// Build checks s and returns its extractor, which parses a .json file with
// p.JSON and any other with p.YAML: of File, or a core.FileSet of one for
// each file Files names.
func Build(s Spec, p core.Parsers) (check.Extractor, error) {
	if err := checkSelectors(s); err != nil {
		return nil, err
	}
	path, err := core.CheckPath(kindKey, s.Path)
	if err != nil {
		return nil, err
	}
	return core.BuildOneOrSet(kindKey, s.Where, func(src core.Source, inSet bool) (core.FileReader, error) {
		return extractor{Source: src, spec: s, path: path, parsers: p, inSet: inSet}, nil
	})
}

// checkSelectors refuses a spec with no kind, a port with no container,
// and a name over maxName bytes.
func checkSelectors(s Spec) error {
	for _, f := range []struct{ what, v string }{
		{"object.apiVersion", s.Object.APIVersion}, {"object.kind", s.Object.Kind}, {"object.namespace", s.Object.Namespace},
		{"object.name", s.Object.Name}, {"container", s.Container}, {"port", s.Port}, {"volume", s.Volume},
	} {
		if len(f.v) > maxName {
			return fmt.Errorf("k8s: %s %s is %d bytes long, over the limit of %d", f.what, core.Quote(f.v), len(f.v), maxName)
		}
	}
	switch {
	case s.Object.Kind == "":
		return errors.New("k8s: object.kind is required; write the kind of the objects to read, such as Deployment")
	case s.Port != "" && s.Container == "":
		return errors.New("k8s: port is a container's; write the container whose port it is")
	case s.Port != "" && s.Volume != "":
		return errors.New("k8s: set at most one of port and volume")
	}
	return nil
}
