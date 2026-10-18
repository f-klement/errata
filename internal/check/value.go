package check

// Location is a place in a file of the repository: a path relative to the
// repository root, and a line counted from 1. It is where an extractor found
// a value, or where a check is written in the config file.
type Location struct {
	File string
	Line int
	// Outside marks a config file outside the repository, such as one given
	// with -c from another directory. File then names it as the user gave
	// it, which is no path from the root, so a report names the file and
	// places nothing in the repository.
	Outside bool
}

// Value is one value an extractor returned, in its string form, with the
// place it was found. A rule compares values; the text and SARIF reports
// point at their locations.
type Value struct {
	Text string
	At   Location
	// NotLiteral marks a value the file only computes, such as $(cmd) in a
	// shell script, ${{ expr }} in a CI file, a reference or a function
	// call: Text is what the file wrote, not what it evaluates to. A rule
	// never compares such a value as text. It is unknown, or, for an
	// extractor that skip_not_literal lists, it leaves the value out. The
	// zero value is literal, so a reader whose every value is literal never
	// sets it.
	NotLiteral bool
}

// Extractor gets values from one place in the repository. An empty list is
// not an error: the rule decides whether finding nothing is unknown. An error
// means the extractor could not read its source, and the rule is unknown.
type Extractor interface {
	Extract(root string) ([]Value, error)
}

// NotLiteralReader is an extractor whose kind declares whether it can return
// a not literal value, as a reader for shell scripts or CI files does. An
// extractor that does not implement it returns literal values only.
type NotLiteralReader interface {
	Extractor
	// MayReturnNotLiteral reports whether the extractor can return a value
	// marked NotLiteral.
	MayReturnNotLiteral() bool
}

// MayReturnNotLiteral reports whether e declares that it can return a not
// literal value. A rule may list only such an extractor in skip_not_literal:
// for any other the key would load and do nothing.
func MayReturnNotLiteral(e Extractor) bool {
	r, ok := e.(NotLiteralReader)
	return ok && r.MayReturnNotLiteral()
}
