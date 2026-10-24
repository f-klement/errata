package gitlabci

import (
	"math/big"
	"regexp"
	"strings"

	"github.com/f-klement/errata/internal/extract/core"
)

// GitLab loads a CI file with Ruby's Psych, a YAML 1.1 loader, which types
// a plain scalar by its text and hands the job the Ruby value's string
// form: 3.10 is the float 3.1, 010 the octal 8, 1:30 the sexagesimal 90,
// and yes the boolean true. The patterns below are Psych's ScalarScanner's
// (Psych 5), widened where that only marks more values: a scalar they match
// is not the text the job sees, so it is not literal. A decimal integer
// written as Ruby prints it is the one number that stays its text.
var (
	psychInt = regexp.MustCompile(`^(?:[-+]?0b[0-1_,]+|[-+]?0[0-7_,]+|[-+]?(?:0|[1-9][0-9_,]*)|[-+]?0x[0-9a-fA-F_,]+)$`)
	// A float needs a dot in Psych: 1e3 is the string "1e3".
	psychFloat = regexp.MustCompile(`^(?:[-+]?(?:[0-9][0-9_,]*)?\.[0-9]*(?:[eE][-+][0-9]+)?|[-+]?\.(?:inf|Inf|INF)|\.(?:nan|NaN|NAN))$`)
	psychSexa  = regexp.MustCompile(`^[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+(?:\.[0-9_]*)?$`)
	psychDate  = regexp.MustCompile(`^-?[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}`)
	// psychDecimal is Psych's decimal integer, narrower than psychInt's:
	// a comma or underscore is followed by a digit.
	psychDecimal  = regexp.MustCompile(`^[-+]?(?:0|[1-9](?:[0-9]|,[0-9]|_[0-9])*)$`)
	rubyInteger   = regexp.MustCompile(`^(?:0|-?[1-9][0-9]*)$`)
	nameReference = regexp.MustCompile(`\$[A-Za-z0-9_{$\[]|%[A-Za-z_][A-Za-z0-9_]*%`)
)

// psychString reports whether Psych reads the plain scalar text as a value
// whose string form is text: a string, or a decimal integer.
func psychString(text string) bool {
	switch {
	case text == "", text == "~", strings.EqualFold(text, "null"):
		return false // nil
	case isPsychBool(text):
		return false
	case strings.HasPrefix(text, ":") && len(text) > 1:
		return false // a Symbol
	case rubyInteger.MatchString(text):
		return true
	}
	return !psychInt.MatchString(text) && !psychFloat.MatchString(text) && !psychSexa.MatchString(text) && !psychDate.MatchString(text)
}

// isPsychBool reports whether Psych reads text as a boolean: yes, true or
// on, or no, false or off, in any case.
func isPsychBool(text string) bool {
	for _, w := range []string{"yes", "true", "on", "no", "false", "off"} {
		if strings.EqualFold(text, w) {
			return true
		}
	}
	return false
}

// psychTrue reports whether a text isPsychBool takes is Psych's true:
// yes, true or on, in any case.
func psychTrue(text string) bool {
	return strings.EqualFold(text, "yes") || strings.EqualFold(text, "true") || strings.EqualFold(text, "on")
}

// references reports whether text names a variable GitLab expands: "in a
// form of $variable, or ${variable} or %variable%". $$ and $[[ (an input's
// interpolation) count too, since what the job sees is not the text.
func references(text string) bool {
	return nameReference.MatchString(text)
}

// psychText is the text of the value Psych loads from the scalar n, as
// Ruby's to_s writes it, for the values errata can write: a boolean is true
// or false, and an integer in another base, with a sign or with , or _ in
// it is written in decimal, so 010 is 8. Any other value is its text: a
// float, date or null among them, which errata reads as not literal.
func (c *config) psychText(n *core.Node) string {
	text := n.Text
	if len(text) > maxCondition || !c.retyped(n, text) {
		return text
	}
	if isPsychBool(text) {
		if psychTrue(text) {
			return "true"
		}
		return "false"
	}
	if !psychInt.MatchString(text) {
		return text
	}
	digits := strings.NewReplacer(",", "", "_", "").Replace(text)
	sign := ""
	if digits[0] == '-' || digits[0] == '+' {
		sign, digits = digits[:1], digits[1:]
	}
	base := 10
	switch {
	case strings.HasPrefix(digits, "0b"):
		base, digits = 2, digits[2:]
	case strings.HasPrefix(digits, "0x"):
		base, digits = 16, digits[2:]
	case len(digits) > 1 && digits[0] == '0':
		base = 8
	case !psychDecimal.MatchString(text):
		return text // a string to Psych, such as 1,
	}
	v, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return text // Ruby's Integer() refuses it too, such as 0b
	}
	if sign == "-" {
		v.Neg(v)
	}
	return v.String()
}
