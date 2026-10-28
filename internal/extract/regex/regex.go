// Package regex is the regex extractor: capture group 1 of every match of a
// pattern in a file, with core's clocked search.
package regex

import (
	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec returns capture group 1 of every match of Pattern in File, or in
// each file Files names, a file or a glob (core.Files).
type Spec struct {
	File     string   `json:"file,omitempty"`
	Files    []string `json:"files,omitempty"`
	Pattern  string   `json:"pattern"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
}

// Build checks r and returns its extractor, core.Search, or a core.FileSet
// of one for each file: the search is the whole extractor, and its tests
// are core's.
func Build(r Spec) (check.Extractor, error) {
	return core.BuildOneOrSet("regex", r.File, r.Files, r.MaxBytes, func(file string, maxBytes int64, _ bool) (core.FileReader, error) {
		e, err := core.CompileSearch("regex", file, r.Pattern, maxBytes)
		if err != nil {
			return nil, err
		}
		return e, nil
	})
}
