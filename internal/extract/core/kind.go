package core

import (
	"maps"
	"path/filepath"
	"slices"

	"github.com/f-klement/errata/internal/check"
)

// Kind is one extractor kind, as its reader declares it beside its Spec:
// the key a rule writes it under in extract:, and the functions over its own
// spec that extract calls for every kind. extract lists each Kind once, with
// the Build that takes the parsers and readers it wires, as a
// check.ExtractorKind.
type Kind[S any] struct {
	// Key is what a rule writes under extract: to choose the kind, such as
	// yaml or tool_version.
	Key string
	// Place is the spec with only what decides where it reads, as
	// extract's Spec.Place says.
	Place func(S) S
	// Resolve is the spec with every default its Build fills in written
	// out, sharing no slice, map or pointer with the spec it is given, as
	// extract's Spec.Resolved says.
	Resolve func(S) S
	// Describe is what the extractor the spec builds returns, told from
	// the spec alone (check.Description).
	Describe func(S) check.Description
}

// PlaceFiles is the file or the files of a spec that takes either, as a
// place writes them: the file cleaned, or the files cleaned, with slashes,
// and in order, since the same files in another order read the same
// values, and a list of one file that is no glob as that file. The two read
// one file and return the same values where both read it, though a yaml
// kind that selects no document is refused for file: and gives no value in
// a set, so config refuses the pair as two names for one place.
func PlaceFiles(file string, files []string) (string, []string) {
	if files == nil {
		return filepath.Clean(file), nil
	}
	clean := Clean(files)
	slices.Sort(clean)
	if len(clean) == 1 && !HasGlob(clean[0]) {
		return filepath.Clean(clean[0]), nil
	}
	return "", clean
}

// TreeShape is the shape a yaml, json or toml extractor's as key gives its
// values, and the fields of a record sorted by name, the order of a
// record's keys: as: map hands over each mapping whole, as: record each
// record of fields, and any other as, which Build refuses, flat values.
// A record with no fields, which Build also refuses, has none.
func TreeShape(as string, fields map[string]string) (check.Shape, []string) {
	if as == "map" {
		return check.ShapeMap, nil
	}
	if as != "record" {
		return check.ShapeFlat, nil
	}
	if len(fields) == 0 {
		return check.ShapeRecord, nil
	}
	return check.ShapeRecord, slices.Sorted(maps.Keys(fields))
}
