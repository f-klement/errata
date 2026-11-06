// Package jsonc is the jsonc and json5 extractors: each reads a path in a
// JSON file written in a dialect that strict JSON refuses, from the tree of
// core.Node the file parses into. JSONC is JSON with comments and trailing
// commas, as tsconfig.json and VS Code's settings are; JSON5 adds what the
// JSON5 specification adds, as renovate.json5 is written. The kind a rule
// writes names the dialect; no file's name chooses one.
package jsonc

import (
	"maps"
	"slices"

	"github.com/f-klement/errata/internal/check"
	"github.com/f-klement/errata/internal/extract/core"
)

// Spec reads a path in a JSONC or JSON5 file, File, or in each file Files
// names, a file or a glob (core.Files), with the fields of the json
// extractor. The kind is the dialect: jsonc reads every file as JSONC, and
// json5 every file as JSON5, whatever its name.
type Spec struct {
	core.Where
	Path     string          `json:"path"`
	Embedded []core.Embedded `json:"embedded,omitempty"`
	// As is map to hand assert each mapping the path selects whole, or
	// record to read Fields from each node it selects into one, as
	// core.CheckAs says; "" for each scalar.
	As string `json:"as,omitempty"`
	// Fields names each path read from a selected node into its record,
	// for as: record, as core.CheckFields says.
	Fields map[string]string `json:"fields,omitempty"`
}

// Kind is the jsonc kind, which a rule writes under the key jsonc, and
// JSON5Kind the json5 kind. Each reads every file in its own dialect, and
// JSON5 reads every JSONC file into the same tree, so a set of files of
// both dialects is read with json5.
var (
	Kind      = kindOf("jsonc")
	JSON5Kind = kindOf("json5")
)

func kindOf(key string) core.Kind[Spec] {
	return core.Kind[Spec]{
		Key:      key,
		Place:    Place,
		Resolve:  Resolve,
		Describe: func(s Spec) check.Description { return describe(s, key) },
	}
}

// Build checks s and returns the jsonc kind's extractor, which reads JSONC
// and the documents embedded names with p.
func Build(s Spec, p core.Parsers) (check.Extractor, error) {
	return core.BuildTree(Kind.Key, tree(s), core.JSONFormat, ParseJSONC, p)
}

// BuildJSON5 is Build for the json5 kind, which reads JSON5.
func BuildJSON5(s Spec, p core.Parsers) (check.Extractor, error) {
	return core.BuildTree(JSON5Kind.Key, tree(s), core.JSONFormat, ParseJSON5, p)
}

func tree(s Spec) core.Tree {
	return core.Tree{Where: s.Where, Path: s.Path, Embedded: s.Embedded, As: s.As, Fields: s.Fields}
}

// Place is s with only what decides where it reads: its where part as
// core.Where.Place writes it.
func Place(s Spec) Spec {
	s.Where = s.Place()
	return s
}

// Resolve is s with its max_bytes written out, and its files, embedded
// steps and fields copied, for errata list.
func Resolve(s Spec) Spec {
	s.Where = s.Resolved()
	s.Embedded, s.Fields = slices.Clone(s.Embedded), maps.Clone(s.Fields)
	return s
}

// describe is what s's extractor of kind returns: literal values, each
// flat, a mapping or a record as its as key says.
func describe(s Spec, kind string) check.Description {
	shape, fields := core.TreeShape(s.As, s.Fields)
	return check.Description{Label: kind, Shape: shape, Fields: fields}
}
