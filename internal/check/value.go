package check

// Location is where an extractor found a value: a path relative to the
// repository root, and a line counted from 1.
type Location struct {
	File string
	Line int
}

// Value is one value an extractor returned, in its string form, with the
// place it was found. A rule compares values; the text and SARIF reports
// point at their locations.
type Value struct {
	Text string
	At   Location
}

// Extractor gets values from one place in the repository. An empty list is
// not an error: the rule decides whether finding nothing is unknown. An error
// means the extractor could not read its source, and the rule is unknown.
type Extractor interface {
	Extract(root string) ([]Value, error)
}
