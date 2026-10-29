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
}
