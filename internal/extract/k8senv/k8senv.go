// Package k8senv is the k8s-env extractor: the value one environment
// variable has in each container of the Kubernetes workloads a manifest
// holds, from the container's env: and envFrom:, each value naming the
// object and the container it holds for. It finds each workload's
// containers through core/workload, reads the YAML or JSON with the parsers
// the facade hands it, since no reader imports another, and reads no
// ConfigMap, Secret or cluster: a value from one of them is not literal.
package k8senv

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads the variable Variable in every container of every workload the
// manifest File holds, or in the containers named Container alone.
type Spec struct {
	core.OneFile
	Variable  string `json:"variable"`
	Container string `json:"container,omitempty"`
}

// kindKey is the kind's key, which Kind and Build both use. Build names the
// constant rather than Kind.Key because an env reader's Read calls Build,
// and Build reading Kind would make an initialization cycle.
const kindKey = "k8s-env"

// Kind is the k8s-env kind, which a rule writes under the key k8s-env. It is
// an env reader, after systemd.
var Kind = core.Kind[Spec]{Key: kindKey, Place: Place, Resolve: Resolve, Describe: Describe,
	Env: &check.EnvChoice{Order: 70, Chooses: chooses}, EnvRead: envRead}

// chooses takes no file by its name: a manifest may have any name, and a
// .yaml file is as often something else, so env reads a manifest with
// k8s-env only when readers: gives it the reader.
func chooses(string) bool { return false }

// envRead is the env kind's reader of the variable name in file: Build's,
// over every container.
func envRead(file, name string, maxBytes int64, p core.Parsers) (check.Extractor, error) {
	return Build(Spec{OneFile: core.OneFile{File: file, MaxBytes: maxBytes}, Variable: name}, p)
}

// Place is s with only what decides where it reads: the file cleaned, and
// no max_bytes.
func Place(s Spec) Spec {
	s.OneFile = s.Place()
	return s
}

// Resolve is s with its max_bytes written out, for errata list.
func Resolve(s Spec) Spec {
	s.OneFile = s.Resolved()
	return s
}

// Describe is what the kind's extractor returns: flat values, some not
// literal, since a value from valueFrom or envFrom lives in the cluster and
// the kubelet expands $(NAME) and $$.
func Describe(Spec) check.Description {
	return check.Description{Label: kindKey, NotLiteral: true}
}

// maxName is the longest variable or container name that loads, in bytes,
// so each comparison with a name in the file is a constant cost.
const maxName = 1024

// Build checks s and returns its extractor, which reads a .json file with
// p.JSON and any other with p.Noted.
func Build(s Spec, p core.Parsers) (check.Extractor, error) {
	src, err := s.Check(kindKey)
	if err != nil {
		return nil, err
	}
	if err := checkVariable(s.Variable); err != nil {
		return nil, err
	}
	if len(s.Container) > maxName {
		return nil, fmt.Errorf("k8s-env: container %s is %d bytes long, over the limit of %d", core.Quote(s.Container), len(s.Container), maxName)
	}
	return extractor{Source: src, variable: s.Variable, container: s.Container, yaml: p.Noted, json: p.JSON}, nil
}

// checkVariable refuses a name Kubernetes refuses for an environment
// variable: since 1.34 any printable ASCII but =, at least one character.
func checkVariable(v string) error {
	if v == "" {
		return errors.New("k8s-env: variable is required")
	}
	if len(v) > maxName || strings.ContainsFunc(v, func(r rune) bool { return r < ' ' || r > '~' || r == '=' }) {
		return fmt.Errorf("k8s-env: variable %s is no name Kubernetes accepts: write up to %d printable ASCII characters other than =", core.Quote(v), maxName)
	}
	return nil
}

// extractor is a built k8s-env extractor.
type extractor struct {
	core.Source
	variable, container string
	yaml                func(data []byte, expanded int, note func(n *core.Node, tag string, plain bool)) ([]*core.Node, int, error)
	json                func([]byte) (*core.Node, error)
	// valueLimit is the most values the extractor returns, 0 for
	// core.MaxValues: a test lowers it to witness the bound with a small
	// file. The refusal still names core.MaxValues.
	valueLimit int
}

// ExtractWith is Extract: the extractor reads no other extractor's values.
func (e extractor) ExtractWith(ctx context.Context, root string, _ map[string][]check.Value) ([]check.Value, error) {
	return e.Extract(ctx, root)
}

// Extract reads the manifest and returns the variable's value in each
// container that sets it.
func (e extractor) Extract(ctx context.Context, root string) ([]check.Value, error) {
	return core.ExtractOne(ctx, root, e.Source, e)
}

// FromShared is fromData: a manifest read has its own bounds, and spends
// none of a set's budget.
func (e extractor) FromShared(data []byte, _ *core.Spent) ([]check.Value, error) {
	return e.fromData(data)
}
