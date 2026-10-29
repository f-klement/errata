package check

// Description is what an extractor returns, as its kind tells it from the
// spec before the extractor is built or run: the label a message names it
// by, whether a value may be not literal, the shape assert reads each value
// in, and the extractors of its rule whose values it reads. A built in kind
// fills it from its spec; a plugin will fill it from its handshake.
type Description struct {
	// Label names the kind for a message: its key, such as json, and for
	// ini the dialect and the file's name, "ini, plain dialect on
	// pytest.ini". It is "" for an ini spec that names no dialect errata
	// reads.
	Label string
	// NotLiteral is whether the extractor may return a value marked
	// NotLiteral, which skip_not_literal can then leave out.
	NotLiteral bool
	// Shape is how assert reads each value.
	Shape Shape
	// Fields are the keys of each record, in the order of a record's keys,
	// for ShapeRecord; nil for any other shape.
	Fields []string
	// Reads names the extractors of the rule whose values the extractor
	// reads, which the rule runs before it.
	Reads []string
}

// Shape is how assert reads each value an extractor returns.
type Shape uint8

// The shapes of an extractor's values.
const (
	// ShapeFlat is a string, the value's Text.
	ShapeFlat Shape = iota
	// ShapeMap is each mapping a path selects, whole (as: map).
	ShapeMap
	// ShapeRecord is a mapping of the fields a rule names (as: record).
	ShapeRecord
)
