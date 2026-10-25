package githubactions

import (
	"regexp"

	"github.com/f-klement/errata/internal/extract/core"
)

// GitHub reads a workflow with YAML 1.2's core schema, which types a plain
// scalar by its text, and hands a step the string form of the value: 3.10
// is the number 3.1, 010 the number 10 and True the boolean true. Every
// number is a double, so a long integer loses digits. The patterns are the
// core schema's. A scalar they match is not the text the step sees, except
// a decimal integer written as a double prints it and the booleans true and
// false, so it is not literal.
var (
	coreNull  = regexp.MustCompile(`^(?:~|null|Null|NULL|)$`)
	coreBool  = regexp.MustCompile(`^(?:true|True|TRUE|false|False|FALSE)$`)
	coreInt   = regexp.MustCompile(`^(?:[-+]?[0-9]+|0o[0-7]+|0x[0-9a-fA-F]+)$`)
	coreFloat = regexp.MustCompile(`^(?:[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	// printedInt is an integer a double holds exactly, at most 15 digits,
	// as it prints: no sign but a minus, no leading zero.
	printedInt = regexp.MustCompile(`^(?:0|-?[1-9][0-9]{0,14})$`)
)

// coreString reports whether the core schema reads the plain scalar text
// as a value whose string form is text.
func coreString(text string) bool {
	switch {
	case coreNull.MatchString(text):
		return false
	case text == "true" || text == "false" || printedInt.MatchString(text):
		return true
	}
	return !coreBool.MatchString(text) && !coreInt.MatchString(text) && !coreFloat.MatchString(text)
}

// retyped reports whether GitHub gives the step a value other than the
// scalar's text: a plain scalar the core schema types, or one with a tag
// other than !!str.
func (w *workflow) retyped(n *core.Node) bool {
	info := w.info[n]
	switch info.tag {
	case "":
		return info.plain && !coreString(n.Text)
	case "!!str":
		return false
	}
	return true
}
