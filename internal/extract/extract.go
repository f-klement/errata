// Package extract builds the extractors a rule names in errata.yaml. Each one
// reads one file and returns every value it finds there, with its location.
//
// The types below are the seam between the config package, which decodes a
// rule, and this package, which runs its extractors. Their JSON names are the
// keys a user writes under a rule's extract:.
package extract

import (
	"errors"

	"github.com/f-klement/errata/internal/check"
)

// Spec is one named entry under a rule's extract:. Exactly one field is set.
type Spec struct {
	YAML  *YAML  `json:"yaml,omitempty"`
	JSON  *JSON  `json:"json,omitempty"`
	TOML  *TOML  `json:"toml,omitempty"`
	Regex *Regex `json:"regex,omitempty"`
}

// YAML reads a path in a YAML file. A file holding several documents needs
// Kind or Name, or both, to select one.
type YAML struct {
	File string `json:"file"`
	Path string `json:"path"`
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
}

// JSON reads a path in a JSON file.
type JSON struct {
	File string `json:"file"`
	Path string `json:"path"`
}

// TOML reads a dotted key path in a TOML file.
type TOML struct {
	File string `json:"file"`
	Path string `json:"path"`
}

// Regex returns capture group 1 of every match of Pattern in File.
type Regex struct {
	File    string `json:"file"`
	Pattern string `json:"pattern"`
}

// ErrNotImplemented is what every extractor returns until the extractors
// land, so a rule that uses one is unknown rather than true.
var ErrNotImplemented = errors.New("extractor not implemented yet")

// Build checks s and returns its extractor. A Spec that sets no kind, or more
// than one, is a load error.
func Build(s Spec) (check.Extractor, error) {
	n := 0
	for _, set := range []bool{s.YAML != nil, s.JSON != nil, s.TOML != nil, s.Regex != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return nil, errors.New("an extractor sets exactly one of yaml, json, toml, regex")
	}
	return pending{}, nil
}

type pending struct{}

func (pending) Extract(string) ([]check.Value, error) { return nil, ErrNotImplemented }
