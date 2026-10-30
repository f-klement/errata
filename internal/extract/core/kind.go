package core

import (
	"maps"
	"path/filepath"
	"slices"

	"github.com/f-klement/errata/internal/check"
)

// Bounded is a kind's spec, which states the max_bytes it sets, 0 for
// DefaultMaxBytes. Where and OneFile, which a spec embeds, give it; a spec
// that embeds neither states its own. extract describes every kind with it,
// so the rule holds an extractor's values to the max_bytes its spec sets.
type Bounded interface {
	Bound() int64
}

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
	// Env, when set, makes the kind a reader the env kind asks: env reads
	// a file with it when the file's name chooses it, or when a rule's
	// readers: names its Key.
	Env *EnvReader
}

// EnvReader is how a kind reads one variable for the env kind. Read is
// given the parsers extract wires, since no reader imports another: the
// gitlab-ci and github-actions readers read their YAML with the yaml
// reader's.
type EnvReader struct {
	// Order is the reader's place among env's readers, lowest first: in
	// the list a message gives, and the one Chooses decides a file's
	// reader when two would.
	Order int
	// Chooses reports whether a file's name chooses the reader. file is
	// cleaned, with slashes.
	Chooses func(file string) bool
	// Read builds the reader's extractor of the variable name in file,
	// which reads at most maxBytes.
	Read func(file, name string, maxBytes int64, p Parsers) (check.Extractor, error)
	// Sniffs, when set, reports whether a file's first bytes choose the
	// reader, at most SniffBytes of them, which is then above 0. env asks
	// it of a file whose name no reader's Chooses takes, the readers that
	// sniff in Order, as shell takes a file that starts with a #! line.
	Sniffs     func(head []byte) bool
	SniffBytes int
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
