package check

import (
	"strconv"
	"unicode/utf8"
)

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
	// Key is the name the file sets the value under, such as API_PORT for
	// the line API_PORT=8080 of an env file, for a reader whose values
	// carry one; "" for any other. A report names it beside the location
	// (KeyNote).
	Key string
	// HasKey marks a value whose reader names its key, so the empty key,
	// which an env file line =x sets, prints as key "" and not as no key.
	HasKey bool
	// Condition is what must hold for a job to see the value, for a reader
	// that returns one value for each case a file chooses between, such as
	// a GitLab CI rule's "if $CI_COMMIT_TAG" or a parallel:matrix instance
	// "build: [a]"; "" for a value that always holds. A rule compares it as
	// any other value; a report names it beside the location
	// (ConditionNote).
	Condition string
	// Origin is the reader that found the value and the part of its file
	// the value holds for, for a value an env extractor returned, such as
	// `gitlab-ci job "deploy"` or `shell`; "" for any other. Its reader
	// quotes the file's text in it, so a report writes it as it is
	// (OriginNote).
	Origin string
}

// maxNote is the most bytes of a key or a condition a note quotes.
const maxNote = 64

// KeyNote is what a report writes after a value's location to name its
// key, such as ` key "API_PORT"`, cut as note says. It is "" for a value
// with no key.
func (v Value) KeyNote() string {
	if !v.HasKey && v.Key == "" {
		return ""
	}
	return note(" key ", v.Key)
}

// Notes are what a report writes after a value's location: its key, its
// origin and its condition, each "" when the value has none.
func (v Value) Notes() string { return v.KeyNote() + v.OriginNote() + v.ConditionNote() }

// OriginNote is what a report writes after a value's location to name the
// reader and the part of the file it came from, such as
// ` from gitlab-ci job "deploy"`. It is "" for a value with no origin.
func (v Value) OriginNote() string {
	if v.Origin == "" {
		return ""
	}
	return " from " + v.Origin
}

// ConditionNote is what a report writes after a value's location to name
// its condition, such as ` when "if $CI_COMMIT_TAG"`, cut as note says. It
// is "" for a value with none.
func (v Value) ConditionNote() string {
	if v.Condition == "" {
		return ""
	}
	return note(" when ", v.Condition)
}

// note is label and s quoted, and for an s longer than maxNote its first
// maxNote bytes, cut back to the start of a character, and ....
func note(label, s string) string {
	if len(s) <= maxNote {
		return label + strconv.Quote(s)
	}
	n := maxNote
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return label + strconv.Quote(s[:n]) + "..."
}

// NoValue is the error an extractor returns when it found no value and
// can say where the value may be instead, such as a variable that only a
// GitLab project's settings could set. The rule treats it as an empty list,
// so allow_empty still decides, and names Reason when the extractor is
// unknown for it.
type NoValue struct {
	Reason string
}

func (e *NoValue) Error() string { return e.Reason }

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
