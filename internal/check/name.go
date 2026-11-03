package check

import (
	"fmt"
	"regexp"
	"slices"
)

// identifierForm is what CEL reads as one name, as a variable or after a
// dot: a letter or _, then letters, digits and _.
var identifierForm = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// celReserved are the words CEL's language definition reserves. It keeps
// false, in, null and true out of names, so r.in is a syntax error, and
// reserves the rest for the languages that embed CEL, advising against them
// as names; cel-go reads those after a dot today, and a later one may not.
var celReserved = []string{
	"as", "break", "const", "continue", "else", "false", "for", "function", "if", "import",
	"in", "let", "loop", "namespace", "null", "package", "return", "true", "var", "void", "while",
}

// IdentifierForm reports whether name has the form of a CEL identifier: a
// letter or _, then letters, digits and _. A reserved word has the form.
func IdentifierForm(name string) bool { return identifierForm.MatchString(name) }

// Reserved reports whether name is one of the words CEL's language
// definition reserves.
func Reserved(name string) bool { return slices.Contains(celReserved, name) }

// ExtractorName refuses an extractor's name that is no CEL identifier, or
// returns nil. agree, assert and a regex's (?{name}) refer to an extractor
// by its name, so one rule holds for all three, and no name holds a dot,
// which keeps x.f in agree a field of the records of x. The empty name is
// refused with the rest.
func ExtractorName(name string) error {
	if !IdentifierForm(name) || Reserved(name) {
		return fmt.Errorf("extractor %q: a rule refers to an extractor by its name, so the name must be letters, digits and _, must not start with a digit and must not be one of CEL's reserved words", name)
	}
	return nil
}
