package check

import (
	"encoding/json"
	"reflect"
)

// ExtractorKind is one extractor kind: the key an entry under a rule's
// extract: names, such as yaml, regex or ini, and the functions that read
// an entry of it. Each takes and returns the entry's spec as a value of the
// type Spec, held as any. A built in kind is a reader's, which the extract
// package lists; a plugin's will be filled from its handshake.
type ExtractorKind struct {
	// Key is what an entry writes to name the kind.
	Key string
	// Spec is the Go type an entry of the kind decodes into.
	Spec reflect.Type
	// Decode reads an entry's value under Key strictly into a Spec: a key
	// the spec does not have is an error.
	Decode func(json.RawMessage) (any, error)
	// Build checks a spec and returns its extractor.
	Build func(spec any) (Extractor, error)
	// Place is a spec with only what decides where it reads.
	Place func(spec any) any
	// Resolve is a spec with every default Build fills in written out.
	Resolve func(spec any) any
	// Describe is what the extractor a spec builds returns.
	Describe func(spec any) Description
	// Env, when set, is how the env kind chooses the kind as the reader of
	// a file, which it lists under Key. It is the declaration the kind
	// writes, not a copy of it.
	Env *EnvChoice
	// EnvRead, set with Env, builds the kind's extractor of the variable
	// name in file, reading at most maxBytes.
	EnvRead func(file, name string, maxBytes int64) (Extractor, error)
}

// EnvChoice is how the env kind chooses a kind as the reader of a file:
// the kind's place in env's order, and the tests a file's name, or its
// first bytes, pass to choose it. A kind that writes one is a reader env
// asks for one variable. A kind writes it once, and the registry hands the
// same value on, so a field added here reaches env with nothing to copy.
// The kind's EnvRead sits beside it on the kind, since a reader package's
// EnvRead takes the parsers extract wires and the registry's takes none.
type EnvChoice struct {
	// Order is the reader's place among env's readers, lowest first: in
	// the list a message gives, and the one Chooses decides a file's
	// reader when two would.
	Order int
	// Chooses reports whether a file's name chooses the reader. file is
	// cleaned, with slashes.
	Chooses func(file string) bool
	// Sniffs, when set, reports whether a file's first bytes choose the
	// reader, at most SniffBytes of them, which is then above 0. env asks
	// it of a file whose name no reader's Chooses takes, the readers that
	// sniff in Order, as shell takes a file that starts with a #! line.
	Sniffs     func(head []byte) bool
	SniffBytes int
}
