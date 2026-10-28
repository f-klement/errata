package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Parsers are the YAML, JSON and TOML readers' parsers: a reader of version
// files parses a manifest with them, such as package.json or Cargo.toml,
// and the yaml, json and toml readers a document embedded in a string. The
// facade hands them the readers' own, since no reader imports another. YAML
// starts the count of the nodes aliases and merge keys add at its second
// argument and returns the count it ends at, as yaml.ParseNoted does.
type Parsers struct {
	YAML func([]byte, int) ([]*Node, int, error)
	JSON func([]byte) (*Node, error)
	TOML func([]byte) (*Node, error)
}

// CheckVersion refuses a version that is empty, or that holds a character
// a terminal or a viewer acts on rather than shows, which no version holds.
// line is where the version is, for the message.
func CheckVersion(line int, text string) error {
	if text == "" {
		return LineError(line, "the version is empty")
	}
	if strings.ContainsFunc(text, actedOn) {
		return LineError(line, "the version %s holds a control character, a direction control, a line separator or a byte that is not UTF-8, which no version holds", Quote(text))
	}
	return nil
}

// actedOn reports whether a terminal or a viewer acts on c rather than
// showing it, as the report package's test has it (a control character, a
// direction control, a line or paragraph separator), or c is the U+FFFD a
// byte that is not UTF-8 decodes to.
func actedOn(c rune) bool {
	return unicode.IsControl(c) || unicode.Is(unicode.Bidi_Control, c) || unicode.In(c, unicode.Zl, unicode.Zp) || c == utf8.RuneError
}
