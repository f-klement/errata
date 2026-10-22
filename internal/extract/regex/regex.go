// Package regex is the regex extractor: capture group 1 of every match of a
// pattern in a file, with core's clocked search.
package regex

import (
	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns capture group 1 of every match of Pattern in File.
type Spec struct {
	File     string `json:"file"`
	Pattern  string `json:"pattern"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

// Build checks r and returns its extractor, core.Search: the search is the
// whole extractor, and its tests are core's.
func Build(r Spec) (check.Extractor, error) {
	e, err := core.CompileSearch("regex", r.File, r.Pattern, r.MaxBytes)
	if err != nil {
		return nil, err
	}
	return e, nil
}
